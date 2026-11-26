package repository

import (
	"fmt"
	"log"

	"gorm.io/gorm"
)

// UserWorkspaceRoleUniqueIndex 직접 배정 (user_id, workspace_id) 유니크 인덱스 이름 (IAM-BUG-050)
const UserWorkspaceRoleUniqueIndex = "uidx_user_workspace_roles_user_workspace"

// EnsureUserWorkspaceRoleUniqueIndex 직접 배정을 (사용자, 워크스페이스)당 1개로 DB 에서도 보장한다.
// 기존 중복이 있으면 인덱스를 만들지 않고 경고만 남긴다(기동은 계속). 릴리스 노트의 정리 SQL 로 중복을
// 정리한 뒤 다시 기동하면 인덱스가 생긴다. 중복이 남아 있어도 역할 결정은 role_id 가 가장 작은 직접 배정을 쓴다.
// 반환: 인덱스가 있거나 새로 만들었으면 true
func EnsureUserWorkspaceRoleUniqueIndex(db *gorm.DB) (bool, error) {
	var dup int64
	if err := db.Raw(`
		SELECT COUNT(*) FROM (
			SELECT user_id, workspace_id FROM mcmp_user_workspace_roles
			GROUP BY user_id, workspace_id HAVING COUNT(*) > 1
		) d`).Scan(&dup).Error; err != nil {
		return false, fmt.Errorf("failed to check duplicate workspace role assignments: %w", err)
	}
	if dup > 0 {
		log.Printf("[WARN] mcmp_user_workspace_roles: (user_id, workspace_id) 중복 %d 건 — 유니크 인덱스 %s 를 만들지 않음. "+
			"릴리스 노트의 정리 SQL 실행 후 재기동하면 생성됨 (중복 중 role_id 가 가장 작은 역할이 유효 역할로 쓰임)", dup, UserWorkspaceRoleUniqueIndex)
		return false, nil
	}
	if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON mcmp_user_workspace_roles (user_id, workspace_id)", UserWorkspaceRoleUniqueIndex)).Error; err != nil {
		return false, fmt.Errorf("failed to create unique index %s: %w", UserWorkspaceRoleUniqueIndex, err)
	}
	return true, nil
}
