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
// 직접 배정은 (사용자, 워크스페이스)당 1개 (IAM-BUG-050).
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

func directRoles(db *gorm.DB, userID, wsID uint) []uint {
	var ids []uint
	db.Model(&model.UserWorkspaceRole{}).Where("user_id = ? AND workspace_id = ?", userID, wsID).Order("role_id").Pluck("role_id", &ids)
	return ids
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
		{"새 사용자 할당 created", `{"workspaceId":"1","userId":"3","roleId":"10"}`, http.StatusOK, `"action":"created"`, 1},
		{"다른 역할 있음 409 + currentRole", `{"workspaceId":"1","userId":"1","roleId":"12"}`, http.StatusConflict, `"currentRole":{"roleId":10,"roleName":"viewer"}`, 0},
		{"replace 로 교체", `{"workspaceId":"1","userId":"1","roleId":"12","replace":true}`, http.StatusOK, `"previousRoleId":10`, 0},
		{"roleName 으로 할당", `{"workspaceId":"1","username":"carol","roleName":"operator"}`, http.StatusOK, `"action":"created"`, 1},
		{"같은 역할 중복", `{"workspaceId":"1","userId":"1","roleId":"10"}`, http.StatusConflict, `"errorCode":"already_assigned"`, 0},
		{"같은 역할 replace 도 409", `{"workspaceId":"1","userId":"1","roleId":"10","replace":true}`, http.StatusConflict, `"errorCode":"already_assigned"`, 0},
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
		{"다른 역할 409", `{"roleType":"workspace","workspaceId":"1","userId":"1","roleId":"12"}`, http.StatusConflict, `"errorCode":"role_conflict"`, 0},
		{"replace 로 교체", `{"roleType":"workspace","workspaceId":"1","userId":"1","roleId":"12","replace":true}`, http.StatusOK, `"action":"replaced"`, 0},
		{"없는 사용자", `{"roleType":"workspace","workspaceId":"1","userId":"99","roleId":"10"}`, http.StatusNotFound, "user not found", 0},
		{"없는 워크스페이스", `{"roleType":"workspace","workspaceId":"99","userId":"3","roleId":"10"}`, http.StatusNotFound, "workspace not found", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupAssignWorkspaceRoleDB(t)
			rec := callRoleHandler(t, db, "/api/roles/id/"+roleIDOf(tc.body)+"/assign", map[string]string{"roleId": "10"}, tc.body,
				func(h *RoleHandler, c echo.Context) error { return h.AssignRole(c) })
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			if tc.contains != "" {
				assert.Contains(t, rec.Body.String(), tc.contains)
			}
			assert.Equal(t, 1+tc.added, countWsRoles(db))
		})
	}
}

func roleIDOf(body string) string {
	i := strings.Index(body, `"roleId":"`)
	if i < 0 {
		return "0"
	}
	rest := body[i+len(`"roleId":"`):]
	return rest[:strings.Index(rest, `"`)]
}

// replace 는 한 트랜잭션에서 교체: 남는 직접 배정은 새 역할 1개
func TestAssignWorkspaceRole_ReplaceLeavesSingleRow(t *testing.T) {
	db := setupAssignWorkspaceRoleDB(t)
	rec := callRoleHandler(t, db, "/api/roles/assign/workspace-role", nil, `{"workspaceId":"1","userId":"1","roleId":"12","replace":true}`,
		func(h *RoleHandler, c echo.Context) error { return h.AssignWorkspaceRole(c) })
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []uint{12}, directRoles(db, 1, 1))
}

// 기존 데이터에 직접 배정이 여러 개 남아 있으면: currentRole 은 role_id 가 가장 작은 것, replace 후 1개로 정리
func TestAssignWorkspaceRole_LegacyDuplicateRows(t *testing.T) {
	db := setupAssignWorkspaceRoleDB(t)
	require.NoError(t, db.Exec(`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id) VALUES (1, 1, 12)`).Error)
	assign := func(h *RoleHandler, c echo.Context) error { return h.AssignWorkspaceRole(c) }

	rec := callRoleHandler(t, db, "/api/roles/assign/workspace-role", nil, `{"workspaceId":"1","userId":"1","roleId":"12"}`, assign)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"currentRole":{"roleId":10,"roleName":"viewer"}`)

	rec = callRoleHandler(t, db, "/api/roles/assign/workspace-role", nil, `{"workspaceId":"1","userId":"1","roleId":"10","replace":true}`, assign)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []uint{10}, directRoles(db, 1, 1), "유효 역할과 같은 역할로 replace 하면 나머지 중복만 정리")
}

// 그 워크스페이스에 다른 그룹 역할이 있으면 warnings (막지 않음)
func TestAssignWorkspaceRole_GroupOverrideWarning(t *testing.T) {
	db := setupAssignWorkspaceRoleDB(t)
	require.NoError(t, db.AutoMigrate(&model.Organization{}, &model.UserOrganization{}, &model.GroupWorkspaceRole{}))
	for _, q := range []string{
		`INSERT INTO mcmp_organizations (id, name, organization_code) VALUES (50, 'team-a', 'team-a')`,
		`INSERT INTO mcmp_user_organizations (user_id, organization_id) VALUES (3, 50)`,
		`INSERT INTO mcmp_group_workspace_roles (group_id, workspace_id, role_id) VALUES (50, 1, 12)`,
	} {
		require.NoError(t, db.Exec(q).Error, q)
	}
	assign := func(h *RoleHandler, c echo.Context) error { return h.AssignWorkspaceRole(c) }

	rec := callRoleHandler(t, db, "/api/roles/assign/workspace-role", nil, `{"workspaceId":"1","userId":"3","roleId":"10"}`, assign)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "그룹 team-a(id=50) 의 역할 operator 대신 직접 배정 viewer 가 적용됩니다")

	// 그룹 역할과 같은 역할로 바꾸면 경고 없음
	rec = callRoleHandler(t, db, "/api/roles/assign/workspace-role", nil, `{"workspaceId":"1","userId":"3","roleId":"12","replace":true}`, assign)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "warnings")
}
