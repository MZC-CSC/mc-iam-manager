package repository

import (
	"context"
	"errors"

	"github.com/m-cmp/mc-iam-manager/model"
	"gorm.io/gorm"
)

var (
	ErrCspMappingNotFound      = errors.New("CSP 역할 매핑을 찾을 수 없습니다")
	ErrCspMappingAlreadyExists = errors.New("이미 존재하는 CSP 역할 매핑입니다")
)

// CspMappingRepository CSP 매핑 레포지토리
type CspMappingRepository struct {
	db *gorm.DB
}

// NewCspMappingRepository 새 CspMappingRepository 인스턴스 생성
func NewCspMappingRepository(db *gorm.DB) *CspMappingRepository {
	return &CspMappingRepository{db: db}
}

// FindCspRoleMappingsByWorkspaceRoleIDAndCspType 워크스페이스 역할 ID와 CSP 타입으로 CSP 역할 매핑 조회
func (r *CspMappingRepository) FindCspRoleMappingsByRoleIDAndCspType(roleID uint, cspType string) (*model.RoleMasterCspRoleMapping, error) {
	var mappings []*model.RoleMasterCspRoleMapping
	err := r.db.
		Joins("JOIN mcmp_role_csp_roles ON mcmp_role_csp_roles.id = mcmp_role_csp_role_mappings.csp_role_id").
		Where("mcmp_role_csp_role_mappings.role_id = ? AND mcmp_role_csp_roles.csp_type = ?", roleID, cspType).
		Find(&mappings).Error
	if err != nil {
		return nil, err
	}

	if len(mappings) == 0 {
		return nil, nil
	}

	// 첫 번째 매핑을 반환하고 CspRoles 배열을 채움
	targetMapping := mappings[0]

	// CspRoles 배열을 채우기 위해 CspRole 정보를 조회
	var cspRole model.CspRole
	if err := r.db.Where("id = ?", targetMapping.CspRoleID).First(&cspRole).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			return nil, err
		}
		// CSP 역할이 없으면 빈 배열로 설정
		targetMapping.CspRoles = []*model.CspRole{}
	} else {
		targetMapping.CspRoles = []*model.CspRole{&cspRole}
	}

	return targetMapping, nil
}

// // CreateWorkspaceRoleCspRoleMapping 워크스페이스 역할과 CSP 역할 매핑 생성
// func (r *CspMappingRepository) CreateWorkspaceRoleCspRoleMapping(ctx context.Context, mapping *model.RoleMasterCspRoleMapping) error {
// 	result := r.db.WithContext(ctx).Create(mapping)
// 	if result.Error != nil {
// 		if r.db.WithContext(ctx).Where(
// 			"workspace_role_id = ? AND auth_method = ? AND csp_role_id = ?",
// 			mapping.RoleID,
// 			mapping.AuthMethod,
// 			mapping.CspRoleID,
// 		).First(&model.RoleMasterCspRoleMapping{}).Error == nil {
// 			return ErrCspMappingAlreadyExists
// 		}
// 		return result.Error
// 	}
// 	return nil
// }

// DeleteWorkspaceRoleCspRoleMapping 워크스페이스 역할과 CSP 역할 매핑 삭제
func (r *CspMappingRepository) DeleteWorkspaceRoleCspRoleMapping(ctx context.Context, workspaceRoleID uint, cspType string, cspRoleID string) error {
	result := r.db.WithContext(ctx).Where(
		"workspace_role_id = ? AND csp_type = ? AND csp_role_id = ?",
		workspaceRoleID,
		cspType,
		cspRoleID,
	).Delete(&model.RoleMasterCspRoleMapping{})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrCspMappingNotFound
	}
	return nil
}

// UpdateWorkspaceRoleCspRoleMapping 워크스페이스 역할 - CSP 역할 매핑 수정
func (r *CspMappingRepository) UpdateWorkspaceRoleCspRoleMapping(mapping *model.RoleMasterCspRoleMapping) error {
	return r.db.Save(mapping).Error
}

// GetByCspID CSP ID로 매핑 조회
func (r *CspMappingRepository) GetCspRoleMappingByCspID(cspID uint) ([]*model.RoleMasterCspRoleMapping, error) {
	var mappings []*model.RoleMasterCspRoleMapping
	err := r.db.Where("csp_id = ?", cspID).Find(&mappings).Error
	if err != nil {
		return nil, err
	}
	return mappings, err
}

// GetByPermissionID 권한 ID로 매핑 조회
func (r *CspMappingRepository) GetCspRoleMappingByPermissionID(permissionID uint) ([]*model.RoleMasterCspRoleMapping, error) {
	var mappings []*model.RoleMasterCspRoleMapping
	err := r.db.Where("permission_id = ?", permissionID).Find(&mappings).Error
	if err != nil {
		return nil, err
	}
	return mappings, err
}

// DeleteByCspID CSP ID로 매핑 삭제
func (r *CspMappingRepository) DeleteCspRoleMappingByCspID(cspID uint) error {
	return r.db.Where("csp_id = ?", cspID).Delete(&model.RoleMasterCspRoleMapping{}).Error
}

// DeleteByPermissionID 권한 ID로 매핑 삭제
func (r *CspMappingRepository) DeleteCspRoleMappingByPermissionID(permissionID uint) error {
	return r.db.Where("permission_id = ?", permissionID).Delete(&model.RoleMasterCspRoleMapping{}).Error
}

// DeleteByCspIDAndPermissionID CSP ID와 권한 ID로 매핑 삭제
func (r *CspMappingRepository) DeleteCspRoleMappingByCspIDAndPermissionID(cspID, permissionID uint) error {
	return r.db.Where("csp_id = ? AND permission_id = ?", cspID, permissionID).Delete(&model.RoleMasterCspRoleMapping{}).Error
}
