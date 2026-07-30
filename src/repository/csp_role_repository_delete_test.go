package repository

import (
	"testing"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupCspRoleDeleteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.CspRole{},
		&model.CspPolicy{},
		&model.CspRolePolicyMapping{},
		&model.CspRolePermission{},
	))
	return db
}

// TC-IAM-TECH-022-01: DeleteCspRoleRecord는 CspRole 레코드뿐 아니라 그 레코드를
// 참조하는 CspRolePolicyMapping/CspRolePermission도 함께 정리해야 한다(cascade).
// 수정 전 DeleteCSPRole은 cascade 정리 없이 레코드만(그것도 AWS 호출 실패 시 도달 못함) 지웠다.
func TestDeleteCspRoleRecord_CascadesPolicyMappingAndPermission(t *testing.T) {
	db := setupCspRoleDeleteTestDB(t)
	r := NewCspRoleRepository(db)

	role := &model.CspRole{Name: "mciam-test-role", CspType: "gcp"}
	require.NoError(t, db.Create(role).Error)

	policy := &model.CspPolicy{Name: "test-policy", CspAccountID: 1, PolicyType: model.PolicyTypeCustom}
	require.NoError(t, db.Create(policy).Error)
	require.NoError(t, db.Create(&model.CspRolePolicyMapping{CspRoleID: role.ID, CspPolicyID: policy.ID}).Error)
	require.NoError(t, db.Create(&model.CspRolePermission{ID: "perm-1", CspRoleID: "1", Permission: "read"}).Error)

	require.NoError(t, r.DeleteCspRoleRecord(role.ID))

	var roleCount, mappingCount, permCount int64
	require.NoError(t, db.Model(&model.CspRole{}).Where("id = ?", role.ID).Count(&roleCount).Error)
	require.NoError(t, db.Model(&model.CspRolePolicyMapping{}).Where("csp_role_id = ?", role.ID).Count(&mappingCount).Error)
	require.NoError(t, db.Model(&model.CspRolePermission{}).Where("csp_role_id = ?", "1").Count(&permCount).Error)

	assert.Zero(t, roleCount, "CspRole 레코드가 삭제되어야 한다")
	assert.Zero(t, mappingCount, "CspRolePolicyMapping이 함께 정리되어야 한다")
	assert.Zero(t, permCount, "CspRolePermission이 함께 정리되어야 한다")
}
