package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"gorm.io/gorm"
)

// attachPolicy·detachPolicy 의 AWS 반영 (IAM-BUG-044, 계획 4a)
//
// Role 1개 × 정책 1개, AWS 호출 1회. 처리 순서는 가드 → AWS → DB 이다.
//   - 요청 자체가 잘못되면 아무것도 기록하지 않고 거절한다: 400 요청 오류, 403 TECH-049, 404 없음, 409 관리용 Role
//   - AWS 가 실패하면 매핑 행에 상태(failed·detach_failed)와 last_error 를 남기고 200 + result·warnings 로 알린다.
//     아래 계층(권한 정책) 실패가 위 계층(대상 CSP Role·Master Role)을 실패시키지 않는다. 같은 요청을 다시 하면 다시 맞춘다.
//   - DB 쓰기가 실패하면 500 이다. 이때 AWS 는 반대 호출로 되돌린다.

// awsPolicyAPI 권한 정책 연결·해제와 고객 관리형 정책 정의(생성·버전·삭제)에 쓰는 AWS IAM 호출.
// *iam.Client 가 만족한다 (테스트는 fake).
type awsPolicyAPI interface {
	AttachRolePolicy(ctx context.Context, params *iam.AttachRolePolicyInput, optFns ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
	DetachRolePolicy(ctx context.Context, params *iam.DetachRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error)
	PutRolePolicy(ctx context.Context, params *iam.PutRolePolicyInput, optFns ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	DeleteRolePolicy(ctx context.Context, params *iam.DeleteRolePolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	GetPolicy(ctx context.Context, params *iam.GetPolicyInput, optFns ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
	CreatePolicy(ctx context.Context, params *iam.CreatePolicyInput, optFns ...func(*iam.Options)) (*iam.CreatePolicyOutput, error)
	CreatePolicyVersion(ctx context.Context, params *iam.CreatePolicyVersionInput, optFns ...func(*iam.Options)) (*iam.CreatePolicyVersionOutput, error)
	ListPolicyVersions(ctx context.Context, params *iam.ListPolicyVersionsInput, optFns ...func(*iam.Options)) (*iam.ListPolicyVersionsOutput, error)
	DeletePolicyVersion(ctx context.Context, params *iam.DeletePolicyVersionInput, optFns ...func(*iam.Options)) (*iam.DeletePolicyVersionOutput, error)
	DeletePolicy(ctx context.Context, params *iam.DeletePolicyInput, optFns ...func(*iam.Options)) (*iam.DeletePolicyOutput, error)
	// CspRole 삭제 (IAM-BUG-057)
	ListAttachedRolePolicies(ctx context.Context, params *iam.ListAttachedRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	ListRolePolicies(ctx context.Context, params *iam.ListRolePoliciesInput, optFns ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	ListInstanceProfilesForRole(ctx context.Context, params *iam.ListInstanceProfilesForRoleInput, optFns ...func(*iam.Options)) (*iam.ListInstanceProfilesForRoleOutput, error)
	DeleteRole(ctx context.Context, params *iam.DeleteRoleInput, optFns ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
}

// 관리용 자격증명 세션 이름 (CloudTrail 에서 IAM 쓰기 주체로 보인다)
const policyBindingSessionName = "mcmp-iam-manager-session"

// PolicyBindingError 기록 없이 거절하는 요청 오류. Status 는 HTTP 상태 코드다.
type PolicyBindingError struct {
	Status  int
	Code    string // invalid_request | not_found | protected | forbidden_policy | conflict | db_error | csp_error
	Msg     string
	Details map[string]interface{} // 응답 본문에 함께 넣는 값 (예: 삭제를 막은 CSP 정책 목록)
}

func (e *PolicyBindingError) Error() string { return e.Msg }

func bindingErr(status int, code, format string, args ...interface{}) *PolicyBindingError {
	return &PolicyBindingError{Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// AsPolicyBindingError err 가 PolicyBindingError 면 돌려준다.
func AsPolicyBindingError(err error) (*PolicyBindingError, bool) {
	var e *PolicyBindingError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// TECH-049 최소 거부 목록. 목록 확장과 Permissions Boundary 는 IAM-TECH-049 에서 다룬다.
var forbiddenManagedPolicyArn = regexp.MustCompile(`^arn:aws[a-z-]*:iam::aws:policy/(AdministratorAccess|IAMFullAccess|PowerUserAccess)$`)

// AWS inline 정책 이름 규칙: 1~128자, 영숫자와 +=,.@_-
var awsInlinePolicyNamePattern = regexp.MustCompile(`^[\w+=,.@-]{1,128}$`)

// policyBindingTarget 가드를 통과한 요청 (AWS 호출에 필요한 값)
type policyBindingTarget struct {
	role      *model.CspRole
	policy    *model.CspPolicy
	idpConfig *model.CspIdpConfig
	mapping   *model.CspRolePolicyMapping // 기존 매핑 행 (없으면 nil)
	inlineDoc string                      // inline 정책 문서 JSON
	dbOnly    bool                        // AWS 에 반영된 적이 없는 정의(ARN 없는 custom): detach 는 매핑만 지운다
}

// AttachPolicyToRole 역할에 권한 정책을 연결하고 AWS 에 반영한다.
func (s *CspPolicyService) AttachPolicyToRole(roleID, policyID uint) (*model.PolicyBindingResult, error) {
	target, err := s.prepareBinding(roleID, policyID, true)
	if err != nil {
		return nil, err
	}
	ctx := context.TODO()
	result := &model.PolicyBindingResult{CspRoleID: roleID, CspPolicyID: policyID, Warnings: []string{}}

	awsErr := s.applyPolicy(ctx, target)
	now := time.Now()
	if awsErr != nil {
		msg := fmt.Sprintf("AWS attach of policy %s to role %s failed: %v", target.policy.Name, target.role.Name, awsErr)
		log.Printf("[WARN] %s", msg)
		if err := s.saveBindingStatus(target, model.PolicyAwsStatusFailed, awsErr.Error(), nil); err != nil {
			return nil, bindingErr(http.StatusInternalServerError, "db_error", "%s; failed to record status: %v", msg, err)
		}
		result.Result = model.PolicyBindingFailed
		result.AwsStatus = model.PolicyAwsStatusFailed
		result.ErrorCode = "csp_error"
		result.Message = msg
		result.Warnings = append(result.Warnings, msg)
		return result, nil
	}

	if err := s.saveBindingStatus(target, model.PolicyAwsStatusApplied, "", &now); err != nil {
		msg := fmt.Sprintf("policy %s was attached to role %s in AWS but the database update failed: %v", target.policy.Name, target.role.Name, err)
		// AWS 에 없던 상태였으면 되돌린다. 이미 반영돼 있던 매핑(applied·detach_failed)은 그대로 둔다.
		if target.mapping == nil || target.mapping.AwsStatus == "" || target.mapping.AwsStatus == model.PolicyAwsStatusFailed {
			if cerr := s.removePolicy(ctx, target); cerr != nil && !isAwsNoSuchEntity(cerr) {
				msg += fmt.Sprintf("; compensation (detach) also failed: %v", cerr)
			} else {
				msg += "; AWS change was reverted"
			}
		}
		log.Printf("[ERROR] %s", msg)
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%s", msg)
	}

	result.Result = model.PolicyBindingApplied
	result.AwsStatus = model.PolicyAwsStatusApplied
	result.Message = fmt.Sprintf("policy %s attached to role %s", target.policy.Name, target.role.Name)
	log.Printf("Attached policy %d to role %d (AWS applied)", policyID, roleID)
	return result, nil
}

// DetachPolicyFromRole 역할에서 권한 정책을 해제하고 AWS 에 반영한다. AWS 가 NoSuchEntity 면 이미 해제된 것으로 본다.
func (s *CspPolicyService) DetachPolicyFromRole(roleID, policyID uint) (*model.PolicyBindingResult, error) {
	target, err := s.prepareBinding(roleID, policyID, false)
	if err != nil {
		return nil, err
	}
	ctx := context.TODO()
	result := &model.PolicyBindingResult{CspRoleID: roleID, CspPolicyID: policyID, Warnings: []string{}}

	removed := false // AWS 에서 실제로 지웠는지 (보상 대상)
	if target.dbOnly {
		// ARN 없는 custom(이전 정의)은 AWS 에 반영된 적이 없다. DB 행만 지운다.
		log.Printf("[WARN] detach policy %d from role %d: policy has no AWS entity, removing the mapping only", policyID, roleID)
	} else if awsErr := s.removePolicy(ctx, target); awsErr != nil {
		if !isAwsNoSuchEntity(awsErr) {
			msg := fmt.Sprintf("AWS detach of policy %s from role %s failed: %v", target.policy.Name, target.role.Name, awsErr)
			log.Printf("[WARN] %s", msg)
			if err := s.saveBindingStatus(target, model.PolicyAwsStatusDetachFailed, awsErr.Error(), nil); err != nil {
				return nil, bindingErr(http.StatusInternalServerError, "db_error", "%s; failed to record status: %v", msg, err)
			}
			result.Result = model.PolicyBindingDetachFailed
			result.AwsStatus = model.PolicyAwsStatusDetachFailed
			result.ErrorCode = "csp_error"
			result.Message = msg
			result.Warnings = append(result.Warnings, msg)
			return result, nil
		}
		log.Printf("[WARN] detach policy %s from role %s: not attached in AWS (NoSuchEntity), removing the mapping", target.policy.Name, target.role.Name)
	} else {
		removed = true
	}

	if err := s.cspPolicyRepo.DetachPolicyFromRole(roleID, policyID); err != nil {
		msg := fmt.Sprintf("policy %s was detached from role %s in AWS but the database update failed: %v", target.policy.Name, target.role.Name, err)
		if removed {
			if cerr := s.applyPolicy(ctx, target); cerr != nil {
				msg += fmt.Sprintf("; compensation (re-attach) also failed: %v", cerr)
			} else {
				msg += "; AWS change was reverted"
			}
		}
		log.Printf("[ERROR] %s", msg)
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%s", msg)
	}

	result.Result = model.PolicyBindingDetached
	result.Message = fmt.Sprintf("policy %s detached from role %s", target.policy.Name, target.role.Name)
	log.Printf("Detached policy %d from role %d (AWS removed=%t)", policyID, roleID, removed)
	return result, nil
}

// prepareBinding 가드. 여기서 실패하면 아무것도 기록하지 않는다.
func (s *CspPolicyService) prepareBinding(roleID, policyID uint, attach bool) (*policyBindingTarget, error) {
	action := "changed by detachPolicy"
	if attach {
		action = "changed by attachPolicy"
	}

	role, err := s.cspRoleRepo.GetRoleByID(roleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bindingErr(http.StatusNotFound, "not_found", "CSP role not found with ID: %d", roleID)
		}
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to get CSP role: %v", err)
	}
	policy, err := s.cspPolicyRepo.GetByID(policyID)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to get CSP policy: %v", err)
	}
	if policy == nil {
		return nil, bindingErr(http.StatusNotFound, "not_found", "CSP policy not found with ID: %d", policyID)
	}
	mapping, err := s.cspPolicyRepo.GetRolePolicyMapping(roleID, policyID)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	if !attach && mapping == nil {
		return nil, bindingErr(http.StatusNotFound, "not_found", "policy %d is not attached to CSP role %d", policyID, roleID)
	}

	if role.CspType != "aws" {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "CSP role %s is %s; only aws is supported", role.Name, role.CspType)
	}
	// 관리용 Role 보호 (409)
	if err := guardNotManagementRole(s.cspIdpConfigRepo, role.Name, role.IamIdentifier, action); err != nil {
		if IsCspRoleProtectedError(err) {
			return nil, bindingErr(http.StatusConflict, "protected", "%s", err.Error())
		}
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	// TECH-049 (c): IAM 이 관리하는 mciam-* Role 에만 쓴다 (AWS 실제 이름 기준)
	if !strings.HasPrefix(awsRoleName(role), constants.CspRoleNamePrefix) {
		return nil, bindingErr(http.StatusForbidden, "forbidden_policy", "CSP role %s is not an IAM-managed role; %s is only allowed on %s* roles",
			role.Name, strings.TrimPrefix(action, "changed by "), constants.CspRoleNamePrefix)
	}

	target := &policyBindingTarget{role: role, policy: policy, mapping: mapping}
	switch policy.PolicyType {
	case model.PolicyTypeManaged:
		if strings.TrimSpace(policy.PolicyArn) == "" {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "managed policy %s has no policy_arn", policy.Name)
		}
		if attach && forbiddenManagedPolicyArn.MatchString(policy.PolicyArn) {
			return nil, bindingErr(http.StatusForbidden, "forbidden_policy", "policy %s cannot be attached through IAM (privilege escalation guard)", policy.PolicyArn)
		}
	case model.PolicyTypeInline:
		if attach {
			// inline 은 Role 1개 전용이다. 여러 Role 이 같은 권한을 쓰면 custom(고객 관리형)을 쓴다 (IAM-TECH-052 D1)
			others, err := s.otherRolesAttached(policyID, roleID)
			if err != nil {
				return nil, bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
			}
			if others > 0 {
				return nil, bindingErr(http.StatusBadRequest, "invalid_request", "inline policy %s is already attached to another CSP role; inline policies belong to one role, use a custom (customer managed) policy to share permissions", policy.Name)
			}
		}
		if !awsInlinePolicyNamePattern.MatchString(policy.Name) {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "invalid AWS inline policy name %q: 1-128 characters of letters, digits and +=,.@_-", policy.Name)
		}
		if attach {
			if len(policy.PolicyDoc) == 0 {
				return nil, bindingErr(http.StatusBadRequest, "invalid_request", "inline policy %s has no policy_doc", policy.Name)
			}
			if reason := forbiddenInlineStatement(policy.PolicyDoc); reason != "" {
				return nil, bindingErr(http.StatusForbidden, "forbidden_policy", "inline policy %s cannot be attached through IAM: %s", policy.Name, reason)
			}
			doc, err := json.Marshal(policy.PolicyDoc)
			if err != nil {
				return nil, bindingErr(http.StatusBadRequest, "invalid_request", "inline policy %s has an invalid policy_doc: %v", policy.Name, err)
			}
			target.inlineDoc = string(doc)
		}
	case model.PolicyTypeCustom:
		// 고객 관리형: ARN 으로 연결·해제한다. 문서 거부 검사는 정의 생성·수정 때 한다 (IAM-TECH-052)
		if strings.TrimSpace(policy.PolicyArn) == "" {
			if attach {
				return nil, bindingErr(http.StatusBadRequest, "invalid_request", "custom policy %s has no policy_arn; create it again so that IAM creates it in AWS", policy.Name)
			}
			target.dbOnly = true
			return target, nil
		}
	default:
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "unsupported policy type %q", policy.PolicyType)
	}

	cfg, err := s.resolveBindingIdpConfig(role, attach)
	if err != nil {
		return nil, err
	}
	// 다른 계정의 정책을 붙이지 않는다
	if policy.CspAccountID != cfg.CspAccountID || (role.CspAccountID != nil && *role.CspAccountID != policy.CspAccountID) {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "policy %s belongs to CSP account %d but role %s belongs to CSP account %d",
			policy.Name, policy.CspAccountID, role.Name, cfg.CspAccountID)
	}
	target.idpConfig = cfg
	return target, nil
}

// resolveBindingIdpConfig 대상 Role 에 AWS 를 쓸 관리용 자격증명의 IdP Config 를 정한다 (IAM-BUG-057).
//  1. 소속 Config 가 활성 OIDC(role_arn 있음)이면 그것
//  2. 소속 Config 가 SAML 이거나 없으면, 같은 CSP 계정의 활성 aws OIDC Config 가 1개일 때 그것 (계정도 모르면 전체에서 1개일 때). 대체하면 WARN
//     소속 Config 가 비활성이면: 정리 작업(detach·CspRole 삭제)은 같은 규칙으로 대체하고,
//     새 권한을 붙이는 작업(attach·inline 문서 반영, grant=true)은 400 이다 (관리자가 끈 연동을 우회하지 않는다)
//  3. 그 밖에는 400 (PUT /api/roles/csp/id/:id 의 cspIdpConfigId 로 지정하도록 안내)
func (s *CspPolicyService) resolveBindingIdpConfig(role *model.CspRole, grant bool) (*model.CspIdpConfig, error) {
	usable := func(cfg *model.CspIdpConfig) bool {
		return cfg != nil && cfg.IsActive && cfg.AuthMethod == model.AuthMethodOIDC && cfg.CspAccount != nil &&
			cfg.CspAccount.CspType == "aws" && cfg.Config != nil && cfg.Config["role_arn"] != ""
	}
	accountID := role.CspAccountID
	reason := "has no IdP config"
	if role.CspIdpConfigID != nil {
		cfg, err := s.cspIdpConfigRepo.GetByID(*role.CspIdpConfigID)
		if err != nil {
			return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to get IdP config: %v", err)
		}
		if usable(cfg) {
			return cfg, nil
		}
		if grant && cfg != nil && !cfg.IsActive {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request",
				"the IdP config %d (%s) of CSP role %s is inactive; activate the IdP config, or set another cspIdpConfigId with PUT /api/roles/csp/id/%d",
				cfg.ID, cfg.Name, role.Name, role.ID)
		}
		reason = fmt.Sprintf("IdP config %d is missing, inactive, not OIDC or has no role_arn", *role.CspIdpConfigID)
		if cfg != nil && accountID == nil {
			accountID = &cfg.CspAccountID
		}
	}
	configs, err := s.cspIdpConfigRepo.GetActiveConfigs()
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to list IdP configs: %v", err)
	}
	var candidates []*model.CspIdpConfig
	for _, cfg := range configs {
		if usable(cfg) && (accountID == nil || cfg.CspAccountID == *accountID) {
			candidates = append(candidates, cfg)
		}
	}
	scope := "in total"
	if accountID != nil {
		scope = fmt.Sprintf("for CSP account %d", *accountID)
	}
	if len(candidates) != 1 {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request",
			"CSP role %s %s and %d active aws OIDC IdP configs were found %s; set cspIdpConfigId with PUT /api/roles/csp/id/%d first",
			role.Name, reason, len(candidates), scope, role.ID)
	}
	log.Printf("[WARN] CSP role %s %s, using the only active aws OIDC IdP config %s (id=%d)", role.Name, reason, scope, candidates[0].ID)
	return candidates[0], nil
}

// forbiddenInlineStatement Allow statement 의 Action 이 * 이거나 iam:* 이면, 또는 NotAction 을 쓰면 사유를 돌려준다.
func forbiddenInlineStatement(doc map[string]interface{}) string {
	var statements []interface{}
	switch st := doc["Statement"].(type) {
	case []interface{}:
		statements = st
	case map[string]interface{}:
		statements = []interface{}{st}
	}
	for _, raw := range statements {
		stmt, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if effect, _ := stmt["Effect"].(string); !strings.EqualFold(effect, "Allow") {
			continue
		}
		if _, ok := stmt["NotAction"]; ok {
			return "Allow statement with NotAction"
		}
		var actions []string
		switch a := stmt["Action"].(type) {
		case string:
			actions = []string{a}
		case []interface{}:
			for _, v := range a {
				if str, ok := v.(string); ok {
					actions = append(actions, str)
				}
			}
		}
		for _, a := range actions {
			if a == "*" || strings.EqualFold(a, "iam:*") {
				return fmt.Sprintf("Allow statement with Action %q", a)
			}
		}
	}
	return ""
}

// applyPolicy AWS 에 정책을 연결한다 (managed: AttachRolePolicy, inline: PutRolePolicy). 두 호출 모두 멱등이다.
func (s *CspPolicyService) applyPolicy(ctx context.Context, t *policyBindingTarget) error {
	api, err := s.awsPolicyClientFor(ctx, t.idpConfig)
	if err != nil {
		return err
	}
	switch t.policy.PolicyType {
	case model.PolicyTypeManaged, model.PolicyTypeCustom:
		_, err = api.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(awsRoleName(t.role)), PolicyArn: aws.String(t.policy.PolicyArn)})
	case model.PolicyTypeInline:
		doc := t.inlineDoc
		if doc == "" {
			b, merr := json.Marshal(t.policy.PolicyDoc)
			if merr != nil {
				return merr
			}
			doc = string(b)
		}
		_, err = api.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(awsRoleName(t.role)), PolicyName: aws.String(t.policy.Name), PolicyDocument: aws.String(doc)})
	}
	return err
}

// removePolicy AWS 에서 정책을 해제한다 (managed: DetachRolePolicy, inline: DeleteRolePolicy).
func (s *CspPolicyService) removePolicy(ctx context.Context, t *policyBindingTarget) error {
	api, err := s.awsPolicyClientFor(ctx, t.idpConfig)
	if err != nil {
		return err
	}
	switch t.policy.PolicyType {
	case model.PolicyTypeManaged, model.PolicyTypeCustom:
		_, err = api.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(awsRoleName(t.role)), PolicyArn: aws.String(t.policy.PolicyArn)})
	case model.PolicyTypeInline:
		_, err = api.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(awsRoleName(t.role)), PolicyName: aws.String(t.policy.Name)})
	}
	return err
}

// saveBindingStatus 매핑 행이 없으면 만들고, 있으면 상태만 갱신한다.
func (s *CspPolicyService) saveBindingStatus(t *policyBindingTarget, status, lastError string, appliedAt *time.Time) error {
	if t.mapping == nil {
		return s.cspPolicyRepo.CreateRolePolicyMapping(&model.CspRolePolicyMapping{
			CspRoleID: t.role.ID, CspPolicyID: t.policy.ID, AwsStatus: status, LastError: lastError, AppliedAt: appliedAt,
		})
	}
	return s.cspPolicyRepo.UpdateRolePolicyMappingStatus(t.role.ID, t.policy.ID, status, lastError, appliedAt)
}

// awsPolicyClientFor 소속 IdP Config 의 관리용 Role(role_arn)을 assume 한 AWS IAM 클라이언트. 테스트는 awsPolicyClient 를 바꿔 끼운다.
func (s *CspPolicyService) awsPolicyClientFor(ctx context.Context, cfg *model.CspIdpConfig) (awsPolicyAPI, error) {
	if s.awsPolicyClient != nil {
		return s.awsPolicyClient(cfg)
	}
	// Config 별 캐시를 공유한다 (IAM-TECH-045 M2)
	return managementIamClient(ctx, s.cspIdpConfigService, cfg)
}

// GetRolePoliciesWithStatus 역할에 연결된 정책과 매핑의 AWS 반영 상태
func (s *CspPolicyService) GetRolePoliciesWithStatus(roleID uint) ([]*model.CspRolePolicy, error) {
	policies, err := s.cspPolicyRepo.GetPoliciesByRoleID(roleID)
	if err != nil {
		return nil, fmt.Errorf("failed to get policies by role: %w", err)
	}
	mappings, err := s.cspPolicyRepo.ListRolePolicyMappings(roleID)
	if err != nil {
		return nil, err
	}
	byPolicy := make(map[uint]*model.CspRolePolicyMapping, len(mappings))
	for _, m := range mappings {
		byPolicy[m.CspPolicyID] = m
	}
	out := make([]*model.CspRolePolicy, 0, len(policies))
	for _, p := range policies {
		item := &model.CspRolePolicy{CspPolicy: p}
		if m := byPolicy[p.ID]; m != nil {
			item.AwsStatus, item.LastError, item.AppliedAt = m.AwsStatus, m.LastError, m.AppliedAt
		}
		out = append(out, item)
	}
	return out, nil
}

// otherRolesAttached 이 정책이 roleID 가 아닌 Role 에 연결된 매핑 수
func (s *CspPolicyService) otherRolesAttached(policyID, roleID uint) (int, error) {
	mappings, err := s.cspPolicyRepo.ListMappingsByPolicyID(policyID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range mappings {
		if m.CspRoleID != roleID {
			n++
		}
	}
	return n, nil
}

// awsRoleName AWS 에서 쓰는 Role 이름. 저장된 ARN(iam_identifier)의 마지막 경로를 쓰고, 없으면 레코드 이름을 쓴다.
// 레코드 이름이 바뀌어도(이전 동작) 실제 AWS Role 을 가리킨다 (IAM-BUG-057).
func awsRoleName(role *model.CspRole) string {
	if strings.HasPrefix(role.IamIdentifier, "arn:") && strings.Contains(role.IamIdentifier, ":role/") {
		return roleNameFromArn(role.IamIdentifier)
	}
	return role.Name
}
