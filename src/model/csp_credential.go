package model

import "time"

// CspCredentialRequest CSP 임시 자격 증명 발급 요청 모델
type CspCredentialRequest struct {
	WorkspaceID string `json:"workspaceId"`         // 대상 워크스페이스 ID
	CspType     string `json:"cspType"`             // 대상 CSP 타입
	Region      string `json:"region"`              // 리전 (선택). 없으면 대상 CSP 계정의 account_info.region
	AuthMethod  string `json:"authMethod,omitempty"` // 인증방식 (OIDC/SAML/SECRET_KEY), 미지정 시 매핑에서 결정
}

// CspCredentialResponse CSP 임시 자격 증명 발급 응답 모델
type CspCredentialResponse struct {
	CspType         string    `json:"cspType"`                    // e.g., "aws", "gcp"
	AccessKeyId     string    `json:"accessKeyId,omitempty"`      // AWS Access Key ID
	SecretAccessKey string    `json:"secretAccessKey,omitempty"`  // AWS Secret Access Key
	SessionToken    string    `json:"sessionToken,omitempty"`     // AWS Session Token
	AccessToken     string    `json:"accessToken,omitempty"`      // GCP OAuth2 Access Token
	TokenType       string    `json:"tokenType,omitempty"`        // GCP Token Type (Bearer)
	AccessKeySecret string    `json:"accessKeySecret,omitempty"`  // Alibaba STS Access Key Secret
	SecurityToken   string    `json:"securityToken,omitempty"`    // Alibaba STS Security Token
	Expiration      time.Time `json:"expiration"`                 // Expiration time
	Region          string    `json:"region,omitempty"`           // Optional: AWS/Alibaba Region
	// UsedRole 발급에 사용한 워크스페이스 역할과 출처 (직접 배정 우선, 없으면 그룹 역할)
	UsedRole *UsedWorkspaceRole `json:"usedRole,omitempty"`
	// RegionSource region 출처: request(요청) | account(CSP 계정 account_info.region) | none(둘 다 없음 — region 비움)
	RegionSource string `json:"regionSource,omitempty"`
	// Warnings 발급은 됐지만 알릴 내용 (예: 계정 region 미설정으로 글로벌 STS 사용)
	Warnings []string `json:"warnings,omitempty"`
}

// TempCredential 임시 자격 증명 관리 테이블 모델
type TempCredential struct {
	ID              uint      `json:"id" gorm:"primaryKey"`
	Provider        string    `json:"provider" gorm:"not null"`        // aws, gcp, azure, nhn
	AuthType        string    `json:"authType" gorm:"not null"`        // oidc, saml, secret_key
	AccessKeyId     string    `json:"accessKeyId" gorm:"not null"`     // Access Key ID
	SecretAccessKey string    `json:"secretAccessKey" gorm:"not null"` // Secret Access Key
	SessionToken    string    `json:"sessionToken"`                    // Session Token (OIDC/SAML용)
	Region          string    `json:"region" gorm:"not null"`          // 리전
	IssuedAt        time.Time `json:"issuedAt" gorm:"not null"`        // 발급 시간
	ExpiresAt       time.Time `json:"expiresAt" gorm:"not null"`       // 만료 시간
	IsActive        bool      `json:"isActive" gorm:"default:true"`    // 활성 상태
	IssuedBy        string    `json:"issuedBy" gorm:"not null"`        // 발급 요청자 (Keycloak User ID)
	RoleMasterID    *uint     `json:"roleMasterId"`                    // RoleMaster ID (선택적)
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// TableName 테이블명 지정
func (TempCredential) TableName() string {
	return "mcmp_temp_credentials"
}

// IsExpired 만료 여부 확인
func (tc *TempCredential) IsExpired() bool {
	return time.Now().After(tc.ExpiresAt)
}

// IsValid 유효성 확인 (활성 상태이고 만료되지 않음)
func (tc *TempCredential) IsValid() bool {
	return tc.IsActive && !tc.IsExpired()
}

// GetExpirationTime 만료까지 남은 시간
func (tc *TempCredential) GetExpirationTime() time.Duration {
	return tc.ExpiresAt.Sub(time.Now())
}
