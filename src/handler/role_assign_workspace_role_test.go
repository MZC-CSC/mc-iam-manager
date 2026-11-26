package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 워크스페이스 역할 할당 (POST /api/roles/assign/workspace-role, POST /api/roles/id/:roleId/assign)
// 사용자: alice(1)=ws1 에 viewer(10) 소속, carol(3)=소속 없음.
// 역할: viewer(10)·operator(12)=workspace, platonly(11)=platform 만.
var assignWsRoleDBSeq int64

func setupAssignWorkspaceRoleDB(t *testing.T) *gorm.DB {
	t.Helper()
	// NewRoleHandler 가 만드는 여러 리포지토리가 같은 인메모리 DB 를 보도록 테스트마다 이름 있는 shared cache 를 쓴다
	dsn := fmt.Sprintf("file:assign_ws_role_%d?mode=memory&cache=shared", atomic.AddInt64(&assignWsRoleDBSeq, 1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.RoleMaster{}, &model.RoleSub{}, &model.Workspace{}, &model.UserWorkspaceRole{}, &model.UserPlatformRole{}))
	for _, q := range []string{
		`INSERT INTO mcmp_users (id, kc_id, username) VALUES (1, 'kc-alice', 'alice'), (3, 'kc-carol', 'carol')`,
		`INSERT INTO mcmp_workspaces (id, name) VALUES (1, 'ws1')`,
		`INSERT INTO mcmp_role_masters (id, name) VALUES (10, 'viewer'), (11, 'platonly'), (12, 'operator')`,
		`INSERT INTO mcmp_role_subs (id, role_id, role_type) VALUES (1, 10, 'workspace'), (2, 11, 'platform'), (3, 12, 'workspace')`,
		`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id) VALUES (1, 1, 10)`,
	} {
		require.NoError(t, db.Exec(q).Error, q)
	}
	return db
}

func callRoleHandler(t *testing.T, db *gorm.DB, path string, params map[string]string, body string, fn func(*RoleHandler, echo.Context) error) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.Validator = &testValidator{v: validator.New()}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	for k, v := range params {
		c.SetParamNames(k)
		c.SetParamValues(v)
	}
	require.NoError(t, fn(NewRoleHandler(db), c))
	return rec
}

func countWsRoles(db *gorm.DB) int64 {
	var n int64
	db.Model(&model.UserWorkspaceRole{}).Count(&n)
	return n
}

func TestAssignWorkspaceRole_ErrorCodes(t *testing.T) {
	cases := []struct {
		name, body string
		code       int
		contains   string
		added      int64 // 기존 1행 대비 늘어난 행 수
	}{
		{"새 사용자 할당", `{"workspaceId":"1","userId":"3","roleId":"10"}`, http.StatusOK, "", 1},
		{"이미 소속된 사용자에게 다른 역할 추가 허용", `{"workspaceId":"1","userId":"1","roleId":"12"}`, http.StatusOK, "", 1},
		{"roleName 으로 할당", `{"workspaceId":"1","username":"carol","roleName":"operator"}`, http.StatusOK, "", 1},
		{"같은 역할 중복", `{"workspaceId":"1","userId":"1","roleId":"10"}`, http.StatusConflict, "already assigned", 0},
		{"없는 사용자", `{"workspaceId":"1","userId":"99","roleId":"10"}`, http.StatusNotFound, "user not found", 0},
		{"없는 워크스페이스", `{"workspaceId":"99","userId":"3","roleId":"10"}`, http.StatusNotFound, "workspace not found", 0},
		{"없는 역할 id", `{"workspaceId":"1","userId":"3","roleId":"999"}`, http.StatusNotFound, "role not found", 0},
		{"workspace 유형 아닌 역할", `{"workspaceId":"1","userId":"3","roleId":"11"}`, http.StatusBadRequest, "not a workspace role", 0},
		{"워크스페이스 누락", `{"userId":"3","roleId":"10"}`, http.StatusBadRequest, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupAssignWorkspaceRoleDB(t)
			rec := callRoleHandler(t, db, "/api/roles/assign/workspace-role", nil, tc.body,
				func(h *RoleHandler, c echo.Context) error { return h.AssignWorkspaceRole(c) })
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			if tc.contains != "" {
				assert.Contains(t, rec.Body.String(), tc.contains)
			}
			assert.Equal(t, 1+tc.added, countWsRoles(db))
		})
	}
}

// POST /api/roles/id/:roleId/assign 의 workspace 유형도 같은 서비스(RoleService.AssignWorkspaceRole)를 쓴다
func TestAssignRole_WorkspaceType_ErrorCodes(t *testing.T) {
	cases := []struct {
		name, body string
		code       int
		contains   string
		added      int64
	}{
		{"새 사용자 할당", `{"roleType":"workspace","workspaceId":"1","userId":"3","roleId":"10"}`, http.StatusOK, "", 1},
		{"같은 역할 중복", `{"roleType":"workspace","workspaceId":"1","userId":"1","roleId":"10"}`, http.StatusConflict, "already assigned", 0},
		{"없는 사용자", `{"roleType":"workspace","workspaceId":"1","userId":"99","roleId":"10"}`, http.StatusNotFound, "user not found", 0},
		{"없는 워크스페이스", `{"roleType":"workspace","workspaceId":"99","userId":"3","roleId":"10"}`, http.StatusNotFound, "workspace not found", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupAssignWorkspaceRoleDB(t)
			rec := callRoleHandler(t, db, "/api/roles/id/10/assign", map[string]string{"roleId": "10"}, tc.body,
				func(h *RoleHandler, c echo.Context) error { return h.AssignRole(c) })
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			if tc.contains != "" {
				assert.Contains(t, rec.Body.String(), tc.contains)
			}
			assert.Equal(t, 1+tc.added, countWsRoles(db))
		})
	}
}
