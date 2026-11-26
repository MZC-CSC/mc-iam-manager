package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/repository"
	"gorm.io/gorm"
)

// readiness 판정에 쓰는 의존성 (테스트 주입용 인터페이스)
type readinessAccountRepo interface {
	GetByID(id uint) (*model.CspAccount, error)
}

type readinessIdpConfigRepo interface {
	List(filter *model.CspIdpConfigFilter) ([]*model.CspIdpConfig, error)
}

type readinessRoleRepo interface {
	FindForAccountReadiness(cspType string, accountID uint) ([]*model.CspRole, error)
}

type readinessMappingRepo interface {
	CountMappingsByCspRoleIDs(cspRoleIDs []uint) (map[uint]int, error)
}

type readinessTokenSource interface {
	GetClientCredentialsToken(ctx context.Context) (*gocloak.JWT, error)
	GetImpersonationTokenByServiceAccount(ctx context.Context) (*gocloak.JWT, error)
}

type readinessConnectionTester interface {
	TestConnection(ctx context.Context, id uint) error
}

type readinessAwsAssumer interface {
	AssumeRoleWithWebIdentity(ctx context.Context, roleArn, kcUserId, webIdentityToken, idpArn, region string) (*model.CspCredentialResponse, error)
}

// readinessTrustVerifier ⑦ 참고용 신뢰 정책 판정 (읽기만, 관리용 자격증명)
type readinessTrustVerifier interface {
	VerifyTargetTrustPolicy(ctx context.Context, role *model.CspRole, methods ...string) *model.TrustPolicyCheck
}

// ErrCspAccountNotFound readiness 대상 계정 없음
var ErrCspAccountNotFound = fmt.Errorf("CSP account not found")

const (
	readinessLiveCallTimeout  = 15 * time.Second
	readinessDefaultAwsRegion = "ap-northeast-2"
)

// CspAccountReadinessService CSP 계정별 임시자격증명 연동 준비 상태(readiness) 판정 (IAM-TECH-044).
// 쓰기 없음. 단계 정의는 wiki CSP-Temporary-Credential-Guide-ko "9. 점검 체크리스트" 기준.
type CspAccountReadinessService struct {
	accountRepo   readinessAccountRepo
	idpConfigRepo readinessIdpConfigRepo
	roleRepo      readinessRoleRepo
	mappingRepo   readinessMappingRepo
	tokens        readinessTokenSource
	connTester    readinessConnectionTester
	awsAssumer    readinessAwsAssumer
	trust         readinessTrustVerifier // nil 이면 신뢰 정책 판정 생략
	now           func() time.Time
}

// NewCspAccountReadinessService 새 CspAccountReadinessService 생성
func NewCspAccountReadinessService(db *gorm.DB) *CspAccountReadinessService {
	keycloakService := NewKeycloakService()
	return &CspAccountReadinessService{
		accountRepo:   repository.NewCspAccountRepository(db),
		idpConfigRepo: repository.NewCspIdpConfigRepository(db),
		roleRepo:      repository.NewCspRoleRepository(db),
		mappingRepo:   repository.NewCspMappingRepository(db),
		tokens:        keycloakService,
		connTester:    NewCspIdpConfigService(db, keycloakService),
		awsAssumer:    NewAwsCredentialService(),
		trust:         NewCspRoleService(db, keycloakService),
		now:           time.Now,
	}
}

// ReadinessOptions 판정 옵션
type ReadinessOptions struct {
	// LiveChecks CSP 실호출(④ 연결 테스트·⑦ assume) 허용 여부 — PlatformAdmin 일 때만 true
	LiveChecks bool
	// CheckIssue ⑦ 실제 assume 수행 여부 (?checkIssue=true)
	CheckIssue bool
}

// idpConfigRequiredKeys CSP·인증방식별 IdP Config 필수 config 키 (가이드 8-2)
func idpConfigRequiredKeys(cspType string, method model.AuthMethodType) []string {
	if method == model.AuthMethodSecretKey {
		return []string{"access_key_id", "secret_access_key"}
	}
	if method != model.AuthMethodOIDC {
		return nil
	}
	switch cspType {
	case "aws":
		return []string{"role_arn"}
	case "alibaba":
		return []string{"audience"}
	case "tencent":
		return []string{"secret_id", "secret_key"}
	case "azure":
		return []string{"tenant_id", "client_id"}
	}
	return nil
}

// supportsConnectionTest CspIdpConfigService.TestConnection 이 실제로 구현된 조합인지
func supportsConnectionTest(cspType string, cfg *model.CspIdpConfig) bool {
	if cspType != "aws" {
		return false
	}
	switch cfg.AuthMethod {
	case model.AuthMethodOIDC:
		return true
	case model.AuthMethodSecretKey:
		return !cfg.IsEncrypted()
	}
	return false
}

// roleAuthMethod 발급 경로와 같은 규칙으로 CspRole 의 인증방식을 정한다 (Config 미연결 시 aws·gcp·alibaba 는 OIDC)
func roleAuthMethod(role *model.CspRole) model.AuthMethodType {
	if role.CspIdpConfig != nil && role.CspIdpConfig.AuthMethod != "" {
		return role.CspIdpConfig.AuthMethod
	}
	switch role.CspType {
	case "aws", "gcp", "alibaba":
		return model.AuthMethodOIDC
	}
	return ""
}

func missingConfigKeys(cfg *model.CspIdpConfig, keys []string) []string {
	var missing []string
	for _, k := range keys {
		if cfg.Config == nil || strings.TrimSpace(cfg.Config[k]) == "" {
			missing = append(missing, k)
		}
	}
	return missing
}

// GetReadiness 계정의 연동 준비 상태를 판정한다. 계정이 없으면 ErrCspAccountNotFound.
func (s *CspAccountReadinessService) GetReadiness(ctx context.Context, accountID uint, opts ReadinessOptions) (*model.CspAccountReadinessResponse, error) {
	account, err := s.accountRepo.GetByID(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get CSP account: %w", err)
	}
	if account == nil {
		return nil, ErrCspAccountNotFound
	}

	resp := &model.CspAccountReadinessResponse{
		AccountID:   account.ID,
		AccountName: account.Name,
		CspType:     account.CspType,
		CspRoles:    []model.CspRoleReadiness{},
		LiveChecks:  opts.LiveChecks,
		CheckedAt:   s.now().UTC().Format(time.RFC3339),
	}

	// ① Keycloak OIDC 클라이언트 — client_credentials 토큰 발급 가능 여부(토큰은 반환하지 않음)
	stage1 := model.ReadinessStage{ID: 1, Key: model.ReadinessStageKeycloakClient, Name: "Keycloak OIDC 클라이언트", GuideAnchor: "1-1"}
	if _, err := s.tokens.GetClientCredentialsToken(ctx); err != nil {
		stage1.Status = model.ReadinessFailed
		stage1.Detail = fmt.Sprintf("OIDC 클라이언트 토큰 발급 실패: %v", err)
		stage1.NextAction = "Keycloak 에 MC_IAM_MANAGER_KEYCLOAK_OIDC_CLIENT_NAME 클라이언트가 있고 secret 이 서버 값과 같은지 확인 (POST /api/initial-admin 재호출로 생성 가능)"
	} else {
		stage1.Status = model.ReadinessOK
		stage1.Detail = "client_credentials 토큰 발급 성공"
	}
	kcOK := stage1.Status == model.ReadinessOK

	// ② CSP Account — 활성, account_info 필수 키
	stage2 := model.ReadinessStage{ID: 2, Key: model.ReadinessStageAccount, Name: "CSP 계정", GuideAnchor: "4-1"}
	if infoErr := validateAccountInfo(account); infoErr != nil {
		stage2.Status = model.ReadinessFailed
		stage2.Detail = infoErr.Error()
		stage2.NextAction = "CSP 계정의 account_info 필수 값을 입력"
	} else if !account.IsActive {
		stage2.Status = model.ReadinessFailed
		stage2.Detail = "계정이 비활성 상태"
		stage2.NextAction = "CSP 계정을 활성화 (POST /api/csp-accounts/id/{id}/activate)"
	} else {
		stage2.Status = model.ReadinessOK
		stage2.Detail = "활성 계정, account_info 필수 값 있음"
	}

	// ③ IdP Config — 활성 설정 존재, 인증방식별 필수 config 키
	stage3 := model.ReadinessStage{ID: 3, Key: model.ReadinessStageIdpConfig, Name: "IdP Config", GuideAnchor: "4-2"}
	configs, err := s.idpConfigRepo.List(&model.CspIdpConfigFilter{CspAccountID: &account.ID})
	if err != nil {
		return nil, fmt.Errorf("failed to list IdP configs: %w", err)
	}
	var validConfigs []*model.CspIdpConfig
	var problems []string
	activeCount := 0
	for _, cfg := range configs {
		if !cfg.IsActive {
			continue
		}
		activeCount++
		if missing := missingConfigKeys(cfg, idpConfigRequiredKeys(account.CspType, cfg.AuthMethod)); len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s(id=%d, %s): config 누락 %s", cfg.Name, cfg.ID, cfg.AuthMethod, strings.Join(missing, ",")))
			continue
		}
		validConfigs = append(validConfigs, cfg)
	}
	switch {
	case activeCount == 0:
		stage3.Status = model.ReadinessPending
		stage3.Detail = fmt.Sprintf("활성 IdP Config 없음 (전체 %d건)", len(configs))
		stage3.NextAction = "이 계정에 IdP Config 등록 (POST /api/csp-idp-configs)"
	case len(validConfigs) == 0:
		stage3.Status = model.ReadinessFailed
		stage3.Detail = strings.Join(problems, "; ")
		stage3.NextAction = "IdP Config 의 config 필수 키 입력 (PUT /api/csp-idp-configs/id/{id})"
	default:
		stage3.Status = model.ReadinessOK
		names := make([]string, 0, len(validConfigs))
		for _, cfg := range validConfigs {
			names = append(names, fmt.Sprintf("%s(id=%d, %s)", cfg.Name, cfg.ID, cfg.AuthMethod))
		}
		stage3.Detail = "활성: " + strings.Join(names, ", ")
		if len(problems) > 0 {
			stage3.Detail += " | 미완료: " + strings.Join(problems, "; ")
		}
	}

	// ④ Provider 신뢰 — IdP Config 연결 테스트 (AWS OIDC: 관리용 Role assume, AWS SECRET_KEY: GetCallerIdentity)
	resp.Stages = append(resp.Stages, stage1, stage2, stage3, s.checkProviderTrust(ctx, account, validConfigs, kcOK, opts))

	// ⑤ CSP Role
	roles, err := s.roleRepo.FindForAccountReadiness(account.CspType, account.ID)
	if err != nil {
		return nil, err
	}
	roleIDs := make([]uint, 0, len(roles))
	for _, role := range roles {
		roleIDs = append(roleIDs, role.ID)
	}
	counts, err := s.mappingRepo.CountMappingsByCspRoleIDs(roleIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to count CSP role mappings: %w", err)
	}

	idents := make(map[uint]roleIdentifiers, len(roles))
	readyCount, mappedReadyCount := 0, 0
	for _, role := range roles {
		idents[role.ID] = roleIdentifiers{iam: role.IamIdentifier, idp: role.IdpIdentifier, role: role}
		rr := evaluateCspRole(role, account)
		rr.MappingCount = counts[role.ID]
		switch {
		case rr.Status != model.ReadinessOK:
			rr.MappingStatus = model.ReadinessSkipped
		case rr.MappingCount > 0:
			rr.MappingStatus = model.ReadinessOK
			mappedReadyCount++
		default:
			rr.MappingStatus = model.ReadinessPending
		}
		if rr.Status == model.ReadinessOK {
			readyCount++
		}
		resp.CspRoles = append(resp.CspRoles, rr)
	}
	resp.CanMapRoles = readyCount > 0

	stage5 := model.ReadinessStage{ID: 5, Key: model.ReadinessStageCspRole, Name: "CSP Role", GuideAnchor: "5"}
	switch {
	case len(roles) == 0:
		stage5.Status = model.ReadinessPending
		stage5.Detail = "등록된 CSP Role 없음"
		stage5.NextAction = "사용자용 CSP Role 등록 (POST /api/roles/csp, cspIdpConfigId 지정)"
	case readyCount == 0:
		stage5.Status = model.ReadinessFailed
		stage5.Detail = fmt.Sprintf("CSP Role %d건 모두 설정 미완료 (cspRoles[].detail 참고)", len(roles))
		stage5.NextAction = "CSP Role 의 IDP/IAM Identifier·cspIdpConfigId 확인 (PUT /api/roles/csp/id/{id})"
	default:
		stage5.Status = model.ReadinessOK
		stage5.Detail = fmt.Sprintf("설정 완료 %d / %d건", readyCount, len(roles))
	}

	// ⑥ 역할 매핑
	stage6 := model.ReadinessStage{ID: 6, Key: model.ReadinessStageRoleMapping, Name: "역할 매핑", GuideAnchor: "6-1"}
	switch {
	case readyCount == 0:
		stage6.Status = model.ReadinessSkipped
		stage6.Detail = "설정 완료된 CSP Role 없음"
	case mappedReadyCount == 0:
		stage6.Status = model.ReadinessPending
		stage6.Detail = "플랫폼 역할과 매핑된 CSP Role 없음"
		stage6.NextAction = "플랫폼 역할 ↔ CSP Role 매핑 (POST /api/roles/csp-roles)"
	default:
		stage6.Status = model.ReadinessOK
		stage6.Detail = fmt.Sprintf("매핑된 CSP Role %d / %d건", mappedReadyCount, readyCount)
	}

	resp.Stages = append(resp.Stages, stage5, stage6, s.checkIssue(ctx, account, resp, idents, kcOK, opts))
	return resp, nil
}

// checkProviderTrust ④ 판정
func (s *CspAccountReadinessService) checkProviderTrust(ctx context.Context, account *model.CspAccount, validConfigs []*model.CspIdpConfig, kcOK bool, opts ReadinessOptions) model.ReadinessStage {
	stage := model.ReadinessStage{ID: 4, Key: model.ReadinessStageProviderTrust, Name: "Provider 신뢰(연결 테스트)", GuideAnchor: "2"}
	if len(validConfigs) == 0 {
		stage.Status = model.ReadinessSkipped
		stage.Detail = "사용할 수 있는 IdP Config 없음"
		return stage
	}
	var testable []*model.CspIdpConfig
	for _, cfg := range validConfigs {
		if supportsConnectionTest(account.CspType, cfg) {
			testable = append(testable, cfg)
		}
	}
	if len(testable) == 0 {
		stage.Status = model.ReadinessNotApplicable
		stage.Detail = fmt.Sprintf("%s 의 등록된 인증방식은 연결 테스트 미지원 — CSP 콘솔에서 Provider·Role 신뢰 설정을 직접 확인", account.CspType)
		stage.GuideAnchor = "8-2"
		return stage
	}
	if !opts.LiveChecks {
		stage.Status = model.ReadinessSkipped
		stage.Detail = "CSP 실호출은 PlatformAdmin 만 수행"
		return stage
	}

	var oks, fails []string
	for _, cfg := range testable {
		if cfg.AuthMethod == model.AuthMethodOIDC && !kcOK {
			fails = append(fails, fmt.Sprintf("%s(id=%d): Keycloak OIDC 토큰 없음(①)", cfg.Name, cfg.ID))
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, readinessLiveCallTimeout)
		err := s.connTester.TestConnection(tctx, cfg.ID)
		cancel()
		if err != nil {
			fails = append(fails, fmt.Sprintf("%s(id=%d): %v", cfg.Name, cfg.ID, err))
		} else {
			oks = append(oks, fmt.Sprintf("%s(id=%d)", cfg.Name, cfg.ID))
		}
	}
	if len(oks) > 0 {
		stage.Status = model.ReadinessOK
		stage.Detail = "연결 성공: " + strings.Join(oks, ", ")
		if len(fails) > 0 {
			stage.Detail += " | 실패: " + strings.Join(fails, "; ")
		}
		return stage
	}
	stage.Status = model.ReadinessFailed
	stage.Detail = strings.Join(fails, "; ")
	stage.NextAction = "AWS OIDC Provider(audience)·관리용 Role 신뢰 정책 확인 (가이드 2·3절)"
	return stage
}

// evaluateCspRole ⑤ 역할 단위 판정 (가이드 C4·C5)
func evaluateCspRole(role *model.CspRole, account *model.CspAccount) model.CspRoleReadiness {
	method := roleAuthMethod(role)
	rr := model.CspRoleReadiness{
		CspRoleID:      role.ID,
		Name:           role.Name,
		AuthMethod:     string(method),
		Linkage:        model.ReadinessLinkageCspType,
		CspIdpConfigID: role.CspIdpConfigID,
		MappingStatus:  model.ReadinessPending,
		IssueStatus:    model.ReadinessPending,
	}
	if role.CspAccountID != nil && *role.CspAccountID == account.ID {
		rr.Linkage = model.ReadinessLinkageAccount
	}

	var problems []string
	if role.Status != "created" {
		problems = append(problems, fmt.Sprintf("status=%q (created 아님)", role.Status))
	}
	if strings.TrimSpace(role.IamIdentifier) == "" {
		problems = append(problems, "IAM Identifier 비어 있음")
	}
	switch idp := strings.TrimSpace(role.IdpIdentifier); {
	case idp == "":
		problems = append(problems, "IDP Identifier 비어 있음")
	case strings.Contains(idp, "notyet"):
		problems = append(problems, "IDP Identifier 에 미설정 값(notyet) 포함 — 서버 환경변수 확인 후 Role 재등록")
	}
	if role.CspIdpConfigID != nil {
		cfg := role.CspIdpConfig
		switch {
		case cfg == nil:
			problems = append(problems, fmt.Sprintf("연결된 IdP Config(id=%d) 없음", *role.CspIdpConfigID))
		case !cfg.IsActive:
			problems = append(problems, fmt.Sprintf("연결된 IdP Config(id=%d) 비활성", cfg.ID))
		case cfg.CspAccountID != account.ID:
			problems = append(problems, fmt.Sprintf("연결된 IdP Config(id=%d) 가 다른 계정(id=%d) 소속", cfg.ID, cfg.CspAccountID))
		}
	} else {
		switch account.CspType {
		case "alibaba", "tencent", "azure":
			// 이 CSP 들은 발급 시 연결된 IdP Config 의 config 키를 읽는다 (가이드 8-2)
			problems = append(problems, "cspIdpConfigId 미연결")
		}
	}
	if method == model.AuthMethodSAML {
		if v, _ := role.ExtendedConfig["saml_client_id"].(string); strings.TrimSpace(v) == "" {
			problems = append(problems, "extendedConfig.saml_client_id 미등록")
		}
	}
	if method == "" {
		problems = append(problems, "인증방식을 정할 수 없음 — cspIdpConfigId 연결 필요")
	}

	if len(problems) > 0 {
		rr.Status = model.ReadinessFailed
		rr.Detail = strings.Join(problems, "; ")
		rr.IssueStatus = model.ReadinessSkipped
		return rr
	}
	rr.Status = model.ReadinessOK
	return rr
}

// checkIssue ⑦ 판정 — AWS OIDC 역할만 실제 AssumeRoleWithWebIdentity 를 시도하고 결과 자격증명은 버린다
func (s *CspAccountReadinessService) checkIssue(ctx context.Context, account *model.CspAccount, resp *model.CspAccountReadinessResponse, idents map[uint]roleIdentifiers, kcOK bool, opts ReadinessOptions) model.ReadinessStage {
	stage := model.ReadinessStage{ID: 7, Key: model.ReadinessStageIssue, Name: "발급 가능(실제 assume)", GuideAnchor: "7"}

	var candidates []int
	for i := range resp.CspRoles {
		if resp.CspRoles[i].Status == model.ReadinessOK {
			candidates = append(candidates, i)
		}
	}
	setAll := func(status model.ReadinessStatus, detail string) {
		for _, i := range candidates {
			resp.CspRoles[i].IssueStatus = status
			resp.CspRoles[i].IssueDetail = detail
		}
	}

	if len(candidates) == 0 {
		stage.Status = model.ReadinessSkipped
		stage.Detail = "설정 완료된 CSP Role 없음"
		return stage
	}
	if !opts.CheckIssue {
		stage.Status = model.ReadinessPending
		stage.Detail = "확인하지 않음 — checkIssue=true 로 다시 조회하면 실제 assume 을 시도"
		return stage
	}
	if !opts.LiveChecks {
		stage.Status = model.ReadinessSkipped
		stage.Detail = "CSP 실호출은 PlatformAdmin 만 수행"
		setAll(model.ReadinessSkipped, stage.Detail)
		return stage
	}

	// 계정에 region 이 없으면 연결 테스트·Role 생성 경로와 같은 기본 리전을 쓴다 (빈 값이면 STS endpoint 해석 실패)
	region := account.GetRegion()
	if region == "" {
		region = readinessDefaultAwsRegion
	}
	var token *gocloak.JWT
	var tokenErr error
	tokenFetched := false
	attempted, succeeded, issuable := 0, 0, 0

	trustProblems := 0
	for _, i := range candidates {
		rr := &resp.CspRoles[i]
		if tr := s.trustReadiness(ctx, account, rr, idents[rr.CspRoleID].role); tr != nil {
			rr.TrustPolicy = tr
			if tr.Status == model.TrustPolicyMissing || tr.Status == model.TrustPolicyMismatch {
				trustProblems++
			}
		}
		if account.CspType != "aws" || rr.AuthMethod != string(model.AuthMethodOIDC) {
			rr.IssueStatus = model.ReadinessNotApplicable
			rr.IssueDetail = fmt.Sprintf("%s %s 는 실제 발급 검사 미지원 — 워크스페이스 기준 진단(POST /api/workspaces/credentials/validate) 사용", account.CspType, rr.AuthMethod)
			continue
		}
		attempted++
		if !kcOK {
			rr.IssueStatus = model.ReadinessSkipped
			rr.IssueDetail = "Keycloak OIDC 토큰 없음(①)"
			continue
		}
		if !tokenFetched {
			token, tokenErr = s.tokens.GetImpersonationTokenByServiceAccount(ctx)
			tokenFetched = true
		}
		if tokenErr != nil || token == nil {
			rr.IssueStatus = model.ReadinessFailed
			rr.IssueDetail = fmt.Sprintf("서비스 계정 토큰 발급 실패: %v", tokenErr)
			continue
		}
		role := idents[rr.CspRoleID]
		actx, cancel := context.WithTimeout(ctx, readinessLiveCallTimeout)
		_, err := s.awsAssumer.AssumeRoleWithWebIdentity(actx, role.iam, "readiness", token.AccessToken, role.idp, region)
		cancel()
		if err != nil {
			rr.IssueStatus = model.ReadinessFailed
			rr.IssueDetail = err.Error()
			continue
		}
		succeeded++
		rr.IssueStatus = model.ReadinessOK
		rr.IssueDetail = "assume 성공 (자격증명은 저장·반환하지 않음)"
		if rr.MappingCount > 0 {
			issuable++
		}
	}

	if attempted == 0 {
		stage.Status = model.ReadinessNotApplicable
		stage.Detail = "실제 발급 검사를 지원하는 CSP Role 없음 (현재 AWS OIDC 만 지원)"
		return stage
	}
	can := issuable > 0
	resp.CanIssue = &can
	switch {
	case issuable > 0:
		stage.Status = model.ReadinessOK
		stage.Detail = fmt.Sprintf("assume 성공 %d / %d건, 매핑까지 된 역할 %d건", succeeded, attempted, issuable)
	case succeeded > 0:
		stage.Status = model.ReadinessPending
		stage.Detail = fmt.Sprintf("assume 성공 %d / %d건, 매핑된 역할 없음", succeeded, attempted)
		stage.NextAction = "assume 에 성공한 CSP Role 을 플랫폼 역할과 매핑"
	default:
		stage.Status = model.ReadinessFailed
		stage.Detail = fmt.Sprintf("assume 성공 0 / %d건 (cspRoles[].issueDetail 참고)", attempted)
		stage.NextAction = "사용자용 Role 신뢰 정책의 Federated(OIDC Provider ARN)·aud 확인 (가이드 B4)"
	}
	if trustProblems > 0 && stage.Status != model.ReadinessOK {
		stage.NextAction = fmt.Sprintf("신뢰 정책이 기대값과 다른 CSP Role %d건 — cspRoles[].trustPolicy.nextAction 대로 재동기화 (POST /api/csp-target-roles/id/{id}/trust-policy/sync)", trustProblems)
	}
	return stage
}

// trustReadiness ⑦ 참고용 신뢰 정책 판정. AWS OIDC·SAML Role 만, 조회 실패는 status=error 로만 표시한다
func (s *CspAccountReadinessService) trustReadiness(ctx context.Context, account *model.CspAccount, rr *model.CspRoleReadiness, role *model.CspRole) *model.CspRoleTrustReadiness {
	if s.trust == nil || role == nil || account.CspType != "aws" {
		return nil
	}
	if rr.AuthMethod != string(model.AuthMethodOIDC) && rr.AuthMethod != string(model.AuthMethodSAML) {
		return nil
	}
	tctx, cancel := context.WithTimeout(ctx, readinessLiveCallTimeout)
	check := s.trust.VerifyTargetTrustPolicy(tctx, role, rr.AuthMethod)
	cancel()
	if check == nil {
		return &model.CspRoleTrustReadiness{Status: model.TrustPolicyError, Detail: "신뢰 정책 판정 결과 없음"}
	}
	out := &model.CspRoleTrustReadiness{Status: check.Status}
	var msgs []string
	for _, st := range check.Statements {
		if st.Status != model.TrustPolicyOK && st.Message != "" {
			msgs = append(msgs, fmt.Sprintf("%s: %s", st.AuthMethod, st.Message))
		}
	}
	if len(msgs) == 0 && check.Message != "" {
		msgs = append(msgs, check.Message)
	}
	switch check.Status {
	case model.TrustPolicyOK:
		out.Detail = fmt.Sprintf("%s 신뢰 정책이 기대값과 일치", rr.AuthMethod)
	case model.TrustPolicyMissing, model.TrustPolicyMismatch:
		out.Detail = strings.Join(msgs, "; ")
		out.NextAction = fmt.Sprintf("POST /api/csp-target-roles/id/%d/trust-policy/sync 로 변경안 확인(dryRun 기본) 후 dryRun=false 로 반영. IAM 밖에서 만든 Role 을 등록한 경우 allowRegistered=true", rr.CspRoleID)
	case model.TrustPolicyNotFound:
		out.Detail = strings.Join(msgs, "; ")
		out.NextAction = "CSP Role 의 IAM Identifier 확인, AWS 에 Role 이 없으면 다시 생성 (POST /api/roles/csp)"
	default:
		out.Detail = strings.Join(msgs, "; ")
	}
	return out
}

// roleIdentifiers ⑦ assume 에 쓰는 역할 식별자
type roleIdentifiers struct {
	iam, idp string
	role     *model.CspRole
}
