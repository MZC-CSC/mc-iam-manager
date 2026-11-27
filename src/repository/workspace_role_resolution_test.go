package repository

// 워크스페이스 역할 결정 (IAM-BUG-050): 직접 배정 우선, 없으면 그룹 역할(role_id 오름차순),
// 기존 중복 직접 배정은 role_id 가 가장 작은 것, overridden 표시와 목록 행 필드.

import (
	"testing"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type resolutionFixture struct {
	db                *gorm.DB
	user              *model.User
	ws, ws2           *model.Workspace
	orgA, orgB        *model.Organization
	viewer, op, admin *model.RoleMaster
}

// 역할 id 순서: admin < op < viewer
func setupResolution(t *testing.T) (*resolutionFixture, func(q string, args ...interface{})) {
	t.Helper()
	db := setupUserRepoTestDB(t)
	f := &resolutionFixture{
		db:     db,
		user:   seedUserRepoUser(t, db, "u-res"),
		ws:     seedUserRepoWorkspace(t, db, "ws-res-1"),
		ws2:    seedUserRepoWorkspace(t, db, "ws-res-2"),
		orgA:   seedUserRepoOrg(t, db, "team-a", "RESA"),
		orgB:   seedUserRepoOrg(t, db, "team-b", "RESB"),
		admin:  seedUserRepoRole(t, db, "res-admin"),
		op:     seedUserRepoRole(t, db, "res-operator"),
		viewer: seedUserRepoRole(t, db, "res-viewer"),
	}
	exec := func(q string, args ...interface{}) { require.NoError(t, db.Exec(q, args...).Error, q) }
	return f, exec
}

func (f *resolutionFixture) resolve(t *testing.T, wsID uint) *model.WorkspaceRoleResolution {
	t.Helper()
	r, err := ResolveUserWorkspaceRole(f.db, f.user.ID, wsID)
	require.NoError(t, err)
	return r
}

func TestResolveWorkspaceRole_NoRole(t *testing.T) {
	f, _ := setupResolution(t)
	r := f.resolve(t, f.ws.ID)
	assert.Nil(t, r.Effective)
	assert.False(t, r.Overridden)
}

func TestResolveWorkspaceRole_DirectWinsAndOverridden(t *testing.T) {
	f, exec := setupResolution(t)
	exec(`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id) VALUES (?, ?, ?)`, f.user.ID, f.ws.ID, f.viewer.ID)
	exec(`INSERT INTO mcmp_user_organizations (user_id, organization_id) VALUES (?, ?)`, f.user.ID, f.orgA.ID)
	exec(`INSERT INTO mcmp_group_workspace_roles (group_id, workspace_id, role_id) VALUES (?, ?, ?)`, f.orgA.ID, f.ws.ID, f.admin.ID)

	r := f.resolve(t, f.ws.ID)
	require.NotNil(t, r.Effective)
	assert.Equal(t, f.viewer.ID, r.Effective.RoleID)
	assert.Equal(t, model.WorkspaceRoleSourceDirect, r.Effective.Source)
	assert.Nil(t, r.Effective.GroupID)
	assert.True(t, r.Overridden, "직접 배정이 그룹 역할과 다르면 overridden")
	require.Len(t, r.GroupRoles, 1)
	assert.Equal(t, "team-a", r.GroupRoles[0].GroupName)
}

func TestResolveWorkspaceRole_DirectSameAsGroupNotOverridden(t *testing.T) {
	f, exec := setupResolution(t)
	exec(`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id) VALUES (?, ?, ?)`, f.user.ID, f.ws.ID, f.viewer.ID)
	exec(`INSERT INTO mcmp_user_organizations (user_id, organization_id) VALUES (?, ?)`, f.user.ID, f.orgA.ID)
	exec(`INSERT INTO mcmp_group_workspace_roles (group_id, workspace_id, role_id) VALUES (?, ?, ?)`, f.orgA.ID, f.ws.ID, f.viewer.ID)

	r := f.resolve(t, f.ws.ID)
	assert.False(t, r.Overridden)
}

// 그룹이 여러 개면 role_id 오름차순 첫 값, source=group·groupId 표시
func TestResolveWorkspaceRole_MultipleGroups(t *testing.T) {
	f, exec := setupResolution(t)
	exec(`INSERT INTO mcmp_user_organizations (user_id, organization_id) VALUES (?, ?), (?, ?)`, f.user.ID, f.orgA.ID, f.user.ID, f.orgB.ID)
	// orgA → viewer(가장 큰 id), orgB → admin(가장 작은 id)
	exec(`INSERT INTO mcmp_group_workspace_roles (group_id, workspace_id, role_id) VALUES (?, ?, ?), (?, ?, ?)`,
		f.orgA.ID, f.ws.ID, f.viewer.ID, f.orgB.ID, f.ws.ID, f.admin.ID)

	r := f.resolve(t, f.ws.ID)
	require.NotNil(t, r.Effective)
	assert.Equal(t, f.admin.ID, r.Effective.RoleID)
	assert.Equal(t, model.WorkspaceRoleSourceGroup, r.Effective.Source)
	require.NotNil(t, r.Effective.GroupID)
	assert.Equal(t, f.orgB.ID, *r.Effective.GroupID)
	assert.False(t, r.Overridden, "직접 배정이 없으면 overridden 아님")
}

// 기존 데이터에 직접 배정이 여러 개 남아 있으면 role_id 가 가장 작은 것 — 몇 번을 조회해도 같다
func TestResolveWorkspaceRole_LegacyDuplicateDirectDeterministic(t *testing.T) {
	f, exec := setupResolution(t)
	exec(`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id) VALUES (?, ?, ?), (?, ?, ?)`,
		f.user.ID, f.ws.ID, f.viewer.ID, f.user.ID, f.ws.ID, f.op.ID)
	for i := 0; i < 5; i++ {
		r := f.resolve(t, f.ws.ID)
		assert.Equal(t, f.op.ID, r.Effective.RoleID)
	}
	got, err := NewUserRepository(f.db).FindEffectiveUserRoleInWorkspace(f.user.ID, f.ws.ID)
	require.NoError(t, err)
	assert.Equal(t, f.op.ID, got.RoleID, "발급 경로도 같은 결정")
}

// 유효 역할 목록: 기존 필드 유지 + 행마다 source·is_effective, 워크스페이스 단위 effective_role·group_roles·overridden
func TestFindEffectiveWorkspaceRoles_RowsCarryResolution(t *testing.T) {
	f, exec := setupResolution(t)
	exec(`INSERT INTO mcmp_user_workspace_roles (user_id, workspace_id, role_id) VALUES (?, ?, ?)`, f.user.ID, f.ws.ID, f.viewer.ID)
	exec(`INSERT INTO mcmp_user_organizations (user_id, organization_id) VALUES (?, ?)`, f.user.ID, f.orgA.ID)
	exec(`INSERT INTO mcmp_group_workspace_roles (group_id, workspace_id, role_id) VALUES (?, ?, ?), (?, ?, ?)`,
		f.orgA.ID, f.ws.ID, f.admin.ID, f.orgA.ID, f.ws2.ID, f.op.ID)

	rows, err := NewRoleRepository(f.db).FindEffectiveWorkspaceRoles(f.user.ID)
	require.NoError(t, err)
	require.Len(t, rows, 3) // ws: admin(group)·viewer(direct), ws2: operator(group)

	byKey := map[[2]uint]model.EffectiveWorkspaceRole{}
	for _, r := range rows {
		byKey[[2]uint{r.WorkspaceID, r.RoleID}] = r
	}
	direct := byKey[[2]uint{f.ws.ID, f.viewer.ID}]
	assert.Equal(t, "ws-res-1", direct.WorkspaceName)
	assert.Equal(t, "res-viewer", direct.RoleName)
	assert.Equal(t, model.WorkspaceRoleSourceDirect, direct.Source)
	assert.True(t, *direct.IsEffective)
	assert.True(t, *direct.Overridden)
	assert.Equal(t, f.viewer.ID, direct.EffectiveRole.RoleID)
	require.Len(t, direct.GroupRoles, 1)

	groupRow := byKey[[2]uint{f.ws.ID, f.admin.ID}]
	assert.Equal(t, model.WorkspaceRoleSourceGroup, groupRow.Source)
	assert.Equal(t, f.orgA.ID, *groupRow.GroupID)
	assert.False(t, *groupRow.IsEffective, "직접 배정에 가려진 그룹 역할")

	ws2 := byKey[[2]uint{f.ws2.ID, f.op.ID}]
	assert.True(t, *ws2.IsEffective)
	assert.False(t, *ws2.Overridden)
	assert.Equal(t, model.WorkspaceRoleSourceGroup, ws2.EffectiveRole.Source)
}
