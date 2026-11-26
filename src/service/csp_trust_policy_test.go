package service

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAudKey          = "mcmp.example.com/auth/realms/mciam:aud"
	testSamlProviderArn = "arn:aws:iam::111111111111:saml-provider/mciam-saml"
)

func oidcTestConfig() *model.CspIdpConfig {
	return &model.CspIdpConfig{ID: 1, Name: "aws-oidc", AuthMethod: model.AuthMethodOIDC, IsActive: true,
		Config: map[string]string{"oidc_provider_arn": testOidcProviderArn, "audience": "mciam-oidc-Client", "role_arn": testManagementRoleArn}}
}

func samlTestConfig() *model.CspIdpConfig {
	return &model.CspIdpConfig{ID: 2, Name: "aws-saml", AuthMethod: model.AuthMethodSAML, IsActive: true,
		Config: map[string]string{"saml_provider_arn": testSamlProviderArn}}
}

const oidcOkDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"` + testOidcProviderArn + `"},"Action":"sts:AssumeRoleWithWebIdentity","Condition":{"StringEquals":{"` + testAudKey + `":"mciam-oidc-Client"}}}]}`

func TestEvaluateTrustPolicy_Verdicts(t *testing.T) {
	oidc := newTrustExpectation("OIDC", oidcTestConfig())
	saml := newTrustExpectation("SAML", samlTestConfig())
	cases := []struct {
		name   string
		doc    string
		exps   []trustExpectation
		status string
		stmt   []string
		extra  int
	}{
		{"ok (URL 인코딩)", url.QueryEscape(oidcOkDoc), []trustExpectation{oidc}, model.TrustPolicyOK, []string{model.TrustPolicyOK}, 0},
		{"ok + 다른 Condition 키는 보지 않음", `{"Statement":{"Effect":"Allow","Principal":{"Federated":["` + testOidcProviderArn + `"]},"Action":["sts:AssumeRoleWithWebIdentity","sts:TagSession"],"Condition":{"StringEquals":{"` + testAudKey + `":["mciam-oidc-Client"]},"StringLike":{"x:sub":"*"}}}}`,
			[]trustExpectation{oidc}, model.TrustPolicyOK, []string{model.TrustPolicyOK}, 0},
		{"missing (다른 provider 만)", `{"Statement":[{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::111111111111:oidc-provider/other"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`,
			[]trustExpectation{oidc}, model.TrustPolicyMissing, []string{model.TrustPolicyMissing}, 1},
		{"mismatch Action", `{"Statement":[{"Effect":"Allow","Principal":{"Federated":"` + testOidcProviderArn + `"},"Action":"sts:AssumeRole","Condition":{"StringEquals":{"` + testAudKey + `":"mciam-oidc-Client"}}}]}`,
			[]trustExpectation{oidc}, model.TrustPolicyMismatch, []string{model.TrustPolicyMismatch}, 0},
		{"mismatch aud 값", `{"Statement":[{"Effect":"Allow","Principal":{"Federated":"` + testOidcProviderArn + `"},"Action":"sts:AssumeRoleWithWebIdentity","Condition":{"StringEquals":{"` + testAudKey + `":"other-client"}}}]}`,
			[]trustExpectation{oidc}, model.TrustPolicyMismatch, []string{model.TrustPolicyMismatch}, 0},
		{"mismatch aud 조건 없음", `{"Statement":[{"Effect":"Allow","Principal":{"Federated":"` + testOidcProviderArn + `"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`,
			[]trustExpectation{oidc}, model.TrustPolicyMismatch, []string{model.TrustPolicyMismatch}, 0},
		{"Deny statement 는 신뢰가 아니다", `{"Statement":[{"Effect":"Deny","Principal":{"Federated":"` + testOidcProviderArn + `"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`,
			[]trustExpectation{oidc}, model.TrustPolicyMissing, []string{model.TrustPolicyMissing}, 1},
		{"extra 는 정보성", `{"Statement":[` + oidcOkDoc[len(`{"Version":"2012-10-17","Statement":[`):len(oidcOkDoc)-2] + `,{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
			[]trustExpectation{oidc}, model.TrustPolicyOK, []string{model.TrustPolicyOK}, 1},
		{"SAML ok", `{"Statement":[{"Effect":"Allow","Principal":{"Federated":"` + testSamlProviderArn + `"},"Action":"sts:AssumeRoleWithSAML","Condition":{"StringEquals":{"SAML:aud":"https://signin.aws.amazon.com/saml"}}}]}`,
			[]trustExpectation{saml}, model.TrustPolicyOK, []string{model.TrustPolicyOK}, 0},
		{"OIDC·SAML 둘 다 요구: SAML 없음", oidcOkDoc, []trustExpectation{oidc, saml}, model.TrustPolicyMissing, []string{model.TrustPolicyOK, model.TrustPolicyMissing}, 0},
		{"기대값 없음(Config 없음) → error", oidcOkDoc, []trustExpectation{newTrustExpectation("SAML", nil)}, model.TrustPolicyError, []string{model.TrustPolicyError}, 1},
		{"JSON 아님 → error", "not-json", []trustExpectation{oidc}, model.TrustPolicyError, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := evaluateTrustPolicy("mciam-t045-x", tc.doc, tc.exps)
			assert.Equal(t, tc.status, res.Status, "%+v", res)
			var got []string
			for _, st := range res.Statements {
				got = append(got, st.Status)
			}
			assert.Equal(t, tc.stmt, got)
			assert.Len(t, res.Extra, tc.extra, "%v", res.Extra)
		})
	}
}

func TestNewTrustExpectation_InvalidConfig(t *testing.T) {
	bad := oidcTestConfig()
	bad.Config["audience"] = "notyet"
	assert.Contains(t, newTrustExpectation("OIDC", bad).err, "audience")
	bad.Config["oidc_provider_arn"] = "not-an-arn"
	assert.Contains(t, newTrustExpectation("OIDC", bad).err, "not an AWS OIDC provider ARN")
	noSaml := samlTestConfig()
	delete(noSaml.Config, "saml_provider_arn")
	assert.Contains(t, newTrustExpectation("SAML", noSaml).err, "saml_provider_arn")
	e := newTrustExpectation("OIDC", oidcTestConfig())
	assert.Equal(t, testAudKey, e.audKey)
}

// 실제 AWS 조회(fake): not_found·ok·권한 없음, Master 매핑 auth_method 로 기대값
func TestVerifyTargetTrustPolicy(t *testing.T) {
	svc, api, cfg := newTargetTestService(t, nil)
	require.NoError(t, svc.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	role := &model.CspRole{Name: "renamed", CspType: "aws", Status: "created", IamIdentifier: "arn:aws:iam::111111111111:role/mciam-t045-tp", CspIdpConfigID: &cfg.ID}
	require.NoError(t, svc.db.Create(role).Error)

	res := svc.VerifyTargetTrustPolicy(context.TODO(), role)
	assert.Equal(t, model.TrustPolicyNotFound, res.Status)
	assert.Equal(t, "mciam-t045-tp", res.RoleName, "ARN 의 이름을 쓴다")

	api.roles["mciam-t045-tp"] = fakeAwsRole("mciam-t045-tp", oidcOkDoc) // fake 가 AWS 처럼 URL 인코딩한다
	res = svc.VerifyTargetTrustPolicy(context.TODO(), role)
	assert.Equal(t, model.TrustPolicyOK, res.Status, "%+v", res)
	require.Len(t, res.Statements, 1)
	assert.Equal(t, "OIDC", res.Statements[0].AuthMethod)

	// Master 매핑에 SAML 이 있으면 SAML statement 도 요구한다 (같은 계정의 SAML Config)
	saml := &model.CspIdpConfig{Name: "aws-saml", CspAccountID: cfg.CspAccountID, AuthMethod: model.AuthMethodSAML, IsActive: true, Config: map[string]string{"saml_provider_arn": testSamlProviderArn}}
	require.NoError(t, svc.db.Create(saml).Error)
	m := &model.RoleMaster{Name: "t045-m"}
	require.NoError(t, svc.db.Create(m).Error)
	require.NoError(t, svc.db.Create(&model.RoleMasterCspRoleMapping{RoleID: m.ID, CspRoleID: role.ID, AuthMethod: constants.AuthMethodSAML}).Error)
	require.NoError(t, svc.db.Create(&model.RoleMasterCspRoleMapping{RoleID: m.ID + 100, CspRoleID: role.ID, AuthMethod: constants.AuthMethodOIDC}).Error)
	res = svc.VerifyTargetTrustPolicy(context.TODO(), role)
	assert.Equal(t, model.TrustPolicyMissing, res.Status)
	require.Len(t, res.Statements, 2)
	assert.Equal(t, saml.ID, res.Statements[1].IdpConfigID)

	// 특정 방식만 (진단 API)
	assert.Equal(t, model.TrustPolicyOK, svc.VerifyTargetTrustPolicy(context.TODO(), role, "OIDC").Status)

	api.getErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
	res = svc.VerifyTargetTrustPolicy(context.TODO(), role, "OIDC")
	assert.Equal(t, model.TrustPolicyError, res.Status)
	assert.Contains(t, res.Message, "iam:GetRole")
}

// Config 규칙: 소속 Config 가 비활성이면 같은 계정의 활성 Config 로 기대값을 만든다(경고). 읽기 전용이라 대체를 허용한다
func TestVerifyTargetTrustPolicy_InactiveConfigUsesAccountConfig(t *testing.T) {
	svc, api, cfg := newTargetTestService(t, nil)
	require.NoError(t, svc.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	old := &model.CspIdpConfig{Name: "old", CspAccountID: cfg.CspAccountID, AuthMethod: model.AuthMethodOIDC, IsActive: true,
		Config: map[string]string{"oidc_provider_arn": "arn:aws:iam::111111111111:oidc-provider/old/auth/realms/x", "audience": "old", "role_arn": testManagementRoleArn}}
	require.NoError(t, svc.db.Create(old).Error)
	require.NoError(t, svc.db.Model(old).Update("is_active", false).Error)
	role := &model.CspRole{Name: "mciam-t045-in", CspType: "aws", Status: "created", CspIdpConfigID: &old.ID, CspAccountID: &cfg.CspAccountID}
	require.NoError(t, svc.db.Create(role).Error)
	api.roles["mciam-t045-in"] = fakeAwsRole("mciam-t045-in", oidcOkDoc)
	res := svc.VerifyTargetTrustPolicy(context.TODO(), role)
	assert.Equal(t, model.TrustPolicyOK, res.Status, "%+v", res)
	assert.Equal(t, cfg.ID, res.Statements[0].IdpConfigID)
	assert.NotEmpty(t, res.Warnings)
}

func TestVerifyTargetProvider(t *testing.T) {
	svc, api, cfg := newTargetTestService(t, nil)
	require.NoError(t, svc.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	role := &model.CspRole{Name: "mciam-t045-pv", CspType: "aws", IdpIdentifier: "arn:aws:iam::111111111111:oidc-provider/stale", CspIdpConfigID: &cfg.ID}
	require.NoError(t, svc.db.Create(role).Error)

	detail, err := svc.VerifyTargetProvider(context.TODO(), role, "OIDC")
	require.NoError(t, err)
	assert.Contains(t, detail, "warning: CSP role idp_identifier", "저장값이 Config 와 다르면 경고")

	api.providers[testOidcProviderArn] = []string{"other"}
	_, err = svc.VerifyTargetProvider(context.TODO(), role, "OIDC")
	assert.ErrorContains(t, err, "not in the client ID list")

	api.providers = map[string][]string{}
	_, err = svc.VerifyTargetProvider(context.TODO(), role, "OIDC")
	assert.ErrorContains(t, err, "does not exist")

	_, err = svc.VerifyTargetProvider(context.TODO(), role, "SAML")
	assert.ErrorContains(t, err, "SAML IdP config", "SAML Config 가 없으면 기대값을 만들 수 없다")

	saml := &model.CspIdpConfig{Name: "aws-saml", CspAccountID: cfg.CspAccountID, AuthMethod: model.AuthMethodSAML, IsActive: true, Config: map[string]string{"saml_provider_arn": testSamlProviderArn}}
	require.NoError(t, svc.db.Create(saml).Error)
	_, err = svc.VerifyTargetProvider(context.TODO(), role, "SAML")
	assert.ErrorContains(t, err, "does not exist")
	var pnf *ProviderNotFoundError
	require.ErrorAs(t, err, &pnf, "provider 없음은 발급 사전 확인이 구분할 수 있는 타입")
	assert.Equal(t, testSamlProviderArn, pnf.ProviderArn)
	assert.ErrorIs(t, err, ErrCspProviderNotFound)
	api.samlProviders = map[string]bool{testSamlProviderArn: true}
	_, err = svc.VerifyTargetProvider(context.TODO(), role, "SAML")
	assert.NoError(t, err)

	api.providerErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
	_, err = svc.VerifyTargetProvider(context.TODO(), role, "SAML")
	assert.ErrorContains(t, err, "not allowed")
	assert.NotErrorIs(t, err, ErrCspProviderNotFound, "권한 부족은 provider 없음이 아니다")
}

// check: DB 에 있는 Role 과 AWS 에만 있는 Role 모두 신뢰 정책 판정을 붙인다
func TestCheckTargetCspRole_TrustPolicy(t *testing.T) {
	svc, api, cfg := newTargetTestService(t, nil)
	require.NoError(t, svc.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	api.roles["mciam-t045-aws"] = fakeAwsRole("mciam-t045-aws", oidcOkDoc)
	res := svc.CheckTargetCspRole(model.CreateCspRoleRequest{CspRoleName: "t045-aws", CspType: "aws", CspIdpConfigID: &cfg.ID})
	assert.Equal(t, TargetCheckExistsInCsp, res.Status)
	require.NotNil(t, res.TrustPolicy)
	assert.Equal(t, model.TrustPolicyOK, res.TrustPolicy.Status)

	db := &model.CspRole{Name: "mciam-t045-db", CspType: "aws", Status: "created", IamIdentifier: "arn:aws:iam::111111111111:role/mciam-t045-db", CspIdpConfigID: &cfg.ID}
	require.NoError(t, svc.db.Create(db).Error)
	api.roles["mciam-t045-db"] = fakeAwsRole("mciam-t045-db", `{"Statement":[{"Effect":"Allow","Principal":{"Federated":"`+testOidcProviderArn+`"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`)
	res = svc.CheckTargetCspRole(model.CreateCspRoleRequest{CspRoleName: "t045-db", CspType: "aws", CspIdpConfigID: &cfg.ID})
	assert.Equal(t, TargetCheckExistsInDB, res.Status, "신뢰 정책 판정이 나빠도 status 는 그대로")
	require.NotNil(t, res.TrustPolicy)
	assert.Equal(t, model.TrustPolicyMismatch, res.TrustPolicy.Status)

	res = svc.CheckTargetCspRole(model.CreateCspRoleRequest{CspRoleName: "t045-none", CspType: "aws", CspIdpConfigID: &cfg.ID})
	assert.Equal(t, TargetCheckNotFound, res.Status)
	assert.Nil(t, res.TrustPolicy)
}

// 관리용 자격증명 캐시: 키에 Config 를 넣는다 (계정이 여럿이어도 섞이지 않는다), 만료 5분 전이면 다시 발급
func TestMgmtCredentialCache(t *testing.T) {
	c := &mgmtCredentialCache{entries: map[string]mgmtCredentialEntry{}}
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	issued := 0
	issue := func(region string, ttl time.Duration) func() (aws.Config, time.Time, error) {
		return func() (aws.Config, time.Time, error) {
			issued++
			return aws.Config{Region: region}, now.Add(ttl), nil
		}
	}
	a := &model.CspIdpConfig{ID: 1, Config: map[string]string{"role_arn": "arn:aws:iam::111111111111:role/mgmt"}}
	b := &model.CspIdpConfig{ID: 2, Config: map[string]string{"role_arn": "arn:aws:iam::222222222222:role/mgmt"}}
	assert.NotEqual(t, mgmtCredentialKey(a), mgmtCredentialKey(b))

	got, err := c.get(mgmtCredentialKey(a), now, issue("a", time.Hour))
	require.NoError(t, err)
	assert.Equal(t, "a", got.Region)
	got, _ = c.get(mgmtCredentialKey(a), now.Add(10*time.Minute), issue("a2", time.Hour))
	assert.Equal(t, "a", got.Region, "유효하면 재사용")
	got, _ = c.get(mgmtCredentialKey(b), now, issue("b", time.Hour))
	assert.Equal(t, "b", got.Region, "다른 Config 는 따로 발급")
	got, _ = c.get(mgmtCredentialKey(a), now.Add(56*time.Minute), issue("a3", time.Hour))
	assert.Equal(t, "a3", got.Region, "만료 5분 전이면 다시 발급")
	assert.Equal(t, 3, issued)

	_, err = c.get("x", now, func() (aws.Config, time.Time, error) { return aws.Config{}, time.Time{}, errors.New("sts down") })
	assert.Error(t, err)
	_, ok := c.entries["x"]
	assert.False(t, ok, "실패는 캐시하지 않는다")
}

// 관리용 Config: 소속 Config 가 SAML 이면 같은 계정의 활성 OIDC
func TestManagementConfigFor(t *testing.T) {
	svc, _, cfg := newTargetTestService(t, nil)
	got, err := managementConfigFor(svc.cspIdpConfigRepo, cfg)
	require.NoError(t, err)
	assert.Equal(t, cfg.ID, got.ID)

	saml := &model.CspIdpConfig{Name: "aws-saml", CspAccountID: cfg.CspAccountID, AuthMethod: model.AuthMethodSAML, IsActive: true, Config: map[string]string{}}
	require.NoError(t, svc.db.Create(saml).Error)
	saml.CspAccount = cfg.CspAccount
	got, err = managementConfigFor(svc.cspIdpConfigRepo, saml)
	require.NoError(t, err)
	assert.Equal(t, cfg.ID, got.ID)

	_, err = managementConfigFor(svc.cspIdpConfigRepo, nil)
	assert.Error(t, err)
	other := seedIdpConfig(t, svc.db, "other", "aws", model.AuthMethodSAML, true, map[string]string{})
	_, err = managementConfigFor(svc.cspIdpConfigRepo, other)
	assert.ErrorContains(t, err, "0 active aws OIDC")
}
