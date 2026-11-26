package handler

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/service"
	"github.com/m-cmp/mc-iam-manager/util"
	"gorm.io/gorm"
)

// CspTargetRoleHandler 대상 CSP Role 신뢰 정책 재동기화 (IAM-TECH-045 M2 PR-2)
type CspTargetRoleHandler struct {
	cspRoleService *service.CspRoleService
}

// NewCspTargetRoleHandler 새 핸들러
func NewCspTargetRoleHandler(db *gorm.DB) *CspTargetRoleHandler {
	return &CspTargetRoleHandler{cspRoleService: service.NewCspRoleService(db, service.NewKeycloakService())}
}

func bindTrustSyncRequest(c echo.Context) (*model.TrustPolicySyncRequest, error) {
	req := &model.TrustPolicySyncRequest{}
	if c.Request().ContentLength == 0 {
		return req, nil
	}
	if err := c.Bind(req); err != nil {
		return nil, err
	}
	return req, nil
}

// trustSyncErrorJSON 오류는 {error, errorCode}. 변경안이 있는 거절(registered_role)은 결과 필드도 함께 넣는다
func trustSyncErrorJSON(c echo.Context, err error) error {
	te, ok := service.AsTrustSyncError(err)
	if !ok {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error(), "errorCode": "db_error"})
	}
	body := map[string]interface{}{}
	if te.Result != nil {
		b, _ := json.Marshal(te.Result)
		_ = json.Unmarshal(b, &body)
	}
	body["error"], body["errorCode"] = te.Msg, te.Code
	return c.JSON(te.Status, body)
}

// SyncTrustPolicy godoc
// @Summary Sync the trust policy of a CSP target role
// @Description Compare the AWS trust policy of the target role with the expected statements built from its IdP config (OIDC: Federated oidc_provider_arn, sts:AssumeRoleWithWebIdentity, <host>:aud = audience; SAML: Federated saml_provider_arn, sts:AssumeRoleWithSAML, SAML:aud) and fix only what differs.
// @Description Only Allow statements whose Federated is the expected provider are treated as IAM statements: the expected Action is added and the aud condition is set. Other statements and condition keys are kept. A missing statement is added.
// @Description dryRun is true by default (changes only). Nothing is written when the verdict is already ok.
// @Description Roles registered from an existing AWS role (origin registered or empty) need allowRegistered=true when there is something to change: 409 registered_role with before, after and changes.
// @Description 409 protected for the management role, 400 when the IdP config is inactive or the expected statements cannot be built, 404 when the role does not exist in AWS, 502 when AWS fails.
// @Tags csp-target-roles
// @Accept json
// @Produce json
// @Param roleId path string true "CSP target role ID"
// @Param request body model.TrustPolicySyncRequest false "dryRun (default true), allowRegistered (default false)"
// @Success 200 {object} model.TrustPolicySyncResult
// @Failure 400 {object} map[string]interface{}
// @Failure 404 {object} map[string]interface{}
// @Failure 409 {object} map[string]interface{}
// @Failure 502 {object} map[string]interface{}
// @Security BearerAuth
// @Router /api/csp-target-roles/id/{roleId}/trust-policy/sync [post]
// @Id syncCspTargetRoleTrustPolicy
func (h *CspTargetRoleHandler) SyncTrustPolicy(c echo.Context) error {
	roleID, err := util.StringToUint(c.Param("roleId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid role ID", "errorCode": "invalid_request"})
	}
	req, err := bindTrustSyncRequest(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format", "errorCode": "invalid_request"})
	}
	res, err := h.cspRoleService.SyncTargetRoleTrustPolicy(c.Request().Context(), roleID, req)
	if err != nil {
		return trustSyncErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// CheckIdpConfigTrustPolicy godoc
// @Summary Check trust policies of the target roles of an IdP config
// @Description Check every CSP target role of the IdP config against the statement of this IdP config's auth method. Nothing is written. Management roles (protected) and non-aws roles (invalid_request) are reported and skipped.
// @Tags csp-idp-configs
// @Accept json
// @Produce json
// @Param configId path string true "IdP config ID"
// @Success 200 {object} model.TrustPolicyBulkResult
// @Failure 400 {object} map[string]interface{}
// @Failure 404 {object} map[string]interface{}
// @Security BearerAuth
// @Router /api/csp-idp-configs/id/{configId}/trust-policy/check [post]
// @Id checkCspIdpConfigTrustPolicy
func (h *CspTargetRoleHandler) CheckIdpConfigTrustPolicy(c echo.Context) error {
	configID, err := util.StringToUint(c.Param("configId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid config ID", "errorCode": "invalid_request"})
	}
	res, err := h.cspRoleService.IdpConfigTrustPolicy(c.Request().Context(), configID, false, nil)
	if err != nil {
		return trustSyncErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// SyncIdpConfigTrustPolicy godoc
// @Summary Sync trust policies of the target roles of an IdP config
// @Description Sync every CSP target role of the IdP config with the statement of this IdP config's auth method (same rules as the single-role sync). dryRun is true by default. Each item has a status (unchanged, would_change, applied, registered_role, protected, not_found, invalid_request, error); one failure does not stop the others. 400 when the IdP config is inactive.
// @Tags csp-idp-configs
// @Accept json
// @Produce json
// @Param configId path string true "IdP config ID"
// @Param request body model.TrustPolicySyncRequest false "dryRun (default true), allowRegistered (default false)"
// @Success 200 {object} model.TrustPolicyBulkResult
// @Failure 400 {object} map[string]interface{}
// @Failure 404 {object} map[string]interface{}
// @Security BearerAuth
// @Router /api/csp-idp-configs/id/{configId}/trust-policy/sync [post]
// @Id syncCspIdpConfigTrustPolicy
func (h *CspTargetRoleHandler) SyncIdpConfigTrustPolicy(c echo.Context) error {
	configID, err := util.StringToUint(c.Param("configId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid config ID", "errorCode": "invalid_request"})
	}
	req, err := bindTrustSyncRequest(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format", "errorCode": "invalid_request"})
	}
	res, err := h.cspRoleService.IdpConfigTrustPolicy(c.Request().Context(), configID, true, req)
	if err != nil {
		return trustSyncErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, res)
}
