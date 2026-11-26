package service

// IAM-BUG-050: 직접 배정 1개 규칙이 초대 수락·승인에도 적용되는지, 그룹 역할 쓰기 경고,
// 발급·진단 응답의 usedRole.

import (
	"context"
	"errors"
	"testing"

	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func invWithRole(t *testing.T, db *gorm.DB, wsID, inviteeID, roleID uint, status model.InvitationStatus) *model.WorkspaceInvitation {
	t.Helper()
	inv := createTestInvitation(t, db, wsID, 1, inviteeID, status)
	require.NoError(t, db.Model(&model.WorkspaceInvitation{}).Where("id = ?", inv.ID).Update("role_id", roleID).Error)
	inv.RoleID = &roleID
	return inv
}

func invStatus(t *testing.T, db *gorm.DB, id uint) model.InvitationStatus {
	var inv model.WorkspaceInvitation
	require.NoError(t, db.First(&inv, id).Error)
	return inv.Status
}

func TestInvitationAccept_SingleRoleRule(t *testing.T) {
	svc, db := newTestInvitationService(t)
	ws := createTestWorkspace(t, db, "ws-inv-050")
	u := createInvTestUser(t, db, "kc-inv-050")
	viewer := &model.RoleMaster{Name: "inv-viewer"}
	op := &model.RoleMaster{Name: "inv-operator"}
	require.NoError(t, db.Create(viewer).Error)
	require.NoError(t, db.Create(op).Error)

	// 역할 없음 → 수락하면 직접 배정 1개
	inv1 := invWithRole(t, db, ws.ID, u.ID, viewer.ID, model.InvitationStatusPending)
	require.NoError(t, svc.AcceptInvitation(inv1.ID, u.ID))
	assert.Equal(t, model.InvitationStatusAccepted, invStatus(t, db, inv1.ID))

	// 다른 역할 초대 수락 → 409 role_conflict, 초대 상태·직접 배정 그대로
	inv2 := invWithRole(t, db, ws.ID, u.ID, op.ID, model.InvitationStatusPending)
	err := svc.AcceptInvitation(inv2.ID, u.ID)
	var conflict *WorkspaceRoleConflictError
	require.True(t, errors.As(err, &conflict), "%v", err)
	assert.Equal(t, viewer.ID, conflict.CurrentRoleID)
	assert.Equal(t, model.InvitationStatusPending, invStatus(t, db, inv2.ID))

	// 같은 역할 초대 승인 → already_assigned
	inv3 := invWithRole(t, db, ws.ID, u.ID, viewer.ID, model.InvitationStatusPendingApproval)
	err = svc.ApproveInvitation(inv3.ID)
	assert.True(t, errors.Is(err, ErrWorkspaceRoleAlreadyAssigned), "%v", err)
	assert.Equal(t, model.InvitationStatusPendingApproval, invStatus(t, db, inv3.ID))

	var n int64
	db.Model(&model.UserWorkspaceRole{}).Where("user_id = ? AND workspace_id = ?", u.ID, ws.ID).Count(&n)
	assert.Equal(t, int64(1), n)
}

// 그룹에 워크스페이스 역할을 주거나 바꿀 때, 직접 배정 때문에 그룹 역할이 적용되지 않는 멤버를 경고
func TestGroupWorkspaceOverrideWarnings(t *testing.T) {
	svc, db := newTestGroupRoleService(t)
	// setupGroupRoleTestDB 의 User 연관 테이블과 겹치지 않게 직접 배정 테이블을 다시 만든다
	require.NoError(t, db.Exec(`DROP TABLE IF EXISTS mcmp_user_workspace_roles`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE mcmp_user_workspace_roles (user_id INTEGER, workspace_id INTEGER, role_id INTEGER, username TEXT, workspace_name TEXT, role_name TEXT, created_at DATETIME, PRIMARY KEY (user_id, workspace_id, role_id))`).Error)
	org := createGRTestOrg(t, db, "team-w", "TEAMW")
	ws := &model.Workspace{Name: "ws-grp-050"}
	require.NoError(t, db.Create(ws).Error)
	viewer := createGRTestRole(t, db, "grp-viewer")
	op := createGRTestRole(t, db, "grp-operator")
	alice := &model.User{Username: "alice", KcId: "kc-a"}
	bob := &model.User{Username: "bob", KcId: "kc-b"}
	require.NoError(t, db.Create(alice).Error)
	require.NoError(t, db.Create(bob).Error)
	for _, u := range []*model.User{alice, bob} {
		require.NoError(t, db.Create(&model.UserOrganization{UserID: u.ID, OrganizationID: org.ID}).Error)
	}
	// alice: 직접 viewer, bob: 직접 배정 없음
	require.NoError(t, db.Create(&model.UserWorkspaceRole{UserID: alice.ID, WorkspaceID: ws.ID, RoleID: viewer.ID}).Error)

	require.NoError(t, svc.AssignGroupWorkspace(org.ID, ws.ID, op.ID))
	w := svc.GroupWorkspaceOverrideWarnings(org.ID, ws.ID)
	require.Len(t, w, 1)
	assert.Contains(t, w[0], "alice")
	assert.Contains(t, w[0], "grp-viewer")

	// 그룹 역할을 직접 배정과 같은 역할로 바꾸면 경고 없음
	require.NoError(t, svc.UpdateGroupWorkspaceRole(org.ID, ws.ID, viewer.ID))
	assert.Empty(t, svc.GroupWorkspaceOverrideWarnings(org.ID, ws.ID))
}

// 발급 응답에 사용한 역할과 출처
func TestGetTemporaryCredentials_UsedRole(t *testing.T) {
	gid := uint(7)
	svc := newCredServiceWithMocks(credServiceDeps{
		aws:      &mockAwsCredService{oidcResult: awsOidcCred},
		gcp:      &mockGcpCredService{},
		alibaba:  &mockAlibabaCredService{},
		kc:       oidcKC(),
		userRepo: &mockUserRepoForCred{role: &model.EffectiveWorkspaceRole{RoleID: 1, RoleName: "viewer", Source: model.WorkspaceRoleSourceGroup, GroupID: &gid}},
		mapRepo:  &mockCspMappingRepo{mapping: buildMapping(constants.AuthMethodOIDC, idpArn, roleArn, model.AuthMethodOIDC, nil)},
	})
	cred, err := svc.GetTemporaryCredentials(context.Background(), 1, "kc_user_id", req("aws", "OIDC"))
	require.NoError(t, err)
	require.NotNil(t, cred.UsedRole)
	assert.Equal(t, uint(1), cred.UsedRole.RoleID)
	assert.Equal(t, "viewer", cred.UsedRole.RoleName)
	assert.Equal(t, model.WorkspaceRoleSourceGroup, cred.UsedRole.Source)
	assert.Equal(t, gid, *cred.UsedRole.GroupID)
}

// 진단 응답에도 같은 역할 결정 결과를 싣는다 (단계 실패여도)
func TestValidateCredentials_UsedRole(t *testing.T) {
	svc := newValService(stdValUserRole(), nil, nil, errValMappingNotFound, nil, nil)
	resp, err := svc.ValidateCredentials(context.Background(), 1, "kc_user", valReq("aws", "OIDC"))
	require.NoError(t, err)
	require.NotNil(t, resp.UsedRole)
	assert.Equal(t, stdValUserRole().RoleID, resp.UsedRole.RoleID)
	assert.Equal(t, model.WorkspaceRoleSourceDirect, resp.UsedRole.Source)

	// 역할이 없으면 생략
	svc = newValService(nil, nil, nil, nil, nil, nil)
	resp, err = svc.ValidateCredentials(context.Background(), 1, "kc_user", valReq("aws", "OIDC"))
	require.NoError(t, err)
	assert.Nil(t, resp.UsedRole)
}
