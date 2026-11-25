package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/m-cmp/mc-iam-manager/model"
	"gorm.io/gorm"
)

// 신뢰 정책 재동기화 (IAM-TECH-045 M2 PR-2)
//
//   - 기대값과 비교해 바꿀 것만 고친다. Federated 가 기대 provider 인 Allow statement 를 IAM 의 것으로 보고 Action·aud 만 맞춘다.
//     다른 statement 와 Condition 키는 보존하고, 맞는 statement 가 없으면 추가한다
//   - dryRun 이 기본이다. 판정이 ok 면 쓰지 않는다(applied=false, changes=[])
//   - registered(빈 origin 포함) Role 은 바꿀 것이 있을 때만 allowRegistered=true 를 요구한다(409 registered_role, 변경안 포함)
//   - 관리용 Role 409, 소속 Config 비활성 400(새 권한 반영 규칙), AWS 에 Role 없음 404, 기대값을 만들 수 없으면 400

// TrustSyncError 재동기화 거절. Result 가 있으면 응답 본문에 함께 넣는다(registered_role 의 변경안 등)
type TrustSyncError struct {
	*PolicyBindingError
	Result *model.TrustPolicySyncResult
}

func trustSyncErr(status int, code, format string, args ...interface{}) *TrustSyncError {
	return &TrustSyncError{PolicyBindingError: bindingErr(status, code, format, args...)}
}

// AsTrustSyncError err 가 TrustSyncError 면 돌려준다
func AsTrustSyncError(err error) (*TrustSyncError, bool) {
	var e *TrustSyncError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

func decodeTrustDoc(doc string) (map[string]interface{}, error) {
	if decoded, err := url.QueryUnescape(doc); err == nil {
		doc = decoded
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		return nil, fmt.Errorf("trust policy is not valid JSON: %w", err)
	}
	return m, nil
}

func listOrOne(v []string) interface{} {
	if len(v) == 1 {
		return v[0]
	}
	out := make([]interface{}, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

// upsertTrustStatements doc 를 고쳐 기대값을 만족하게 한다. 바뀐 내용을 돌려준다
func upsertTrustStatements(doc map[string]interface{}, exps []trustExpectation) []model.TrustPolicyChange {
	var statements []interface{}
	switch st := doc["Statement"].(type) {
	case []interface{}:
		statements = st
	case map[string]interface{}:
		statements = []interface{}{st}
	}
	if _, ok := doc["Version"]; !ok {
		doc["Version"] = "2012-10-17"
	}
	changes := []model.TrustPolicyChange{}
	for _, e := range exps {
		found := false
		for _, raw := range statements {
			st, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			effect, _ := st["Effect"].(string)
			principal, _ := st["Principal"].(map[string]interface{})
			if !strings.EqualFold(effect, "Allow") || principal == nil || !containsFold(stringsOf(principal["Federated"]), e.federated) {
				continue
			}
			found = true
			actions := stringsOf(st["Action"])
			if !containsFold(actions, e.action) {
				actions = append(actions, e.action)
				st["Action"] = listOrOne(actions)
				changes = append(changes, model.TrustPolicyChange{AuthMethod: e.authMethod, Change: "action_added", Detail: e.action})
			}
			cond, _ := st["Condition"].(map[string]interface{})
			if cond == nil {
				cond = map[string]interface{}{}
				st["Condition"] = cond
			}
			for op, kv := range cond {
				if op == "StringEquals" {
					continue
				}
				if km, ok := kv.(map[string]interface{}); ok {
					if old, ok := km[e.audKey]; ok {
						delete(km, e.audKey)
						changes = append(changes, model.TrustPolicyChange{AuthMethod: e.authMethod, Change: "aud_removed", Detail: fmt.Sprintf("%s %s %v", op, e.audKey, old)})
						if len(km) == 0 {
							delete(cond, op)
						}
					}
				}
			}
			se, _ := cond["StringEquals"].(map[string]interface{})
			if se == nil {
				se = map[string]interface{}{}
				cond["StringEquals"] = se
			}
			if current := stringsOf(se[e.audKey]); !containsFold(current, e.aud) {
				se[e.audKey] = e.aud
				changes = append(changes, model.TrustPolicyChange{AuthMethod: e.authMethod, Change: "aud_set", Detail: fmt.Sprintf("%s: %v → %s", e.audKey, current, e.aud)})
			}
		}
		if !found {
			statements = append(statements, map[string]interface{}{
				"Effect":    "Allow",
				"Principal": map[string]interface{}{"Federated": e.federated},
				"Action":    e.action,
				"Condition": map[string]interface{}{"StringEquals": map[string]interface{}{e.audKey: e.aud}},
			})
			changes = append(changes, model.TrustPolicyChange{AuthMethod: e.authMethod, Change: "statement_added",
				Detail: fmt.Sprintf("Allow %s %s with %s=%s", e.federated, e.action, e.audKey, e.aud)})
		}
	}
	doc["Statement"] = statements
	return changes
}

// syncTrustWith 기대값(exps)으로 한 Role 을 재동기화한다. 가드(관리용·aws·Config)는 호출자가 이미 확인했다
func (s *CspRoleService) syncTrustWith(ctx context.Context, role *model.CspRole, clientCfg *model.CspIdpConfig, exps []trustExpectation,
	dryRun, allowRegistered bool, warnings []string) (*model.TrustPolicySyncResult, error) {
	name := awsRoleName(role)
	res := &model.TrustPolicySyncResult{CspRoleID: role.ID, RoleName: name, Changes: []model.TrustPolicyChange{}, DryRun: dryRun, Warnings: warnings}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	for _, e := range exps {
		if e.err != "" {
			return nil, trustSyncErr(http.StatusBadRequest, "invalid_request", "cannot build the expected trust policy: %s", e.err)
		}
	}
	api, err := s.awsRoleClientFor(clientCfg)
	if err != nil {
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "management credential: %v", err)
	}
	got, err := api.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	switch {
	case isAwsNoSuchEntity(err):
		return nil, trustSyncErr(http.StatusNotFound, "not_found", "role %s does not exist in AWS; sync does not create roles", name)
	case isAwsAccessDenied(err):
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "the management role is not allowed to call iam:GetRole on %s: %v", name, err)
	case err != nil:
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "failed to get role %s: %v", name, err)
	}
	raw := aws.ToString(got.Role.AssumeRolePolicyDocument)
	check := evaluateTrustPolicy(name, raw, exps)
	res.Verdict = check.Status
	if check.Status == model.TrustPolicyOK {
		res.Message = "trust policy already matches; nothing to change"
		return res, nil
	}
	if check.Status != model.TrustPolicyMissing && check.Status != model.TrustPolicyMismatch {
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "cannot sync trust policy of %s: %s", name, check.Message)
	}
	before, err := decodeTrustDoc(raw)
	if err != nil {
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "%v", err)
	}
	after, _ := decodeTrustDoc(raw)
	res.Changes = upsertTrustStatements(after, exps)
	res.Before, res.After = before, after
	if dryRun {
		res.Message = "dry run: no changes were written"
		return res, nil
	}
	if role.Origin != model.CspRoleOriginCreated && !allowRegistered {
		res.Message = fmt.Sprintf("role %s was registered from an existing AWS role (origin=%q); send the request again with allowRegistered=true to apply these changes", name, role.Origin)
		e := trustSyncErr(http.StatusConflict, "registered_role", "%s", res.Message)
		e.Result = res
		return nil, e
	}
	body, _ := json.Marshal(after)
	if _, err := api.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{RoleName: aws.String(name), PolicyDocument: aws.String(string(body))}); err != nil {
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "AWS UpdateAssumeRolePolicy %s failed: %v", name, err)
	}
	res.Applied = true
	res.Message = fmt.Sprintf("trust policy of %s updated (%d change(s))", name, len(res.Changes))
	log.Printf("[INFO] trust policy of %s synced: %d change(s)", name, len(res.Changes))
	return res, nil
}

// guardTrustTarget 관리용 Role 409, aws 아님 400
func (s *CspRoleService) guardTrustTarget(role *model.CspRole) error {
	if role.CspType != "aws" {
		return trustSyncErr(http.StatusBadRequest, "invalid_request", "CSP role %s is %s; only aws is supported", role.Name, role.CspType)
	}
	if err := s.GuardCspRoleNotManagementRole(role, "changed"); err != nil {
		if IsCspRoleProtectedError(err) {
			return trustSyncErr(http.StatusConflict, "protected", "%s", err.Error())
		}
		return trustSyncErr(http.StatusInternalServerError, "db_error", "%v", err)
	}
	return nil
}

// SyncTargetRoleTrustPolicy POST /api/csp-target-roles/id/:id/trust-policy/sync
func (s *CspRoleService) SyncTargetRoleTrustPolicy(ctx context.Context, roleID uint, req *model.TrustPolicySyncRequest) (*model.TrustPolicySyncResult, error) {
	role, err := s.cspRoleRepo.GetRoleByID(roleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, trustSyncErr(http.StatusNotFound, "not_found", "CSP role not found with ID: %d", roleID)
		}
		return nil, trustSyncErr(http.StatusInternalServerError, "db_error", "failed to get CSP role: %v", err)
	}
	if err := s.guardTrustTarget(role); err != nil {
		return nil, err
	}
	roleCfg := s.roleIdpConfig(role)
	// 새 권한 반영 규칙: 소속 Config 가 비활성이면 대체하지 않는다 (SAML·없음은 대체)
	if roleCfg != nil && !roleCfg.IsActive {
		return nil, trustSyncErr(http.StatusBadRequest, "invalid_request",
			"the IdP config %d (%s) of CSP role %s is inactive; activate the IdP config, or set another cspIdpConfigId with PUT /api/roles/csp/id/%d",
			roleCfg.ID, roleCfg.Name, role.Name, role.ID)
	}
	exps, warnings := s.trustExpectationsFor(role, roleCfg, nil)
	clientCfg := roleCfg
	if clientCfg == nil && len(exps) > 0 {
		clientCfg = exps[0].cfg
	}
	return s.syncTrustWith(ctx, role, clientCfg, exps, req.IsDryRun(), req != nil && req.AllowRegistered, warnings)
}

// IdpConfigTrustPolicy POST /api/csp-idp-configs/id/:id/trust-policy/check·sync
// 그 Config 에 소속된 대상 Role 전부를 그 Config 의 인증 방식 statement 기준으로 본다. 한 항목이 실패해도 나머지는 계속한다
func (s *CspRoleService) IdpConfigTrustPolicy(ctx context.Context, configID uint, sync bool, req *model.TrustPolicySyncRequest) (*model.TrustPolicyBulkResult, error) {
	cfg, err := s.cspIdpConfigRepo.GetByID(configID)
	if err != nil {
		return nil, trustSyncErr(http.StatusInternalServerError, "db_error", "failed to get IdP config: %v", err)
	}
	if cfg == nil {
		return nil, trustSyncErr(http.StatusNotFound, "not_found", "IdP config not found with ID: %d", configID)
	}
	if cfg.CspAccount == nil || cfg.CspAccount.CspType != "aws" {
		return nil, trustSyncErr(http.StatusBadRequest, "invalid_request", "IdP config %d is not an aws IdP config", cfg.ID)
	}
	if cfg.AuthMethod != model.AuthMethodOIDC && cfg.AuthMethod != model.AuthMethodSAML {
		return nil, trustSyncErr(http.StatusBadRequest, "invalid_request", "IdP config %d uses %s; trust policies apply to OIDC and SAML only", cfg.ID, cfg.AuthMethod)
	}
	if sync && !cfg.IsActive {
		return nil, trustSyncErr(http.StatusBadRequest, "invalid_request", "IdP config %d (%s) is inactive; activate it before syncing trust policies", cfg.ID, cfg.Name)
	}
	exp := newTrustExpectation(string(cfg.AuthMethod), cfg)
	if exp.err != "" {
		return nil, trustSyncErr(http.StatusBadRequest, "invalid_request", "cannot build the expected trust policy: %s", exp.err)
	}
	var roles []*model.CspRole
	if err := s.db.Where("csp_idp_config_id = ?", cfg.ID).Order("id").Find(&roles).Error; err != nil {
		return nil, trustSyncErr(http.StatusInternalServerError, "db_error", "failed to list CSP roles: %v", err)
	}
	dryRun := req.IsDryRun()
	out := &model.TrustPolicyBulkResult{CspIdpConfigID: cfg.ID, AuthMethod: string(cfg.AuthMethod), DryRun: sync && dryRun, Items: []model.TrustPolicyBulkItem{}, Warnings: []string{}}
	for _, role := range roles {
		item := model.TrustPolicyBulkItem{CspRoleID: role.ID, RoleName: awsRoleName(role)}
		if err := s.guardTrustTarget(role); err != nil {
			if te, ok := AsTrustSyncError(err); ok {
				item.Status, item.Message = te.Code, te.Msg
			}
			out.Items = append(out.Items, item)
			continue
		}
		if !sync {
			check := s.verifyWith(ctx, role, cfg, []trustExpectation{exp}, nil)
			item.Status, item.TrustPolicy = check.Status, check
			out.Items = append(out.Items, item)
			continue
		}
		res, err := s.syncTrustWith(ctx, role, cfg, []trustExpectation{exp}, dryRun, req != nil && req.AllowRegistered, nil)
		switch {
		case err != nil:
			if te, ok := AsTrustSyncError(err); ok {
				item.Status, item.Message, item.Sync = te.Code, te.Msg, te.Result
			} else {
				item.Status, item.Message = "error", err.Error()
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %s", item.RoleName, item.Message))
		case res.Applied:
			item.Status, item.Sync = "applied", res
		case len(res.Changes) > 0:
			item.Status, item.Sync = "would_change", res
		default:
			item.Status, item.Sync = "unchanged", res
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// verifyWith 기대값을 직접 주는 판정 (Config 일괄 check)
func (s *CspRoleService) verifyWith(ctx context.Context, role *model.CspRole, clientCfg *model.CspIdpConfig, exps []trustExpectation, warnings []string) *model.TrustPolicyCheck {
	name := awsRoleName(role)
	fail := func(status, msg string) *model.TrustPolicyCheck {
		return &model.TrustPolicyCheck{Status: status, RoleName: name, Statements: []model.TrustStatementCheck{}, Extra: []string{}, Warnings: []string{}, Message: msg}
	}
	api, err := s.awsRoleClientFor(clientCfg)
	if err != nil {
		return fail(model.TrustPolicyError, err.Error())
	}
	got, err := api.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	switch {
	case isAwsNoSuchEntity(err):
		return fail(model.TrustPolicyNotFound, fmt.Sprintf("role %s does not exist in AWS", name))
	case err != nil:
		return fail(model.TrustPolicyError, fmt.Sprintf("failed to get role %s: %v", name, err))
	}
	out := evaluateTrustPolicy(name, aws.ToString(got.Role.AssumeRolePolicyDocument), exps)
	out.Warnings = append(out.Warnings, warnings...)
	return out
}

// GetAwsRoleView GET /api/csp/iam/roles/:roleName — 계정의 관리용 자격증명으로 AWS Role 과 신뢰 정책 원문을 조회한다 (IAM-BUG-046)
func (s *CspRoleService) GetAwsRoleView(ctx context.Context, accountID uint, roleName string) (*model.AwsRoleView, error) {
	configs, err := s.cspIdpConfigRepo.GetActiveByAccountID(accountID)
	if err != nil {
		return nil, trustSyncErr(http.StatusInternalServerError, "db_error", "failed to list IdP configs: %v", err)
	}
	var mgmt *model.CspIdpConfig
	for _, c := range configs {
		if usableManagementConfig(c) {
			if mgmt != nil {
				return nil, trustSyncErr(http.StatusBadRequest, "invalid_request", "CSP account %d has more than one active aws OIDC IdP config", accountID)
			}
			mgmt = c
		}
	}
	if mgmt == nil {
		return nil, trustSyncErr(http.StatusBadRequest, "invalid_request", "CSP account %d has no active aws OIDC IdP config with role_arn", accountID)
	}
	api, err := s.awsRoleClientFor(mgmt)
	if err != nil {
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "management credential: %v", err)
	}
	got, err := api.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	switch {
	case isAwsNoSuchEntity(err):
		return nil, trustSyncErr(http.StatusNotFound, "not_found", "role %s does not exist in AWS", roleName)
	case isAwsAccessDenied(err):
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "the management role is not allowed to call iam:GetRole on %s: %v", roleName, err)
	case err != nil:
		return nil, trustSyncErr(http.StatusBadGateway, "csp_error", "failed to get role %s: %v", roleName, err)
	}
	view := &model.AwsRoleView{Name: aws.ToString(got.Role.RoleName), Arn: aws.ToString(got.Role.Arn), Description: aws.ToString(got.Role.Description),
		MaxSessionDuration: aws.ToInt32(got.Role.MaxSessionDuration)}
	if doc, err := decodeTrustDoc(aws.ToString(got.Role.AssumeRolePolicyDocument)); err == nil {
		view.TrustPolicy = doc
	}
	return view, nil
}
