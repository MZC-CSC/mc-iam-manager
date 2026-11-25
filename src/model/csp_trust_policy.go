package model

// 대상 CSP Role 신뢰 정책 검증 결과 (IAM-TECH-045 M2)
// 소속 IdP Config 로 만든 기대값(Federated·Action·aud)과 AWS 의 실제 신뢰 정책을 비교한다.

// 판정 값
const (
	TrustPolicyOK       = "ok"        // 기대 statement 가 있고 Action·aud 가 맞다
	TrustPolicyMissing  = "missing"   // 기대 provider(Federated) 를 신뢰하는 statement 가 없다
	TrustPolicyMismatch = "mismatch"  // provider 는 있지만 Action 이나 aud 가 다르다
	TrustPolicyExtra    = "extra"     // IAM 이 쓰지 않은 statement (정보성, 전체 판정에 영향 없음)
	TrustPolicyNotFound = "not_found" // AWS 에 Role 이 없다
	TrustPolicyError    = "error"     // 조회 실패(권한 등) 또는 기대값을 만들 IdP Config 가 없다
)

// TrustPolicyCheck 한 Role 의 판정
type TrustPolicyCheck struct {
	Status     string                `json:"status"` // ok | missing | mismatch | not_found | error
	RoleName   string                `json:"roleName"`
	Statements []TrustStatementCheck `json:"statements"`
	Extra      []string              `json:"extra"` // IAM 이 쓰지 않은 statement 의 principal 요약
	Warnings   []string              `json:"warnings"`
	Message    string                `json:"message,omitempty"`
}

// TrustStatementCheck 인증 방식(OIDC·SAML)별 기대 statement 판정
type TrustStatementCheck struct {
	AuthMethod        string   `json:"authMethod"`
	IdpConfigID       uint     `json:"idpConfigId,omitempty"`
	Status            string   `json:"status"` // ok | missing | mismatch | error
	ExpectedFederated string   `json:"expectedFederated"`
	ExpectedAction    string   `json:"expectedAction"`
	ExpectedAudKey    string   `json:"expectedAudKey"`
	ExpectedAud       string   `json:"expectedAud"`
	ActualActions     []string `json:"actualActions,omitempty"`
	ActualAud         []string `json:"actualAud,omitempty"`
	Message           string   `json:"message,omitempty"`
}

// TrustPolicySyncRequest 신뢰 정책 재동기화 요청 (IAM-TECH-045 M2 PR-2)
type TrustPolicySyncRequest struct {
	DryRun          *bool `json:"dryRun"`          // 기본 true (쓰지 않고 변경안만)
	AllowRegistered bool  `json:"allowRegistered"` // registered(빈 origin 포함) Role 도 바꾼다
}

// IsDryRun 기본값 true
func (r *TrustPolicySyncRequest) IsDryRun() bool {
	return r == nil || r.DryRun == nil || *r.DryRun
}

// TrustPolicyChange 재동기화로 바뀌는 항목
type TrustPolicyChange struct {
	AuthMethod string `json:"authMethod"`
	Change     string `json:"change"` // statement_added | action_added | aud_set | aud_removed
	Detail     string `json:"detail"`
}

// TrustPolicySyncResult 한 Role 의 재동기화 결과
type TrustPolicySyncResult struct {
	CspRoleID uint                   `json:"cspRoleId"`
	RoleName  string                 `json:"roleName"`
	Verdict   string                 `json:"verdict"` // 동기화 전 판정 (ok | missing | mismatch)
	Before    map[string]interface{} `json:"before,omitempty"`
	After     map[string]interface{} `json:"after,omitempty"`
	Changes   []TrustPolicyChange    `json:"changes"`
	Applied   bool                   `json:"applied"`
	DryRun    bool                   `json:"dryRun"`
	Message   string                 `json:"message,omitempty"`
	Warnings  []string               `json:"warnings"`
}

// TrustPolicyBulkItem IdP Config 단위 일괄 check·sync 의 항목
type TrustPolicyBulkItem struct {
	CspRoleID   uint                   `json:"cspRoleId"`
	RoleName    string                 `json:"roleName"`
	Status      string                 `json:"status"` // check: 판정 / sync: unchanged | applied | would_change | registered_role | protected | not_found | invalid_request | error
	TrustPolicy *TrustPolicyCheck      `json:"trustPolicy,omitempty"`
	Sync        *TrustPolicySyncResult `json:"sync,omitempty"`
	Message     string                 `json:"message,omitempty"`
}

// TrustPolicyBulkResult IdP Config 단위 일괄 결과
type TrustPolicyBulkResult struct {
	CspIdpConfigID uint                  `json:"cspIdpConfigId"`
	AuthMethod     string                `json:"authMethod"`
	DryRun         bool                  `json:"dryRun"`
	Items          []TrustPolicyBulkItem `json:"items"`
	Warnings       []string              `json:"warnings"`
}

// AwsRoleView AWS IAM Role 조회 결과 (GET /api/csp/iam/roles/:roleName, 신뢰 정책 원문 포함)
type AwsRoleView struct {
	Name               string                 `json:"name"`
	Arn                string                 `json:"arn"`
	Description        string                 `json:"description"`
	MaxSessionDuration int32                  `json:"maxSessionDuration"`
	TrustPolicy        map[string]interface{} `json:"trustPolicy"`
}
