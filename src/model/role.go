package model

import (
	"time"

	"github.com/m-cmp/mc-iam-manager/constants"
)

// RoleMaster 역할 마스터 모델 (DB 테이블: mcmp_role_masters)
type RoleMaster struct {
	ID              uint                        `json:"id" gorm:"primaryKey;column:id"`
	ParentID        *uint                       `json:"parent_id" gorm:"column:parent_id"`
	Name            string                      `json:"name" gorm:"column:name;size:255;not null;unique"`
	Description     string                      `json:"description" gorm:"column:description;size:1000"`
	Predefined      bool                        `json:"predefined" gorm:"column:predefined;not null;default:false"`
	CreatedAt       time.Time                   `json:"created_at" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time                   `json:"updated_at" gorm:"column:updated_at;autoUpdateTime"`
	Parent          *RoleMaster                 `json:"parent,omitempty" gorm:"foreignKey:ParentID"`
	Children        []RoleMaster                `json:"children,omitempty" gorm:"foreignKey:ParentID"`
	RoleSubs        []RoleSub                   `json:"role_subs,omitempty" gorm:"foreignKey:RoleID"`
	CspRoleMappings []*RoleMasterCspRoleMapping `json:"csp_role_mappings,omitempty" gorm:"foreignKey:RoleID"`
}

// TableName RoleMaster의 테이블 이름을 반환
func (RoleMaster) TableName() string {
	return "mcmp_role_masters"
}

// RoleSub 역할 서브 모델 (DB 테이블: mcmp_role_sub)
type RoleSub struct {
	ID        uint                  `json:"id" gorm:"primaryKey;column:id"`
	RoleID    uint                  `json:"role_id" gorm:"column:role_id;not null"`
	RoleType  constants.IAMRoleType `json:"role_type" gorm:"column:role_type;size:50;not null"`
	CreatedAt time.Time             `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time             `json:"updated_at" gorm:"column:updated_at"`
}

// TableName RoleSub의 테이블 이름을 반환
func (RoleSub) TableName() string {
	return "mcmp_role_subs"
}

// UserPlatformRole 사용자-역할 매핑 모델 (DB 테이블: mcmp_user_platform_roles)
type UserPlatformRole struct {
	UserID    uint       `json:"user_id" gorm:"primaryKey;column:user_id"`
	RoleID    uint       `json:"role_id" gorm:"primaryKey;column:role_id"`
	CreatedAt time.Time  `json:"created_at" gorm:"column:created_at;autoCreateTime"`
	User      User       `json:"-" gorm:"foreignKey:UserID"`
	Role      RoleMaster `json:"-" gorm:"foreignKey:RoleID"`

	// 사용자 정보 (JOIN으로 가져올 필드들)
	Username string `json:"username" gorm:"column:username"`
}

// TableName UserPlatformRole의 테이블 이름을 반환
func (UserPlatformRole) TableName() string {
	return "mcmp_user_platform_roles"
}

// UserWorkspaceRole 사용자-워크스페이스-역할 매핑 모델 (DB 테이블: mcmp_user_workspace_roles)
// 사용자 기준으로 workspace와 role 을 표시 . workspace 기준으로는 WorkspaceWithProjects 를 사용
type UserWorkspaceRole struct {
	UserID        uint        `json:"user_id" gorm:"primaryKey;column:user_id"`
	WorkspaceID   uint        `json:"workspace_id" gorm:"primaryKey;column:workspace_id"`
	RoleID        uint        `json:"role_id" gorm:"primaryKey;column:role_id"`
	Username      string      `json:"username" gorm:"column:username"`
	WorkspaceName string      `json:"workspace_name" gorm:"column:workspace_name"`
	RoleName      string      `json:"role_name" gorm:"column:role_name"`
	CreatedAt     time.Time   `json:"created_at" gorm:"column:created_at;autoCreateTime"`
	User          *User       `json:"user,omitempty" gorm:"foreignKey:UserID"`
	Workspace     *Workspace  `json:"workspace,omitempty" gorm:"foreignKey:WorkspaceID;references:ID"`
	Role          *RoleMaster `json:"role,omitempty" gorm:"foreignKey:RoleID"`
}

// TableName UserWorkspaceRole의 테이블 이름을 반환
func (UserWorkspaceRole) TableName() string {
	return "mcmp_user_workspace_roles"
}

// RoleMasterCspRoleMapping 역할 마스터-CSP 역할 매핑 모델 (DB 테이블: mcmp_role_csp_role_mapping)
// (role_id, auth_method, csp_role_id) 조합으로 하나의 매핑 레코드 관리
// JSON 응답에서는 (role_id, auth_method) 조합에 대해 여러 CspRole을 배열로 반환
type RoleMasterCspRoleMapping struct {
	RoleID      uint                 `json:"roleId" gorm:"column:role_id;primaryKey;foreignKey:id;references:mcmp_role_masters"`
	AuthMethod  constants.AuthMethod `json:"auth_method" gorm:"column:auth_method;primaryKey"`
	CspRoleID   uint                 `json:"-" gorm:"column:csp_role_id;primaryKey;foreignKey:ID;references:mcmp_csp_roles"`
	Description string               `json:"description" gorm:"column:description"`
	CreatedAt   time.Time            `json:"createdAt" gorm:"column:created_at"`
	CspRoles    []*CspRole           `json:"cspRoles" gorm:"-"` // 서비스 레이어에서 조합
}

// TableName RoleMasterCspRoleMapping의 테이블 이름을 반환
func (RoleMasterCspRoleMapping) TableName() string {
	return "mcmp_role_csp_role_mappings"
}

// RoleMaster와 연결된 것들.
type RoleMasterMapping struct {
	RoleID                    uint                       `json:"role_id" gorm:"column:role_id"`
	RoleName                  string                     `json:"role_name" gorm:"column:role_name"`
	UserPlatformRoles         []UserPlatformRole         `json:"user_platform_roles" gorm:"-"`
	UserWorkspaceRoles        []UserWorkspaceRole        `json:"user_workspace_roles" gorm:"-"`
	RoleMasterCspRoleMappings []RoleMasterCspRoleMapping `json:"role_master_csp_role_mappings" gorm:"-"`
}

// 워크스페이스 역할 출처
const (
	WorkspaceRoleSourceDirect = "direct" // 사용자 직접 배정 (mcmp_user_workspace_roles)
	WorkspaceRoleSourceGroup  = "group"  // 소속 그룹의 워크스페이스 역할 (mcmp_group_workspace_roles)
)

// EffectiveWorkspaceRole 사용자의 유효 워크스페이스 역할 응답 (직접 할당 + 그룹 상속 통합)
//
// 단건 결정(FindEffectiveUserRoleInWorkspace)에서는 Source·GroupID 만 채운다.
// 목록(FindEffectiveWorkspaceRoles, GET /api/users/me/workspace-roles)은 (워크스페이스, 역할) 행마다
// 그 행의 출처와, 그 워크스페이스의 결정 결과(effective_role·group_roles·overridden)를 함께 싣는다.
// 기존 필드(workspace_id·workspace_name·role_id·role_name)는 그대로다.
type EffectiveWorkspaceRole struct {
	WorkspaceID   uint   `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	RoleID        uint   `json:"role_id"`
	RoleName      string `json:"role_name"`

	// Source 이 역할의 출처 (direct|group). 같은 역할이 직접·그룹 둘 다면 direct
	Source string `json:"source,omitempty" gorm:"-"`
	// GroupID Source=group 일 때 역할을 준 그룹 (여러 그룹이면 group_id 가 가장 작은 것)
	GroupID *uint `json:"group_id,omitempty" gorm:"-"`

	// 아래는 목록 응답에서만 채운다 (워크스페이스 단위 값을 그 워크스페이스의 모든 행에 같이 싣는다)
	// IsEffective 이 행이 그 워크스페이스에서 실제로 쓰이는 역할(발급·진단 기준)인지
	IsEffective *bool `json:"is_effective,omitempty" gorm:"-"`
	// EffectiveRole 그 워크스페이스의 유효 역할 (직접 배정 우선, 없으면 그룹 역할 중 role_id 가 가장 작은 것)
	EffectiveRole *WorkspaceRoleRef `json:"effective_role,omitempty" gorm:"-"`
	// GroupRoles 그 워크스페이스에 대해 소속 그룹들이 주는 역할 전부
	GroupRoles []GroupWorkspaceRoleRef `json:"group_roles,omitempty" gorm:"-"`
	// Overridden 직접 배정이 있고 그룹 역할과 다르면 true (그룹 역할이 적용되지 않음)
	Overridden *bool `json:"overridden,omitempty" gorm:"-"`
}

// WorkspaceRoleRef 결정된 워크스페이스 역할 (목록 응답용, snake_case)
type WorkspaceRoleRef struct {
	RoleID   uint   `json:"role_id"`
	RoleName string `json:"role_name"`
	Source   string `json:"source"`
	GroupID  *uint  `json:"group_id,omitempty"`
}

// GroupWorkspaceRoleRef 그룹이 워크스페이스에 주는 역할
type GroupWorkspaceRoleRef struct {
	GroupID   uint   `json:"group_id"`
	GroupName string `json:"group_name"`
	RoleID    uint   `json:"role_id"`
	RoleName  string `json:"role_name"`
}

// UsedWorkspaceRole 임시자격증명·진단에 사용한 워크스페이스 역할 (camelCase 응답용)
type UsedWorkspaceRole struct {
	RoleID   uint   `json:"roleId"`
	RoleName string `json:"roleName"`
	Source   string `json:"source"`
	GroupID  *uint  `json:"groupId,omitempty"`
}

// ToUsedRole 단건 결정 결과를 응답용 값으로 바꾼다 (nil 이면 nil)
func (e *EffectiveWorkspaceRole) ToUsedRole() *UsedWorkspaceRole {
	if e == nil {
		return nil
	}
	return &UsedWorkspaceRole{RoleID: e.RoleID, RoleName: e.RoleName, Source: e.Source, GroupID: e.GroupID}
}

// WorkspaceRoleResolution 한 사용자·워크스페이스의 역할 결정 결과
type WorkspaceRoleResolution struct {
	WorkspaceID   uint
	WorkspaceName string
	// DirectRoles 직접 배정 (role_id 오름차순). 정상은 0~1개, 기존 데이터는 여러 개일 수 있다
	DirectRoles []WorkspaceRoleRef
	// GroupRoles 그룹 역할 (role_id, group_id 오름차순)
	GroupRoles []GroupWorkspaceRoleRef
	// Effective 유효 역할 (없으면 nil)
	Effective *EffectiveWorkspaceRole
	// Overridden 직접 배정이 있고, 그와 다른 그룹 역할이 있음
	Overridden bool
}
