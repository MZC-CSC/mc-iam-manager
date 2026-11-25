package service

import (
	"context"
	"errors"
	"testing"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
)

// CSP 계정 검증(POST /csp-accounts/id/:id/validate)의 AWS OIDC·SAML Role 확인은 관리용 자격증명과 공통 신뢰 정책 검증을 쓴다 (IAM-TECH-045 M2)
func TestValidateAWSRole_UsesTrustVerifier(t *testing.T) {
	newRole := func(method model.AuthMethodType) *model.CspRole {
		return &model.CspRole{ID: 7, Name: "mciam-t045-acct", CspType: "aws", CspIdpConfig: &model.CspIdpConfig{ID: 1, AuthMethod: method, IsActive: true}}
	}
	cases := []struct {
		name     string
		method   model.AuthMethodType
		verifier *mockValAwsService
		valid    bool
		errPart  string
		trust    string
	}{
		{"OIDC ok", model.AuthMethodOIDC, &mockValAwsService{}, true, "", model.TrustPolicyOK},
		{"SAML ok", model.AuthMethodSAML, &mockValAwsService{}, true, "", model.TrustPolicyOK},
		{"OIDC provider 실패 → 신뢰 확인 안 함", model.AuthMethodOIDC, &mockValAwsService{oidcProviderErr: errors.New("provider does not exist")}, false, "OIDC provider check failed", ""},
		{"SAML provider 실패", model.AuthMethodSAML, &mockValAwsService{samlProviderErr: errors.New("saml provider does not exist")}, false, "SAML provider check failed", ""},
		{"신뢰 정책 mismatch", model.AuthMethodOIDC, &mockValAwsService{roleTrustErr: errors.New("no aud condition")}, false, "Role trust check failed", model.TrustPolicyMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &CspAccountService{trustVerifier: tc.verifier}
			role := newRole(tc.method)
			result := model.CspAccountValidationResult{CspRoleID: role.ID, AuthMethod: string(tc.method)}
			svc.validateAWSRole(context.Background(), role, &result)
			assert.Equal(t, tc.valid, result.Valid, "%+v", result)
			if tc.errPart != "" {
				assert.Contains(t, result.Error, tc.errPart)
			}
			if tc.trust == "" {
				assert.Nil(t, result.TrustPolicy)
			} else {
				if assert.NotNil(t, result.TrustPolicy) {
					assert.Equal(t, tc.trust, result.TrustPolicy.Status)
				}
			}
		})
	}
}
