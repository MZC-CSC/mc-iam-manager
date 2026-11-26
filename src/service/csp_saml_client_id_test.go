package service

import (
	"context"
	"testing"

	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SAML 클라이언트 ID 는 CspRole extendedConfig.saml_client_id 에서만 읽는다 (env SAML_CLIENT_ID_* 대체 없음)

func TestSamlClientIDMissingError_NoEnvValue(t *testing.T) {
	t.Setenv("SAML_CLIENT_ID_AWS", "env-aws-client")
	t.Setenv("SAML_CLIENT_ID_ALIBABA", "env-alibaba-client")

	awsErr := samlClientIDMissingError(7, "aws")
	assert.Contains(t, awsErr.Error(), "saml_client_id 미등록")
	assert.Contains(t, awsErr.Error(), "PUT /api/roles/csp/id/7")
	assert.Contains(t, awsErr.Error(), "urn:amazon:webservices")
	assert.NotContains(t, awsErr.Error(), "env-aws-client")

	aliErr := samlClientIDMissingError(8, "alibaba")
	assert.Contains(t, aliErr.Error(), "PUT /api/roles/csp/id/8")
	assert.NotContains(t, aliErr.Error(), "env-alibaba-client")
}

func TestGetTemporaryCredentials_SAML_MissingClientIDIgnoresEnv(t *testing.T) {
	t.Setenv("SAML_CLIENT_ID_AWS", "urn:amazon:webservices")
	t.Setenv("SAML_CLIENT_ID_ALIBABA", "urn:alibaba:cloudcomputing")
	for _, csp := range []string{"aws", "alibaba"} {
		t.Run(csp, func(t *testing.T) {
			mapping := buildMapping(constants.AuthMethodSAML, idpArn, roleArn, model.AuthMethodSAML, nil)
			aws := &mockAwsCredService{samlResult: awsSamlCred}
			svc := newCredServiceWithMocks(credServiceDeps{
				aws:      aws,
				gcp:      &mockGcpCredService{},
				alibaba:  &mockAlibabaCredService{result: alibabaSamlCred},
				kc:       samlKC(),
				userRepo: &mockUserRepoForCred{role: stdUserRole()},
				mapRepo:  &mockCspMappingRepo{mapping: mapping},
			})
			_, err := svc.GetTemporaryCredentials(context.Background(), 1, "kc_user_id", req(csp, "SAML"))
			require.Error(t, err, "env 가 있어도 CspRole 에 saml_client_id 가 없으면 중단")
			assert.Contains(t, err.Error(), "saml_client_id 미등록")
			assert.Equal(t, 0, aws.samlCalls)
		})
	}
}

func TestValidateAWSWithSAML_Step2_MissingClientIDIgnoresEnv(t *testing.T) {
	t.Setenv("SAML_CLIENT_ID_AWS", "urn:amazon:webservices")
	mapping := buildValMapping("SAML", "arn:aws:iam::123:saml-provider/keycloak", "arn:aws:iam::123:role/test-role")
	mapping.CspRoles[0].ExtendedConfig = nil
	kc := &mockValKcService{samlAssertion: "assertion-data"}
	svc := newValService(stdValUserRole(), nil, mapping, nil, kc, &mockValAwsService{})

	resp, err := svc.ValidateCredentials(context.Background(), 1, "kc_user", valReq("aws", "SAML"))

	require.NoError(t, err)
	assert.False(t, resp.Valid)
	assert.Equal(t, 2, resp.FailedStep, "발급과 같이 2 단계에서 saml_client_id 누락으로 실패")
	assert.Contains(t, resp.Steps[1].Detail, "saml_client_id 미등록")
}

// recordingSamlKC CheckSAMLClientConfig 호출 인자를 기록한다
type recordingSamlKC struct {
	mockKeycloakService
	checked []string
}

func (m *recordingSamlKC) CheckSAMLClientConfig(ctx context.Context, clientID string) (string, error) {
	m.checked = append(m.checked, clientID)
	return "", nil
}

func TestEnsureKeycloakClientForCspRole_UsesExtendedConfigNotEnv(t *testing.T) {
	t.Setenv("SAML_CLIENT_ID_AWS", "env-aws-client")
	kc := &recordingSamlKC{}
	s := &CspRoleService{keycloakService: kc}
	role := &model.CspRole{ID: 3}

	err := s.ensureKeycloakClientForCspRole(&model.CreateCspRoleRequest{CspType: "aws", AuthMethod: constants.AuthMethodSAML,
		ExtendedConfig: map[string]interface{}{"saml_client_id": "urn:amazon:webservices"}}, role)
	require.NoError(t, err)
	assert.Equal(t, []string{"urn:amazon:webservices"}, kc.checked)

	kc.checked = nil
	err = s.ensureKeycloakClientForCspRole(&model.CreateCspRoleRequest{CspType: "aws", AuthMethod: constants.AuthMethodSAML}, role)
	require.Error(t, err, "saml_client_id 가 없으면 경고용 오류")
	assert.Contains(t, err.Error(), "saml_client_id is not set")
	assert.Empty(t, kc.checked, "env 값으로 Keycloak 을 확인하지 않는다")
}
