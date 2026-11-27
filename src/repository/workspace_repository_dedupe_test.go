package repository

import (
	"testing"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ListWorkspaces(UserID): 직접 배정이 여러 개 남은 사용자도 워크스페이스는 한 번만 (IAM-BUG-050)
func TestFindWorkspaces_UserFilterNoDuplicates(t *testing.T) {
	db := setupUserRepoTestDB(t)
	u := seedUserRepoUser(t, db, "u-dedupe")
	ws := seedUserRepoWorkspace(t, db, "ws-dedupe")
	other := seedUserRepoWorkspace(t, db, "ws-other")
	r1 := seedUserRepoRole(t, db, "r1-dedupe")
	r2 := seedUserRepoRole(t, db, "r2-dedupe")
	require.NoError(t, db.Create(&model.UserWorkspaceRole{UserID: u.ID, WorkspaceID: ws.ID, RoleID: r1.ID}).Error)
	require.NoError(t, db.Create(&model.UserWorkspaceRole{UserID: u.ID, WorkspaceID: ws.ID, RoleID: r2.ID}).Error)
	_ = other

	got, err := NewWorkspaceRepository(db).FindWorkspaces(&model.WorkspaceFilterRequest{UserID: "1"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, ws.ID, got[0].ID)
}
