package repository

import (
	"fmt"
	"sort"

	"github.com/m-cmp/mc-iam-manager/model"
	"gorm.io/gorm"
)

// 워크스페이스 역할 결정 규칙 (IAM-BUG-050)
//
//   - 직접 배정이 있으면 그것을 쓴다. 직접 배정은 (사용자, 워크스페이스)당 1개가 원칙이지만, 기존 데이터에
//     여러 개가 남아 있으면 role_id 가 가장 작은 것을 쓴다(결과가 항상 같도록).
//   - 직접 배정이 없으면 소속 그룹의 워크스페이스 역할을 쓴다. 그룹이 여러 개면 role_id 오름차순(같으면
//     group_id 오름차순) 첫 값이다.
//   - 직접 배정이 있고 그와 다른 그룹 역할이 있으면 Overridden 이다. 그룹 역할은 지우지 않으며, 직접 배정이
//     해제되면 그룹 역할이 다시 유효 역할이 된다.
//
// 발급(FindEffectiveUserRoleInWorkspace)·진단·유효 역할 목록(FindEffectiveWorkspaceRoles)이 이 함수 하나를 쓴다.

type directRoleRow struct {
	WorkspaceID   uint
	WorkspaceName string
	RoleID        uint
	RoleName      string
}

type groupRoleRow struct {
	WorkspaceID   uint
	WorkspaceName string
	GroupID       uint
	GroupName     string
	RoleID        uint
	RoleName      string
}

// resolveWorkspaceRoles 사용자의 워크스페이스 역할을 결정한다. workspaceID 가 0 이면 모든 워크스페이스.
// 반환: 워크스페이스 id 오름차순 결과
func resolveWorkspaceRoles(db *gorm.DB, userID, workspaceID uint) ([]*model.WorkspaceRoleResolution, error) {
	directQ := `
		SELECT uwr.workspace_id, w.name AS workspace_name, uwr.role_id, rm.name AS role_name
		FROM mcmp_user_workspace_roles uwr
		JOIN mcmp_workspaces w ON w.id = uwr.workspace_id
		JOIN mcmp_role_masters rm ON rm.id = uwr.role_id
		WHERE uwr.user_id = ?`
	groupQ := `
		SELECT gwr.workspace_id, w.name AS workspace_name, gwr.group_id, COALESCE(o.name, '') AS group_name, gwr.role_id, rm.name AS role_name
		FROM mcmp_group_workspace_roles gwr
		JOIN mcmp_user_organizations uo ON uo.organization_id = gwr.group_id
		LEFT JOIN mcmp_organizations o ON o.id = gwr.group_id
		JOIN mcmp_workspaces w ON w.id = gwr.workspace_id
		JOIN mcmp_role_masters rm ON rm.id = gwr.role_id
		WHERE uo.user_id = ?`
	args := []interface{}{userID}
	if workspaceID != 0 {
		directQ += " AND uwr.workspace_id = ?"
		groupQ += " AND gwr.workspace_id = ?"
		args = append(args, workspaceID)
	}
	directQ += " ORDER BY uwr.workspace_id ASC, uwr.role_id ASC"
	groupQ += " ORDER BY gwr.workspace_id ASC, gwr.role_id ASC, gwr.group_id ASC"

	var direct []directRoleRow
	if err := db.Raw(directQ, args...).Scan(&direct).Error; err != nil {
		return nil, fmt.Errorf("error finding direct workspace roles for user %d: %w", userID, err)
	}
	var groups []groupRoleRow
	if err := db.Raw(groupQ, args...).Scan(&groups).Error; err != nil {
		return nil, fmt.Errorf("error finding group workspace roles for user %d: %w", userID, err)
	}

	byWs := map[uint]*model.WorkspaceRoleResolution{}
	get := func(id uint, name string) *model.WorkspaceRoleResolution {
		r, ok := byWs[id]
		if !ok {
			r = &model.WorkspaceRoleResolution{WorkspaceID: id, WorkspaceName: name}
			byWs[id] = r
		}
		return r
	}
	for _, d := range direct {
		r := get(d.WorkspaceID, d.WorkspaceName)
		r.DirectRoles = append(r.DirectRoles, model.WorkspaceRoleRef{RoleID: d.RoleID, RoleName: d.RoleName, Source: model.WorkspaceRoleSourceDirect})
	}
	for _, g := range groups {
		r := get(g.WorkspaceID, g.WorkspaceName)
		r.GroupRoles = append(r.GroupRoles, model.GroupWorkspaceRoleRef{GroupID: g.GroupID, GroupName: g.GroupName, RoleID: g.RoleID, RoleName: g.RoleName})
	}

	out := make([]*model.WorkspaceRoleResolution, 0, len(byWs))
	for _, r := range byWs {
		switch {
		case len(r.DirectRoles) > 0:
			d := r.DirectRoles[0]
			r.Effective = &model.EffectiveWorkspaceRole{WorkspaceID: r.WorkspaceID, WorkspaceName: r.WorkspaceName, RoleID: d.RoleID, RoleName: d.RoleName, Source: model.WorkspaceRoleSourceDirect}
			for _, g := range r.GroupRoles {
				if g.RoleID != d.RoleID {
					r.Overridden = true
					break
				}
			}
		case len(r.GroupRoles) > 0:
			g := r.GroupRoles[0]
			gid := g.GroupID
			r.Effective = &model.EffectiveWorkspaceRole{WorkspaceID: r.WorkspaceID, WorkspaceName: r.WorkspaceName, RoleID: g.RoleID, RoleName: g.RoleName, Source: model.WorkspaceRoleSourceGroup, GroupID: &gid}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkspaceID < out[j].WorkspaceID })
	return out, nil
}

// ResolveUserWorkspaceRole 한 워크스페이스의 역할 결정 결과 (역할이 전혀 없으면 Effective 가 nil 인 빈 결과)
func ResolveUserWorkspaceRole(db *gorm.DB, userID, workspaceID uint) (*model.WorkspaceRoleResolution, error) {
	res, err := resolveWorkspaceRoles(db, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return &model.WorkspaceRoleResolution{WorkspaceID: workspaceID}, nil
	}
	return res[0], nil
}

// effectiveRoleRows 결정 결과를 목록 응답 행으로 펼친다: (워크스페이스, 역할) 마다 한 행, 행마다 출처와
// 그 워크스페이스의 결정 결과를 싣는다. 행 순서는 워크스페이스 id, role_id 오름차순.
func effectiveRoleRows(res []*model.WorkspaceRoleResolution) []model.EffectiveWorkspaceRole {
	rows := []model.EffectiveWorkspaceRole{}
	for _, r := range res {
		eff := r.Effective
		effRef := &model.WorkspaceRoleRef{RoleID: eff.RoleID, RoleName: eff.RoleName, Source: eff.Source, GroupID: eff.GroupID}
		overridden := r.Overridden
		groupRoles := r.GroupRoles
		if groupRoles == nil {
			groupRoles = []model.GroupWorkspaceRoleRef{}
		}

		type src struct {
			source  string
			groupID *uint
		}
		seen := map[uint]src{}
		var order []uint
		for _, d := range r.DirectRoles {
			if _, ok := seen[d.RoleID]; !ok {
				seen[d.RoleID] = src{source: model.WorkspaceRoleSourceDirect}
				order = append(order, d.RoleID)
			}
		}
		names := map[uint]string{}
		for _, d := range r.DirectRoles {
			names[d.RoleID] = d.RoleName
		}
		for _, g := range r.GroupRoles {
			names[g.RoleID] = g.RoleName
			if _, ok := seen[g.RoleID]; !ok {
				gid := g.GroupID
				seen[g.RoleID] = src{source: model.WorkspaceRoleSourceGroup, groupID: &gid}
				order = append(order, g.RoleID)
			}
		}
		sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
		for _, roleID := range order {
			s := seen[roleID]
			isEff := roleID == eff.RoleID
			rows = append(rows, model.EffectiveWorkspaceRole{
				WorkspaceID:   r.WorkspaceID,
				WorkspaceName: r.WorkspaceName,
				RoleID:        roleID,
				RoleName:      names[roleID],
				Source:        s.source,
				GroupID:       s.groupID,
				IsEffective:   &isEff,
				EffectiveRole: effRef,
				GroupRoles:    groupRoles,
				Overridden:    &overridden,
			})
		}
	}
	return rows
}
