package handler

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/m-cmp/mc-iam-manager/repository"
	"github.com/m-cmp/mc-iam-manager/service"
	"gorm.io/gorm"
)

// CspIAMHandler CSP IAM 직접 관리 핸들러
// CSP IAM Role CRUD를 CSP API를 직접 호출하여 처리합니다.
type CspIAMHandler struct {
	cspAccountRepo   *repository.CspAccountRepository
	cspIdpConfigRepo *repository.CspIdpConfigRepository
	db               *gorm.DB
	cspRoleService   *service.CspRoleService
}

// csIamWriteBlocked /api/csp/iam/roles 쓰기 차단 안내 (IAM-BUG-046)
const csIamWriteBlocked = "writing AWS IAM roles through /api/csp/iam/roles is disabled; use POST /api/roles/csp to create or register a CSP role, " +
	"POST /api/csp-target-roles/id/{id}/trust-policy/sync to fix its trust policy, and DELETE /api/roles/csp/id/{id} to delete it"

// NewCspIAMHandler 새 CspIAMHandler 인스턴스 생성
func NewCspIAMHandler(db *gorm.DB) *CspIAMHandler {
	return &CspIAMHandler{
		cspAccountRepo:   repository.NewCspAccountRepository(db),
		cspIdpConfigRepo: repository.NewCspIdpConfigRepository(db),
		db:               db,
		cspRoleService:   service.NewCspRoleService(db, service.NewKeycloakService()),
	}
}

func parseCspAccountID(c echo.Context) (uint, error) {
	idStr := c.QueryParam("csp_account_id")
	if idStr == "" {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "csp_account_id query parameter is required")
	}
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "invalid csp_account_id")
	}
	return uint(id), nil
}

// CreateIAMRole godoc
// @Summary CSP IAM 역할 생성
// @Description Disabled (IAM-BUG-046): always 400. Use POST /api/roles/csp to create or register a CSP role (the trust policy is built from the IdP config).
// @Tags csp-iam
// @Accept json
// @Produce json
// @Param csp_account_id query int true "CSP 계정 ID"
// @Success 201 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp/iam/roles [post]
// @Id createCspIAMRole
func (h *CspIAMHandler) CreateIAMRole(c echo.Context) error {
	// IAM-BUG-046: 이 API 는 신뢰 정책을 Federated·Condition 없이 통째로 교체하고 호출자 토큰으로 관리용 Role 을 assume 했다. 쓰기는 막고 대체 API 로 안내한다
	return c.JSON(http.StatusBadRequest, map[string]string{"error": csIamWriteBlocked, "errorCode": "invalid_request"})
}

// GetIAMRole godoc
// @Summary CSP IAM 역할 조회
// @Description Get an AWS IAM role with the management role of the account (active aws OIDC IdP config). The trust policy is returned as is.
// @Tags csp-iam
// @Accept json
// @Produce json
// @Param roleName path string true "IAM 역할명"
// @Param csp_account_id query int true "CSP 계정 ID"
// @Success 200 {object} model.AwsRoleView
// @Failure 502 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp/iam/roles/{roleName} [get]
// @Id getCspIAMRole
func (h *CspIAMHandler) GetIAMRole(c echo.Context) error {
	roleName := c.Param("roleName")
	if roleName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "roleName is required", "errorCode": "invalid_request"})
	}
	cspAccountID, err := parseCspAccountID(c)
	if err != nil {
		return err
	}
	// 계정의 관리용 자격증명으로 조회한다 (이전에는 호출자 토큰으로 assume 해 항상 실패했다)
	view, err := h.cspRoleService.GetAwsRoleView(c.Request().Context(), cspAccountID, roleName)
	if err != nil {
		return trustSyncErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, view)
}

// UpdateIAMRole godoc
// @Summary CSP IAM 역할 수정
// @Description Disabled (IAM-BUG-046): always 400. It replaced the whole trust policy without Federated and Condition. Use POST /api/csp-target-roles/id/{id}/trust-policy/sync.
// @Tags csp-iam
// @Accept json
// @Produce json
// @Param roleName path string true "IAM 역할명"
// @Param csp_account_id query int true "CSP 계정 ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp/iam/roles/{roleName} [put]
// @Id updateCspIAMRole
func (h *CspIAMHandler) UpdateIAMRole(c echo.Context) error {
	// IAM-BUG-046: 이 API 는 신뢰 정책을 Federated·Condition 없이 통째로 교체하고 호출자 토큰으로 관리용 Role 을 assume 했다. 쓰기는 막고 대체 API 로 안내한다
	return c.JSON(http.StatusBadRequest, map[string]string{"error": csIamWriteBlocked, "errorCode": "invalid_request"})
}

// DeleteIAMRole godoc
// @Summary CSP IAM 역할 삭제
// @Description Disabled (IAM-BUG-046): always 400. Use DELETE /api/roles/csp/id/{id} (detaches IAM policies and keeps registered roles in AWS).
// @Tags csp-iam
// @Accept json
// @Produce json
// @Param roleName path string true "IAM 역할명"
// @Param csp_account_id query int true "CSP 계정 ID"
// @Success 204
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp/iam/roles/{roleName} [delete]
// @Id deleteCspIAMRole
func (h *CspIAMHandler) DeleteIAMRole(c echo.Context) error {
	// IAM-BUG-046: 이 API 는 신뢰 정책을 Federated·Condition 없이 통째로 교체하고 호출자 토큰으로 관리용 Role 을 assume 했다. 쓰기는 막고 대체 API 로 안내한다
	return c.JSON(http.StatusBadRequest, map[string]string{"error": csIamWriteBlocked, "errorCode": "invalid_request"})
}

// handleIAMError IAM 클라이언트 생성 오류를 HTTP 오류로 변환합니다.
func handleIAMError(c echo.Context, err error) error {
	if httpErr, ok := err.(*echo.HTTPError); ok {
		return c.JSON(httpErr.Code, map[string]string{"error": httpErr.Message.(string)})
	}
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
}
