package service

import (
	"testing"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IAM-BUG-051: POST /roles/csp 는 AWS 레코드가 DB 에 있어도 GetRole 로 다시 확인한다
func TestCreateCspRole_ExistingAwsRecordRechecked(t *testing.T) {
	svc, api, cfg := newTargetTestService(t, nil)
	stale := &model.CspRole{Name: "mciam-t045-stale", CspType: "aws", Status: "created", IamRoleId: "AROA-OLD", CspIdpConfigID: &cfg.ID}
	require.NoError(t, svc.db.Create(stale).Error)

	t.Run("AWS 에 있으면 값을 다시 동기화", func(t *testing.T) {
		api.roles["mciam-t045-stale"] = fakeAwsRole("mciam-t045-stale", `{"Statement":[{"Principal":{"Federated":"`+testOidcProviderArn+`"}}]}`)
		got, err := svc.CreateCspRole(&model.CreateCspRoleRequest{CspRoleName: "mciam-t045-stale", CspType: "aws"})
		require.NoError(t, err)
		assert.Equal(t, stale.ID, got.ID, "같은 레코드")
		assert.Equal(t, "AROAmciam-t045-stale", got.IamRoleId, "AWS 값으로 갱신")
		assert.Empty(t, api.createCalls)
		assert.Equal(t, model.CspRoleOriginRegistered, got.Origin, "출처 모르는 이전 레코드는 registered")
	})
	t.Run("AWS 에서 지워졌으면 소속 Config 기준으로 다시 만든다", func(t *testing.T) {
		delete(api.roles, "mciam-t045-stale")
		got, err := svc.CreateCspRole(&model.CreateCspRoleRequest{CspRoleName: "mciam-t045-stale", CspType: "aws"})
		require.NoError(t, err)
		assert.Equal(t, stale.ID, got.ID)
		require.Len(t, api.createCalls, 1, "stale 레코드를 그대로 돌려주지 않는다")
		assert.Equal(t, "created", got.Status)
		assert.Equal(t, model.CspRoleOriginCreated, got.Origin, "IAM-BUG-051 재생성은 created")
	})
	t.Run("IAM 이 만든 레코드는 다시 등록해도 created 유지", func(t *testing.T) {
		got, err := svc.CreateCspRole(&model.CreateCspRoleRequest{CspRoleName: "mciam-t045-stale", CspType: "aws"})
		require.NoError(t, err)
		assert.Equal(t, model.CspRoleOriginCreated, got.Origin)
	})
}

// AWS 에 없는 Role 삭제(IAM-BUG-052)는 csp_role_delete_test.go 로 옮겼다 (IAM-BUG-057)

// aws CspRole 이름 변경은 400 (AWS Role 은 이름을 바꿀 수 없다, IAM-BUG-057). 비 aws 는 기존대로 허용
func TestUpdateCspRole_AwsRenameRejected(t *testing.T) {
	svc, _, cfg := newTargetTestService(t, nil)
	r := &model.CspRole{Name: "mciam-t045-name", CspType: "aws", Status: "created", CspIdpConfigID: &cfg.ID}
	require.NoError(t, svc.db.Create(r).Error)
	err := svc.UpdateCspRole(r.ID, &model.CreateCspRoleRequest{CspRoleName: "mciam-t045-other"})
	assert.True(t, IsCspRoleRequestError(err), "%v", err)
	require.NoError(t, svc.UpdateCspRole(r.ID, &model.CreateCspRoleRequest{CspRoleName: "mciam-t045-name", Description: "same name ok"}))

	g := &model.CspRole{Name: "gcp-sa", CspType: "gcp", Status: "created"}
	require.NoError(t, svc.db.Create(g).Error)
	require.NoError(t, svc.UpdateCspRole(g.ID, &model.CreateCspRoleRequest{CspRoleName: "gcp-sa2"}))
}
