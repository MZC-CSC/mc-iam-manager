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
