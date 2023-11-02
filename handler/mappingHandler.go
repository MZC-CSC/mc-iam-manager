package handler

import (
	"mc_iam_manager/models"

	"github.com/gobuffalo/pop/v6"
	"github.com/gofrs/uuid"
)

func WsUserRoleMapping(tx *pop.Connection, bindModel *models.MCIamWsUserRoleMapping) map[string]interface{} {

	err := tx.Create(bindModel)

	if err != nil {

	}
	return map[string]interface{}{
		"": "",
	}
}

func GetWsUserRole(tx *pop.Connection, bindModel *models.MCIamWsUserRoleMapping) *models.MCIamWsUserRoleMappings {

	respModel := &models.MCIamWsUserRoleMappings{}

	if user_id := bindModel.UserID; user_id != uuid.Nil {
		q := tx.Eager().Where("user_id = ?", user_id)
		err := q.All(respModel)
		if err != nil {

		}
	}

	if role_id := bindModel.RoleID; role_id != uuid.Nil {
		q := tx.Eager().Where("role_id = ?", role_id)
		err := q.All(respModel)
		if err != nil {

		}
	}
	if ws_id := bindModel.WsID; ws_id != uuid.Nil {
		q := tx.Eager().Where("ws_id = ?", ws_id)
		err := q.All(respModel)
		if err != nil {

		}
	}
	return respModel
}

func WsProjectMapping(tx *pop.Connection, bindModel *models.MCIamWsProjectMapping) map[string]interface{} {

	err := tx.Create(bindModel)

	if err != nil {

	}
	return map[string]interface{}{
		"": "",
	}
}

func UserRoleMapping(tx *pop.Connection, bindModel *models.MCIamUserRoleMapping) map[string]interface{} {

	err := tx.Create(bindModel)

	if err != nil {

	}
	return map[string]interface{}{
		"": "",
	}
}