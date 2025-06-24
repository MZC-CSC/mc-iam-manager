package model

import "github.com/m-cmp/mc-iam-manager/constants"

// 각종 요청에 대한 구조체 정의
// naming convention : XxxRequest

type Response struct {
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

// SetupInitialAdminRequest represents the request body for setting up the initial admin
type SetupInitialAdminRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
}

type ProjectFilterRequest struct {
	ProjectID     string `json:"projectId"`
	ProjectName   string `json:"projectName"`
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
}

type CreateProjectRequest struct {
	Name        string `json:"name" validate:"required"`
	Description string `json:"description"`
}

// McmpApiRequestParams defines the structure for parameters needed in an API call.
type McmpApiRequestParams struct {
	PathParams  map[string]string `json:"pathParams"`  // Parameters to replace in the resource path (e.g., {userId})
	QueryParams map[string]string `json:"queryParams"` // Parameters to append as query string (?key=value)
	Body        interface{}       `json:"body"`        // Request body (accept any JSON structure) - Changed from json.RawMessage for swag compatibility
}

// McmpApiCallRequest defines the structure for the API call request body.
type McmpApiCallRequest struct {
	ServiceName   string               `json:"serviceName" validate:"required"` // Target service name
	ActionName    string               `json:"actionName" validate:"required"`  // Target action name (operationId)
	RequestParams McmpApiRequestParams `json:"requestParams"`                   // Parameters for the external API call
}

// AssignRoleRequest 역할 할당/ 해제 요청 구조체
type AssignRoleRequest struct {
	UserID      string `json:"userId,omitempty"`      // 사용자 ID (문자열로 받음)
	Username    string `json:"username,omitempty"`    // 사용자명
	RoleID      string `json:"roleId,omitempty"`      // 역할 ID (문자열로 받음)
	RoleName    string `json:"roleName,omitempty"`    // 역할명
	RoleType    string `json:"roleType"`              // 역할 타입 (platform/workspace)
	WorkspaceID string `json:"workspaceId,omitempty"` // 워크스페이스 ID (문자열로 받음)
}

// RoleMasterSubRequest 역할 생성 요청 구조체
type CreateRoleRequest struct {
	Name        string `json:"name" validate:"required"`
	Description string `json:"description"`
	ParentID    *uint  `json:"parentId"`
	//RoleTypes   []constants.IAMRoleType `json:"roleTypes" validate:"required,dive,oneof=platform workspace csp"`
	RoleTypes []constants.IAMRoleType `json:"roleTypes,omitempty"`

	MenuIDs  []string  `json:"menuIds,omitempty"`
	CspRoles []CspRole `json:"cspRoles,omitempty"`
}

// RoleRequest 역할 조회 요청 구조체
type RoleFilterRequest struct {
	RoleID    string                  `json:"roleId,omitempty"`
	RoleName  string                  `json:"roleName",omitempty"`
	RoleTypes []constants.IAMRoleType `json:"roleTypes",omitempty"`
}

// 워크스페이스와 프로젝트 매핑 추가 또는 해제
// POST /WorkspaceProjectMapping
// { "workspaceId": 1, "projectId": [2, 3] }
type WorkspaceProjectMappingRequest struct {
	WorkspaceID string   `json:"workspaceId" validate:"required"`
	ProjectID   []string `json:"projectId" validate:"required"`
}

type WorkspaceFilterRequest struct {
	WorkspaceID   string `json:"workspaceId,omitempty"`
	WorkspaceName string `json:"workspaceName,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	UserID        string `json:"userId,omitempty"`
	RoleID        string `json:"roleId,omitempty"`
}

type CreateCspRoleRequest struct {
	RoleName      string `json:"roleName",omitempty"`
	Description   string `json:"description,omitempty"`
	CspType       string `json:"cspType,omitempty"`
	IdpIdentifier string `json:"idpIdentifier,omitempty"`
	IamIdentifier string `json:"iamIdentifier,omitempty"`
	Status        string `json:"status,omitempty"`
	Path          string `json:"path,omitempty"`
	IamRoleId     string `json:"iamRoleId,omitempty"`
	Tags          []Tag  `json:"tags,omitempty"`
}

// CreateCspRolesRequest 복수 CSP 역할 생성 요청 구조체
type CreateCspRolesRequest struct {
	CspRoles []CreateCspRoleRequest `json:"cspRoles" validate:"required,dive"`
}

// CreateCspRolesRequest 복수 CSP 역할 생성 요청 구조체
type CreateCspRolesMappingRequest struct {
	RoleID      string               `json:"roleId"`
	AuthMethod  constants.AuthMethod `json:"authMethod"`
	Description string               `json:"description"`

	CspRoles []CreateCspRoleRequest `json:"cspRoles" validate:"required,dive"`
}

type CreateMenuRequest struct {
	ID          string `json:"id" validate:"required"`
	ParentID    string `json:"parentId,omitempty"`
	DisplayName string `json:"displayName"`
	ResType     string `json:"resType"`
	IsAction    bool   `json:"isAction"`
	Priority    string `json:"priority"`
	MenuNumber  string `json:"menuNumber"`
}

type MenuFilterRequest struct {
	MenuID   []*string `json:"menuId",omitempty"`
	MenuName []*string `json:"menuName",omitempty"`
}

// CreateMenuMappingRequest 메뉴 매핑 생성을 위한 요청 구조체
type CreateMenuMappingRequest struct {
	RoleID string   `json:"roleId" validate:"required"`
	MenuID []string `json:"menuId" validate:"required"`
}

type MenuMappingFilterRequest struct {
	RoleID []string `json:"roleId",omitempty"`
	MenuID string   `json:"menuId",omitempty"`
}

// AssignWorkspaceRoleRequest 워크스페이스 역할 할당 요청 구조체
type AssignWorkspaceRoleRequest struct {
	UserID      string `json:"userId,omitempty"`   // 사용자 ID (문자열로 받음)
	Username    string `json:"username,omitempty"` // 사용자명
	RoleID      string `json:"roleId,omitempty"`   // 역할 ID (문자열로 받음)
	RoleName    string `json:"roleName,omitempty"` // 역할명
	WorkspaceID string `json:"workspaceId"`        // 워크스페이스 ID (문자열로 받음)
}

// RemoveWorkspaceRoleRequest 워크스페이스 역할 제거 요청 구조체
type RemoveWorkspaceRoleRequest struct {
	UserID      string `json:"userId,omitempty"`   // 사용자 ID (문자열로 받음)
	Username    string `json:"username,omitempty"` // 사용자명
	RoleID      string `json:"roleId,omitempty"`   // 역할 ID (문자열로 받음)
	RoleName    string `json:"roleName,omitempty"` // 역할명
	WorkspaceID string `json:"workspaceId"`        // 워크스페이스 ID (문자열로 받음)
}

// WorkspaceRoleCspRoleMappingRequest 워크스페이스 역할-CSP 역할 매핑 요청 구조체
type CreateRoleCspRoleMappingRequest struct {
	//WorkspaceID     string `json:"workspaceId" validate:"required"`     // 워크스페이스 ID
	WorkspaceRoleID string                 `json:"workspaceRoleId" validate:"required"` // 역할 ID
	CspRoleID       string                 `json:"cspRoleId" validate:"required"`       // CSP 역할 ID
	AuthMethod      constants.AuthMethod   `json:"authMethod" validate:"required"`      // CSP 타입
	CspRoles        []CreateCspRoleRequest `json:"cspRoles" validate:"required,dive"`
}

// UserStatusRequest 사용자 상태 변경 요청 구조체( request, confirm, reject, active, inactive)
type UserStatusRequest struct {

	// DB에 저장되는 정보 (mcmp_users 테이블)
	ID     string `json:"id"`     // DB Primary Key (Renamed from DbId)
	KcId   string `json:"kc_id"`  // Keycloak User ID
	Status string `json:"status"` // 사용자 상태
}

// WorkspaceRoleCspRoleMapping 워크스페이스 역할 - CSP 역할 매핑 (DB 테이블: mcmp_workspace_role_csp_role_mapping)
type CreateRoleMasterCspRoleMappingRequest struct {
	RoleID      string                 `json:"roleId,omitempty"`
	CspType     constants.CSPType      `json:"cspType,omitempty"`
	CspRoleID   string                 `json:"cspRoleId,omitempty"`
	Description string                 `json:"description,omitempty"`
	AuthMethod  constants.AuthMethod   `json:"authMethod,omitempty"`
	CspRoles    []CreateCspRoleRequest `json:"cspRoles,omitempty"`
}

// 조회 request 구조체
type RoleMasterCspRoleMappingRequest struct {
	RoleID      string               `json:"roleId,omitempty"`
	CspType     constants.CSPType    `json:"cspType,omitempty"`
	CspRoleID   string               `json:"cspRoleId,omitempty"`
	Description string               `json:"description,omitempty"`
	AuthMethod  constants.AuthMethod `json:"authMethod,omitempty"`
}

type WorkspaceWithUsersAndRolesRequest struct {
	WorkspaceID string `json:"workspaceId"`
}

// RoleMapping을 조회하기 위한 요청 구조체
type RoleMappingRequest struct {
	RoleID      string                `json:"roleId"`
	RoleType    constants.IAMRoleType `json:"roleType"`
	CspType     string                `json:"cspType"`
	CspRoleID   string                `json:"cspRoleId"`
	WorkspaceID string                `json:"workspaceId"`
}
