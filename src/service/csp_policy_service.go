package service

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/repository"
	"gorm.io/gorm"
)

// CspPolicyService CSP 정책 서비스
type CspPolicyService struct {
	db                  *gorm.DB
	cspPolicyRepo       *repository.CspPolicyRepository
	cspAccountRepo      *repository.CspAccountRepository
	cspRoleRepo         *repository.CspRoleRepository
	cspIdpConfigRepo    *repository.CspIdpConfigRepository
	cspIdpConfigService *CspIdpConfigService
	// awsPolicyClient 테스트에서 AWS IAM 호출을 바꿔 끼울 때만 쓴다 (nil 이면 소속 Config 로 assume 한 실제 클라이언트)
	awsPolicyClient func(cfg *model.CspIdpConfig) (awsPolicyAPI, error)
}

// NewCspPolicyService 새 CspPolicyService 인스턴스 생성
func NewCspPolicyService(db *gorm.DB, cspIdpConfigService *CspIdpConfigService) *CspPolicyService {
	return &CspPolicyService{
		db:                  db,
		cspPolicyRepo:       repository.NewCspPolicyRepository(db),
		cspAccountRepo:      repository.NewCspAccountRepository(db),
		cspRoleRepo:         repository.NewCspRoleRepository(db),
		cspIdpConfigRepo:    repository.NewCspIdpConfigRepository(db),
		cspIdpConfigService: cspIdpConfigService,
	}
}

// GetCspPolicyByID ID로 CSP 정책 조회
func (s *CspPolicyService) GetCspPolicyByID(id uint) (*model.CspPolicy, error) {
	policy, err := s.cspPolicyRepo.GetByID(id)
	if err != nil {
		return nil, fmt.Errorf("failed to get policy: %w", err)
	}
	if policy == nil {
		return nil, bindingErr(http.StatusNotFound, "not_found", "policy not found with ID: %d", id)
	}
	return policy, nil
}

// ListCspPolicies CSP 정책 목록 조회
func (s *CspPolicyService) ListCspPolicies(filter *model.CspPolicyFilter) ([]*model.CspPolicy, error) {
	policies, err := s.cspPolicyRepo.List(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list policies: %w", err)
	}
	return policies, nil
}

// GetPoliciesByRoleID 역할에 연결된 정책 목록 조회
func (s *CspPolicyService) GetPoliciesByRoleID(roleID uint) ([]*model.CspPolicy, error) {
	policies, err := s.cspPolicyRepo.GetPoliciesByRoleID(roleID)
	if err != nil {
		return nil, fmt.Errorf("failed to get policies by role: %w", err)
	}
	return policies, nil
}

// SyncPoliciesFromCloud CSP에서 정책 동기화
func (s *CspPolicyService) SyncPoliciesFromCloud(ctx context.Context, req *model.SyncPoliciesRequest) ([]*model.CspPolicy, error) {
	// CSP 계정 조회
	account, err := s.cspAccountRepo.GetByID(req.CspAccountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get CSP account: %w", err)
	}
	if account == nil {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "CSP account not found with ID: %d", req.CspAccountID)
	}

	switch account.CspType {
	case "aws":
		return s.syncAwsPolicies(ctx, account, req.PolicyScope)
	case "gcp":
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "GCP policy sync not implemented yet")
	case "azure":
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "Azure policy sync not implemented yet")
	default:
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "unsupported CSP type: %s", account.CspType)
	}
}

// syncAwsPolicies AWS에서 정책 동기화
func (s *CspPolicyService) syncAwsPolicies(ctx context.Context, account *model.CspAccount, scope string) ([]*model.CspPolicy, error) {
	// IDP 설정을 통해 임시 자격 증명 획득
	idpConfigs, err := s.cspIdpConfigService.GetActiveIdpConfigsByAccountID(account.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get IDP configs: %w", err)
	}
	if len(idpConfigs) == 0 {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "no active IDP config found for account")
	}

	// 첫 번째 활성 IDP 설정 사용
	idpConfig := idpConfigs[0]

	// 임시 자격 증명 획득
	tempCred, err := s.cspIdpConfigService.AssumeRoleWithIdpConfig(ctx, idpConfig.ID,
		idpConfig.Config["role_arn"],
		"mciam-policy-sync",
		3600)
	if err != nil {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "failed to assume role: %v", err)
	}

	// AWS IAM 클라이언트 생성
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(tempCred.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			tempCred.AccessKeyId,
			tempCred.SecretAccessKey,
			tempCred.SessionToken,
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	iamClient := iam.NewFromConfig(cfg)

	// 정책 목록 조회
	var policyScope iamtypes.PolicyScopeType
	switch scope {
	case "All":
		policyScope = iamtypes.PolicyScopeTypeAll
	case "AWS":
		policyScope = iamtypes.PolicyScopeTypeAws
	case "Local":
		policyScope = iamtypes.PolicyScopeTypeLocal
	default:
		policyScope = iamtypes.PolicyScopeTypeLocal
	}

	input := &iam.ListPoliciesInput{
		Scope: policyScope,
	}

	var syncedPolicies []*model.CspPolicy
	paginator := iam.NewListPoliciesPaginator(iamClient, input)

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, bindingErr(http.StatusBadGateway, "csp_error", "failed to list AWS policies: %v", err)
		}

		for _, awsPolicy := range page.Policies {
			arn := aws.ToString(awsPolicy.Arn)
			policyType := model.PolicyTypeForArn(arn)
			// 고객 관리형은 IAM 이 관리하는 mciam-* 만 가져온다 (IAM 밖 서비스가 쓰는 정책은 들이지 않는다, IAM-TECH-052)
			if policyType == model.PolicyTypeCustom && !isIamOwnedPolicyName(aws.ToString(awsPolicy.PolicyName)) {
				continue
			}
			// 기존 정책 확인 (계정 조건 포함, D4)
			existingPolicy, err := s.cspPolicyRepo.GetByArnAndAccountID(arn, account.ID)
			if err != nil {
				log.Printf("Failed to look up policy %s: %v", arn, err)
				continue
			}
			if existingPolicy != nil {
				values := map[string]interface{}{"name": aws.ToString(awsPolicy.PolicyName), "policy_type": policyType}
				if awsPolicy.Description != nil {
					values["description"] = *awsPolicy.Description
				}
				if err := s.cspPolicyRepo.UpdatePolicyFields(existingPolicy.ID, values); err != nil {
					log.Printf("Failed to update policy %s: %v", *awsPolicy.PolicyName, err)
					continue
				}
				existingPolicy.Name, existingPolicy.PolicyType = aws.ToString(awsPolicy.PolicyName), policyType
				if awsPolicy.Description != nil {
					existingPolicy.Description = *awsPolicy.Description
				}
				syncedPolicies = append(syncedPolicies, existingPolicy)
			} else {
				// 새로 생성
				newPolicy := &model.CspPolicy{
					Name:         *awsPolicy.PolicyName,
					CspAccountID: account.ID,
					PolicyType:   policyType,
					PolicyArn:    arn,
				}
				if awsPolicy.Description != nil {
					newPolicy.Description = *awsPolicy.Description
				}
				if err := s.cspPolicyRepo.Create(newPolicy); err != nil {
					log.Printf("Failed to create policy %s: %v", *awsPolicy.PolicyName, err)
					continue
				}
				syncedPolicies = append(syncedPolicies, newPolicy)
			}
		}
	}

	log.Printf("Synced %d policies from AWS", len(syncedPolicies))
	return syncedPolicies, nil
}

// GetPolicyDocument CSP에서 정책 문서 조회
func (s *CspPolicyService) GetPolicyDocument(ctx context.Context, policyID uint) (map[string]interface{}, error) {
	policy, err := s.cspPolicyRepo.GetByID(policyID)
	if err != nil {
		return nil, fmt.Errorf("failed to get policy: %w", err)
	}
	if policy == nil {
		return nil, bindingErr(http.StatusNotFound, "not_found", "policy not found with ID: %d", policyID)
	}

	// 이미 PolicyDoc가 있으면 반환
	if policy.PolicyDoc != nil {
		return policy.PolicyDoc, nil
	}

	// 관리형(AWS·고객) 정책이고 ARN이 있으면 CSP에서 조회
	if (policy.PolicyType == model.PolicyTypeManaged || policy.PolicyType == model.PolicyTypeCustom) && policy.PolicyArn != "" {
		account, err := s.cspAccountRepo.GetByID(policy.CspAccountID)
		if err != nil {
			return nil, fmt.Errorf("failed to get CSP account: %w", err)
		}

		if account.CspType == "aws" {
			return s.getAwsPolicyDocument(ctx, account, policy.PolicyArn)
		}
	}

	return nil, bindingErr(http.StatusNotFound, "not_found", "policy document not available")
}

// getAwsPolicyDocument AWS에서 정책 문서 조회
func (s *CspPolicyService) getAwsPolicyDocument(ctx context.Context, account *model.CspAccount, policyArn string) (map[string]interface{}, error) {
	// IDP 설정을 통해 임시 자격 증명 획득
	idpConfigs, err := s.cspIdpConfigService.GetActiveIdpConfigsByAccountID(account.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get IDP configs: %w", err)
	}
	if len(idpConfigs) == 0 {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "no active IDP config found for account")
	}

	idpConfig := idpConfigs[0]
	tempCred, err := s.cspIdpConfigService.AssumeRoleWithIdpConfig(ctx, idpConfig.ID,
		idpConfig.Config["role_arn"],
		"mciam-policy-get",
		900)
	if err != nil {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "failed to assume role: %v", err)
	}

	// AWS IAM 클라이언트 생성
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(tempCred.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			tempCred.AccessKeyId,
			tempCred.SecretAccessKey,
			tempCred.SessionToken,
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	iamClient := iam.NewFromConfig(cfg)

	// 정책 정보 조회
	getPolicyInput := &iam.GetPolicyInput{
		PolicyArn: aws.String(policyArn),
	}
	policyResult, err := iamClient.GetPolicy(ctx, getPolicyInput)
	if err != nil {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "failed to get policy: %v", err)
	}

	// 정책 버전 문서 조회
	getPolicyVersionInput := &iam.GetPolicyVersionInput{
		PolicyArn: aws.String(policyArn),
		VersionId: policyResult.Policy.DefaultVersionId,
	}
	versionResult, err := iamClient.GetPolicyVersion(ctx, getPolicyVersionInput)
	if err != nil {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "failed to get policy version: %v", err)
	}

	// URL 디코딩 및 JSON 파싱
	if versionResult.PolicyVersion.Document != nil {
		// AWS는 URL 인코딩된 JSON을 반환
		// 여기서는 간단히 string으로 반환 (실제로는 파싱 필요)
		return map[string]interface{}{
			"document": *versionResult.PolicyVersion.Document,
		}, nil
	}

	return nil, bindingErr(http.StatusNotFound, "not_found", "policy document not found")
}

// GetManagedPoliciesByAccountID 특정 계정의 관리형 정책 목록 조회
func (s *CspPolicyService) GetManagedPoliciesByAccountID(accountID uint) ([]*model.CspPolicy, error) {
	policies, err := s.cspPolicyRepo.GetManagedPoliciesByAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get managed policies: %w", err)
	}
	return policies, nil
}

// GetPoliciesByAccountID 특정 계정의 모든 정책 목록 조회
func (s *CspPolicyService) GetPoliciesByAccountID(accountID uint) ([]*model.CspPolicy, error) {
	policies, err := s.cspPolicyRepo.GetByAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get policies by account: %w", err)
	}
	return policies, nil
}
