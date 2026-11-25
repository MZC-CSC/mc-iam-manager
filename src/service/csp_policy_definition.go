package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
)

// CSP 정책 정의(생성·수정·삭제)의 AWS 반영 (IAM-TECH-052, 계획 4b)
//
// 타입
//   - inline: 문서를 Role 1개 안에 사본으로 저장한다. 정의 생성은 DB 만, Role 반영은 attach 때 한다.
//     문서를 고치면 DB 를 먼저 갱신하고 연결된 Role 1개에 다시 저장한다. 실패는 매핑 상태로 남긴다 (4a 방식).
//   - managed: AWS 관리형(arn:aws:iam::aws:policy/...). 연결·해제만 하고 문서는 고칠 수 없다.
//   - custom: 고객 관리형. IAM 이 만들거나(ARN 없이 문서로 생성 → CreatePolicy) ARN 으로 등록한다.
//     생성·문서 수정·삭제는 이름이 mciam-* 인 정책만 한다 (IAM 밖에서 쓰는 정책을 바꾸지 않는다).
//     AWS 에 먼저 반영하고 성공해야 DB 를 바꾼다. AWS 가 실패하면 502, DB 는 그대로다.
//   - 연결 중에는 inline 이름, managed·custom ARN 을 바꾸지 않는다 (409). 바꾸면 detach 가 다른 대상을 가리켜 AWS 에 정책이 남는다.

// AWS 고객 관리형 정책 이름 규칙: 1~128자, 영숫자와 +=,.@_-
var awsManagedPolicyNamePattern = regexp.MustCompile(`^[\w+=,.@-]{1,128}$`)

// AWS 정책 버전은 정책당 5개까지다
const awsMaxPolicyVersions = 5

// CreateCspPolicy CSP 정책 정의 생성
func (s *CspPolicyService) CreateCspPolicy(req *model.CreateCspPolicyRequest) (*model.CspPolicy, error) {
	account, err := s.cspAccountRepo.GetByID(req.CspAccountID)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to get CSP account: %v", err)
	}
	if account == nil {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "CSP account not found with ID: %d", req.CspAccountID)
	}

	policy := &model.CspPolicy{
		Name:         strings.TrimSpace(req.Name),
		CspAccountID: req.CspAccountID,
		PolicyType:   req.PolicyType,
		PolicyArn:    strings.TrimSpace(req.PolicyArn),
		PolicyDoc:    req.PolicyDoc,
		Description:  req.Description,
	}

	createInAws := false
	switch req.PolicyType {
	case model.PolicyTypeInline:
		if policy.PolicyArn != "" {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "inline policy does not take policy_arn")
		}
		if !awsInlinePolicyNamePattern.MatchString(policy.Name) {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "invalid AWS inline policy name %q: 1-128 characters of letters, digits and +=,.@_-", policy.Name)
		}
		if err := validatePolicyDocument(policy.Name, policy.PolicyDoc); err != nil {
			return nil, err
		}
	case model.PolicyTypeManaged:
		if !model.IsAwsManagedPolicyArn(policy.PolicyArn) {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "managed policy needs an AWS managed policy ARN (arn:aws:iam::aws:policy/...); use custom for customer managed policies")
		}
		if policy.PolicyDoc != nil {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "AWS managed policy documents cannot be set")
		}
	case model.PolicyTypeCustom:
		if policy.PolicyArn != "" {
			// 이미 AWS 에 있는 고객 관리형 정책 등록 (이름과 관계없이 연결·해제용)
			if err := validateCustomPolicyArn(policy, account); err != nil {
				return nil, err
			}
			if policy.PolicyDoc != nil {
				return nil, bindingErr(http.StatusBadRequest, "invalid_request", "policy_doc is not used when registering an existing policy by policy_arn")
			}
			break
		}
		// ARN 없이 문서로 만들면 IAM 이 AWS 에 생성한다
		if !strings.HasPrefix(policy.Name, constants.CspRoleNamePrefix) {
			policy.Name = constants.CspRoleNamePrefix + policy.Name
		}
		if !awsManagedPolicyNamePattern.MatchString(policy.Name) {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "invalid AWS policy name %q: 1-128 characters of letters, digits and +=,.@_-", policy.Name)
		}
		if err := validatePolicyDocument(policy.Name, policy.PolicyDoc); err != nil {
			return nil, err
		}
		if account.GetAccountID() == "" {
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "CSP account %d has no account_id", account.ID)
		}
		createInAws = true
	default:
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "invalid policy type %q: must be one of inline, managed, custom", req.PolicyType)
	}

	exists, err := s.cspPolicyRepo.ExistsByNameAndAccountID(policy.Name, policy.CspAccountID)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to check policy existence: %v", err)
	}
	if exists {
		return nil, bindingErr(http.StatusConflict, "conflict", "policy with name '%s' already exists for this account", policy.Name)
	}

	if !createInAws {
		if err := s.cspPolicyRepo.Create(policy); err != nil {
			return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to create policy: %v", err)
		}
		log.Printf("Created CSP policy: %s (type: %s)", policy.Name, policy.PolicyType)
		return policy, nil
	}

	// custom: AWS 먼저 (같은 이름이 있으면 409, 덮어쓰거나 가져오지 않는다)
	ctx := context.TODO()
	api, err := s.accountPolicyClient(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	arn := customPolicyArn(account.GetAccountID(), policy.Name)
	if _, err := api.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)}); err == nil {
		return nil, bindingErr(http.StatusConflict, "conflict", "policy %s already exists in AWS; register it with policy_arn or sync instead", arn)
	} else if !isAwsNoSuchEntity(err) {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "failed to check AWS policy %s: %v", arn, err)
	}
	doc, _ := json.Marshal(policy.PolicyDoc)
	input := &iam.CreatePolicyInput{PolicyName: aws.String(policy.Name), PolicyDocument: aws.String(string(doc))}
	if policy.Description != "" {
		input.Description = aws.String(policy.Description)
	}
	out, err := api.CreatePolicy(ctx, input)
	if err != nil {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "AWS CreatePolicy %s failed: %v", policy.Name, err)
	}
	policy.PolicyArn = aws.ToString(out.Policy.Arn)
	if err := s.cspPolicyRepo.Create(policy); err != nil {
		msg := fmt.Sprintf("policy %s was created in AWS but the database insert failed: %v", policy.PolicyArn, err)
		if _, cerr := api.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(policy.PolicyArn)}); cerr != nil {
			msg += fmt.Sprintf("; compensation (DeletePolicy) also failed: %v", cerr)
		} else {
			msg += "; AWS policy was deleted"
		}
		log.Printf("[ERROR] %s", msg)
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%s", msg)
	}
	log.Printf("Created CSP policy: %s (type: custom, AWS %s)", policy.Name, policy.PolicyArn)
	return policy, nil
}

// UpdateCspPolicy CSP 정책 정의 수정
func (s *CspPolicyService) UpdateCspPolicy(id uint, req *model.UpdateCspPolicyRequest) (*model.CspPolicyChangeResult, error) {
	policy, err := s.cspPolicyRepo.GetByID(id)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to get policy: %v", err)
	}
	if policy == nil {
		return nil, bindingErr(http.StatusNotFound, "not_found", "policy not found with ID: %d", id)
	}
	mappings, err := s.cspPolicyRepo.ListMappingsByPolicyID(id)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	// AWS 에 붙어 있을 수 있는 연결 수. attach 가 AWS 에서 실패한(failed) 연결은 AWS 에 없으므로 빼고 센다 (IAM-BUG-058).
	// detach_failed 와 상태가 빈 이전 연결은 AWS 에 남아 있을 수 있어 센다.
	liveMappings := 0
	for _, m := range mappings {
		if m.AwsStatus != model.PolicyAwsStatusFailed {
			liveMappings++
		}
	}
	attached := liveMappings > 0
	result := &model.CspPolicyChangeResult{BindingResults: []*model.PolicyBindingResult{}, Warnings: []string{}}

	values := map[string]interface{}{}
	newName := strings.TrimSpace(req.Name)
	if newName != "" && newName != policy.Name {
		switch {
		case policy.PolicyType == model.PolicyTypeCustom && policy.PolicyArn != "":
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "AWS policies cannot be renamed; create a new custom policy instead")
		case policy.PolicyType == model.PolicyTypeInline && attached:
			return nil, bindingErr(http.StatusConflict, "conflict", "inline policy %s is attached to %d CSP role(s) in AWS; detach it before renaming", policy.Name, liveMappings)
		case policy.PolicyType == model.PolicyTypeInline && !awsInlinePolicyNamePattern.MatchString(newName):
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "invalid AWS inline policy name %q", newName)
		}
		exists, err := s.cspPolicyRepo.ExistsByNameAndAccountID(newName, policy.CspAccountID)
		if err != nil {
			return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to check policy existence: %v", err)
		}
		if exists {
			return nil, bindingErr(http.StatusConflict, "conflict", "policy with name '%s' already exists", newName)
		}
		values["name"] = newName
	}

	newArn := strings.TrimSpace(req.PolicyArn)
	if newArn != "" && newArn != policy.PolicyArn {
		switch {
		case attached:
			return nil, bindingErr(http.StatusConflict, "conflict", "policy %s is attached to %d CSP role(s) in AWS; detach it before changing policy_arn (connections that failed to apply do not block the change; retry them after fixing)", policy.Name, liveMappings)
		case policy.PolicyType == model.PolicyTypeManaged:
			if !model.IsAwsManagedPolicyArn(newArn) {
				return nil, bindingErr(http.StatusBadRequest, "invalid_request", "managed policy needs an AWS managed policy ARN (arn:aws:iam::aws:policy/...)")
			}
		default:
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "policy_arn of a %s policy cannot be changed; register a new policy instead", policy.PolicyType)
		}
		values["policy_arn"] = newArn
	}
	if req.Description != "" && req.Description != policy.Description {
		values["description"] = req.Description
	}

	var inlineRoleID uint
	if req.PolicyDoc != nil {
		switch policy.PolicyType {
		case model.PolicyTypeManaged:
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "AWS managed policy documents cannot be changed")
		case model.PolicyTypeCustom:
			if err := s.updateCustomPolicyDocument(policy, req.PolicyDoc); err != nil {
				return nil, err
			}
		case model.PolicyTypeInline:
			if err := validatePolicyDocument(policy.Name, req.PolicyDoc); err != nil {
				return nil, err
			}
			if len(mappings) > 1 {
				return nil, bindingErr(http.StatusConflict, "conflict", "inline policy %s is attached to %d CSP roles; keep one role (detach the others) or use a custom policy before changing the document", policy.Name, len(mappings))
			}
			if len(mappings) == 1 {
				inlineRoleID = mappings[0].CspRoleID
			}
		default:
			return nil, bindingErr(http.StatusBadRequest, "invalid_request", "policy type %q does not support documents", policy.PolicyType)
		}
		values["policy_doc"] = mustJSON(req.PolicyDoc)
	}

	if err := s.cspPolicyRepo.UpdatePolicyFields(id, values); err != nil {
		msg := fmt.Sprintf("failed to update policy: %v", err)
		if policy.PolicyType == model.PolicyTypeCustom && req.PolicyDoc != nil {
			msg += "; the new document is already the default version in AWS"
		}
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%s", msg)
	}

	// inline: 연결된 Role 1개에 새 문서를 저장한다. 실패는 매핑 상태로 남기고 attach 재요청으로 맞춘다 (4a)
	if inlineRoleID != 0 {
		res, err := s.AttachPolicyToRole(inlineRoleID, id)
		if err != nil {
			msg := fmt.Sprintf("document saved but applying it to CSP role %d failed: %v", inlineRoleID, err)
			log.Printf("[WARN] %s", msg)
			result.Warnings = append(result.Warnings, msg)
		} else {
			result.BindingResults = append(result.BindingResults, res)
			result.Warnings = append(result.Warnings, res.Warnings...)
		}
	}

	updated, err := s.cspPolicyRepo.GetByID(id)
	if err != nil || updated == nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to reload policy: %v", err)
	}
	result.CspPolicy = updated
	log.Printf("Updated CSP policy: %s (ID: %d)", updated.Name, updated.ID)
	return result, nil
}

// updateCustomPolicyDocument 고객 관리형 정책에 새 기본 버전을 만든다. 버전이 5개면 가장 오래된 비기본 버전을 먼저 지운다.
func (s *CspPolicyService) updateCustomPolicyDocument(policy *model.CspPolicy, doc map[string]interface{}) error {
	if policy.PolicyArn == "" {
		return bindingErr(http.StatusBadRequest, "invalid_request", "custom policy %s has no AWS policy; delete it and create it again", policy.Name)
	}
	if err := guardIamOwnedPolicy(policy, "changed"); err != nil {
		return err
	}
	if err := validatePolicyDocument(policy.Name, doc); err != nil {
		return err
	}
	ctx := context.TODO()
	api, err := s.accountPolicyClient(ctx, policy.CspAccountID)
	if err != nil {
		return err
	}
	versions, err := api.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(policy.PolicyArn)})
	if err != nil {
		return bindingErr(http.StatusBadGateway, "csp_error", "AWS ListPolicyVersions %s failed: %v", policy.PolicyArn, err)
	}
	if len(versions.Versions) >= awsMaxPolicyVersions {
		var oldest *string
		nonDefault := versions.Versions[:0:0]
		for _, v := range versions.Versions {
			if !v.IsDefaultVersion {
				nonDefault = append(nonDefault, v)
			}
		}
		sort.Slice(nonDefault, func(i, j int) bool {
			return aws.ToTime(nonDefault[i].CreateDate).Before(aws.ToTime(nonDefault[j].CreateDate))
		})
		if len(nonDefault) > 0 {
			oldest = nonDefault[0].VersionId
		}
		if oldest != nil {
			if _, err := api.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{PolicyArn: aws.String(policy.PolicyArn), VersionId: oldest}); err != nil {
				return bindingErr(http.StatusBadGateway, "csp_error", "AWS DeletePolicyVersion %s %s failed: %v", policy.PolicyArn, aws.ToString(oldest), err)
			}
			log.Printf("Deleted oldest version %s of policy %s (version limit)", aws.ToString(oldest), policy.PolicyArn)
		}
	}
	body, _ := json.Marshal(doc)
	if _, err := api.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{PolicyArn: aws.String(policy.PolicyArn), PolicyDocument: aws.String(string(body)), SetAsDefault: true}); err != nil {
		return bindingErr(http.StatusBadGateway, "csp_error", "AWS CreatePolicyVersion %s failed: %v", policy.PolicyArn, err)
	}
	return nil
}

// DeleteCspPolicy CSP 정책 정의 삭제. IAM 이 만든 고객 관리형(mciam-*)은 AWS 정책도 지운다.
func (s *CspPolicyService) DeleteCspPolicy(id uint) error {
	policy, err := s.cspPolicyRepo.GetByID(id)
	if err != nil {
		return bindingErr(http.StatusInternalServerError, "db_error", "failed to get policy: %v", err)
	}
	if policy == nil {
		return bindingErr(http.StatusNotFound, "not_found", "policy not found with ID: %d", id)
	}
	mappings, err := s.cspPolicyRepo.ListMappingsByPolicyID(id)
	if err != nil {
		return bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	if len(mappings) > 0 {
		return bindingErr(http.StatusConflict, "conflict", "cannot delete policy: %d roles are attached", len(mappings))
	}

	if policy.PolicyType == model.PolicyTypeCustom && policy.PolicyArn != "" && isIamOwnedPolicyName(policy.Name) {
		if err := s.deleteCustomPolicyInAws(policy); err != nil {
			return err
		}
	} else if policy.PolicyType == model.PolicyTypeCustom && policy.PolicyArn != "" {
		// IAM 밖 고객 관리형은 등록만 해제한다 (AWS 정책은 건드리지 않는다)
		log.Printf("[INFO] unregistering custom policy %s (not %s*): AWS policy is kept", policy.PolicyArn, constants.CspRoleNamePrefix)
	}

	if err := s.cspPolicyRepo.Delete(id); err != nil {
		return bindingErr(http.StatusInternalServerError, "db_error", "failed to delete policy: %v", err)
	}
	log.Printf("Deleted CSP policy with ID: %d", id)
	return nil
}

// deleteCustomPolicyInAws IAM 밖 연결이 없을 때만 비기본 버전 → 정책 순으로 지운다. 이미 없으면 성공.
func (s *CspPolicyService) deleteCustomPolicyInAws(policy *model.CspPolicy) error {
	ctx := context.TODO()
	api, err := s.accountPolicyClient(ctx, policy.CspAccountID)
	if err != nil {
		return err
	}
	got, err := api.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(policy.PolicyArn)})
	if err != nil {
		if isAwsNoSuchEntity(err) {
			log.Printf("[WARN] policy %s no longer exists in AWS, removing the record only", policy.PolicyArn)
			return nil
		}
		return bindingErr(http.StatusBadGateway, "csp_error", "AWS GetPolicy %s failed: %v", policy.PolicyArn, err)
	}
	if n := aws.ToInt32(got.Policy.AttachmentCount); n > 0 {
		return bindingErr(http.StatusConflict, "conflict", "policy %s is still attached to %d AWS entities outside IAM; detach them in AWS first", policy.PolicyArn, n)
	}
	versions, err := api.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(policy.PolicyArn)})
	if err != nil {
		return bindingErr(http.StatusBadGateway, "csp_error", "AWS ListPolicyVersions %s failed: %v", policy.PolicyArn, err)
	}
	for _, v := range versions.Versions {
		if v.IsDefaultVersion {
			continue
		}
		if _, err := api.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{PolicyArn: aws.String(policy.PolicyArn), VersionId: v.VersionId}); err != nil && !isAwsNoSuchEntity(err) {
			return bindingErr(http.StatusBadGateway, "csp_error", "AWS DeletePolicyVersion %s %s failed: %v", policy.PolicyArn, aws.ToString(v.VersionId), err)
		}
	}
	if _, err := api.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(policy.PolicyArn)}); err != nil && !isAwsNoSuchEntity(err) {
		return bindingErr(http.StatusBadGateway, "csp_error", "AWS DeletePolicy %s failed: %v", policy.PolicyArn, err)
	}
	log.Printf("Deleted AWS policy %s", policy.PolicyArn)
	return nil
}

// validatePolicyDocument 문서가 있어야 하고 TECH-049 거부 목록에 걸리지 않아야 한다
func validatePolicyDocument(name string, doc map[string]interface{}) error {
	if len(doc) == 0 {
		return bindingErr(http.StatusBadRequest, "invalid_request", "policy %s needs a policy_doc", name)
	}
	if _, ok := doc["Statement"]; !ok {
		return bindingErr(http.StatusBadRequest, "invalid_request", "policy_doc of %s has no Statement", name)
	}
	if reason := forbiddenInlineStatement(doc); reason != "" {
		return bindingErr(http.StatusForbidden, "forbidden_policy", "policy %s is not allowed through IAM: %s", name, reason)
	}
	return nil
}

// validateCustomPolicyArn ARN 등록: 고객 관리형 정책 ARN 이고, 계정·이름이 맞아야 한다
func validateCustomPolicyArn(policy *model.CspPolicy, account *model.CspAccount) error {
	if model.IsAwsManagedPolicyArn(policy.PolicyArn) {
		return bindingErr(http.StatusBadRequest, "invalid_request", "%s is an AWS managed policy; use policy_type managed", policy.PolicyArn)
	}
	parts := strings.SplitN(policy.PolicyArn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "iam" || !strings.HasPrefix(parts[5], "policy/") {
		return bindingErr(http.StatusBadRequest, "invalid_request", "invalid policy ARN %q", policy.PolicyArn)
	}
	if acct := account.GetAccountID(); acct != "" && parts[4] != acct {
		return bindingErr(http.StatusBadRequest, "invalid_request", "policy ARN %s belongs to AWS account %s, not %s", policy.PolicyArn, parts[4], acct)
	}
	if arnName := roleNameFromArn(policy.PolicyArn); policy.Name != arnName {
		return bindingErr(http.StatusBadRequest, "invalid_request", "name %q does not match the policy name in the ARN (%s)", policy.Name, arnName)
	}
	return nil
}

func isIamOwnedPolicyName(name string) bool {
	return strings.HasPrefix(name, constants.CspRoleNamePrefix)
}

// guardIamOwnedPolicy 고객 관리형 정책의 생성·수정·삭제는 mciam-* 만 (IAM 밖에서 쓰는 정책은 바꾸지 않는다)
func guardIamOwnedPolicy(policy *model.CspPolicy, action string) error {
	if !isIamOwnedPolicyName(policy.Name) {
		return bindingErr(http.StatusForbidden, "forbidden_policy", "policy %s is not managed by IAM (%s*) and cannot be %s through IAM", policy.Name, constants.CspRoleNamePrefix, action)
	}
	return nil
}

func customPolicyArn(accountID, name string) string {
	return fmt.Sprintf("arn:aws:iam::%s:policy/%s", accountID, name)
}

func mustJSON(doc map[string]interface{}) string {
	b, _ := json.Marshal(doc)
	return string(b)
}

// accountPolicyClient 정책 계정의 활성 aws OIDC IdP Config 로 관리용 자격증명을 받는다 (1개가 아니면 400)
func (s *CspPolicyService) accountPolicyClient(ctx context.Context, accountID uint) (awsPolicyAPI, error) {
	configs, err := s.cspIdpConfigRepo.GetActiveByAccountID(accountID)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to list IdP configs: %v", err)
	}
	var candidates []*model.CspIdpConfig
	for _, cfg := range configs {
		if cfg.AuthMethod == model.AuthMethodOIDC && cfg.Config != nil && cfg.Config["role_arn"] != "" {
			candidates = append(candidates, cfg)
		}
	}
	if len(candidates) != 1 {
		return nil, bindingErr(http.StatusBadRequest, "invalid_request", "CSP account %d needs exactly one active OIDC IdP config with role_arn (found %d)", accountID, len(candidates))
	}
	api, err := s.awsPolicyClientFor(ctx, candidates[0])
	if err != nil {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "%v", err)
	}
	return api, nil
}
