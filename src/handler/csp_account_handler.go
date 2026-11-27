package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/service"
	"github.com/m-cmp/mc-iam-manager/util"
	"gorm.io/gorm"
)

// validCspTypes 지원하는 CSP 타입 목록
var validCspTypes = map[string]bool{
	"aws": true, "gcp": true, "azure": true, "alibaba": true,
	"tencent": true, "ibm": true, "ncp": true, "nhn": true,
	"kt": true, "openstack": true,
}

// CspAccountHandler CSP 계정 관리 핸들러
type CspAccountHandler struct {
	cspAccountService *service.CspAccountService
	readinessService  *service.CspAccountReadinessService
}

// NewCspAccountHandler 새 CspAccountHandler 인스턴스 생성
func NewCspAccountHandler(db *gorm.DB) *CspAccountHandler {
	return &CspAccountHandler{
		cspAccountService: service.NewCspAccountService(db),
		readinessService:  service.NewCspAccountReadinessService(db),
	}
}

// CreateCspAccount godoc
// @Summary Create CSP account
// @Description Create a new CSP account
// @Description account_info.region is the default region for temporary credentials (used when a request has no region). Required for tencent (400 region_required); when set it must be a region code (400 invalid_region).
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param account body model.CreateCspAccountRequest true "CSP Account Info"
// @Success 201 {object} model.CspAccount
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts [post]
// @Id createCspAccount
func (h *CspAccountHandler) CreateCspAccount(c echo.Context) error {
	var req model.CreateCspAccountRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	// 필수 필드 검증
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Name is required"})
	}
	if req.CspType == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "CSP type is required"})
	}
	if !validCspTypes[req.CspType] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid CSP type. Must be one of: aws, gcp, azure, alibaba, tencent, ibm, ncp, nhn, kt, openstack"})
	}

	account, err := h.cspAccountService.CreateCspAccount(&req)
	if err != nil {
		if code, body, ok := accountRegionErrorResponse(err); ok {
			return c.JSON(code, body)
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to create CSP account: %v", err)})
	}

	return c.JSON(http.StatusCreated, account)
}

// ListCspAccounts godoc
// @Summary List CSP accounts
// @Description Retrieve a list of CSP accounts with optional filters
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param filter body model.CspAccountFilter false "Filter options"
// @Success 200 {array} model.CspAccount
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/list [post]
// @Id listCspAccounts
func (h *CspAccountHandler) ListCspAccounts(c echo.Context) error {
	var filter model.CspAccountFilter
	if err := c.Bind(&filter); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	accounts, err := h.cspAccountService.ListCspAccounts(&filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to list CSP accounts: %v", err)})
	}

	if accounts == nil {
		accounts = []*model.CspAccount{}
	}

	return c.JSON(http.StatusOK, accounts)
}

// GetCspAccountByID godoc
// @Summary Get CSP account by ID
// @Description Retrieve CSP account details by ID
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param accountId path string true "Account ID"
// @Success 200 {object} model.CspAccount
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/id/{accountId} [get]
// @Id getCspAccountByID
func (h *CspAccountHandler) GetCspAccountByID(c echo.Context) error {
	accountID, err := util.StringToUint(c.Param("accountId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid account ID"})
	}

	account, err := h.cspAccountService.GetCspAccountByID(accountID)
	if err != nil {
		if err.Error() == fmt.Sprintf("CSP account not found with ID: %d", accountID) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to get CSP account: %v", err)})
	}

	return c.JSON(http.StatusOK, account)
}

// UpdateCspAccount godoc
// @Summary Update CSP account
// @Description Update CSP account details
// @Description account_info replaces the stored map, except region: when account_info has no "region" key the stored region is kept; "region": "" removes it (not allowed for tencent).
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param accountId path string true "Account ID"
// @Param account body model.UpdateCspAccountRequest true "CSP Account Info"
// @Success 200 {object} model.CspAccount
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/id/{accountId} [put]
// @Id updateCspAccount
func (h *CspAccountHandler) UpdateCspAccount(c echo.Context) error {
	accountID, err := util.StringToUint(c.Param("accountId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid account ID"})
	}

	var req model.UpdateCspAccountRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	account, err := h.cspAccountService.UpdateCspAccount(accountID, &req)
	if err != nil {
		if code, body, ok := accountRegionErrorResponse(err); ok {
			return c.JSON(code, body)
		}
		if err.Error() == fmt.Sprintf("CSP account not found with ID: %d", accountID) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to update CSP account: %v", err)})
	}

	return c.JSON(http.StatusOK, account)
}

// DeleteCspAccount godoc
// @Summary Delete CSP account
// @Description Delete a CSP account by ID
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param accountId path string true "Account ID"
// @Success 204 "No Content"
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/id/{accountId} [delete]
// @Id deleteCspAccount
func (h *CspAccountHandler) DeleteCspAccount(c echo.Context) error {
	accountID, err := util.StringToUint(c.Param("accountId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid account ID"})
	}

	if err := h.cspAccountService.DeleteCspAccount(accountID); err != nil {
		if err.Error() == fmt.Sprintf("CSP account not found with ID: %d", accountID) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to delete CSP account: %v", err)})
	}

	return c.NoContent(http.StatusNoContent)
}

// ValidateCspAccount godoc
// @Summary Validate CSP account
// @Description Validate CSP account by checking actual CSP infrastructure (IDP provider existence, role trust policy, or credential validity) for each linked CspRole
// @Description AWS OIDC·SAML roles are checked with the management role of the IdP config (no server AWS credentials needed): provider existence (OIDC audience included) and the trust policy (Federated, Action, aud). Each result carries trust_policy with the same verdict as the credential validation API.
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param accountId path string true "Account ID"
// @Success 200 {object} model.CspAccountValidationResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/id/{accountId}/validate [post]
// @Id validateCspAccount
func (h *CspAccountHandler) ValidateCspAccount(c echo.Context) error {
	accountID, err := util.StringToUint(c.Param("accountId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid account ID"})
	}

	result, err := h.cspAccountService.ValidateCspAccount(c.Request().Context(), accountID)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": msg})
		}
		if strings.Contains(msg, "no CSP roles") {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": msg})
		}
		if c.Request().Context().Err() != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "CSP validation timeout"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": msg})
	}

	return c.JSON(http.StatusOK, result)
}

// GetCspAccountReadiness godoc
// @Summary Get CSP account readiness
// @Description CSP 계정의 임시자격증명 연동 준비 상태를 단계별로 반환합니다 (쓰기 없음).
// @Description ① Keycloak OIDC 클라이언트 ② CSP 계정 ③ IdP Config ④ Provider 신뢰(연결 테스트) ⑤ CSP Role ⑥ 역할 매핑 ⑦ 실제 assume.
// @Description 대상 CSP Role 은 같은 CSP 타입이면서 csp_account_id 가 이 계정이거나 미기록인 역할입니다.
// @Description ④·⑦ 은 CSP 를 실제로 호출하므로 PlatformAdmin 일 때만 수행하고, ⑦ 은 checkIssue=true 일 때만 수행합니다. 자격증명은 반환하지 않습니다.
// @Description ⑦ 을 수행할 때 AWS OIDC·SAML Role 마다 신뢰 정책 판정(cspRoles[].trustPolicy: ok·missing·mismatch·not_found·error)을 참고 정보로 붙입니다. 관리용 자격증명으로 읽기만 하며, ⑦ 판정은 실제 assume 결과로만 정합니다. missing·mismatch 이면 POST /api/csp-target-roles/id/{id}/trust-policy/sync 를 안내합니다.
// @Tags csp-accounts
// @Produce json
// @Param accountId path string true "Account ID"
// @Param checkIssue query bool false "⑦ 실제 assume 수행 여부 (PlatformAdmin 만 유효)"
// @Success 200 {object} model.CspAccountReadinessResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/id/{accountId}/readiness [get]
// @Id getCspAccountReadiness
func (h *CspAccountHandler) GetCspAccountReadiness(c echo.Context) error {
	accountID, err := util.StringToUint(c.Param("accountId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid account ID"})
	}

	opts := service.ReadinessOptions{
		LiveChecks: isPlatformAdmin(c),
		CheckIssue: c.QueryParam("checkIssue") == "true",
	}
	result, err := h.readinessService.GetReadiness(c.Request().Context(), accountID, opts)
	if err != nil {
		if errors.Is(err, service.ErrCspAccountNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("CSP account not found with ID: %d", accountID)})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

// isPlatformAdmin 요청자가 platformAdmin 역할을 가졌는지 (PlatformAdminMiddleware 와 같은 기준)
func isPlatformAdmin(c echo.Context) bool {
	roles, _ := c.Get("platformRoles").([]string)
	for _, r := range roles {
		if r == "platformAdmin" {
			return true
		}
	}
	return false
}

// ActivateCspAccount godoc
// @Summary Activate CSP account
// @Description Activate a CSP account
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param accountId path string true "Account ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/id/{accountId}/activate [post]
// @Id activateCspAccount
func (h *CspAccountHandler) ActivateCspAccount(c echo.Context) error {
	accountID, err := util.StringToUint(c.Param("accountId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid account ID"})
	}

	if err := h.cspAccountService.ActivateCspAccount(accountID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to activate CSP account: %v", err)})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "CSP account activated successfully"})
}

// DeactivateCspAccount godoc
// @Summary Deactivate CSP account
// @Description Deactivate a CSP account
// @Tags csp-accounts
// @Accept json
// @Produce json
// @Param accountId path string true "Account ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-accounts/id/{accountId}/deactivate [post]
// @Id deactivateCspAccount
func (h *CspAccountHandler) DeactivateCspAccount(c echo.Context) error {
	accountID, err := util.StringToUint(c.Param("accountId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid account ID"})
	}

	if err := h.cspAccountService.DeactivateCspAccount(accountID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to deactivate CSP account: %v", err)})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "CSP account deactivated successfully"})
}

// accountRegionErrorResponse 계정 region 검증 오류 → 400 (IAM-BUG-065)
func accountRegionErrorResponse(err error) (int, map[string]string, bool) {
	switch {
	case errors.Is(err, service.ErrRegionRequired):
		return http.StatusBadRequest, map[string]string{"error": err.Error(), "errorCode": "region_required"}, true
	case errors.Is(err, service.ErrInvalidRegion):
		return http.StatusBadRequest, map[string]string{"error": err.Error(), "errorCode": "invalid_region"}, true
	}
	return 0, nil, false
}
