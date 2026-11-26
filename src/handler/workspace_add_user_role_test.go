package handler

import (
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

// POST /api/workspaces/id/:id/users — 역할 필수(기본 역할 없음), 역할은 sub 유형 workspace 만
// 사용자: alice(1)=ws1 소속, carol(3)=소속 없음. 역할: viewer(10)=workspace, platonly(11)=platform 만
func setupAddUserRoleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.RoleMaster{}, &model.RoleSub{}, &model.Workspace{}, &model.UserWorkspaceRole{}, &model.UserPlatformRole{}))
	for _, q := range []string{
		`INSERT INTO mcmp_users (id, kc_id, username) VALUES (1, 'kc-alice', 'alice'), (3, 'kc-carol', 'carol')`,
		`INSERT INTO mcmp_workspaces (id, name) VALUES (1, 'ws1')`,
		`INSERT INTO mcmp_role_masters (id, name) VALUES (10, 'viewer'), (11, 'platonly')`,
		`INSERT INTO mcmp_role_subs (id, role_id, role_type) VALUES (1, 10, 'workspace'), (2, 11, 'platform')`,
		`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id) VALUES (1, 1, 10)`,
	} {
		require.NoError(t, db.Exec(q).Error, q)
	}
	return db
}

func callAddUser(t *testing.T, h *WorkspaceHandler, workspaceID, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/id/"+workspaceID+"/users", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/workspaces/id/:id/users")
	c.SetParamNames("id")
	c.SetParamValues(workspaceID)
	require.NoError(t, h.AddUserToWorkspace(c))
	return rec
}

func TestAddUserToWorkspace_RoleRules(t *testing.T) {
	cases := []struct {
		name, ws, body string
		code           int
		contains       string
	}{
		{"roleId 로 추가", "1", `{"userId":"3","roleId":"10"}`, http.StatusNoContent, ""},
		{"roleName 으로 추가", "1", `{"userId":"3","roleName":"viewer"}`, http.StatusNoContent, ""},
		{"역할 없음", "1", `{"userId":"3"}`, http.StatusBadRequest, "roleId or roleName is required"},
		{"잘못된 roleId", "1", `{"userId":"3","roleId":"abc"}`, http.StatusBadRequest, "Invalid role ID"},
		{"없는 워크스페이스", "99", `{"userId":"3","roleId":"10"}`, http.StatusNotFound, "workspace not found"},
		{"없는 사용자", "1", `{"userId":"99","roleId":"10"}`, http.StatusNotFound, "user not found"},
		{"없는 역할 id", "1", `{"userId":"3","roleId":"999"}`, http.StatusNotFound, "role not found"},
		{"없는 역할 이름", "1", `{"userId":"3","roleName":"nope"}`, http.StatusNotFound, "role not found"},
		{"workspace 유형 아닌 역할", "1", `{"userId":"3","roleId":"11"}`, http.StatusBadRequest, "not a workspace role"},
		{"이미 소속", "1", `{"userId":"1","roleId":"10"}`, http.StatusConflict, "already in workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupAddUserRoleDB(t)
			rec := callAddUser(t, NewWorkspaceHandler(db), tc.ws, tc.body)
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			if tc.contains != "" {
				assert.Contains(t, rec.Body.String(), tc.contains)
			}
			if tc.code == http.StatusNoContent {
				var n int64
				db.Model(&model.UserWorkspaceRole{}).Where("user_id = 3 AND workspace_id = 1 AND role_id = 10").Count(&n)
				assert.Equal(t, int64(1), n, "지정한 역할로 한 행만 생긴다")
			} else {
				var n int64
				db.Model(&model.UserWorkspaceRole{}).Where("user_id = 3").Count(&n)
				assert.Equal(t, int64(0), n, "실패하면 아무것도 쓰지 않는다")
			}
		})
	}
}
