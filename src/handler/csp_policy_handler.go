package handler

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/service"
	"github.com/m-cmp/mc-iam-manager/util"
	"gorm.io/gorm"
)

// CspPolicyHandler CSP 정책 관리 핸들러
type CspPolicyHandler struct {
	cspPolicyService *service.CspPolicyService
}

// NewCspPolicyHandler 새 CspPolicyHandler 인스턴스 생성
func NewCspPolicyHandler(db *gorm.DB) *CspPolicyHandler {
	keycloakService := service.NewKeycloakService()
	cspIdpConfigService := service.NewCspIdpConfigService(db, keycloakService)
	return &CspPolicyHandler{
		cspPolicyService: service.NewCspPolicyService(db, cspIdpConfigService),
	}
}

// CreateCspPolicy godoc
// @Summary Create CSP policy
// @Description Create a new CSP policy
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param policy body model.CreateCspPolicyRequest true "CSP Policy Info"
// @Success 201 {object} model.CspPolicy
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies [post]
// @Id createCspPolicy
func (h *CspPolicyHandler) CreateCspPolicy(c echo.Context) error {
	var req model.CreateCspPolicyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	// 필수 필드 검증
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Name is required"})
	}
	if req.CspAccountID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "CSP Account ID is required"})
	}
	if req.PolicyType == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Policy type is required"})
	}
	if req.PolicyType != model.PolicyTypeInline && req.PolicyType != model.PolicyTypeManaged && req.PolicyType != model.PolicyTypeCustom {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid policy type. Must be one of: inline, managed, custom"})
	}

	policy, err := h.cspPolicyService.CreateCspPolicy(&req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to create policy: %v", err)})
	}

	return c.JSON(http.StatusCreated, policy)
}

// ListCspPolicies godoc
// @Summary List CSP policies
// @Description Retrieve a list of CSP policies with optional filters
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param filter body model.CspPolicyFilter false "Filter options"
// @Success 200 {array} model.CspPolicy
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/list [post]
// @Id listCspPolicies
func (h *CspPolicyHandler) ListCspPolicies(c echo.Context) error {
	var filter model.CspPolicyFilter
	if err := c.Bind(&filter); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	policies, err := h.cspPolicyService.ListCspPolicies(&filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to list policies: %v", err)})
	}

	if policies == nil {
		policies = []*model.CspPolicy{}
	}

	return c.JSON(http.StatusOK, policies)
}

// GetCspPolicyByID godoc
// @Summary Get CSP policy by ID
// @Description Retrieve CSP policy details by ID
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param policyId path string true "Policy ID"
// @Success 200 {object} model.CspPolicy
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/id/{policyId} [get]
// @Id getCspPolicyByID
func (h *CspPolicyHandler) GetCspPolicyByID(c echo.Context) error {
	policyID, err := util.StringToUint(c.Param("policyId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid policy ID"})
	}

	policy, err := h.cspPolicyService.GetCspPolicyByID(policyID)
	if err != nil {
		if err.Error() == fmt.Sprintf("policy not found with ID: %d", policyID) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to get policy: %v", err)})
	}

	return c.JSON(http.StatusOK, policy)
}

// UpdateCspPolicy godoc
// @Summary Update CSP policy
// @Description Update CSP policy details
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param policyId path string true "Policy ID"
// @Param policy body model.UpdateCspPolicyRequest true "CSP Policy Info"
// @Success 200 {object} model.CspPolicy
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/id/{policyId} [put]
// @Id updateCspPolicy
func (h *CspPolicyHandler) UpdateCspPolicy(c echo.Context) error {
	policyID, err := util.StringToUint(c.Param("policyId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid policy ID"})
	}

	var req model.UpdateCspPolicyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	policy, err := h.cspPolicyService.UpdateCspPolicy(policyID, &req)
	if err != nil {
		if err.Error() == fmt.Sprintf("policy not found with ID: %d", policyID) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to update policy: %v", err)})
	}

	return c.JSON(http.StatusOK, policy)
}

// DeleteCspPolicy godoc
// @Summary Delete CSP policy
// @Description Delete a CSP policy by ID
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param policyId path string true "Policy ID"
// @Success 204 "No Content"
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/id/{policyId} [delete]
// @Id deleteCspPolicy
func (h *CspPolicyHandler) DeleteCspPolicy(c echo.Context) error {
	policyID, err := util.StringToUint(c.Param("policyId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid policy ID"})
	}

	if err := h.cspPolicyService.DeleteCspPolicy(policyID); err != nil {
		if err.Error() == fmt.Sprintf("policy not found with ID: %d", policyID) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to delete policy: %v", err)})
	}

	return c.NoContent(http.StatusNoContent)
}

// SyncCspPolicies godoc
// @Summary Sync CSP policies from cloud
// @Description Synchronize policies from the CSP cloud
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param request body model.SyncPoliciesRequest true "Sync Policies Request"
// @Success 200 {array} model.CspPolicy
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/sync [post]
// @Id syncCspPolicies
func (h *CspPolicyHandler) SyncCspPolicies(c echo.Context) error {
	var req model.SyncPoliciesRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	if req.CspAccountID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "CSP Account ID is required"})
	}

	policies, err := h.cspPolicyService.SyncPoliciesFromCloud(c.Request().Context(), &req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to sync policies: %v", err)})
	}

	return c.JSON(http.StatusOK, policies)
}

// AttachPolicyToRole godoc
// @Summary Attach policy to role
// @Description Attach a CSP policy to a CSP role and apply it in AWS (managed: AttachRolePolicy, inline: PutRolePolicy).
// @Description If the AWS call fails, the mapping is kept with aws_status=failed and the response is 200 with result=failed and warnings; requesting again re-applies it.
// @Description Rejected without changes: 400 invalid request (no ARN/document, custom type, account mismatch), 403 forbidden_policy (non mciam-* role or denied policy), 404 role/policy not found, 409 management role. 500 only when the database update fails (the AWS change is reverted).
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param request body model.AttachPolicyRequest true "Attach Policy Request"
// @Success 200 {object} model.PolicyBindingResult
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/attach [post]
// @Id attachPolicyToRole
func (h *CspPolicyHandler) AttachPolicyToRole(c echo.Context) error {
	return h.bindPolicy(c, true)
}

// DetachPolicyFromRole godoc
// @Summary Detach policy from role
// @Description Detach a CSP policy from a CSP role in AWS (managed: DetachRolePolicy, inline: DeleteRolePolicy), then remove the mapping. NoSuchEntity from AWS counts as already detached.
// @Description If the AWS call fails, the mapping is kept with aws_status=detach_failed and the response is 200 with result=detach_failed and warnings; requesting again retries.
// @Description Rejected without changes: 400, 403 forbidden_policy (non mciam-* role), 404 (role/policy not found or not attached), 409 management role. 500 only when the database update fails.
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param request body model.AttachPolicyRequest true "Detach Policy Request"
// @Success 200 {object} model.PolicyBindingResult
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/detach [post]
// @Id detachPolicyFromRole
func (h *CspPolicyHandler) DetachPolicyFromRole(c echo.Context) error {
	return h.bindPolicy(c, false)
}

func (h *CspPolicyHandler) bindPolicy(c echo.Context, attach bool) error {
	var req model.AttachPolicyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format", "errorCode": "invalid_request"})
	}
	if req.CspRoleID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "CSP Role ID is required", "errorCode": "invalid_request"})
	}
	if req.CspPolicyID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "CSP Policy ID is required", "errorCode": "invalid_request"})
	}

	var (
		result *model.PolicyBindingResult
		err    error
	)
	if attach {
		result, err = h.cspPolicyService.AttachPolicyToRole(req.CspRoleID, req.CspPolicyID)
	} else {
		result, err = h.cspPolicyService.DetachPolicyFromRole(req.CspRoleID, req.CspPolicyID)
	}
	if err != nil {
		if be, ok := service.AsPolicyBindingError(err); ok {
			return c.JSON(be.Status, map[string]string{"error": be.Msg, "errorCode": be.Code})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error(), "errorCode": "db_error"})
	}
	return c.JSON(http.StatusOK, result)
}

// GetRolePolicies godoc
// @Summary Get policies attached to role
// @Description Get list of policies attached to a CSP role, with the AWS state of each mapping (aws_status: applied | failed | detach_failed, empty for mappings made before AWS was applied; last_error; applied_at)
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param roleId path string true "Role ID"
// @Success 200 {array} model.CspRolePolicy
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/role/{roleId} [get]
// @Id getRolePolicies
func (h *CspPolicyHandler) GetRolePolicies(c echo.Context) error {
	roleID, err := util.StringToUint(c.Param("roleId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid role ID"})
	}

	policies, err := h.cspPolicyService.GetRolePoliciesWithStatus(roleID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to get policies: %v", err)})
	}

	return c.JSON(http.StatusOK, policies)
}

// GetPolicyDocument godoc
// @Summary Get policy document
// @Description Get the policy document content
// @Tags csp-policies
// @Accept json
// @Produce json
// @Param policyId path string true "Policy ID"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Security BearerAuth
// @Router /api/csp-policies/id/{policyId}/document [get]
// @Id getPolicyDocument
func (h *CspPolicyHandler) GetPolicyDocument(c echo.Context) error {
	policyID, err := util.StringToUint(c.Param("policyId"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid policy ID"})
	}

	document, err := h.cspPolicyService.GetPolicyDocument(c.Request().Context(), policyID)
	if err != nil {
		if err.Error() == fmt.Sprintf("policy not found with ID: %d", policyID) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to get policy document: %v", err)})
	}

	return c.JSON(http.StatusOK, document)
}
