package model

import (
	"time"
)

// PolicyType 정책 타입
type PolicyType string

const (
	PolicyTypeInline  PolicyType = "inline"  // 인라인 정책 (역할에 직접 포함)
	PolicyTypeManaged PolicyType = "managed" // 관리형 정책 (독립 정책)
	PolicyTypeCustom  PolicyType = "custom"  // 사용자 정의 정책
)

// CspPolicy CSP 정책 모델
// AWS IAM Policy, GCP IAM Role, Azure Role Definition 등을 관리
type CspPolicy struct {
	ID           uint                   `gorm:"primaryKey" json:"id"`
	Name         string                 `gorm:"size:255;not null" json:"name"`
	CspAccountID uint                   `gorm:"not null" json:"csp_account_id"`
	CspAccount   *CspAccount            `gorm:"foreignKey:CspAccountID" json:"csp_account,omitempty"`
	PolicyType   PolicyType             `gorm:"size:50;not null" json:"policy_type"` // inline, managed, custom
	PolicyArn    string                 `gorm:"size:500" json:"policy_arn,omitempty"`
	PolicyDoc    map[string]interface{} `gorm:"type:jsonb;serializer:json" json:"policy_doc,omitempty"`
	Description  string                 `gorm:"size:500" json:"description"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// TableName CspPolicy 테이블 이름 반환
func (CspPolicy) TableName() string {
	return "mcmp_csp_policies"
}

// CspRolePolicyMapping CSP 역할-정책 매핑 모델
type CspRolePolicyMapping struct {
	CspRoleID   uint       `gorm:"primaryKey" json:"csp_role_id"`
	CspPolicyID uint       `gorm:"primaryKey" json:"csp_policy_id"`
	CspRole     *CspRole   `gorm:"foreignKey:CspRoleID" json:"csp_role,omitempty"`
	CspPolicy   *CspPolicy `gorm:"foreignKey:CspPolicyID" json:"csp_policy,omitempty"`
	// AWS 반영 상태 (IAM-BUG-044). 기존(DB 전용) 행은 빈 값이다. attach 를 다시 요청하면 다시 반영한다.
	AwsStatus string     `gorm:"column:aws_status;size:20" json:"aws_status"`
	LastError string     `gorm:"column:last_error;type:text" json:"last_error"`
	AppliedAt *time.Time `gorm:"column:applied_at" json:"applied_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// 정책 매핑의 AWS 반영 상태
const (
	PolicyAwsStatusApplied      = "applied"       // AWS 에 반영됨
	PolicyAwsStatusFailed       = "failed"        // attach 가 AWS 에서 실패 (재요청으로 다시 반영)
	PolicyAwsStatusDetachFailed = "detach_failed" // detach 가 AWS 에서 실패 (재요청으로 다시 해제)
)

// CspRolePolicy 역할에 연결된 정책과 그 매핑의 AWS 반영 상태 (GET /csp-policies/role/:roleId)
type CspRolePolicy struct {
	*CspPolicy
	AwsStatus string     `json:"aws_status"`
	LastError string     `json:"last_error"`
	AppliedAt *time.Time `json:"applied_at"`
}

// PolicyBindingResult attachPolicy·detachPolicy 결과.
// AWS 반영이 실패해도 200 으로 돌려주고 result·warnings 로 알린다 (아래 계층 실패가 위 계층을 실패시키지 않음).
type PolicyBindingResult struct {
	Result      string   `json:"result"` // applied | failed | detached | detach_failed
	CspRoleID   uint     `json:"cspRoleId"`
	CspPolicyID uint     `json:"cspPolicyId"`
	AwsStatus   string   `json:"awsStatus,omitempty"` // 남은 매핑 행의 상태 (detached 면 비어 있음)
	ErrorCode   string   `json:"errorCode,omitempty"` // failed·detach_failed: csp_error
	Message     string   `json:"message"`
	Warnings    []string `json:"warnings"`
}

// PolicyBinding 결과 값
const (
	PolicyBindingApplied      = "applied"
	PolicyBindingFailed       = "failed"
	PolicyBindingDetached     = "detached"
	PolicyBindingDetachFailed = "detach_failed"
)

// TableName CspRolePolicyMapping 테이블 이름 반환
func (CspRolePolicyMapping) TableName() string {
	return "mcmp_csp_role_policy_mappings"
}

// PolicyDocument AWS IAM Policy Document 구조
// PolicyDoc 예시 (AWS):
//
//	{
//	  "Version": "2012-10-17",
//	  "Statement": [
//	    {
//	      "Effect": "Allow",
//	      "Action": ["s3:GetObject", "s3:PutObject"],
//	      "Resource": ["arn:aws:s3:::my-bucket/*"]
//	    }
//	  ]
//	}
//
// PolicyDoc 예시 (GCP):
//
//	{
//	  "included_permissions": ["storage.objects.get", "storage.objects.create"],
//	  "stage": "GA"
//	}
//
// PolicyDoc 예시 (Azure):
//
//	{
//	  "actions": ["Microsoft.Storage/storageAccounts/read"],
//	  "not_actions": [],
//	  "data_actions": [],
//	  "not_data_actions": []
//	}

// IsInline 인라인 정책인지 확인
func (p *CspPolicy) IsInline() bool {
	return p.PolicyType == PolicyTypeInline
}

// IsManaged 관리형 정책인지 확인
func (p *CspPolicy) IsManaged() bool {
	return p.PolicyType == PolicyTypeManaged
}

// IsCustom 사용자 정의 정책인지 확인
func (p *CspPolicy) IsCustom() bool {
	return p.PolicyType == PolicyTypeCustom
}

// GetPolicyVersion 정책 버전 반환 (AWS)
func (p *CspPolicy) GetPolicyVersion() string {
	if p.PolicyDoc == nil {
		return ""
	}
	if version, ok := p.PolicyDoc["Version"].(string); ok {
		return version
	}
	return ""
}

// GetStatements 정책 Statement 반환 (AWS)
func (p *CspPolicy) GetStatements() []interface{} {
	if p.PolicyDoc == nil {
		return nil
	}
	if statements, ok := p.PolicyDoc["Statement"].([]interface{}); ok {
		return statements
	}
	return nil
}

// CspPolicyFilter CSP 정책 조회 필터
type CspPolicyFilter struct {
	CspAccountID *uint      `json:"csp_account_id,omitempty"`
	PolicyType   PolicyType `json:"policy_type,omitempty"`
	Name         string     `json:"name,omitempty"`
}

// CreateCspPolicyRequest CSP 정책 생성 요청
type CreateCspPolicyRequest struct {
	Name         string                 `json:"name" binding:"required"`
	CspAccountID uint                   `json:"csp_account_id" binding:"required"`
	PolicyType   PolicyType             `json:"policy_type" binding:"required,oneof=inline managed custom"`
	PolicyArn    string                 `json:"policy_arn"`
	PolicyDoc    map[string]interface{} `json:"policy_doc"`
	Description  string                 `json:"description"`
}

// UpdateCspPolicyRequest CSP 정책 수정 요청
type UpdateCspPolicyRequest struct {
	Name        string                 `json:"name"`
	PolicyArn   string                 `json:"policy_arn"`
	PolicyDoc   map[string]interface{} `json:"policy_doc"`
	Description string                 `json:"description"`
}

// AttachPolicyRequest 정책 연결 요청
type AttachPolicyRequest struct {
	CspRoleID   uint `json:"csp_role_id" binding:"required"`
	CspPolicyID uint `json:"csp_policy_id" binding:"required"`
}

// SyncPoliciesRequest 정책 동기화 요청
type SyncPoliciesRequest struct {
	CspAccountID uint   `json:"csp_account_id" binding:"required"`
	PolicyScope  string `json:"policy_scope"` // All, AWS, Local
}
