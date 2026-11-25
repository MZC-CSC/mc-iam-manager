package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IAM-BUG-046: /api/csp/iam/roles 의 쓰기는 항상 400 이고 대체 API 를 안내한다 (AWS 호출 없음)
func TestCspIAMHandler_WritesBlocked(t *testing.T) {
	h := &CspIAMHandler{}
	for _, tc := range []struct {
		method string
		fn     func(echo.Context) error
	}{
		{http.MethodPost, h.CreateIAMRole},
		{http.MethodPut, h.UpdateIAMRole},
		{http.MethodDelete, h.DeleteIAMRole},
	} {
		e := echo.New()
		req := httptest.NewRequest(tc.method, "/api/csp/iam/roles/mciam-x?csp_account_id=1", strings.NewReader(`{"Name":"mciam-x","Policy":{"Version":"2012-10-17"}}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("roleName")
		c.SetParamValues("mciam-x")
		require.NoError(t, tc.fn(c))
		assert.Equal(t, http.StatusBadRequest, rec.Code, tc.method)
		var body map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "invalid_request", body["errorCode"])
		for _, alt := range []string{"POST /api/roles/csp", "/api/csp-target-roles/id/{id}/trust-policy/sync", "DELETE /api/roles/csp/id/{id}"} {
			assert.Contains(t, body["error"], alt)
		}
	}
}
