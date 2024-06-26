package mcinframanager

import "github.com/gobuffalo/nulls"

type McInfraCreateNamespaceRequest struct {
	Name        string       `json:"name"`
	Description nulls.String `json:"description"`
}
type McInfraUpdateNamespaceRequest struct {
	NsId        string
	Name        string       `json:"name"`
	Description nulls.String `json:"description"`
}
