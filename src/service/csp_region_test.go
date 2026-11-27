package service

// IAM-BUG-065: 임시자격증명 region 결정 — 요청 우선, 계정 대체, 필수 CSP 400, aws 글로벌, 계정 PUT 보존

import (
	"context"
	"errors"
	"testing"

	"github.com/Nerzal/gocloak/v13"
	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acct(cspType, region string) *model.CspAccount {
	info := map[string]string{"account_id": "111122223333"}
	if region != "" {
		info["region"] = region
	}
	return &model.CspAccount{CspType: cspType, AccountInfo: info}
}

func TestResolveCspRegion(t *testing.T) {
	cases := []struct {
		name, csp, requested string
		account              *model.CspAccount
		region, source, call string
		warn                 bool
		err                  error
	}{
		{"요청 우선", "aws", "us-west-2", acct("aws", "ap-northeast-2"), "us-west-2", RegionSourceRequest, "us-west-2", false, nil},
		{"계정 대체", "aws", "", acct("aws", "ap-northeast-2"), "ap-northeast-2", RegionSourceAccount, "ap-northeast-2", false, nil},
		{"aws 둘 다 없음 → 글로벌", "aws", "", acct("aws", ""), "", RegionSourceNone, "us-east-1", true, nil},
		{"aws 계정 nil → 글로벌", "aws", "", nil, "", RegionSourceNone, "us-east-1", true, nil},
		{"tencent 필수", "tencent", "", acct("tencent", ""), "", "", "", false, ErrRegionRequired},
		{"tencent 계정 region", "tencent", "", acct("tencent", "ap-guangzhou"), "ap-guangzhou", RegionSourceAccount, "ap-guangzhou", false, nil},
		{"alibaba 없이 진행", "alibaba", "", nil, "", RegionSourceNone, "", false, nil},
		{"gcp 없이 진행", "gcp", "", nil, "", RegionSourceNone, "", false, nil},
		{"요청 형식 오류", "aws", "Seoul!", nil, "", "", "", false, ErrInvalidRegion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCspRegion(tc.csp, tc.requested, tc.account)
			if tc.err != nil {
				assert.True(t, errors.Is(err, tc.err), "%v", err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.region, got.Region)
			assert.Equal(t, tc.source, got.Source)
			assert.Equal(t, tc.call, got.CallRegion)
			assert.Equal(t, tc.warn, got.Warning != "")
		})
	}
}

func TestValidateRegionFormat(t *testing.T) {
	for _, ok := range []string{"ap-northeast-2", "us-gov-west-1", "cn-hangzhou", "ap-guangzhou", "asia-northeast3", "na-siliconvalley"} {
		assert.NoError(t, ValidateRegionFormat(ok), ok)
	}
	for _, bad := range []string{"", "AP-NORTHEAST-2", "seoul", "ap_northeast_2", "ap-northeast-2 "} {
		assert.Error(t, ValidateRegionFormat(bad), bad)
	}
}

func credSvcWithAccountRegion(aws *mockAwsCredService, accountRegion string) *CspCredentialService {
	mapping := buildMapping(constants.AuthMethodOIDC, idpArn, roleArn, model.AuthMethodOIDC, nil)
	mapping.CspRoles[0].CspIdpConfig.CspAccount = acct("aws", accountRegion)
	return newCredServiceWithMocks(credServiceDeps{
		aws: aws, gcp: &mockGcpCredService{}, alibaba: &mockAlibabaCredService{}, kc: oidcKC(),
		userRepo: &mockUserRepoForCred{role: stdUserRole()},
		mapRepo:  &mockCspMappingRepo{mapping: mapping},
	})
}

// 발급: region 없는 요청은 계정 region 으로 STS 호출·응답 region·regionSource=account
func TestGetTemporaryCredentials_Region_AccountFallback(t *testing.T) {
	aws := &mockAwsCredService{oidcResult: &model.CspCredentialResponse{CspType: "aws", AccessKeyId: "ASIA", Region: "ignored"}}
	svc := credSvcWithAccountRegion(aws, "ap-northeast-2")
	r := req("aws", "OIDC")
	r.Region = ""
	cred, err := svc.GetTemporaryCredentials(context.Background(), 1, "kc", r)
	require.NoError(t, err)
	assert.Equal(t, "ap-northeast-2", aws.lastRegion)
	assert.Equal(t, "ap-northeast-2", cred.Region)
	assert.Equal(t, RegionSourceAccount, cred.RegionSource)
	assert.Empty(t, cred.Warnings)
}

// 발급: 요청 region 이 계정 region 보다 우선
func TestGetTemporaryCredentials_Region_RequestWins(t *testing.T) {
	aws := &mockAwsCredService{oidcResult: &model.CspCredentialResponse{CspType: "aws", AccessKeyId: "ASIA"}}
	svc := credSvcWithAccountRegion(aws, "ap-northeast-2")
	r := req("aws", "OIDC")
	r.Region = "us-west-2"
	cred, err := svc.GetTemporaryCredentials(context.Background(), 1, "kc", r)
	require.NoError(t, err)
	assert.Equal(t, "us-west-2", aws.lastRegion)
	assert.Equal(t, RegionSourceRequest, cred.RegionSource)
}

// 발급: aws 요청·계정 모두 없음 → 글로벌 STS, 응답 region 비움, warnings
func TestGetTemporaryCredentials_Region_AwsGlobal(t *testing.T) {
	aws := &mockAwsCredService{oidcResult: &model.CspCredentialResponse{CspType: "aws", AccessKeyId: "ASIA", Region: "us-east-1"}}
	svc := credSvcWithAccountRegion(aws, "")
	r := req("aws", "OIDC")
	r.Region = ""
	cred, err := svc.GetTemporaryCredentials(context.Background(), 1, "kc", r)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", aws.lastRegion)
	assert.Equal(t, "", cred.Region)
	assert.Equal(t, RegionSourceNone, cred.RegionSource)
	require.Len(t, cred.Warnings, 1)
	assert.Contains(t, cred.Warnings[0], "region 미설정")
}

// 발급: tencent 요청·계정 모두 없음 → ErrRegionRequired, STS 미호출
func TestGetTemporaryCredentials_Region_TencentRequired(t *testing.T) {
	tencent := &mockTencentCredService{result: tencentSamlCred}
	mapping := buildMapping(constants.AuthMethodSAML, idpArn, roleArn, model.AuthMethodSAML, map[string]string{"secret_id": "id", "secret_key": "key"})
	svc := newCredServiceWithMocks(credServiceDeps{
		aws: &mockAwsCredService{}, gcp: &mockGcpCredService{}, alibaba: &mockAlibabaCredService{}, azure: &mockAzureCredService{},
		tencent: tencent, ibm: &mockIbmCredService{}, kc: samlKC(),
		userRepo: &mockUserRepoForCred{role: stdUserRole()},
		mapRepo:  &mockCspMappingRepo{mapping: mapping},
	})
	r := req("tencent", "SAML")
	r.Region = ""
	_, err := svc.GetTemporaryCredentials(context.Background(), 1, "kc", r)
	assert.True(t, errors.Is(err, ErrRegionRequired), "%v", err)
	assert.Equal(t, 0, tencent.calls, "STS 를 부르지 않는다")
}

// 계정: 생성 시 tencent region 필수·형식 검증, PUT 에 region 키가 없으면 보존, "" 는 삭제
func TestCspAccount_RegionRules(t *testing.T) {
	svc, _ := newTestService(t)

	_, err := svc.CreateCspAccount(&model.CreateCspAccountRequest{Name: "tc", CspType: "tencent", AccountInfo: map[string]string{"app_id": "1"}})
	assert.True(t, errors.Is(err, ErrRegionRequired), "%v", err)
	_, err = svc.CreateCspAccount(&model.CreateCspAccountRequest{Name: "aws-bad", CspType: "aws", AccountInfo: map[string]string{"account_id": "111122223333", "region": "Seoul"}})
	assert.True(t, errors.Is(err, ErrInvalidRegion), "%v", err)

	created, err := svc.CreateCspAccount(&model.CreateCspAccountRequest{Name: "aws-ok", CspType: "aws", AccountInfo: map[string]string{"account_id": "111122223333", "region": "ap-northeast-2"}})
	require.NoError(t, err)

	// region 키 없이 account_info 수정 → region 보존
	updated, err := svc.UpdateCspAccount(created.ID, &model.UpdateCspAccountRequest{AccountInfo: map[string]string{"account_id": "111122223333", "alias": "x"}})
	require.NoError(t, err)
	assert.Equal(t, "ap-northeast-2", updated.GetRegion())
	assert.Equal(t, "x", updated.AccountInfo["alias"])

	// 형식 오류
	_, err = svc.UpdateCspAccount(created.ID, &model.UpdateCspAccountRequest{AccountInfo: map[string]string{"account_id": "111122223333", "region": "bad region"}})
	assert.True(t, errors.Is(err, ErrInvalidRegion), "%v", err)

	// "" → 명시적 삭제 (aws 는 허용)
	updated, err = svc.UpdateCspAccount(created.ID, &model.UpdateCspAccountRequest{AccountInfo: map[string]string{"account_id": "111122223333", "region": ""}})
	require.NoError(t, err)
	assert.Equal(t, "", updated.GetRegion())

	// tencent 는 삭제 불가
	tc, err := svc.CreateCspAccount(&model.CreateCspAccountRequest{Name: "tc-ok", CspType: "tencent", AccountInfo: map[string]string{"app_id": "1", "region": "ap-guangzhou"}})
	require.NoError(t, err)
	_, err = svc.UpdateCspAccount(tc.ID, &model.UpdateCspAccountRequest{AccountInfo: map[string]string{"app_id": "1", "region": ""}})
	assert.True(t, errors.Is(err, ErrRegionRequired), "%v", err)
}

// 진단: 발급 단계에 계정 region 을 쓰고 응답에 region·regionSource 를 싣는다. 형식 오류 요청은 단계 전에 거절
func TestValidateCredentials_Region(t *testing.T) {
	mapping := buildValMapping("OIDC", "arn:aws:iam::123:oidc-provider/keycloak.example.com", "arn:aws:iam::123:role/test-role")
	mapping.CspRoles[0].CspAccount = acct("aws", "ap-northeast-2")
	kc := &mockValKcService{oidcToken: &gocloak.JWT{AccessToken: "a_very_long_kc_access_token_that_exceeds_100_chars_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}
	awsSvc := &mockValAwsService{oidcErr: errValAwsFail}
	svc := newValService(stdValUserRole(), nil, mapping, nil, kc, awsSvc)

	resp, err := svc.ValidateCredentials(context.Background(), 1, "kc_user", valReq("aws", "OIDC"))
	require.NoError(t, err)
	assert.Equal(t, 6, resp.FailedStep)
	assert.Equal(t, "ap-northeast-2", resp.Region)
	assert.Equal(t, RegionSourceAccount, resp.RegionSource)

	bad := valReq("aws", "OIDC")
	bad.Region = "Seoul"
	_, err = svc.ValidateCredentials(context.Background(), 1, "kc_user", bad)
	assert.True(t, errors.Is(err, ErrInvalidRegion), "%v", err)
}
