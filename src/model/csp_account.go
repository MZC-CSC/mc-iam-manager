package model

import (
	"encoding/json"
	"time"
)

// CspAccount CSP 계정 정보 모델
// AWS Account ID, GCP Project ID, Azure Subscription ID 등 CSP별 계정 정보를 관리
type CspAccount struct {
	ID          uint              `gorm:"primaryKey" json:"id"`
	Name        string            `gorm:"size:255;not null" json:"name"`
	CspType     string            `gorm:"size:50;not null" json:"csp_type"` // aws, gcp, azure
	AccountInfo map[string]string `gorm:"type:jsonb;serializer:json" json:"account_info"`
	IsActive    bool              `gorm:"default:true" json:"is_active"`
	Description string            `gorm:"size:500" json:"description"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// TableName CspAccount 테이블 이름 반환
func (CspAccount) TableName() string {
	return "mcmp_csp_accounts"
}

// CspAccountInfo CSP별 계정 정보 구조
// AWS AccountInfo 예시:
//
//	{
//	  "account_id": "050864702683",
//	  "alias": "my-aws-account",
//	  "region": "ap-northeast-2"
//	}
//
// GCP AccountInfo 예시:
//
//	{
//	  "project_id": "my-gcp-project",
//	  "project_number": "123456789"
//	}
//
// Azure AccountInfo 예시:
//
//	{
//	  "subscription_id": "xxx-xxx-xxx",
//	  "tenant_id": "yyy-yyy-yyy",
//	  "directory_id": "zzz-zzz-zzz"
//	}

// GetAccountID AWS Account ID 반환
func (c *CspAccount) GetAccountID() string {
	if c.AccountInfo == nil {
		return ""
	}
	return c.AccountInfo["account_id"]
}

// GetProjectID GCP Project ID 반환
func (c *CspAccount) GetProjectID() string {
	if c.AccountInfo == nil {
		return ""
	}
	return c.AccountInfo["project_id"]
}

// GetSubscriptionID Azure Subscription ID 반환
func (c *CspAccount) GetSubscriptionID() string {
	if c.AccountInfo == nil {
		return ""
	}
	return c.AccountInfo["subscription_id"]
}

// GetTenantID Azure Tenant ID 반환
func (c *CspAccount) GetTenantID() string {
	if c.AccountInfo == nil {
		return ""
	}
	return c.AccountInfo["tenant_id"]
}

// GetRegion 리전 정보 반환
func (c *CspAccount) GetRegion() string {
	if c.AccountInfo == nil {
		return ""
	}
	return c.AccountInfo["region"]
}

// CspAccountFilter CSP 계정 조회 필터
type CspAccountFilter struct {
	CspType  string `json:"csp_type,omitempty"`
	IsActive *bool  `json:"is_active,omitempty"`
	Name     string `json:"name,omitempty"`
}

// CreateCspAccountRequest CSP 계정 생성 요청
type CreateCspAccountRequest struct {
	Name        string            `json:"name" binding:"required"`
	CspType     string            `json:"csp_type" binding:"required,oneof=aws gcp azure"`
	AccountInfo map[string]string `json:"account_info"`
	Description string            `json:"description"`
}

// UpdateCspAccountRequest CSP 계정 수정 요청
type UpdateCspAccountRequest struct {
	Name        string            `json:"name"`
	AccountInfo map[string]string `json:"account_info"`
	IsActive    *bool             `json:"is_active"`
	Description string            `json:"description"`
}

// CreateCloudIamRoleRequest 클라우드 IAM 역할 생성 요청
// CspRoleConfig는 CSP 타입에 따라 서비스 계층에서 각 CSP별 구조체로 언마샬된다.
type CreateCloudIamRoleRequest struct {
	RoleName      string          `json:"role_name" binding:"required"`
	Description   string          `json:"description"`
	CspRoleConfig json.RawMessage `json:"csp_role_config" binding:"required"`
}

// AwsIamRoleConfig AWS IAM Role 설정
// AssumeRolePolicyDocument에 직접 매핑된다.
// 예: {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}
type AwsIamRoleConfig struct {
	TrustPolicy map[string]interface{} `json:"trust_policy" binding:"required"`
}

// GcpIamRoleConfig GCP Custom Role 설정
// iam.roles.create의 Role.includedPermissions 배열을 사용한다.
// 예: ["iam.roles.get", "iam.roles.list", "resourcemanager.projects.get"]
type GcpIamRoleConfig struct {
	IncludedPermissions []string `json:"included_permissions" binding:"required"`
	Stage               string   `json:"stage,omitempty"` // GA(기본), BETA, ALPHA
}

// AzureIamRoleConfig Azure Custom Role Definition 설정
// roleDefinitions의 assignableScopes 및 permissions를 정의한다.
type AzureIamRoleConfig struct {
	AssignableScopes []string             `json:"assignable_scopes" binding:"required"`
	Permissions      []AzureRolePermission `json:"permissions" binding:"required"`
}

// AzureRolePermission Azure 역할 권한 정의
type AzureRolePermission struct {
	Actions        []string `json:"actions"`
	NotActions     []string `json:"not_actions,omitempty"`
	DataActions    []string `json:"data_actions,omitempty"`
	NotDataActions []string `json:"not_data_actions,omitempty"`
}

// CspCloudInfoResponse 클라우드 실시간 계정 정보 응답
type CspCloudInfoResponse struct {
	AccountId uint              `json:"account_id"`
	CspType   string            `json:"csp_type"`
	CloudInfo map[string]string `json:"cloud_info"`
}

// CloudIamRoleResponse 클라우드 IAM 역할 생성 응답
type CloudIamRoleResponse struct {
	CspRoleId     uint   `json:"csp_role_id"`      // 플랫폼 DB ID
	AccountId     uint   `json:"account_id"`
	CspType       string `json:"csp_type"`
	RoleName      string `json:"role_name"`
	IamIdentifier string `json:"iam_identifier"` // ARN(AWS) / Resource Name(GCP) / RoleDefinition ID(Azure)
	IamRoleId     string `json:"iam_role_id"`    // AWS RoleId, 타 CSP는 빈 문자열
}
