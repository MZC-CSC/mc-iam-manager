/*
 * MCIAMManager API 명세서
 *
 * MCIAMManager API 명세서
 *
 * API version: v1
 * Generated by: Swagger Codegen (https://github.com/swagger-api/swagger-codegen.git)
 */
package iammodels

type UserReq struct {
	UserId        string `json:"userId,omitempty"`
	UserFirstName string `json:"userFirstName,omitempty"`
	UserLastName  string `json:"userLastName,omitempty"`
	Email         string `json:"email,omitempty"`
	UserName      string `json:"userName,omitempty"`
}
