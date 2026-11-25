package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── readiness 전용 fake ──────────────────────────────────────────────────────

type fakeRdAccountRepo struct{ account *model.CspAccount }

func (f *fakeRdAccountRepo) GetByID(id uint) (*model.CspAccount, error) {
	if f.account == nil || f.account.ID != id {
		return nil, nil
	}
	return f.account, nil
}

type fakeRdIdpRepo struct{ configs []*model.CspIdpConfig }

func (f *fakeRdIdpRepo) List(filter *model.CspIdpConfigFilter) ([]*model.CspIdpConfig, error) {
	return f.configs, nil
}

type fakeRdRoleRepo struct{ roles []*model.CspRole }

func (f *fakeRdRoleRepo) FindForAccountReadiness(cspType string, accountID uint) ([]*model.CspRole, error) {
	return f.roles, nil
}

type fakeRdMappingRepo struct{ counts map[uint]int }

func (f *fakeRdMappingRepo) CountMappingsByCspRoleIDs(ids []uint) (map[uint]int, error) {
	if f.counts == nil {
		return map[uint]int{}, nil
	}
	return f.counts, nil
}

type fakeRdTokens struct {
	ccErr, impErr error
	impCalls      int
}

func (f *fakeRdTokens) GetClientCredentialsToken(ctx context.Context) (*gocloak.JWT, error) {
	if f.ccErr != nil {
		return nil, f.ccErr
	}
	return &gocloak.JWT{AccessToken: "cc"}, nil
}

func (f *fakeRdTokens) GetImpersonationTokenByServiceAccount(ctx context.Context) (*gocloak.JWT, error) {
	f.impCalls++
	if f.impErr != nil {
		return nil, f.impErr
	}
	return &gocloak.JWT{AccessToken: "imp"}, nil
}

type fakeRdConnTester struct {
	errs  map[uint]error
	calls []uint
}

func (f *fakeRdConnTester) TestConnection(ctx context.Context, id uint) error {
	f.calls = append(f.calls, id)
	return f.errs[id]
}

type fakeRdAssumer struct {
	errs    map[string]error // roleArn -> err
	calls   []string
	regions []string
}

func (f *fakeRdAssumer) AssumeRoleWithWebIdentity(ctx context.Context, roleArn, kcUserId, token, idpArn, region string) (*model.CspCredentialResponse, error) {
	f.calls = append(f.calls, roleArn)
	f.regions = append(f.regions, region)
	if err := f.errs[roleArn]; err != nil {
		return nil, err
	}
	return &model.CspCredentialResponse{AccessKeyId: "AKIA-SECRET"}, nil
}

// ── fixture ──────────────────────────────────────────────────────────────────

const (
	rdIdp  = "arn:aws:iam::111122223333:oidc-provider/kc.example.com/auth/realms/mciam"
	rdRole = "arn:aws:iam::111122223333:role/mciam-dev"
)

func uintP(v uint) *uint { return &v }

type rdFixture struct {
	svc      *CspAccountReadinessService
	tokens   *fakeRdTokens
	conn     *fakeRdConnTester
	assumer  *fakeRdAssumer
	roles    *fakeRdRoleRepo
	configs  *fakeRdIdpRepo
	mappings *fakeRdMappingRepo
	account  *model.CspAccount
}

// 정상 AWS OIDC 구성: 계정 1, IdP Config 10(role_arn), CspRole 100(매핑 1건)
func newRdFixture() *rdFixture {
	account := &model.CspAccount{ID: 1, Name: "aws-dev", CspType: "aws", IsActive: true,
		AccountInfo: map[string]string{"account_id": "111122223333", "region": "ap-northeast-2"}}
	cfg := &model.CspIdpConfig{ID: 10, Name: "aws-oidc", CspAccountID: 1, AuthMethod: model.AuthMethodOIDC, IsActive: true,
		Config: map[string]string{"role_arn": "arn:aws:iam::111122223333:role/mciam-admin"}}
	role := &model.CspRole{ID: 100, Name: "mciam-dev", CspType: "aws", Status: "created",
		IdpIdentifier: rdIdp, IamIdentifier: rdRole,
		CspAccountID: uintP(1), CspIdpConfigID: uintP(10), CspIdpConfig: cfg}

	f := &rdFixture{
		tokens:   &fakeRdTokens{},
		conn:     &fakeRdConnTester{errs: map[uint]error{}},
		assumer:  &fakeRdAssumer{errs: map[string]error{}},
		roles:    &fakeRdRoleRepo{roles: []*model.CspRole{role}},
		configs:  &fakeRdIdpRepo{configs: []*model.CspIdpConfig{cfg}},
		mappings: &fakeRdMappingRepo{counts: map[uint]int{100: 1}},
		account:  account,
	}
	f.svc = &CspAccountReadinessService{
		accountRepo:   &fakeRdAccountRepo{account: account},
		idpConfigRepo: f.configs,
		roleRepo:      f.roles,
		mappingRepo:   f.mappings,
		tokens:        f.tokens,
		connTester:    f.conn,
		awsAssumer:    f.assumer,
		now:           func() time.Time { return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) },
	}
	return f
}

func stageStatus(t *testing.T, resp *model.CspAccountReadinessResponse) map[int]model.ReadinessStatus {
	t.Helper()
	require.Len(t, resp.Stages, 7)
	out := map[int]model.ReadinessStatus{}
	for i, st := range resp.Stages {
		require.Equal(t, i+1, st.ID, "단계 순서")
		out[st.ID] = st.Status
	}
	return out
}

var adminIssue = ReadinessOptions{LiveChecks: true, CheckIssue: true}

// ── tests ────────────────────────────────────────────────────────────────────

func TestReadiness_AccountNotFound(t *testing.T) {
	f := newRdFixture()
	_, err := f.svc.GetReadiness(context.Background(), 999, adminIssue)
	assert.ErrorIs(t, err, ErrCspAccountNotFound)
}

func TestReadiness_AllOK_AdminCheckIssue(t *testing.T) {
	f := newRdFixture()
	resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)

	st := stageStatus(t, resp)
	for id := 1; id <= 7; id++ {
		assert.Equal(t, model.ReadinessOK, st[id], "stage %d: %s", id, resp.Stages[id-1].Detail)
	}
	assert.True(t, resp.CanMapRoles)
	require.NotNil(t, resp.CanIssue)
	assert.True(t, *resp.CanIssue)
	assert.True(t, resp.LiveChecks)
	assert.Equal(t, "2026-11-25T00:00:00Z", resp.CheckedAt)

	require.Len(t, resp.CspRoles, 1)
	rr := resp.CspRoles[0]
	assert.Equal(t, model.ReadinessLinkageAccount, rr.Linkage)
	assert.Equal(t, "OIDC", rr.AuthMethod)
	assert.Equal(t, 1, rr.MappingCount)
	assert.Equal(t, model.ReadinessOK, rr.IssueStatus)
	assert.NotContains(t, rr.IssueDetail, "AKIA", "자격증명은 응답에 담지 않음")

	assert.Equal(t, []uint{10}, f.conn.calls)
	assert.Equal(t, []string{rdRole}, f.assumer.calls)
	assert.Equal(t, 1, f.tokens.impCalls)
}

func TestReadiness_NoCheckIssue_DoesNotAssume(t *testing.T) {
	f := newRdFixture()
	resp, err := f.svc.GetReadiness(context.Background(), 1, ReadinessOptions{LiveChecks: true})
	require.NoError(t, err)

	st := stageStatus(t, resp)
	assert.Equal(t, model.ReadinessOK, st[4], "Admin 이면 ④ 는 항상 수행")
	assert.Equal(t, model.ReadinessPending, st[7])
	assert.Nil(t, resp.CanIssue)
	assert.True(t, resp.CanMapRoles)
	assert.Empty(t, f.assumer.calls)
	assert.Equal(t, 0, f.tokens.impCalls)
	assert.Equal(t, model.ReadinessPending, resp.CspRoles[0].IssueStatus)
}

func TestReadiness_NonAdmin_SkipsLiveCalls(t *testing.T) {
	f := newRdFixture()
	resp, err := f.svc.GetReadiness(context.Background(), 1, ReadinessOptions{LiveChecks: false, CheckIssue: true})
	require.NoError(t, err)

	st := stageStatus(t, resp)
	assert.Equal(t, model.ReadinessOK, st[1], "Keycloak 토큰 확인은 권한과 무관")
	assert.Equal(t, model.ReadinessSkipped, st[4])
	assert.Equal(t, model.ReadinessSkipped, st[7])
	assert.Nil(t, resp.CanIssue)
	assert.False(t, resp.LiveChecks)
	assert.Empty(t, f.conn.calls)
	assert.Empty(t, f.assumer.calls)
	assert.Equal(t, model.ReadinessSkipped, resp.CspRoles[0].IssueStatus)
}

func TestReadiness_KeycloakClientMissing(t *testing.T) {
	f := newRdFixture()
	f.tokens.ccErr = errors.New("401 Unauthorized: invalid_client")
	resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)

	st := stageStatus(t, resp)
	assert.Equal(t, model.ReadinessFailed, st[1])
	assert.Contains(t, resp.Stages[0].Detail, "invalid_client")
	assert.NotEmpty(t, resp.Stages[0].NextAction)
	assert.Equal(t, model.ReadinessFailed, st[4], "OIDC 연결 테스트는 토큰 없이 불가")
	assert.Empty(t, f.conn.calls)
	assert.Empty(t, f.assumer.calls)
	assert.Equal(t, model.ReadinessSkipped, resp.CspRoles[0].IssueStatus)
	require.NotNil(t, resp.CanIssue)
	assert.False(t, *resp.CanIssue)
}

func TestReadiness_InactiveAccountAndMissingAccountInfo(t *testing.T) {
	f := newRdFixture()
	f.account.IsActive = false
	resp, err := f.svc.GetReadiness(context.Background(), 1, ReadinessOptions{})
	require.NoError(t, err)
	assert.Equal(t, model.ReadinessFailed, stageStatus(t, resp)[2])

	f.account.IsActive = true
	f.account.AccountInfo = map[string]string{}
	resp, err = f.svc.GetReadiness(context.Background(), 1, ReadinessOptions{})
	require.NoError(t, err)
	assert.Equal(t, model.ReadinessFailed, stageStatus(t, resp)[2])
	assert.Contains(t, resp.Stages[1].Detail, "account_id")
}

func TestReadiness_IdpConfig(t *testing.T) {
	t.Run("활성 설정 없음 → pending, ④ skipped", func(t *testing.T) {
		f := newRdFixture()
		f.configs.configs[0].IsActive = false
		resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
		require.NoError(t, err)
		st := stageStatus(t, resp)
		assert.Equal(t, model.ReadinessPending, st[3])
		assert.Equal(t, model.ReadinessSkipped, st[4])
		assert.Empty(t, f.conn.calls)
	})
	t.Run("role_arn 누락 → failed", func(t *testing.T) {
		f := newRdFixture()
		f.configs.configs[0].Config = map[string]string{}
		resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
		require.NoError(t, err)
		st := stageStatus(t, resp)
		assert.Equal(t, model.ReadinessFailed, st[3])
		assert.Contains(t, resp.Stages[2].Detail, "role_arn")
		assert.Equal(t, model.ReadinessSkipped, st[4])
	})
	t.Run("연결 테스트 실패 → ④ failed", func(t *testing.T) {
		f := newRdFixture()
		f.conn.errs[10] = errors.New("AccessDenied")
		resp, err := f.svc.GetReadiness(context.Background(), 1, ReadinessOptions{LiveChecks: true})
		require.NoError(t, err)
		assert.Equal(t, model.ReadinessFailed, stageStatus(t, resp)[4])
		assert.Contains(t, resp.Stages[3].Detail, "AccessDenied")
	})
}

func TestReadiness_CspRoleProblems(t *testing.T) {
	f := newRdFixture()
	otherCfg := &model.CspIdpConfig{ID: 20, Name: "other", CspAccountID: 2, AuthMethod: model.AuthMethodOIDC, IsActive: true}
	f.roles.roles = append(f.roles.roles,
		// notyet 이 들어간 신뢰 정책 (IAM-BUG-041 증상), 계정 미기록
		&model.CspRole{ID: 101, Name: "mciam-notyet", CspType: "aws", Status: "created",
			IdpIdentifier: "arn:aws:iam::notyet:oidc-provider/x/realms/mciam", IamIdentifier: "arn:aws:iam::111122223333:role/mciam-notyet"},
		// 생성 실패
		&model.CspRole{ID: 102, Name: "mciam-failed", CspType: "aws", Status: "failed", IdpIdentifier: rdIdp},
		// 다른 계정의 IdP Config 에 연결
		&model.CspRole{ID: 103, Name: "mciam-other", CspType: "aws", Status: "created", IdpIdentifier: rdIdp,
			IamIdentifier: "arn:aws:iam::111122223333:role/mciam-other", CspIdpConfigID: uintP(20), CspIdpConfig: otherCfg},
		// SAML 인데 saml_client_id 없음
		&model.CspRole{ID: 104, Name: "mciam-saml", CspType: "aws", Status: "created", IdpIdentifier: rdIdp,
			IamIdentifier: "arn:aws:iam::111122223333:role/mciam-saml", CspIdpConfigID: uintP(30),
			CspIdpConfig: &model.CspIdpConfig{ID: 30, CspAccountID: 1, AuthMethod: model.AuthMethodSAML, IsActive: true}},
	)
	resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)

	byID := map[uint]model.CspRoleReadiness{}
	for _, rr := range resp.CspRoles {
		byID[rr.CspRoleID] = rr
	}
	assert.Equal(t, model.ReadinessOK, byID[100].Status)

	assert.Equal(t, model.ReadinessFailed, byID[101].Status)
	assert.Contains(t, byID[101].Detail, "notyet")
	assert.Equal(t, model.ReadinessLinkageCspType, byID[101].Linkage)

	assert.Equal(t, model.ReadinessFailed, byID[102].Status)
	assert.Contains(t, byID[102].Detail, "status")
	assert.Contains(t, byID[102].Detail, "IAM Identifier")

	assert.Equal(t, model.ReadinessFailed, byID[103].Status)
	assert.Contains(t, byID[103].Detail, "다른 계정")

	assert.Equal(t, model.ReadinessFailed, byID[104].Status)
	assert.Contains(t, byID[104].Detail, "saml_client_id")

	for _, id := range []uint{101, 102, 103, 104} {
		assert.Equal(t, model.ReadinessSkipped, byID[id].MappingStatus)
		assert.Equal(t, model.ReadinessSkipped, byID[id].IssueStatus)
	}
	// 설정 미완료 역할은 assume 하지 않음
	assert.Equal(t, []string{rdRole}, f.assumer.calls)
	assert.Equal(t, model.ReadinessOK, stageStatus(t, resp)[5])
	assert.Contains(t, resp.Stages[4].Detail, "1 / 5")
}

func TestReadiness_NoRoles(t *testing.T) {
	f := newRdFixture()
	f.roles.roles = nil
	resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)
	st := stageStatus(t, resp)
	assert.Equal(t, model.ReadinessPending, st[5])
	assert.Equal(t, model.ReadinessSkipped, st[6])
	assert.Equal(t, model.ReadinessSkipped, st[7])
	assert.False(t, resp.CanMapRoles)
	assert.Nil(t, resp.CanIssue)
	assert.NotNil(t, resp.CspRoles, "빈 배열로 직렬화")
}

func TestReadiness_AssumeOKButNotMapped(t *testing.T) {
	f := newRdFixture()
	f.mappings.counts = nil
	resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)
	st := stageStatus(t, resp)
	assert.Equal(t, model.ReadinessPending, st[6])
	assert.Equal(t, model.ReadinessPending, st[7])
	assert.True(t, resp.CanMapRoles)
	require.NotNil(t, resp.CanIssue)
	assert.False(t, *resp.CanIssue, "매핑 없이는 실제 발급 불가")
	assert.Equal(t, model.ReadinessOK, resp.CspRoles[0].IssueStatus)
}

func TestReadiness_AssumeFails(t *testing.T) {
	f := newRdFixture()
	f.assumer.errs[rdRole] = errors.New("InvalidIdentityToken: audience mismatch")
	resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)
	assert.Equal(t, model.ReadinessFailed, stageStatus(t, resp)[7])
	assert.Equal(t, model.ReadinessFailed, resp.CspRoles[0].IssueStatus)
	assert.Contains(t, resp.CspRoles[0].IssueDetail, "audience")
	require.NotNil(t, resp.CanIssue)
	assert.False(t, *resp.CanIssue)
}

func TestReadiness_NonAwsCsp_NotApplicable(t *testing.T) {
	f := newRdFixture()
	f.account.CspType = "gcp"
	f.account.AccountInfo = map[string]string{"project_id": "p1"}
	cfg := &model.CspIdpConfig{ID: 10, Name: "gcp-oidc", CspAccountID: 1, AuthMethod: model.AuthMethodOIDC, IsActive: true}
	f.configs.configs = []*model.CspIdpConfig{cfg}
	f.roles.roles = []*model.CspRole{{ID: 100, Name: "mciam-gcp", CspType: "gcp", Status: "created",
		IdpIdentifier: "projects/1/locations/global/workloadIdentityPools/p/providers/kc", IamIdentifier: "sa@p1.iam.gserviceaccount.com",
		CspAccountID: uintP(1), CspIdpConfigID: uintP(10), CspIdpConfig: cfg}}

	resp, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)
	st := stageStatus(t, resp)
	assert.Equal(t, model.ReadinessOK, st[3], "GCP OIDC 는 필수 config 키 없음")
	assert.Equal(t, model.ReadinessNotApplicable, st[4])
	assert.Equal(t, model.ReadinessOK, st[5])
	assert.Equal(t, model.ReadinessNotApplicable, st[7])
	assert.Equal(t, model.ReadinessNotApplicable, resp.CspRoles[0].IssueStatus)
	assert.Nil(t, resp.CanIssue)
	assert.Empty(t, f.conn.calls)
	assert.Empty(t, f.assumer.calls)
}

func TestReadiness_AlibabaRequiresConfigLink(t *testing.T) {
	f := newRdFixture()
	f.account.CspType = "alibaba"
	f.configs.configs = []*model.CspIdpConfig{{ID: 10, Name: "ali", CspAccountID: 1, AuthMethod: model.AuthMethodOIDC, IsActive: true,
		Config: map[string]string{}}}
	f.roles.roles = []*model.CspRole{{ID: 100, Name: "mciam-ali", CspType: "alibaba", Status: "created",
		IdpIdentifier: "acs:ram::1:oidc-provider/kc", IamIdentifier: "acs:ram::1:role/mciam-ali"}}

	resp, err := f.svc.GetReadiness(context.Background(), 1, ReadinessOptions{})
	require.NoError(t, err)
	st := stageStatus(t, resp)
	assert.Equal(t, model.ReadinessFailed, st[3])
	assert.Contains(t, resp.Stages[2].Detail, "audience")
	assert.Equal(t, model.ReadinessFailed, resp.CspRoles[0].Status)
	assert.Contains(t, resp.CspRoles[0].Detail, "cspIdpConfigId")
}

func TestIdpConfigRequiredKeys(t *testing.T) {
	assert.Equal(t, []string{"role_arn"}, idpConfigRequiredKeys("aws", model.AuthMethodOIDC))
	assert.Equal(t, []string{"tenant_id", "client_id"}, idpConfigRequiredKeys("azure", model.AuthMethodOIDC))
	assert.Equal(t, []string{"secret_id", "secret_key"}, idpConfigRequiredKeys("tencent", model.AuthMethodOIDC))
	assert.Equal(t, []string{"access_key_id", "secret_access_key"}, idpConfigRequiredKeys("ncp", model.AuthMethodSecretKey))
	assert.Nil(t, idpConfigRequiredKeys("aws", model.AuthMethodSAML))
	assert.Nil(t, idpConfigRequiredKeys("gcp", model.AuthMethodOIDC))
}

func TestReadiness_AssumeRegion(t *testing.T) {
	f := newRdFixture()
	_, err := f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)
	assert.Equal(t, []string{"ap-northeast-2"}, f.assumer.regions)

	// 계정에 region 이 없으면 기본 리전 (빈 region 은 STS endpoint 해석 실패)
	f = newRdFixture()
	f.account.AccountInfo = map[string]string{"account_id": "111122223333"}
	_, err = f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)
	assert.Equal(t, []string{readinessDefaultAwsRegion}, f.assumer.regions)

	f = newRdFixture()
	f.account.AccountInfo["region"] = "us-east-1"
	_, err = f.svc.GetReadiness(context.Background(), 1, adminIssue)
	require.NoError(t, err)
	assert.Equal(t, []string{"us-east-1"}, f.assumer.regions)
}
