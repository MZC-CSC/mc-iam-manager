package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withGcpTestURLs swaps the package-level GCP STS/IAM Credentials endpoint
// vars for the given httptest server URLs for the duration of the test.
func withGcpTestURLs(t *testing.T, stsURL, iamURLFormat string) {
	t.Helper()
	origSTS, origIAM := gcpSTSURL, gcpIAMCredentialsURLFormat
	if stsURL != "" {
		gcpSTSURL = stsURL
	}
	if iamURLFormat != "" {
		gcpIAMCredentialsURLFormat = iamURLFormat
	}
	t.Cleanup(func() {
		gcpSTSURL = origSTS
		gcpIAMCredentialsURLFormat = origIAM
	})
}

// TC-GCP-CRED-01: ExchangeToken — STS 200 응답 시 federated token 반환
func TestGcpCredentialService_ExchangeToken_Success(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"federated-token-123","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer sts.Close()
	withGcpTestURLs(t, sts.URL, "")

	svc := NewGcpCredentialService()
	token, err := svc.ExchangeToken(context.Background(), "//iam.googleapis.com/projects/123/.../providers/kc", "kc-jwt", "jwt")

	require.NoError(t, err)
	assert.Equal(t, "federated-token-123", token)
}

// TC-GCP-CRED-02: ExchangeToken — STS 4xx 응답 시 오류 반환 (WIF Pool/Provider 미설정 시나리오)
func TestGcpCredentialService_ExchangeToken_HttpError(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_target","error_description":"unknown audience"}`))
	}))
	defer sts.Close()
	withGcpTestURLs(t, sts.URL, "")

	svc := NewGcpCredentialService()
	token, err := svc.ExchangeToken(context.Background(), "bad-audience", "kc-jwt", "jwt")

	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "GCP STS returned HTTP 400")
}

// TC-GCP-CRED-03: GenerateAccessToken — IAM Credentials 200 응답 시 CspCredentialResponse 반환
func TestGcpCredentialService_GenerateAccessToken_Success(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"accessToken":"sa-access-token-456","expireTime":"2099-01-01T00:00:00Z"}`))
	}))
	defer iam.Close()
	withGcpTestURLs(t, "", iam.URL+"/%s")

	svc := NewGcpCredentialService()
	creds, err := svc.GenerateAccessToken(context.Background(), "sa@project.iam.gserviceaccount.com", "federated-token-123")

	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "gcp", creds.CspType)
	assert.Equal(t, "sa-access-token-456", creds.AccessToken)
	assert.Equal(t, "Bearer", creds.TokenType)
}

// TC-GCP-CRED-04: GenerateAccessToken — IAM Credentials 403 응답 시 오류 반환 (SA 없음/권한 없음 시나리오)
func TestGcpCredentialService_GenerateAccessToken_HttpError(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"permission_denied"}`))
	}))
	defer iam.Close()
	withGcpTestURLs(t, "", iam.URL+"/%s")

	svc := NewGcpCredentialService()
	creds, err := svc.GenerateAccessToken(context.Background(), "sa@project.iam.gserviceaccount.com", "federated-token-123")

	require.Error(t, err)
	assert.Nil(t, creds)
	assert.Contains(t, err.Error(), "GCP IAM Credentials returned HTTP 403")
}

// TC-GCP-CRED-05: ExchangeTokenAndImpersonate — 내부적으로 ExchangeToken → GenerateAccessToken 순서로
// 합성되며, 두 단계 모두 성공하면 최종 자격증명을 반환한다 (기존 호출부 동작 불변 확인).
func TestGcpCredentialService_ExchangeTokenAndImpersonate_ComposesBothSteps(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"federated-token-xyz","token_type":"Bearer","expires_in":3600}`))
	}))
	defer sts.Close()
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"accessToken":"sa-access-token-final","expireTime":"2099-01-01T00:00:00Z"}`))
	}))
	defer iam.Close()
	withGcpTestURLs(t, sts.URL, iam.URL+"/%s")

	svc := NewGcpCredentialService()
	creds, err := svc.ExchangeTokenAndImpersonate(context.Background(), "wif-provider", "sa@project.iam.gserviceaccount.com", "kc-jwt", "jwt")

	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "sa-access-token-final", creds.AccessToken)
}

// TC-GCP-CRED-06: ExchangeTokenAndImpersonate — STS 단계 실패 시 SA impersonation은 호출되지 않고
// 오류가 전파된다.
func TestGcpCredentialService_ExchangeTokenAndImpersonate_StsFailureShortCircuits(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_target"}`))
	}))
	defer sts.Close()
	iamCalled := false
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iamCalled = true
		w.Write([]byte(`{"accessToken":"should-not-be-called","expireTime":"2099-01-01T00:00:00Z"}`))
	}))
	defer iam.Close()
	withGcpTestURLs(t, sts.URL, iam.URL+"/%s")

	svc := NewGcpCredentialService()
	creds, err := svc.ExchangeTokenAndImpersonate(context.Background(), "wif-provider", "sa@project.iam.gserviceaccount.com", "kc-jwt", "jwt")

	require.Error(t, err)
	assert.Nil(t, creds)
	assert.False(t, iamCalled, "SA impersonation should not be attempted if STS token exchange failed")
	assert.Contains(t, err.Error(), "GCP STS token exchange failed")
}
