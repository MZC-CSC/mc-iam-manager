package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
)

// 대상 CSP Role 신뢰 정책 검증 (IAM-TECH-045 M2)
//
// 기대값: 인증 방식마다 statement 1개
//   - OIDC: Federated = Config oidc_provider_arn, Action = sts:AssumeRoleWithWebIdentity, <provider host>:aud = Config audience
//   - SAML: Federated = Config saml_provider_arn,  Action = sts:AssumeRoleWithSAML,       SAML:aud = https://signin.aws.amazon.com/saml
// 인증 방식은 그 CspRole 의 Master 매핑 auth_method 들이다. 매핑이 없으면 소속 Config 의 방식이다.
// 비교는 Federated·Action·aud 3 항목만 한다. 다른 Condition 키(sub 등)는 보지 않는다.
// IAM 이 쓰지 않은 statement 는 extra 로 알리기만 한다(전체 판정에 영향 없음).

const (
	awsOidcTrustAction = "sts:AssumeRoleWithWebIdentity"
	awsSamlTrustAction = "sts:AssumeRoleWithSAML"
	awsSamlAudKey      = "SAML:aud"
	awsSamlAudience    = "https://signin.aws.amazon.com/saml"
)

type trustExpectation struct {
	authMethod string
	cfg        *model.CspIdpConfig
	federated  string
	action     string
	audKey     string
	aud        string
	err        string // 기대값을 만들 수 없으면 사유
}

func newTrustExpectation(method string, cfg *model.CspIdpConfig) trustExpectation {
	e := trustExpectation{authMethod: method, cfg: cfg}
	if cfg == nil {
		e.err = fmt.Sprintf("no %s IdP config to build the expected trust policy", method)
		return e
	}
	if method == string(constants.AuthMethodSAML) {
		e.federated, e.action, e.audKey, e.aud = cfg.GetSamlProviderArn(), awsSamlTrustAction, awsSamlAudKey, awsSamlAudience
		if e.federated == "" {
			e.err = fmt.Sprintf("IdP config %d (%s) has no saml_provider_arn", cfg.ID, cfg.Name)
		}
		return e
	}
	e.federated, e.action, e.aud = cfg.GetOidcProviderArn(), awsOidcTrustAction, cfg.GetAudience()
	const marker = ":oidc-provider/"
	if i := strings.Index(e.federated, marker); i >= 0 {
		e.audKey = e.federated[i+len(marker):] + ":aud"
	}
	switch {
	case e.audKey == "":
		e.err = fmt.Sprintf("IdP config %d (%s): oidc_provider_arn %q is not an AWS OIDC provider ARN", cfg.ID, cfg.Name, e.federated)
	case e.aud == "" || e.aud == "notyet":
		e.err = fmt.Sprintf("IdP config %d (%s): audience is not set", cfg.ID, cfg.Name)
	}
	return e
}

// trustStatement 실제 신뢰 정책 statement 하나
type trustStatement struct {
	effect     string
	federated  []string
	others     []string // Federated 가 아닌 principal (Service·AWS 등)
	actions    []string
	conditions map[string][]string // 조건 키 → 값 (연산자 무관하게 모음)
}

func stringsOf(v interface{}) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []interface{}:
		var out []string
		for _, i := range x {
			if s, ok := i.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// parseTrustPolicy GetRole 의 AssumeRolePolicyDocument(URL 인코딩 JSON)를 statement 목록으로 푼다
func parseTrustPolicy(doc string) ([]trustStatement, error) {
	if decoded, err := url.QueryUnescape(doc); err == nil {
		doc = decoded
	}
	var raw struct {
		Statement interface{} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		return nil, fmt.Errorf("trust policy is not valid JSON: %w", err)
	}
	var list []interface{}
	switch st := raw.Statement.(type) {
	case []interface{}:
		list = st
	case map[string]interface{}:
		list = []interface{}{st}
	}
	var out []trustStatement
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		st := trustStatement{conditions: map[string][]string{}}
		st.effect, _ = m["Effect"].(string)
		st.actions = stringsOf(m["Action"])
		switch p := m["Principal"].(type) {
		case map[string]interface{}:
			for k, v := range p {
				if k == "Federated" {
					st.federated = append(st.federated, stringsOf(v)...)
				} else {
					for _, s := range stringsOf(v) {
						st.others = append(st.others, k+":"+s)
					}
				}
			}
		case string:
			st.others = append(st.others, p)
		}
		if conds, ok := m["Condition"].(map[string]interface{}); ok {
			for _, kv := range conds {
				if km, ok := kv.(map[string]interface{}); ok {
					for k, v := range km {
						st.conditions[k] = append(st.conditions[k], stringsOf(v)...)
					}
				}
			}
		}
		sort.Strings(st.others)
		out = append(out, st)
	}
	return out, nil
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// evaluateTrustPolicy 실제 문서와 기대값을 비교한다 (AWS 호출 없음)
func evaluateTrustPolicy(roleName, doc string, exps []trustExpectation) *model.TrustPolicyCheck {
	out := &model.TrustPolicyCheck{RoleName: roleName, Statements: []model.TrustStatementCheck{}, Extra: []string{}, Warnings: []string{}}
	statements, err := parseTrustPolicy(doc)
	if err != nil {
		out.Status, out.Message = model.TrustPolicyError, err.Error()
		return out
	}
	used := make([]bool, len(statements))
	worst := model.TrustPolicyOK
	rank := map[string]int{model.TrustPolicyOK: 0, model.TrustPolicyMismatch: 1, model.TrustPolicyMissing: 2, model.TrustPolicyError: 3}
	for _, e := range exps {
		sc := model.TrustStatementCheck{AuthMethod: e.authMethod, ExpectedFederated: e.federated, ExpectedAction: e.action, ExpectedAudKey: e.audKey, ExpectedAud: e.aud}
		if e.cfg != nil {
			sc.IdpConfigID = e.cfg.ID
		}
		if e.err != "" {
			sc.Status, sc.Message = model.TrustPolicyError, e.err
		} else {
			sc.Status = model.TrustPolicyMissing
			sc.Message = fmt.Sprintf("no statement trusts %s", e.federated)
			for i, st := range statements {
				if !strings.EqualFold(st.effect, "Allow") || !containsFold(st.federated, e.federated) {
					continue
				}
				used[i] = true
				actionOK := containsFold(st.actions, e.action)
				aud := st.conditions[e.audKey]
				audOK := containsFold(aud, e.aud)
				if actionOK && audOK {
					sc.Status, sc.ActualActions, sc.ActualAud, sc.Message = model.TrustPolicyOK, st.actions, aud, ""
					break
				}
				var reasons []string
				if !actionOK {
					reasons = append(reasons, fmt.Sprintf("Action %v does not include %s", st.actions, e.action))
				}
				if !audOK {
					if len(aud) == 0 {
						reasons = append(reasons, fmt.Sprintf("no %s condition", e.audKey))
					} else {
						reasons = append(reasons, fmt.Sprintf("%s %v does not include %s", e.audKey, aud, e.aud))
					}
				}
				sc.Status, sc.ActualActions, sc.ActualAud, sc.Message = model.TrustPolicyMismatch, st.actions, aud, strings.Join(reasons, "; ")
			}
		}
		if rank[sc.Status] > rank[worst] {
			worst = sc.Status
		}
		out.Statements = append(out.Statements, sc)
	}
	for i, st := range statements {
		if used[i] {
			continue
		}
		principals := append(append([]string{}, st.federated...), st.others...)
		out.Extra = append(out.Extra, fmt.Sprintf("%s %s %v", st.effect, strings.Join(principals, ","), st.actions))
	}
	out.Status = worst
	return out
}

// trustAuthMethods 그 CspRole 을 쓰는 인증 방식 (Master 매핑의 auth_method, 없으면 소속 Config 의 방식)
func (s *CspRoleService) trustAuthMethods(role *model.CspRole, roleCfg *model.CspIdpConfig) []string {
	var methods []string
	if role.ID != 0 {
		_ = s.db.Model(&model.RoleMasterCspRoleMapping{}).Where("csp_role_id = ?", role.ID).Distinct().Pluck("auth_method", &methods).Error
	}
	var out []string
	for _, m := range methods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if m == string(constants.AuthMethodOIDC) || m == string(constants.AuthMethodSAML) {
			if !containsFold(out, m) {
				out = append(out, m)
			}
		}
	}
	if len(out) == 0 {
		if roleCfg != nil && roleCfg.AuthMethod == model.AuthMethodSAML {
			out = []string{string(constants.AuthMethodSAML)}
		} else {
			out = []string{string(constants.AuthMethodOIDC)}
		}
	}
	sort.Strings(out)
	return out
}

// trustConfigFor 인증 방식의 기대값을 만들 Config: 소속 Config 가 그 방식이고 활성이면 그것,
// 아니면 같은 계정의 그 방식 활성 Config 가 1개일 때 그것, 그것도 없으면 소속 Config(비활성이어도 값은 쓴다, 경고)
func (s *CspRoleService) trustConfigFor(role *model.CspRole, roleCfg *model.CspIdpConfig, method string) (*model.CspIdpConfig, string) {
	if roleCfg != nil && string(roleCfg.AuthMethod) == method && roleCfg.IsActive {
		return roleCfg, ""
	}
	var accountID uint
	if role.CspAccountID != nil {
		accountID = *role.CspAccountID
	} else if roleCfg != nil {
		accountID = roleCfg.CspAccountID
	}
	if accountID != 0 {
		configs, err := s.cspIdpConfigRepo.GetActiveByAccountID(accountID)
		if err == nil {
			var candidates []*model.CspIdpConfig
			for _, c := range configs {
				if string(c.AuthMethod) == method {
					candidates = append(candidates, c)
				}
			}
			if len(candidates) == 1 {
				return candidates[0], fmt.Sprintf("%s expectation uses active IdP config %d of the account", method, candidates[0].ID)
			}
		}
	}
	if roleCfg != nil && string(roleCfg.AuthMethod) == method {
		return roleCfg, fmt.Sprintf("IdP config %d (%s) is inactive; its values are used for the %s expectation", roleCfg.ID, roleCfg.Name, method)
	}
	return nil, ""
}

func (s *CspRoleService) roleIdpConfig(role *model.CspRole) *model.CspIdpConfig {
	if role.CspIdpConfigID == nil {
		return nil
	}
	cfg, err := s.cspIdpConfigRepo.GetByID(*role.CspIdpConfigID)
	if err != nil {
		return nil
	}
	return cfg
}

// trustExpectationsFor methods 가 비면 Master 매핑에서 정한다
func (s *CspRoleService) trustExpectationsFor(role *model.CspRole, roleCfg *model.CspIdpConfig, methods []string) ([]trustExpectation, []string) {
	if len(methods) == 0 {
		methods = s.trustAuthMethods(role, roleCfg)
	}
	var exps []trustExpectation
	var warnings []string
	for _, m := range methods {
		cfg, warn := s.trustConfigFor(role, roleCfg, m)
		if warn != "" {
			warnings = append(warnings, warn)
		}
		exps = append(exps, newTrustExpectation(m, cfg))
	}
	return exps, warnings
}

// VerifyTargetTrustPolicy AWS 의 실제 신뢰 정책을 기대값과 비교한다. DB·AWS 에 쓰지 않는다.
// methods 를 주면 그 인증 방식만 본다(진단 API). 비면 Master 매핑의 방식 전부.
func (s *CspRoleService) VerifyTargetTrustPolicy(ctx context.Context, role *model.CspRole, methods ...string) *model.TrustPolicyCheck {
	roleCfg := s.roleIdpConfig(role)
	exps, warnings := s.trustExpectationsFor(role, roleCfg, methods)
	name := awsRoleName(role)
	fail := func(status, msg string) *model.TrustPolicyCheck {
		return &model.TrustPolicyCheck{Status: status, RoleName: name, Statements: []model.TrustStatementCheck{}, Extra: []string{}, Warnings: warnings, Message: msg}
	}
	clientCfg := roleCfg
	if clientCfg == nil && len(exps) > 0 {
		clientCfg = exps[0].cfg
	}
	api, err := s.awsRoleClientFor(clientCfg)
	if err != nil {
		return fail(model.TrustPolicyError, err.Error())
	}
	got, err := api.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	switch {
	case isAwsNoSuchEntity(err):
		return fail(model.TrustPolicyNotFound, fmt.Sprintf("role %s does not exist in AWS", name))
	case isAwsAccessDenied(err):
		return fail(model.TrustPolicyError, fmt.Sprintf("the management role is not allowed to call iam:GetRole on %s: %v", name, err))
	case err != nil:
		return fail(model.TrustPolicyError, fmt.Sprintf("failed to get role %s: %v", name, err))
	}
	out := evaluateTrustPolicy(name, aws.ToString(got.Role.AssumeRolePolicyDocument), exps)
	out.Warnings = append(out.Warnings, warnings...)
	return out
}

// VerifyTargetProvider 인증 방식의 provider 가 AWS 에 있는지 관리용 자격증명으로 확인한다 (진단 API).
// OIDC 는 audience 가 provider 의 client ID 목록에 있는지도 본다. 저장된 idp_identifier 가 Config 와 다르면 경고를 붙인다.
func (s *CspRoleService) VerifyTargetProvider(ctx context.Context, role *model.CspRole, method string) (string, error) {
	roleCfg := s.roleIdpConfig(role)
	cfg, warn := s.trustConfigFor(role, roleCfg, method)
	e := newTrustExpectation(method, cfg)
	if e.err != "" {
		return "", fmt.Errorf("%s", e.err)
	}
	clientCfg := roleCfg
	if clientCfg == nil {
		clientCfg = cfg
	}
	api, err := s.awsRoleClientFor(clientCfg)
	if err != nil {
		return "", fmt.Errorf("management credential: %v", err)
	}
	var detail string
	if method == string(constants.AuthMethodSAML) {
		_, err = api.GetSAMLProvider(ctx, &iam.GetSAMLProviderInput{SAMLProviderArn: aws.String(e.federated)})
		detail = fmt.Sprintf("SAML provider %s exists", e.federated)
	} else {
		var out *iam.GetOpenIDConnectProviderOutput
		out, err = api.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(e.federated)})
		if err == nil && !containsString(out.ClientIDList, e.aud) {
			return "", fmt.Errorf("audience %q is not in the client ID list of OIDC provider %s (%s) — add it in AWS IAM", e.aud, e.federated, strings.Join(out.ClientIDList, ", "))
		}
		detail = fmt.Sprintf("OIDC provider %s exists, audience %s registered", e.federated, e.aud)
	}
	switch {
	case isAwsNoSuchEntity(err):
		return "", fmt.Errorf("%s provider %s does not exist in AWS — create it in AWS IAM", method, e.federated)
	case isAwsAccessDenied(err):
		return "", fmt.Errorf("the management role is not allowed to read provider %s: %v", e.federated, err)
	case err != nil:
		return "", fmt.Errorf("failed to get provider %s: %v", e.federated, err)
	}
	if role.IdpIdentifier != "" && role.IdpIdentifier != e.federated {
		detail += fmt.Sprintf("; warning: CSP role idp_identifier %s differs from IdP config %d", role.IdpIdentifier, cfg.ID)
	}
	if warn != "" {
		detail += "; " + warn
	}
	return detail, nil
}
