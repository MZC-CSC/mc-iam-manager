package model

import (
	"time"
)

// RoleLastUsed AWS IAM Role의 마지막 사용 정보
type RoleLastUsed struct {
	LastUsedDate time.Time `json:"last_used_date"`
	Region       string    `json:"region"`
}

// Tag AWS IAM Role의 태그 정보
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CspRole CSP 역할 모델
// 대상 CSP와 연결하기 위한 연결정보
// CspAccount: CSP 계정 정보 참조
// CspIdpConfig: IDP 연동 설정 참조 (OIDC, SAML, SECRET_KEY)
type CspRole struct {
	ID                  uint          `gorm:"primaryKey" json:"id"`
	Name                string        `gorm:"size:255;not null" json:"name"`
	Description         string        `gorm:"size:255" json:"description"`
	CspType             string        `gorm:"size:50;not null" json:"csp_type"`
	IdpIdentifier       string        `gorm:"size:255" json:"idp_identifier"`
	IamIdentifier       string        `gorm:"size:255" json:"iam_identifier"`
	Status              string        `gorm:"size:50" json:"status"`
	CreateDate          time.Time     `json:"create_date"`
	Path                string        `gorm:"size:255" json:"path"`
	IamRoleId           string        `gorm:"size:255" json:"iam_role_id"`
	MaxSessionDuration  *int32        `json:"max_session_duration"`
	Permissions         []string      `gorm:"-" json:"permissions"`
	PermissionsBoundary string        `gorm:"size:255" json:"permissions_boundary"`
	RoleLastUsed        *RoleLastUsed `gorm:"type:jsonb;serializer:json" json:"role_last_used"`
	Tags                []Tag         `gorm:"-" json:"tags"`

	// CSP 계정 및 IDP 설정 참조 (신규 추가)
	CspAccountID   *uint                  `gorm:"column:csp_account_id" json:"csp_account_id"`
	CspAccount     *CspAccount            `gorm:"foreignKey:CspAccountID" json:"csp_account,omitempty"`
	CspIdpConfigID *uint                  `gorm:"column:csp_idp_config_id" json:"csp_idp_config_id"`
	CspIdpConfig   *CspIdpConfig          `gorm:"foreignKey:CspIdpConfigID" json:"csp_idp_config,omitempty"`
	ExtendedConfig map[string]interface{} `gorm:"type:jsonb;serializer:json" json:"extended_config,omitempty"`

	// 출처 (IAM-BUG-057): created = IAM 이 CSP 에 만든 Role, registered = CSP 에 이미 있던 Role 을 등록.
	// 빈 값(이전 레코드)은 registered 와 같이 다룬다. 삭제 시 created 만 CSP Role 까지 지운다.
	Origin string `gorm:"column:origin;size:20" json:"origin"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at" gorm:"index"`
}

func (CspRole) TableName() string { // Renamed receiver
	return "mcmp_role_csp_roles"
}

// CspRole 출처
const (
	CspRoleOriginCreated    = "created"
	CspRoleOriginRegistered = "registered"
)

// CspRoleDeleteResult CspRole 삭제가 중간에 멈췄을 때(정책 해제 실패) 돌려주는 결과. 다 지우면 204 로 본문이 없다.
type CspRoleDeleteResult struct {
	Result         string                 `json:"result"` // detach_failed
	CspRoleID      uint                   `json:"cspRoleId"`
	BindingResults []*PolicyBindingResult `json:"bindingResults"`
	Warnings       []string               `json:"warnings"`
}
