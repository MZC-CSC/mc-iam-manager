package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupPolicyBindingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.CspAccount{}, &model.CspIdpConfig{}, &model.CspRole{}, &model.CspPolicy{}, &model.CspRolePolicyMapping{}))
	return db
}

// attachPolicy·detachPolicy 요청 오류의 HTTP 상태와 errorCode (AWS 호출 전에 거절, 매핑 행 없음)
func TestPolicyBindingHandler_RejectionStatus(t *testing.T) {
	db := setupPolicyBindingTestDB(t)
	cfg := seedAwsManagementConfig(t, db, "t044", "mciam-t044-mgmt")
	h := NewCspPolicyHandler(db)

	mgmt := &model.CspRole{Name: "mciam-t044-mgmt", CspType: "aws", Status: "created", CspIdpConfigID: &cfg.ID}
	shared := &model.CspRole{Name: "shared-role", CspType: "aws", Status: "created", CspIdpConfigID: &cfg.ID}
	target := &model.CspRole{Name: "mciam-t044-target", CspType: "aws", Status: "created", CspIdpConfigID: &cfg.ID}
	for _, r := range []*model.CspRole{mgmt, shared, target} {
		require.NoError(t, db.Create(r).Error)
	}
	managed := &model.CspPolicy{Name: "p", CspAccountID: cfg.CspAccountID, PolicyType: model.PolicyTypeManaged, PolicyArn: "arn:aws:iam::111111111111:policy/p"}
	admin := &model.CspPolicy{Name: "AdministratorAccess", CspAccountID: cfg.CspAccountID, PolicyType: model.PolicyTypeManaged, PolicyArn: "arn:aws:iam::aws:policy/AdministratorAccess"}
	custom := &model.CspPolicy{Name: "c", CspAccountID: cfg.CspAccountID, PolicyType: model.PolicyTypeCustom}
	for _, p := range []*model.CspPolicy{managed, admin, custom} {
		require.NoError(t, db.Create(p).Error)
	}

	call := func(path string, fn func(echo.Context) error, body string) (int, map[string]string) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		require.NoError(t, fn(e.NewContext(req, rec)))
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	body := func(roleID, policyID uint) string {
		return fmt.Sprintf(`{"csp_role_id":%d,"csp_policy_id":%d}`, roleID, policyID)
	}

	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"id 없음", `{"csp_role_id":0,"csp_policy_id":1}`, http.StatusBadRequest, "invalid_request"},
		{"Role 없음", body(999, managed.ID), http.StatusNotFound, "not_found"},
		{"관리용 Role", body(mgmt.ID, managed.ID), http.StatusConflict, "protected"},
		{"mciam-* 아님", body(shared.ID, managed.ID), http.StatusForbidden, "forbidden_policy"},
		{"거부 정책", body(target.ID, admin.ID), http.StatusForbidden, "forbidden_policy"},
		{"custom", body(target.ID, custom.ID), http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range cases {
		code, out := call("/api/csp-policies/attach", h.AttachPolicyToRole, tc.body)
		assert.Equal(t, tc.status, code, "%s: %v", tc.name, out)
		assert.Equal(t, tc.code, out["errorCode"], tc.name)
	}
	// 매핑이 없는 detach 는 404
	code, out := call("/api/csp-policies/detach", h.DetachPolicyFromRole, body(target.ID, managed.ID))
	assert.Equal(t, http.StatusNotFound, code, "%v", out)

	var n int64
	require.NoError(t, db.Model(&model.CspRolePolicyMapping{}).Count(&n).Error)
	assert.Zero(t, n)
}
