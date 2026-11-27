package service

import (
	"errors"
	"fmt"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/repository"
	"gorm.io/gorm"
)

// 직접 배정은 (사용자, 워크스페이스)당 역할 1개다 (IAM-BUG-050, 2026-11-26 결정).
// 역할 할당 API·초대 수락·초대 승인이 assignDirectWorkspaceRole 하나로 직접 배정을 쓴다.

// ErrWorkspaceRoleConflict 다른 역할이 이미 직접 배정돼 있음 (교체하려면 replace)
var ErrWorkspaceRoleConflict = errors.New("user already has a different role in this workspace")

// WorkspaceRoleConflictError 다른 역할이 이미 있을 때 현재 역할을 함께 돌려준다 (errors.Is → ErrWorkspaceRoleConflict)
type WorkspaceRoleConflictError struct {
	UserID, WorkspaceID uint
	CurrentRoleID       uint
	CurrentRoleName     string
}

func (e *WorkspaceRoleConflictError) Error() string {
	return fmt.Sprintf("%s: user %d, workspace %d, current role %s(id=%d) — send replace=true to replace it",
		ErrWorkspaceRoleConflict.Error(), e.UserID, e.WorkspaceID, e.CurrentRoleName, e.CurrentRoleID)
}

func (e *WorkspaceRoleConflictError) Is(target error) bool { return target == ErrWorkspaceRoleConflict }

// 직접 배정 결과
const (
	WorkspaceRoleActionCreated  = "created"
	WorkspaceRoleActionReplaced = "replaced"
)

// WorkspaceRoleAssignResult 직접 배정 결과 (응답용)
type WorkspaceRoleAssignResult struct {
	Action         string   `json:"action"` // created|replaced
	RoleID         uint     `json:"roleId"`
	RoleName       string   `json:"roleName"`
	PreviousRoleID *uint    `json:"previousRoleId,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// assignDirectWorkspaceRole tx 안에서 직접 배정을 쓴다. 역할(role)은 호출 전에 workspace 유형으로 검증돼 있어야 한다.
//   - 직접 배정 없음: 추가 (created)
//   - 현재 역할이 요청 역할과 같음: ErrWorkspaceRoleAlreadyAssigned (기존 중복 데이터는 replace=true 면 1개로 정리)
//   - 다른 역할 있음, replace=false: *WorkspaceRoleConflictError (현재 역할 = role_id 가 가장 작은 직접 배정)
//   - 다른 역할 있음, replace=true: 그 워크스페이스의 직접 배정을 모두 지우고 새 역할 1개로 (replaced)
//
// 기존 데이터에 직접 배정이 여러 개 남아 있어도 replace 후에는 1개만 남는다.
func assignDirectWorkspaceRole(tx *gorm.DB, userID, workspaceID uint, role *model.RoleMaster, replace bool) (*WorkspaceRoleAssignResult, error) {
	var current []model.UserWorkspaceRole
	if err := tx.Where("user_id = ? AND workspace_id = ?", userID, workspaceID).
		Order("role_id ASC").Find(&current).Error; err != nil {
		return nil, fmt.Errorf("failed to check workspace role assignment: %w", err)
	}

	if len(current) == 0 {
		if err := tx.Create(&model.UserWorkspaceRole{UserID: userID, WorkspaceID: workspaceID, RoleID: role.ID}).Error; err != nil {
			return nil, fmt.Errorf("failed to assign workspace role: %w", err)
		}
		return &WorkspaceRoleAssignResult{Action: WorkspaceRoleActionCreated, RoleID: role.ID, RoleName: role.Name}, nil
	}

	prev := current[0].RoleID // 현재 유효 직접 배정 (role_id 가 가장 작은 것)
	if prev == role.ID && (len(current) == 1 || !replace) {
		return nil, fmt.Errorf("%w: user %d, workspace %d, role %s(id=%d)", ErrWorkspaceRoleAlreadyAssigned, userID, workspaceID, role.Name, role.ID)
	}
	if !replace {
		var prevRole model.RoleMaster
		_ = tx.Select("id", "name").First(&prevRole, prev).Error
		return nil, &WorkspaceRoleConflictError{UserID: userID, WorkspaceID: workspaceID, CurrentRoleID: prev, CurrentRoleName: prevRole.Name}
	}

	// replace: 기존 중복 데이터(여러 개)도 이 시점에 1개로 정리된다
	if err := tx.Where("user_id = ? AND workspace_id = ?", userID, workspaceID).Delete(&model.UserWorkspaceRole{}).Error; err != nil {
		return nil, fmt.Errorf("failed to remove current workspace role: %w", err)
	}
	if err := tx.Create(&model.UserWorkspaceRole{UserID: userID, WorkspaceID: workspaceID, RoleID: role.ID}).Error; err != nil {
		return nil, fmt.Errorf("failed to assign workspace role: %w", err)
	}
	return &WorkspaceRoleAssignResult{Action: WorkspaceRoleActionReplaced, RoleID: role.ID, RoleName: role.Name, PreviousRoleID: &prev}, nil
}

// directOverrideWarnings 직접 배정 roleID 가 그 워크스페이스의 다른 그룹 역할을 대신하면 경고를 만든다 (막지 않음)
func directOverrideWarnings(db *gorm.DB, userID, workspaceID, roleID uint, roleName string) []string {
	res, err := repository.ResolveUserWorkspaceRole(db, userID, workspaceID)
	if err != nil || res == nil {
		return nil
	}
	var warnings []string
	for _, g := range res.GroupRoles {
		if g.RoleID != roleID {
			warnings = append(warnings, fmt.Sprintf("그룹 %s(id=%d) 의 역할 %s 대신 직접 배정 %s 가 적용됩니다", g.GroupName, g.GroupID, g.RoleName, roleName))
		}
	}
	return warnings
}
