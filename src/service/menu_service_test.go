package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupMenuServiceTestDB는 sqlite 인메모리 shared-cache DB에 메뉴/역할 관련 테이블을
// AutoMigrate한다. role_handler_createplatformrole_test.go의 패턴을 재사용하되,
// DSN에 테스트 이름을 넣어 shared-cache DB가 테스트 간에 공유되지 않도록 격리한다.
func setupMenuServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(
		&model.RoleMaster{},
		&model.RoleSub{},
		&model.Menu{},
		&model.RoleMenuMapping{},
	))
	return db
}

// UpsertMenus는 "SET CONSTRAINTS ALL DEFERRED" 등 PostgreSQL 전용 구문을 사용해
// sqlite에서 동작하지 않는다(이 티켓 범위 밖). 체이닝 동작만 검증하기 위해
// menu.yaml에는 upsert 대상에서 제외되는 home 메뉴만 둬서 UpsertMenus 호출 자체를 건너뛴다.
const testMenuYAML = `
menus:
  - id: home
    parentid: ""
    displayname: Home
    restype: menu
    isaction: false
    priority: 0
    menunumber: 1
`

const testPermissionYAML = `
permissions:
  - role: admin
    menus:
      - home
`

// seedAdminPlatformRole은 permission.yaml의 admin 역할이 조회되도록 role_masters/role_subs를 시딩한다.
func seedAdminPlatformRole(t *testing.T, db *gorm.DB) {
	t.Helper()
	role := model.RoleMaster{Name: "admin"}
	require.NoError(t, db.Create(&role).Error)
	require.NoError(t, db.Create(&model.RoleSub{
		RoleID:   role.ID,
		RoleType: constants.RoleTypePlatform,
	}).Error)
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

// TC-IAM-TECH-034-01: permission.yaml이 정상이면 메뉴 등록에 이어 권한 시딩까지
// 체이닝되어 성공하고, 경고 문자열은 빈 값이어야 한다.
func TestLoadAndRegisterMenusFromYAML_ChainsPermissionSeed_Success(t *testing.T) {
	db := setupMenuServiceTestDB(t)
	seedAdminPlatformRole(t, db)

	permPath := writeTempFile(t, "permission.yaml", testPermissionYAML)
	t.Setenv("MC_WEB_CONSOLE_MENU_PERMISSIONS", permPath)

	menuPath := writeTempFile(t, "menu.yaml", testMenuYAML)

	s := NewMenuService(db)
	warning, err := s.LoadAndRegisterMenusFromYAML(menuPath)
	require.NoError(t, err)
	require.Empty(t, warning, "permission seed succeeded, so no warning should be returned")

	var mapping model.RoleMenuMapping
	require.NoError(t, db.Where("menu_id = ?", "home").First(&mapping).Error)
}

// TC-IAM-TECH-034-02: permission.yaml을 찾지 못해 권한 시딩이 실패해도 메뉴 등록
// 자체는 실패하지 않아야 하며(에러가 nil), 실패 사실은 경고 문자열로 반환되어야 한다.
func TestLoadAndRegisterMenusFromYAML_PermissionSeedMissing_ReturnsWarningNotError(t *testing.T) {
	db := setupMenuServiceTestDB(t)

	missingPermPath := filepath.Join(t.TempDir(), "does-not-exist-permission.yaml")
	t.Setenv("MC_WEB_CONSOLE_MENU_PERMISSIONS", missingPermPath)

	menuPath := writeTempFile(t, "menu.yaml", testMenuYAML)

	s := NewMenuService(db)
	warning, err := s.LoadAndRegisterMenusFromYAML(menuPath)
	require.NoError(t, err, "permission seed failure must not fail menu registration")
	require.NotEmpty(t, warning, "permission seed failure should surface as a warning")

	var mappingCount int64
	require.NoError(t, db.Model(&model.RoleMenuMapping{}).Count(&mappingCount).Error)
	require.Zero(t, mappingCount, "no mapping should be created when permission seed fails")
}
