package repository

import (
	"testing"

	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupFindMappingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoleMasterCspRoleMapping{}, &model.CspRole{}))
	return db
}

func seedMappingsWithAllAuthMethods(t *testing.T, db *gorm.DB, roleID uint) {
	t.Helper()
	for i, am := range []constants.AuthMethod{constants.AuthMethodOIDC, constants.AuthMethodSAML, constants.AuthMethodSecretKey} {
		require.NoError(t, db.Create(&model.RoleMasterCspRoleMapping{
			RoleID:     roleID,
			AuthMethod: am,
			CspRoleID:  uint(i + 1),
		}).Error)
	}
}

// TC-IAM-BUG-020-01: authMethod 미지정 시 OIDC 외 SAML/SECRET_KEY 매핑도 전부 조회되어야 한다
// (수정 전에는 auth_method='OIDC' 하드코딩으로 SAML/SECRET_KEY 매핑이 항상 누락됐다)
func TestFindRoleMasterCspRoleMappings_NoAuthMethodFilter_ReturnsAllAuthMethods(t *testing.T) {
	db := setupFindMappingsTestDB(t)
	r := NewRoleRepository(db)
	seedMappingsWithAllAuthMethods(t, db, 1)

	mappings, err := r.FindRoleMasterCspRoleMappings(&model.RoleMasterCspRoleMappingRequest{RoleID: "1"})

	require.NoError(t, err)
	assert.Len(t, mappings, 3, "OIDC/SAML/SECRET_KEY 매핑이 모두 조회되어야 한다")
}

// TC-IAM-BUG-020-02: authMethod 지정 시 해당 인증방식 매핑만 조회되어야 한다
func TestFindRoleMasterCspRoleMappings_WithAuthMethodFilter_FiltersToRequestedMethod(t *testing.T) {
	db := setupFindMappingsTestDB(t)
	r := NewRoleRepository(db)
	seedMappingsWithAllAuthMethods(t, db, 1)

	mappings, err := r.FindRoleMasterCspRoleMappings(&model.RoleMasterCspRoleMappingRequest{
		RoleID:     "1",
		AuthMethod: constants.AuthMethodSAML,
	})

	require.NoError(t, err)
	require.Len(t, mappings, 1)
	assert.Equal(t, constants.AuthMethodSAML, mappings[0].AuthMethod)
}

// TC-IAM-BUG-020-03: FindWorkspaceRoleCspRoleMappings도 동일한 하드코딩 버그가 있었다 —
// 같은 파일에서 발견된 동일 패턴을 함께 수정(부수 발견)
func TestFindWorkspaceRoleCspRoleMappings_NoAuthMethodFilter_ReturnsAllAuthMethods(t *testing.T) {
	db := setupFindMappingsTestDB(t)
	r := NewRoleRepository(db)
	seedMappingsWithAllAuthMethods(t, db, 2)

	mappings, err := r.FindWorkspaceRoleCspRoleMappings(&model.RoleMasterCspRoleMappingRequest{RoleID: "2"})

	require.NoError(t, err)
	assert.Len(t, mappings, 3, "OIDC/SAML/SECRET_KEY 매핑이 모두 조회되어야 한다")
}

func TestFindWorkspaceRoleCspRoleMappings_WithAuthMethodFilter_FiltersToRequestedMethod(t *testing.T) {
	db := setupFindMappingsTestDB(t)
	r := NewRoleRepository(db)
	seedMappingsWithAllAuthMethods(t, db, 2)

	mappings, err := r.FindWorkspaceRoleCspRoleMappings(&model.RoleMasterCspRoleMappingRequest{
		RoleID:     "2",
		AuthMethod: constants.AuthMethodSecretKey,
	})

	require.NoError(t, err)
	require.Len(t, mappings, 1)
	assert.Equal(t, constants.AuthMethodSecretKey, mappings[0].AuthMethod)
}
