package service

import (
	"fmt"

	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/repository"
	"github.com/m-cmp/mc-iam-manager/util"
	"gorm.io/gorm"
)

// RoleService 역할 관리 서비스
type RoleService struct {
	db             *gorm.DB
	roleRepository *repository.RoleRepository
}

// NewRoleService 새 RoleService 인스턴스 생성
func NewRoleService(db *gorm.DB) *RoleService {
	return &RoleService{
		db:             db,
		roleRepository: repository.NewRoleRepository(db),
	}
}

// List 역할 목록 조회
func (s *RoleService) ListRoles(req *model.RoleFilterRequest) ([]*model.RoleMaster, error) {
	return s.roleRepository.FindRoles(req)
}

// GetByID ID로 역할 조회
func (s *RoleService) GetRoleByID(roleId uint, roleType constants.IAMRoleType) (*model.RoleMaster, error) {
	return s.roleRepository.FindRoleByRoleID(roleId, roleType)
}

// GetByName Name으로 역할 조회
func (s *RoleService) GetRoleByName(roleName string, roleType constants.IAMRoleType) (*model.RoleMaster, error) {
	return s.roleRepository.FindRoleByRoleName(roleName, roleType)
}

// ExistRoleByName 이름으로 역할 존재 여부 확인 (RoleMaster와 RoleSub를 통해)
func (s *RoleService) ExistRoleByName(roleName string, roleType constants.IAMRoleType) (bool, error) {
	return s.roleRepository.ExistRoleByName(roleName, roleType)
}

// CreateRoleWithSubs 역할과 서브 타입을 함께 생성합니다.
func (s *RoleService) CreateRoleWithSubs(role *model.RoleMaster, roleSubs []model.RoleSub) (*model.RoleMaster, error) {
	return s.roleRepository.CreateRoleWithSubs(role, roleSubs)
}

// CreateRoleWithAllDependencies 역할과 모든 의존성을 트랜잭션으로 함께 생성
func (s *RoleService) CreateRoleWithAllDependencies(
	role *model.RoleMaster,
	roleSubs []model.RoleSub,
	//menuIDs []uint,
	menuIDs []string,
	cspRoles []model.CreateCspRoleRequest,
	description string,
) (*model.RoleMaster, error) {
	var createdRole *model.RoleMaster

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 1. 역할과 서브타입 생성
		roleResult, err := s.roleRepository.CreateRoleWithSubsWithTx(tx, role, roleSubs)
		if err != nil {
			return fmt.Errorf("역할과 서브타입 생성 실패: %w", err)
		}
		createdRole = roleResult

		// 2. 메뉴 매핑 생성 (Platform 역할인 경우)
		if len(menuIDs) > 0 {
			hasPlatformRole := false
			for _, roleSub := range roleSubs {
				if roleSub.RoleType == constants.RoleTypePlatform {
					hasPlatformRole = true
					break
				}
			}

			if hasPlatformRole {
				mappings := make([]*model.RoleMenuMapping, 0)
				for _, menuID := range menuIDs {
					mapping := &model.RoleMenuMapping{
						RoleID: createdRole.ID,
						MenuID: menuID,
					}
					mappings = append(mappings, mapping)
				}

				// 메뉴 매핑 생성 (트랜잭션 내에서)
				for _, mapping := range mappings {
					if err := tx.Create(mapping).Error; err != nil {
						return fmt.Errorf("메뉴 매핑 생성 실패: %w", err)
					}
				}
			}
		}

		// 3. CSP 역할 매핑 생성 (CSP 역할은 이미 생성되어 있다고 가정)
		if len(cspRoles) > 0 {
			for _, cspRole := range cspRoles {
				// CSP 역할 ID를 uint로 변환
				cspRoleID, err := util.StringToUint(cspRole.ID)
				if err != nil {
					return fmt.Errorf("잘못된 CSP 역할 ID 형식: %w", err)
				}

				// 매핑 생성 (트랜잭션 내에서)
				mapping := &model.RoleMasterCspRoleMapping{
					RoleID:      createdRole.ID,
					CspRoleID:   cspRoleID,
					AuthMethod:  constants.AuthMethodOIDC,
					Description: description,
				}

				if err := tx.Create(mapping).Error; err != nil {
					return fmt.Errorf("CSP 역할 매핑 생성 실패: %w", err)
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return createdRole, nil
}

// UpdateRoleWithSubs 역할과 역할 서브 타입들을 함께 수정
func (s *RoleService) UpdateRoleWithSubs(role model.RoleMaster, roleTypes []constants.IAMRoleType) (*model.RoleMaster, error) {
	return s.roleRepository.UpdateRoleWithSubs(role, roleTypes)
}

// UpdateRoleWithSubsWithTx 트랜잭션 내에서 역할과 역할 서브 타입들을 함께 수정
func (s *RoleService) UpdateRoleWithSubsWithTx(tx *gorm.DB, role model.RoleMaster, roleTypes []constants.IAMRoleType) (*model.RoleMaster, error) {
	return s.roleRepository.UpdateRoleWithSubsWithTx(tx, role, roleTypes)
}

// DeleteRoleSubs 서브 타입을 삭제( master 역할은 Role 삭제에서 처리)
func (s *RoleService) DeleteRoleSubs(roleID uint, roleType []constants.IAMRoleType) error {
	return s.roleRepository.DeleteRoleSubs(roleID, roleType)
}

// DeleteRoleSubsWithTx 트랜잭션 내에서 서브 타입을 삭제
func (s *RoleService) DeleteRoleSubsWithTx(tx *gorm.DB, roleID uint, roleType []constants.IAMRoleType) error {
	return s.roleRepository.DeleteRoleSubsWithTx(tx, roleID, roleType)
}

// DeleteRoleMaster 역할 마스터 삭제
func (s *RoleService) DeleteRoleMaster(roleID uint) error {
	return s.roleRepository.DeleteRoleMaster(roleID)
}

// DeleteRoleWithSubsAndMappings 역할과 관련된 모든 데이터를 트랜잭션으로 삭제
func (s *RoleService) DeleteRoleWithSubsAndMappings(roleID uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 1. 역할 서브 타입들 삭제
		if err := s.roleRepository.DeleteRoleSubsWithTx(tx, roleID, []constants.IAMRoleType{
			constants.RoleTypePlatform,
			constants.RoleTypeWorkspace,
			constants.RoleTypeCSP,
		}); err != nil {
			return fmt.Errorf("역할 서브 타입 삭제 실패: %w", err)
		}

		// 2. CSP 역할 매핑 삭제
		if err := s.roleRepository.DeleteRoleCspRoleMappings(roleID); err != nil {
			return fmt.Errorf("CSP 역할 매핑 삭제 실패: %w", err)
		}

		// 3. 역할 마스터 삭제
		if err := tx.Delete(&model.RoleMaster{}, roleID).Error; err != nil {
			return fmt.Errorf("역할 마스터 삭제 실패: %w", err)
		}

		return nil
	})
}

// AssignPlatformRole 플랫폼 역할 할당
func (s *RoleService) AssignPlatformRole(userID, roleID uint) error {
	// 1. 역할이 존재하는지 확인
	role, err := s.roleRepository.FindRoleByRoleID(roleID, constants.RoleTypePlatform)
	if err != nil {
		return fmt.Errorf("역할 조회 실패: %w", err)
	}
	if role == nil {
		return fmt.Errorf("역할을 찾을 수 없습니다")
	}

	// 2. 역할이 platform 타입인지 확인
	isPlatformRole := false
	for _, sub := range role.RoleSubs {
		if sub.RoleType == constants.RoleTypePlatform {
			isPlatformRole = true
			break
		}
	}
	if !isPlatformRole {
		return fmt.Errorf("플랫폼 역할이 아닙니다")
	}

	// 3. 역할 할당
	return s.roleRepository.AssignPlatformRole(userID, roleID)
}

// AssignWorkspaceRole 워크스페이스 역할 할당
func (s *RoleService) AssignWorkspaceRole(userID, workspaceID, roleID uint) error {
	// 1. 역할이 존재하는지 확인
	role, err := s.roleRepository.FindRoleByRoleID(roleID, constants.RoleTypeWorkspace)
	if err != nil {
		return fmt.Errorf("역할 조회 실패: %w", err)
	}
	if role == nil {
		return fmt.Errorf("역할을 찾을 수 없습니다")
	}

	// 2. 역할이 workspace 타입인지 확인
	isWorkspaceRole := false
	for _, sub := range role.RoleSubs {
		if sub.RoleType == constants.RoleTypeWorkspace {
			isWorkspaceRole = true
			break
		}
	}
	if !isWorkspaceRole {
		return fmt.Errorf("워크스페이스 역할이 아닙니다")
	}

	// 3. 역할 할당
	return s.roleRepository.AssignWorkspaceRole(userID, workspaceID, roleID)
}

// AssignRole 역할 할당 (플랫폼/워크스페이스)
func (s *RoleService) AssignRole(userID, workspaceID, roleID uint) error {
	// 1. 역할이 존재하는지 확인
	role, err := s.roleRepository.FindRoleByRoleID(roleID, "")
	if err != nil {
		return fmt.Errorf("역할 조회 실패: %w", err)
	}
	if role == nil {
		return fmt.Errorf("역할을 찾을 수 없습니다")
	}

	// 2. 역할 타입 확인
	isWorkspaceRole := false
	isPlatformRole := false
	for _, sub := range role.RoleSubs {
		if sub.RoleType == constants.RoleTypeWorkspace {
			isWorkspaceRole = true
		}
		if sub.RoleType == constants.RoleTypePlatform {
			isPlatformRole = true
		}
	}

	// 3. 역할 타입에 따라 할당
	if isWorkspaceRole {
		if workspaceID == 0 {
			return fmt.Errorf("워크스페이스 역할 할당을 위해 워크스페이스 ID가 필요합니다")
		}
		return s.roleRepository.AssignWorkspaceRole(userID, workspaceID, roleID)
	} else if isPlatformRole {
		return s.roleRepository.AssignPlatformRole(userID, roleID)
	} else {
		return fmt.Errorf("지원하지 않는 역할 타입입니다")
	}
}

// RemovePlatformRole 플랫폼 역할 제거
func (s *RoleService) RemovePlatformRole(userID, roleID uint) error {
	return s.roleRepository.RemovePlatformRole(userID, roleID)
}

// RemoveWorkspaceRole 워크스페이스 역할 제거
func (s *RoleService) RemoveWorkspaceRole(userID, workspaceID, roleID uint) error {
	return s.roleRepository.RemoveWorkspaceRole(userID, workspaceID, roleID)
}

// GetUserWorkspaceRoles 사용자의 워크스페이스 역할 목록 조회
func (s *RoleService) GetUserWorkspaceRoles(userID, workspaceID uint) ([]model.UserWorkspaceRole, error) {
	return s.roleRepository.FindUserWorkspaceRoles(userID, workspaceID)
}

// GetUserPlatformRoles 사용자의 플랫폼 역할 목록 조회
func (s *RoleService) GetUserPlatformRoles(userID uint) ([]model.RoleMaster, error) {
	return s.roleRepository.FindUserPlatformRoles(userID)
}

// 있으면 update, 없으면 insert
func (s *RoleService) CreateRoleCspRoleMapping(req *model.CreateRoleMasterCspRoleMappingRequest) error {

	// 매핑 생성
	err := s.roleRepository.CreateRoleCspRoleMapping(req)
	if err != nil {
		return err
	}

	return nil
}

// CreateWorkspaceRoleCspRoleMapping 워크스페이스 역할-CSP 역할 매핑 생성
func (s *RoleService) CreateWorkspaceRoleCspRoleMapping(mapping model.CreateCspRolesMappingRequest) error {
	roleIDInt, err := util.StringToUint(mapping.RoleID)
	if err != nil {
		return fmt.Errorf("잘못된 역할 ID 형식: %w", err)
	}

	// 1. 워크스페이스 역할이 존재하는지 확인
	workspaceRole, err := s.roleRepository.FindRoleByRoleID(roleIDInt, constants.RoleTypeWorkspace)
	if err != nil {
		return fmt.Errorf("워크스페이스 역할 조회 실패: %w", err)
	}
	if workspaceRole == nil {
		return fmt.Errorf("워크스페이스 역할을 찾을 수 없습니다")
	}

	// 2. CSP 역할이 존재하는지 확인
	cspRole, err := s.roleRepository.FindRoleByRoleID(roleIDInt, constants.RoleTypeCSP)
	if err != nil {
		return fmt.Errorf("CSP 역할 조회 실패: %w", err)
	}
	if cspRole == nil {
		return fmt.Errorf("CSP 역할을 찾을 수 없습니다")
	}

	// 3. 매핑 생성
	err = s.roleRepository.CreateWorkspaceRoleCspRoleMapping(&mapping)
	if err != nil {
		return fmt.Errorf("매핑 생성 실패: %w", err)
	}

	return err
}

// DeleteWorkspaceRoleCspRoleMapping 워크스페이스 역할-CSP 역할 매핑 삭제
func (s *RoleService) DeleteRoleCspRoleMapping(roleID uint, cspRoleID uint, cspType constants.AuthMethod) error {
	return s.roleRepository.DeleteRoleCspRoleMapping(roleID, cspRoleID, cspType)
}

// 해당 Role 과 매핑된 모든 csp 역할 매핑 삭제 ( csp 역할을 삭제하는 것은 아님)
func (s *RoleService) DeleteRoleCspRoleMappingsByRoleId(roleID uint) error {
	return s.roleRepository.DeleteRoleCspRoleMappings(roleID)
}

// ListWorkspaceRoleCspRoleMappings 워크스페이스 역할-CSP 역할 매핑 목록 조회
func (s *RoleService) ListWorkspaceRoleCspRoleMappings(req *model.RoleMasterCspRoleMappingRequest) ([]*model.RoleMasterCspRoleMapping, error) {
	return s.roleRepository.FindWorkspaceRoleCspRoleMappings(req)
}

// ListWorkspaceRoleCspRoleMappings 워크스페이스 역할-CSP 역할 매핑 목록 조회
func (s *RoleService) ListRoleCspRoleMappings(req *model.RoleMasterCspRoleMappingRequest) ([]*model.RoleMasterCspRoleMapping, error) {
	return s.roleRepository.FindRoleMasterCspRoleMappings(req)
}

func (s *RoleService) GetCspRoleByID(cspRoleId uint) (*model.CspRole, error) {
	return s.roleRepository.FindCspRoleById(cspRoleId)
}

func (s *RoleService) GetCspRoleByName(cspRoleName string) (*model.CspRole, error) {
	return s.roleRepository.FindCspRoleByName(cspRoleName)
}

// GetWorkspaceRoleCspRoleMappings 역할-CSP 역할 매핑 목록 조회. 1개 master Role에 여러개의 csp Role이 나온다.
func (s *RoleService) GetRoleCspRoleMappings(req *model.RoleMasterCspRoleMappingRequest) (*model.RoleMasterCspRoleMapping, error) {
	// 단건 return
	mappings, err := s.roleRepository.FindRoleMasterCspRoleMappings(req)
	if err != nil {
		return nil, err
	}

	if len(mappings) == 0 {
		return nil, nil
	}

	return mappings[0], nil
}

// GetUsersByWorkspaceID 워크스페이스에 속한 사용자 목록을 조회합니다.
func (s *RoleService) ListWorkspaceUsersAndRoles(req model.WorkspaceFilterRequest) ([]*model.WorkspaceWithUsersAndRoles, error) {
	// 워크스페이스 존재 여부 확인
	workspaceUsers, err := s.roleRepository.FindWorkspaceWithUsersRoles(req)
	if err != nil {
		return nil, err
	}

	return workspaceUsers, nil
}

// GetUsersAndRolesByWorkspaceID 워크스페이스에 속한 사용자와 역할 조회 : 사용자기준
func (s *RoleService) ListUsersAndRolesWithWorkspaces(req model.WorkspaceFilterRequest) ([]*model.UserWorkspaceRole, error) {

	userRoleWorkspaces, err := s.roleRepository.FindUsersAndRolesWithWorkspaces(req)
	if err != nil {
		return nil, err
	}

	return userRoleWorkspaces, nil
}

// GetWorkspaceRoles 워크스페이스의 모든 역할 목록 조회 Role만
func (s *RoleService) ListWorkspaceRoles(req *model.RoleFilterRequest) ([]*model.RoleMaster, error) {
	roles, err := s.roleRepository.FindRoles(req)
	if err != nil {
		return nil, err
	}

	return roles, nil
}

// IsAssignedPlatformRole 사용자에게 특정 플랫폼 역할이 할당되어 있는지 확인
func (s *RoleService) IsAssignedPlatformRole(userID uint, roleID uint) (bool, error) {
	return s.roleRepository.IsAssignedPlatformRole(userID, roleID)
}
func (s *RoleService) IsAssignedWorkspaceRole(userID uint, roleID uint) (bool, error) {
	return s.roleRepository.IsAssignedWorkspaceRole(userID, roleID)
}
func (s *RoleService) IsAssignedRole(userID uint, roleID uint, roleType constants.IAMRoleType) (bool, error) {
	return s.roleRepository.IsAssignedRole(userID, roleID, roleType)
}

// AddRoleSub RoleSub만 추가 (중복 체크 포함)
func (s *RoleService) AddRoleSub(roleID uint, roleSub *model.RoleSub) error {
	return s.roleRepository.CreateRoleSub(roleID, roleSub)
}

func (s *RoleService) AddCspRolesMapping(req *model.CreateRoleMasterCspRoleMappingRequest) error {
	return s.roleRepository.CreateRoleCspRoleMapping(req)
}

// RoleMaster와 연결된 것들. 사용자, csp역할, 워크스페이스 역할 모두 조회
func (s *RoleService) ListRoleMasterMappings(req *model.FilterRoleMasterMappingRequest) ([]*model.RoleMasterMapping, error) {
	return s.roleRepository.FindRoleMasterMappings(req)
}

// RoleMaster와 연결된 것들. 사용자, csp역할, 워크스페이스 역할 모두 조회
func (s *RoleService) GetRoleMasterMappings(req *model.FilterRoleMasterMappingRequest) (*model.RoleMasterMapping, error) {
	mappings, err := s.roleRepository.FindRoleMasterMappings(req)
	if err != nil {
		return nil, err
	}

	if len(mappings) == 0 {
		return nil, nil
	}

	return mappings[0], nil
}
