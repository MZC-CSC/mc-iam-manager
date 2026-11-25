package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/m-cmp/mc-iam-manager/model"
	"gorm.io/gorm"
)

// CspRole 삭제 (IAM-BUG-057, DELETE /api/roles/csp/id/:roleId)
//
//  1. 관리용 Role 이면 409
//  2. IAM 이 연결한 정책을 AWS 에서 해제한다 (출처와 관계없이, IAM 이 붙인 것은 IAM 이 뗀다)
//     - 해제가 실패하면 그 연결을 detach_failed 로 남기고 200 + result·warnings 로 멈춘다. CspRole·Master 매핑은 유지, 재요청으로 이어서
//  3. 출처가 created(IAM 이 만든 Role)면
//     - 사전 확인: IAM 밖 정책이나 instance profile 이 있으면 409 + 목록, 아무것도 지우지 않는다
//     - DeleteRole. NoSuchEntity 는 성공, DeleteConflict 는 409(DB 유지)
//     - 조회가 AccessDenied 면 아무것도 바꾸지 않고 502(빠진 권한 이름)
//  4. registered·빈 값(이미 있던 Role 을 등록)이면 AWS Role 은 지우지 않고 IAM 등록만 해제한다
//  5. Master 매핑·정책 매핑·CspRole 행을 한 트랜잭션에서 지운다
//
// AWS Role 이름은 저장된 ARN 에서 얻는다(awsRoleName). 자격증명은 소속 IdP Config 의 관리용 Role(resolveBindingIdpConfig).

// IAM 이 실제로 AWS 에 연결했을 수 있는 매핑 (failed 는 AWS 에 없다)
func appliedByIam(m *model.CspRolePolicyMapping) bool {
	return m.AwsStatus == model.PolicyAwsStatusApplied || m.AwsStatus == model.PolicyAwsStatusDetachFailed
}

// DeleteCspRole CspRole 을 지운다. 다 지우면 (nil, nil), 정책 해제가 실패해 멈추면 (result, nil), 거절이면 (nil, PolicyBindingError).
func (s *CspPolicyService) DeleteCspRole(roleID uint) (*model.CspRoleDeleteResult, error) {
	role, err := s.cspRoleRepo.GetRoleByID(roleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bindingErr(http.StatusNotFound, "not_found", "CSP role not found with ID: %d", roleID)
		}
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to get CSP role: %v", err)
	}
	if err := guardNotManagementRole(s.cspIdpConfigRepo, role.Name, role.IamIdentifier, "deleted"); err != nil {
		if IsCspRoleProtectedError(err) {
			return nil, bindingErr(http.StatusConflict, "protected", "%s", err.Error())
		}
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	if role.CspType != "aws" {
		return nil, s.deleteCspRoleRows(roleID)
	}

	mappings, err := s.cspPolicyRepo.ListRolePolicyMappings(roleID)
	if err != nil {
		return nil, bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	created := role.Origin == model.CspRoleOriginCreated
	// 해제할 연결: IAM 이 반영한 것(applied·detach_failed). IAM 이 만든 Role 은 상태가 빈 이전 연결도 해제를 시도한다(Role 을 지울 것이므로)
	var toDetach []*model.CspRolePolicyMapping
	for _, m := range mappings {
		if appliedByIam(m) || (created && m.AwsStatus == "") {
			toDetach = append(toDetach, m)
		}
	}
	if !created && len(toDetach) == 0 {
		// 이미 있던 Role 이고 IAM 이 붙인 정책이 없다: AWS 를 부르지 않고 등록만 해제한다 (Config 불필요)
		log.Printf("[INFO] CSP role %s (origin=%q): unregistering only, AWS role is kept", role.Name, role.Origin)
		return nil, s.deleteCspRoleRows(roleID)
	}

	cfg, err := s.resolveBindingIdpConfig(role, false)
	if err != nil {
		return nil, err
	}
	ctx := context.TODO()
	api, err := s.awsPolicyClientFor(ctx, cfg)
	if err != nil {
		return nil, bindingErr(http.StatusBadGateway, "csp_error", "%v", err)
	}
	name := awsRoleName(role)

	policies := map[uint]*model.CspPolicy{}
	for _, m := range toDetach {
		p, err := s.cspPolicyRepo.GetByID(m.CspPolicyID)
		if err != nil {
			return nil, bindingErr(http.StatusInternalServerError, "db_error", "failed to get CSP policy %d: %v", m.CspPolicyID, err)
		}
		if p != nil {
			policies[m.CspPolicyID] = p
		}
	}

	roleGone := false
	if created {
		state, err := listAwsRoleState(ctx, api, name)
		switch {
		case err == nil:
			if blocked := unmanagedOnRole(state, toDetach, policies); blocked != nil {
				return nil, blocked
			}
		case isAwsNoSuchEntity(err):
			roleGone = true
			log.Printf("[WARN] CSP role %s no longer exists in AWS, removing the record only", name)
		default:
			return nil, err
		}
	}

	if !roleGone {
		if result := s.detachForDelete(ctx, role, cfg, toDetach, policies); result != nil {
			return result, nil
		}
		if created {
			if _, err := api.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}); err != nil {
				var conflict *iamtypes.DeleteConflictException
				switch {
				case isAwsNoSuchEntity(err):
					log.Printf("[WARN] CSP role %s no longer exists in AWS, removing the record only", name)
				case errors.As(err, &conflict):
					return nil, bindingErr(http.StatusConflict, "conflict", "AWS refused to delete role %s (something is still attached): %v", name, err)
				default:
					return nil, bindingErr(http.StatusBadGateway, "csp_error", "AWS DeleteRole %s failed: %v", name, err)
				}
			}
		} else {
			log.Printf("[INFO] CSP role %s (origin=%q): IAM policies detached, AWS role is kept", role.Name, role.Origin)
		}
	}
	return nil, s.deleteCspRoleRows(roleID)
}

// awsRoleState AWS 에 실제로 붙어 있는 것
type awsRoleState struct {
	attached []string // 관리형(AWS·고객) 정책 ARN
	inline   []string
	profiles []string
}

// listAwsRoleState 페이지를 끝까지 읽는다. AccessDenied 는 빠진 권한 이름을 담은 502.
func listAwsRoleState(ctx context.Context, api awsPolicyAPI, name string) (*awsRoleState, error) {
	denied := func(action string, err error) error {
		return bindingErr(http.StatusBadGateway, "csp_error",
			"the management role is not allowed to call iam:%s on role %s; add the permission and retry (nothing was changed): %v", action, name, err)
	}
	st := &awsRoleState{}
	var marker *string
	for {
		out, err := api.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(name), Marker: marker})
		if err != nil {
			if isAwsAccessDenied(err) {
				return nil, denied("ListAttachedRolePolicies", err)
			}
			return nil, err
		}
		for _, p := range out.AttachedPolicies {
			st.attached = append(st.attached, aws.ToString(p.PolicyArn))
		}
		if !out.IsTruncated || out.Marker == nil {
			break
		}
		marker = out.Marker
	}
	marker = nil
	for {
		out, err := api.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(name), Marker: marker})
		if err != nil {
			if isAwsAccessDenied(err) {
				return nil, denied("ListRolePolicies", err)
			}
			return nil, err
		}
		st.inline = append(st.inline, out.PolicyNames...)
		if !out.IsTruncated || out.Marker == nil {
			break
		}
		marker = out.Marker
	}
	marker = nil
	for {
		out, err := api.ListInstanceProfilesForRole(ctx, &iam.ListInstanceProfilesForRoleInput{RoleName: aws.String(name), Marker: marker})
		if err != nil {
			if isAwsAccessDenied(err) {
				return nil, denied("ListInstanceProfilesForRole", err)
			}
			return nil, err
		}
		for _, p := range out.InstanceProfiles {
			st.profiles = append(st.profiles, aws.ToString(p.InstanceProfileName))
		}
		if !out.IsTruncated || out.Marker == nil {
			break
		}
		marker = out.Marker
	}
	return st, nil
}

// unmanagedOnRole IAM 이 연결하지 않은 정책·instance profile 이 있으면 409(목록 포함)
func unmanagedOnRole(st *awsRoleState, toDetach []*model.CspRolePolicyMapping, policies map[uint]*model.CspPolicy) error {
	managed, inline := map[string]bool{}, map[string]bool{}
	for _, m := range toDetach {
		p := policies[m.CspPolicyID]
		if p == nil {
			continue
		}
		if p.PolicyType == model.PolicyTypeInline {
			inline[p.Name] = true
		} else if p.PolicyArn != "" {
			managed[p.PolicyArn] = true
		}
	}
	var otherManaged, otherInline []string
	for _, a := range st.attached {
		if !managed[a] {
			otherManaged = append(otherManaged, a)
		}
	}
	for _, n := range st.inline {
		if !inline[n] {
			otherInline = append(otherInline, n)
		}
	}
	if len(otherManaged)+len(otherInline)+len(st.profiles) == 0 {
		return nil
	}
	sort.Strings(otherManaged)
	sort.Strings(otherInline)
	var parts []string
	if len(otherManaged) > 0 {
		parts = append(parts, "attached policies "+strings.Join(otherManaged, ", "))
	}
	if len(otherInline) > 0 {
		parts = append(parts, "inline policies "+strings.Join(otherInline, ", "))
	}
	if len(st.profiles) > 0 {
		parts = append(parts, "instance profiles "+strings.Join(st.profiles, ", "))
	}
	e := bindingErr(http.StatusConflict, "conflict",
		"the AWS role has %s that IAM did not attach; remove them in AWS first (nothing was changed)", strings.Join(parts, "; "))
	e.Details = map[string]interface{}{
		"unmanagedAttachedPolicies": nonNil(otherManaged),
		"unmanagedInlinePolicies":   nonNil(otherInline),
		"instanceProfiles":          nonNil(st.profiles),
	}
	return e
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// detachForDelete IAM 연결을 AWS 에서 해제한다. 해제한 연결 행은 바로 지운다(재요청 시 이어서).
// 하나라도 실패하면 결과를 돌려주고 호출자는 Role 삭제를 멈춘다.
func (s *CspPolicyService) detachForDelete(ctx context.Context, role *model.CspRole, cfg *model.CspIdpConfig,
	toDetach []*model.CspRolePolicyMapping, policies map[uint]*model.CspPolicy) *model.CspRoleDeleteResult {
	result := &model.CspRoleDeleteResult{Result: model.PolicyBindingDetachFailed, CspRoleID: role.ID, BindingResults: []*model.PolicyBindingResult{}, Warnings: []string{}}
	failed := false
	for _, m := range toDetach {
		p := policies[m.CspPolicyID]
		if p == nil {
			continue // 정책 정의가 없으면 마지막 트랜잭션에서 행만 지운다
		}
		br := &model.PolicyBindingResult{CspRoleID: role.ID, CspPolicyID: p.ID, Warnings: []string{}}
		target := &policyBindingTarget{role: role, policy: p, idpConfig: cfg, mapping: m}
		if p.PolicyType == model.PolicyTypeCustom && p.PolicyArn == "" {
			target.dbOnly = true
		}
		var err error
		if !target.dbOnly {
			err = s.removePolicy(ctx, target)
		}
		if err != nil && !isAwsNoSuchEntity(err) {
			failed = true
			msg := fmt.Sprintf("AWS detach of policy %s from role %s failed: %v", p.Name, awsRoleName(role), err)
			log.Printf("[WARN] %s", msg)
			if serr := s.saveBindingStatus(target, model.PolicyAwsStatusDetachFailed, err.Error(), nil); serr != nil {
				msg += fmt.Sprintf("; failed to record status: %v", serr)
			}
			br.Result, br.AwsStatus, br.ErrorCode, br.Message = model.PolicyBindingDetachFailed, model.PolicyAwsStatusDetachFailed, "csp_error", msg
			br.Warnings = append(br.Warnings, msg)
			result.Warnings = append(result.Warnings, msg)
		} else {
			if derr := s.cspPolicyRepo.DetachPolicyFromRole(role.ID, p.ID); derr != nil {
				log.Printf("[WARN] policy %s detached in AWS but the mapping row was not removed: %v (removed with the role)", p.Name, derr)
			}
			br.Result, br.Message = model.PolicyBindingDetached, fmt.Sprintf("policy %s detached from role %s", p.Name, awsRoleName(role))
		}
		result.BindingResults = append(result.BindingResults, br)
	}
	if !failed {
		return nil
	}
	result.Warnings = append(result.Warnings, fmt.Sprintf("CSP role %s was not deleted; fix the cause and send the delete request again", role.Name))
	return result
}

// deleteCspRoleRows Master 매핑·정책 매핑·CspRole 행을 한 트랜잭션에서 지운다
func (s *CspPolicyService) deleteCspRoleRows(roleID uint) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("csp_role_id = ?", roleID).Delete(&model.RoleMasterCspRoleMapping{}).Error; err != nil {
			return fmt.Errorf("failed to delete master role mappings: %w", err)
		}
		if err := tx.Where("csp_role_id = ?", roleID).Delete(&model.CspRolePolicyMapping{}).Error; err != nil {
			return fmt.Errorf("failed to delete policy mappings: %w", err)
		}
		if err := tx.Where("id = ?", roleID).Delete(&model.CspRole{}).Error; err != nil {
			return fmt.Errorf("failed to delete CSP role: %w", err)
		}
		return nil
	})
	if err != nil {
		return bindingErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	return nil
}
