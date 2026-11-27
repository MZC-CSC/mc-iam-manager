package repository

import (
	"testing"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureUserWorkspaceRoleUniqueIndex(t *testing.T) {
	db := setupUserRepoTestDB(t)
	u := seedUserRepoUser(t, db, "u-idx")
	ws := seedUserRepoWorkspace(t, db, "ws-idx")
	r1 := seedUserRepoRole(t, db, "r1-idx")
	r2 := seedUserRepoRole(t, db, "r2-idx")

	// 기존 중복이 있으면 만들지 않고 오류 없이 넘어간다 (기동 계속)
	require.NoError(t, db.Create(&model.UserWorkspaceRole{UserID: u.ID, WorkspaceID: ws.ID, RoleID: r1.ID}).Error)
	require.NoError(t, db.Create(&model.UserWorkspaceRole{UserID: u.ID, WorkspaceID: ws.ID, RoleID: r2.ID}).Error)
	created, err := EnsureUserWorkspaceRoleUniqueIndex(db)
	require.NoError(t, err)
	assert.False(t, created)

	// 정리 후에는 만들고, 같은 (사용자, 워크스페이스) 두 번째 행을 DB 가 거부한다
	require.NoError(t, db.Exec(`DELETE FROM mcmp_user_workspace_roles WHERE role_id = ?`, r2.ID).Error)
	created, err = EnsureUserWorkspaceRoleUniqueIndex(db)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Error(t, db.Create(&model.UserWorkspaceRole{UserID: u.ID, WorkspaceID: ws.ID, RoleID: r2.ID}).Error)

	// 다시 불러도 오류 없음 (IF NOT EXISTS)
	created, err = EnsureUserWorkspaceRoleUniqueIndex(db)
	require.NoError(t, err)
	assert.True(t, created)
}
