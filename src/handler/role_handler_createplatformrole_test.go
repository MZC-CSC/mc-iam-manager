package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type testValidator struct{ v *validator.Validate }

func (tv *testValidator) Validate(i interface{}) error { return tv.v.Struct(i) }

func setupCreatePlatformRoleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// sqlite ":memory:" DSN을 여러 커넥션 풀로 열면 각 커넥션이 서로 다른 빈 DB를 보게 된다
	// (NewRoleHandler가 구성하는 MenuRepository 등 다른 리포지토리들이 자체적으로
	// AutoMigrate를 호출하며 새 커넥션을 열 수 있음). shared cache 모드로 여러 커넥션이
	// 같은 인메모리 DB를 공유하도록 한다. (MaxOpenConns(1)은 CreateRoleWithSubs 트랜잭션
	// 내부에서 r.db로 별도 커넥션을 요구하는 기존 코드와 만나 데드락이 나서 사용 불가)
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&model.RoleMaster{}, &model.RoleSub{}))
	return db
}

// TC-IAM-BUG-019-01: RoleTypes 2건(중복 값) 요청 시에도 RoleType이 빈 문자열인
// 오염 row가 생기지 않아야 한다. (레포지토리의 role_id+role_type 기준 dedup 때문에
// 최종 row 수는 1건으로 수렴하지만, 수정 전 버그는 이 dedup 이전에 RoleType=""인
// 첫 절반 슬롯이 별도 row로 항상 끼어들었다 — 그 오염 row가 재발하지 않는지 검증)
func TestCreatePlatformRole_MultipleRoleTypes_NoEmptyRoleTypePollution(t *testing.T) {
	db := setupCreatePlatformRoleTestDB(t)
	h := NewRoleHandler(db)

	e := echo.New()
	e.Validator = &testValidator{v: validator.New()}

	body := `{"name":"tc-iam-bug-019-platform-role","roleTypes":["platform","platform"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/roles/platform-roles", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.CreatePlatformRole(c))
	require.Equal(t, http.StatusCreated, rec.Code)

	var role model.RoleMaster
	require.NoError(t, db.Where("name = ?", "tc-iam-bug-019-platform-role").First(&role).Error)

	var subs []model.RoleSub
	require.NoError(t, db.Where("role_id = ?", role.ID).Find(&subs).Error)

	require.NotEmpty(t, subs)
	for _, s := range subs {
		assert.Equal(t, constants.RoleTypePlatform, s.RoleType, "빈 RoleType 오염 row가 없어야 한다")
	}
}

// TC-IAM-BUG-019-02: RoleTypes 1건 요청 시 정확히 1건만 생성(회귀 방지 — 이전 버그는 2건 생성)
func TestCreatePlatformRole_SingleRoleType_CreatesExactlyOneRoleSub(t *testing.T) {
	db := setupCreatePlatformRoleTestDB(t)
	h := NewRoleHandler(db)

	e := echo.New()
	e.Validator = &testValidator{v: validator.New()}

	body := `{"name":"tc-iam-bug-019-single","roleTypes":["platform"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/roles/platform-roles", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.CreatePlatformRole(c))
	require.Equal(t, http.StatusCreated, rec.Code)

	var role model.RoleMaster
	require.NoError(t, db.Where("name = ?", "tc-iam-bug-019-single").First(&role).Error)

	var subs []model.RoleSub
	require.NoError(t, db.Where("role_id = ?", role.ID).Find(&subs).Error)

	require.Len(t, subs, 1)
	assert.Equal(t, constants.RoleTypePlatform, subs[0].RoleType)
}
