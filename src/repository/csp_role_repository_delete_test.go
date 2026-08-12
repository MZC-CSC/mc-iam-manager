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
	// CspRolePermission(mcmp_csp_role_permissions)은 의도적으로 마이그레이트하지 않는다 —
	// 실 환경(main.go AutoMigrate)에도 이 테이블이 없으므로, 여기서 만들어버리면
	// IAM-BUG-021이 재발해도 테스트가 잡아내지 못한다.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.CspRole{},
		&model.CspPolicy{},
		&model.CspRolePolicyMapping{},
	))
	return db
}

// TC-IAM-TECH-022-01: DeleteCspRoleRecord는 CspRole 레코드와 그 레코드를 참조하는
// CspRolePolicyMapping을 함께 정리해야 한다(cascade).
// IAM-BUG-021: CspRolePermission(mcmp_csp_role_permissions)은 어떤 환경에도 마이그레이트된
// 적이 없는 테이블이라, 이를 cascade 대상에 포함하면 실제 삭제 호출이 항상 실패한다 —
// 그 컬럼을 건드리지 않고도 정상 동작해야 함을 이 테스트로 고정한다.
func TestDeleteCspRoleRecord_CascadesPolicyMapping(t *testing.T) {
	db := setupCspRoleDeleteTestDB(t)
	r := NewCspRoleRepository(db)

	role := &model.CspRole{Name: "mciam-test-role", CspType: "gcp"}
	require.NoError(t, db.Create(role).Error)

	policy := &model.CspPolicy{Name: "test-policy", CspAccountID: 1, PolicyType: model.PolicyTypeCustom}
	require.NoError(t, db.Create(policy).Error)
	require.NoError(t, db.Create(&model.CspRolePolicyMapping{CspRoleID: role.ID, CspPolicyID: policy.ID}).Error)

	require.NoError(t, r.DeleteCspRoleRecord(role.ID), "존재하지 않는 mcmp_csp_role_permissions 테이블을 건드리지 않아야 에러 없이 삭제된다")

	var roleCount, mappingCount int64
	require.NoError(t, db.Model(&model.CspRole{}).Where("id = ?", role.ID).Count(&roleCount).Error)
	require.NoError(t, db.Model(&model.CspRolePolicyMapping{}).Where("csp_role_id = ?", role.ID).Count(&mappingCount).Error)

	assert.Zero(t, roleCount, "CspRole 레코드가 삭제되어야 한다")
	assert.Zero(t, mappingCount, "CspRolePolicyMapping이 함께 정리되어야 한다")
}
