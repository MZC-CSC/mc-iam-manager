package service

import (
	"errors"
	"fmt"

	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/repository"
	"gorm.io/gorm"
)

// WorkspaceInvitationService 워크스페이스 초대 서비스
type WorkspaceInvitationService struct {
	db                *gorm.DB
	invitationRepo    *repository.WorkspaceInvitationRepository
	workspaceRepo     *repository.WorkspaceRepository
	userRepo          *repository.UserRepository
	workspaceRoleRepo *repository.WorkspaceRoleRepository
}

// NewWorkspaceInvitationService 새 WorkspaceInvitationService 인스턴스 생성
func NewWorkspaceInvitationService(db *gorm.DB) *WorkspaceInvitationService {
	return &WorkspaceInvitationService{
		db:                db,
		invitationRepo:    repository.NewWorkspaceInvitationRepository(db),
		workspaceRepo:     repository.NewWorkspaceRepository(db),
		userRepo:          repository.NewUserRepository(db),
		workspaceRoleRepo: repository.NewWorkspaceRoleRepository(db),
	}
}

// SendInvitation 워크스페이스 초대 발송
func (s *WorkspaceInvitationService) SendInvitation(workspaceID, inviterUserID, inviteeUserID uint, roleID *uint) (*model.WorkspaceInvitation, error) {
	// 워크스페이스 존재 확인
	ws, err := s.workspaceRepo.FindWorkspaceByID(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("workspace not found: %w", err)
	}
	if ws == nil {
		return nil, fmt.Errorf("workspace not found")
	}

	// 초대 대상 사용자 존재 확인
	if _, err := s.userRepo.FindUserByID(inviteeUserID); err != nil {
		return nil, fmt.Errorf("invitee user not found: %w", err)
	}

	// 이미 워크스페이스 멤버인지 확인
	existingRoles, err := s.workspaceRoleRepo.GetUserRoles(inviteeUserID, workspaceID)
	if err == nil && len(existingRoles) > 0 {
		return nil, errors.New("user is already a member of this workspace")
	}

	// 중복 PENDING 초대 확인
	hasPending, err := s.invitationRepo.HasPendingInvitation(workspaceID, inviteeUserID)
	if err != nil {
		return nil, err
	}
	if hasPending {
		return nil, errors.New("pending invitation already exists for this user")
	}

	invitation := &model.WorkspaceInvitation{
		WorkspaceID:   workspaceID,
		InviterUserID: inviterUserID,
		InviteeUserID: inviteeUserID,
		RoleID:        roleID,
		Status:        model.InvitationStatusPending,
	}

	if err := s.invitationRepo.Create(invitation); err != nil {
		return nil, err
	}
	return invitation, nil
}

// ListWorkspaceInvitations 워크스페이스 초대 목록 조회
func (s *WorkspaceInvitationService) ListWorkspaceInvitations(workspaceID uint, status string) ([]model.WorkspaceInvitation, error) {
	return s.invitationRepo.ListByWorkspace(workspaceID, status)
}

// ListMyInvitations 내 초대 목록 조회
func (s *WorkspaceInvitationService) ListMyInvitations(userID uint) ([]model.WorkspaceInvitation, error) {
	return s.invitationRepo.ListByInvitee(userID)
}

// AcceptInvitation 초대 수락 (초대받은 사용자)
func (s *WorkspaceInvitationService) AcceptInvitation(invitationID, userID uint) error {
	invitation, err := s.invitationRepo.FindByID(invitationID)
	if err != nil {
		return fmt.Errorf("invitation not found: %w", err)
	}
	if invitation.InviteeUserID != userID {
		return errors.New("forbidden: not your invitation")
	}
	if invitation.Status != model.InvitationStatusPending {
		return fmt.Errorf("invitation is not in PENDING state (current: %s)", invitation.Status)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 워크스페이스 멤버로 등록 — 역할 할당 API 와 같은 규칙(직접 배정 1개, 이미 있으면 409, 교체 없음)
		if err := assignInvitationRole(tx, userID, invitation); err != nil {
			return err
		}
		// 초대 상태 업데이트
		return tx.Model(&model.WorkspaceInvitation{}).
			Where("id = ?", invitationID).
			Update("status", model.InvitationStatusAccepted).Error
	})
}

// RejectInvitation 초대 거절 (초대받은 사용자)
func (s *WorkspaceInvitationService) RejectInvitation(invitationID, userID uint) error {
	invitation, err := s.invitationRepo.FindByID(invitationID)
	if err != nil {
		return fmt.Errorf("invitation not found: %w", err)
	}
	if invitation.InviteeUserID != userID {
		return errors.New("forbidden: not your invitation")
	}
	if invitation.Status != model.InvitationStatusPending {
		return fmt.Errorf("invitation is not in PENDING state (current: %s)", invitation.Status)
	}
	return s.invitationRepo.UpdateStatus(invitationID, model.InvitationStatusRejected)
}

// ListPendingApprovals 관리자: 승인 대기 초대 목록 조회
func (s *WorkspaceInvitationService) ListPendingApprovals(status string) ([]model.WorkspaceInvitation, error) {
	return s.invitationRepo.ListByStatus(status)
}

// ApproveInvitation 관리자: 초대 승인
func (s *WorkspaceInvitationService) ApproveInvitation(invitationID uint) error {
	invitation, err := s.invitationRepo.FindByID(invitationID)
	if err != nil {
		return fmt.Errorf("invitation not found: %w", err)
	}
	if invitation.Status != model.InvitationStatusPendingApproval {
		return fmt.Errorf("invitation is not in PENDING_APPROVAL state (current: %s)", invitation.Status)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 역할 할당 API 와 같은 규칙(직접 배정 1개, 이미 있으면 409, 교체 없음)
		if err := assignInvitationRole(tx, invitation.InviteeUserID, invitation); err != nil {
			return err
		}
		return tx.Model(&model.WorkspaceInvitation{}).
			Where("id = ?", invitationID).
			Update("status", model.InvitationStatusAccepted).Error
	})
}

// RejectInvitationByAdmin 관리자: 초대 거절
func (s *WorkspaceInvitationService) RejectInvitationByAdmin(invitationID uint) error {
	invitation, err := s.invitationRepo.FindByID(invitationID)
	if err != nil {
		return fmt.Errorf("invitation not found: %w", err)
	}
	if invitation.Status != model.InvitationStatusPendingApproval {
		return fmt.Errorf("invitation is not in PENDING_APPROVAL state (current: %s)", invitation.Status)
	}
	return s.invitationRepo.UpdateStatus(invitationID, model.InvitationStatusRejected)
}

// assignInvitationRole 초대의 역할을 직접 배정한다. 역할이 없는 초대는 아무것도 쓰지 않는다.
// 사용자가 그 워크스페이스에 이미 역할이 있으면 ErrWorkspaceRoleAlreadyAssigned(같은 역할) 또는
// *WorkspaceRoleConflictError(다른 역할) 이고, 초대 상태는 바뀌지 않는다. 역할 교체는 역할 할당 API 로 한다.
func assignInvitationRole(tx *gorm.DB, userID uint, invitation *model.WorkspaceInvitation) error {
	if invitation.RoleID == nil {
		return nil
	}
	var role model.RoleMaster
	if err := tx.First(&role, *invitation.RoleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: id=%d", ErrRoleNotFound, *invitation.RoleID)
		}
		return fmt.Errorf("failed to find role %d: %w", *invitation.RoleID, err)
	}
	_, err := assignDirectWorkspaceRole(tx, userID, invitation.WorkspaceID, &role, false)
	return err
}
