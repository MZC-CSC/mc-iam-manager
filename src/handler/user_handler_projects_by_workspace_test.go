package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// direct 는 ws1 에 직접 배정, grouped 는 그룹(org 100) 경유로만 ws1 viewer, outsider 는 아무 역할 없음.
func setupUserProjectsByWorkspaceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	for _, ddl := range []string{
		`CREATE TABLE mcmp_users (id INTEGER PRIMARY KEY, kc_id TEXT, username TEXT, status TEXT, description TEXT,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE mcmp_workspaces (id INTEGER PRIMARY KEY, name TEXT, description TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE mcmp_projects (id INTEGER PRIMARY KEY, nsid TEXT, name TEXT, description TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE mcmp_workspace_projects (workspace_id INTEGER, project_id INTEGER, PRIMARY KEY (workspace_id, project_id))`,
		`CREATE TABLE mcmp_role_masters (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE mcmp_user_workspace_roles (user_id INTEGER, workspace_id INTEGER, role_id INTEGER, created_at DATETIME,
			PRIMARY KEY (user_id, workspace_id, role_id))`,
		`CREATE TABLE mcmp_user_platform_roles (user_id INTEGER, role_id INTEGER, created_at DATETIME, PRIMARY KEY (user_id, role_id))`,
		`CREATE TABLE mcmp_user_organizations (user_id INTEGER, organization_id INTEGER, created_at DATETIME)`,
		`CREATE TABLE mcmp_group_workspace_roles (group_id INTEGER, workspace_id INTEGER, role_id INTEGER, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE mcmp_organizations (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO mcmp_organizations (id, name) VALUES (100, 'grp100')`,
		`INSERT INTO mcmp_users (id, kc_id, username) VALUES (1, 'kc-direct', 'direct'), (2, 'kc-grouped', 'grouped'), (3, 'kc-outsider', 'outsider')`,
		`INSERT INTO mcmp_workspaces (id, name) VALUES (1, 'ws1')`,
		`INSERT INTO mcmp_projects (id, nsid, name) VALUES (1, 'ns01', 'p1')`,
		`INSERT INTO mcmp_workspace_projects (workspace_id, project_id) VALUES (1, 1)`,
		`INSERT INTO mcmp_role_masters (id, name) VALUES (3, 'viewer')`,
		`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id, created_at) VALUES (1, 1, 3, CURRENT_TIMESTAMP)`,
		`INSERT INTO mcmp_user_organizations (user_id, organization_id, created_at) VALUES (2, 100, CURRENT_TIMESTAMP)`,
		`INSERT INTO mcmp_group_workspace_roles (group_id, workspace_id, role_id) VALUES (100, 1, 3)`,
	} {
		require.NoError(t, db.Exec(ddl).Error, ddl)
	}
	return db
}

func callListUserProjectsByWorkspace(t *testing.T, h *UserHandler, kcUserID, workspaceID string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/users/workspaces/id/"+workspaceID+"/projects/list", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/users/workspaces/id/:workspaceId/projects/list")
	c.SetParamNames("workspaceId")
	c.SetParamValues(workspaceID)
	c.Set("kcUserId", kcUserID)
	c.Set("platformRoles", []string{"viewer"})
	require.NoError(t, h.ListUserProjectsByWorkspace(c))
	return rec
}

// BAR-2300: 그룹 상속 Workspace 역할만 가진 사용자가 403 을 받았다.
func TestListUserProjectsByWorkspace_GroupInheritedMember(t *testing.T) {
	h := NewUserHandler(setupUserProjectsByWorkspaceTestDB(t))

	rec := callListUserProjectsByWorkspace(t, h, "kc-direct", "1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"ns01"`)

	rec = callListUserProjectsByWorkspace(t, h, "kc-grouped", "1")
	require.Equal(t, http.StatusOK, rec.Code, "그룹 경유 viewer 도 project 목록을 볼 수 있어야 한다: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), `"ns01"`)

	rec = callListUserProjectsByWorkspace(t, h, "kc-outsider", "1")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}
