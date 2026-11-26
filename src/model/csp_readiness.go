package model

// ReadinessStatus 연동 준비 단계 상태
type ReadinessStatus string

const (
	ReadinessOK            ReadinessStatus = "ok"             // 충족
	ReadinessFailed        ReadinessStatus = "failed"         // 확인했으나 미충족
	ReadinessPending       ReadinessStatus = "pending"        // 아직 진행 전(등록·매핑 없음, 또는 확인 요청 안 함)
	ReadinessSkipped       ReadinessStatus = "skipped"        // 앞 단계 실패나 권한 부족으로 확인하지 않음
	ReadinessNotApplicable ReadinessStatus = "not_applicable" // 이 CSP·인증방식에는 해당 없음(미지원 포함)
)

// 단계 키 (stages[].key)
const (
	ReadinessStageKeycloakClient = "keycloak_oidc_client"
	ReadinessStageAccount        = "csp_account"
	ReadinessStageIdpConfig      = "idp_config"
	ReadinessStageProviderTrust  = "provider_trust"
	ReadinessStageCspRole        = "csp_role"
	ReadinessStageRoleMapping    = "role_mapping"
	ReadinessStageIssue          = "issue"
)

// 역할 연결 근거 (cspRoles[].linkage)
const (
	ReadinessLinkageAccount = "account"  // csp_account_id 가 이 계정
	ReadinessLinkageCspType = "csp_type" // csp_account_id 미기록, 같은 CSP 타입이라 포함 (IAM-BUG-005)
)

// ReadinessStage 연동 준비 단계 하나의 판정 결과
type ReadinessStage struct {
	ID          int             `json:"id"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Status      ReadinessStatus `json:"status"`
	Detail      string          `json:"detail,omitempty"`
	GuideAnchor string          `json:"guideAnchor,omitempty"` // CSP-Temporary-Credential-Guide-ko 절 번호
	NextAction  string          `json:"nextAction,omitempty"`
}

// CspRoleReadiness CSP Role 단위 ⑤·⑥·⑦ 판정 결과
type CspRoleReadiness struct {
	CspRoleID      uint            `json:"cspRoleId"`
	Name           string          `json:"name"`
	AuthMethod     string          `json:"authMethod"`
	Linkage        string          `json:"linkage"`
	CspIdpConfigID *uint           `json:"cspIdpConfigId,omitempty"`
	Status         ReadinessStatus `json:"status"` // ⑤ 역할 설정
	Detail         string          `json:"detail,omitempty"`
	MappingCount   int             `json:"mappingCount"`  // ⑥ 매핑된 플랫폼 역할 수
	MappingStatus  ReadinessStatus `json:"mappingStatus"` // ⑥
	IssueStatus    ReadinessStatus `json:"issueStatus"`   // ⑦
	IssueDetail    string          `json:"issueDetail,omitempty"`
	// TrustPolicy ⑦ 참고 정보: AWS 신뢰 정책 판정 (checkIssue·PlatformAdmin 일 때 AWS Role 만). ⑦ 판정은 실제 assume 결과로만 정한다
	TrustPolicy *CspRoleTrustReadiness `json:"trustPolicy,omitempty"`
}

// CspRoleTrustReadiness CSP Role 신뢰 정책 판정 요약 (IAM-TECH-045 M2, 읽기만)
type CspRoleTrustReadiness struct {
	Status     string `json:"status"` // ok | missing | mismatch | not_found | error
	Detail     string `json:"detail,omitempty"`
	NextAction string `json:"nextAction,omitempty"`
}

// CspAccountReadinessResponse CSP 계정별 임시자격증명 연동 준비 상태
type CspAccountReadinessResponse struct {
	AccountID   uint               `json:"accountId"`
	AccountName string             `json:"accountName"`
	CspType     string             `json:"cspType"`
	Stages      []ReadinessStage   `json:"stages"`
	CspRoles    []CspRoleReadiness `json:"cspRoles"`
	// CanMapRoles 설정이 끝난 CSP Role 이 1개 이상 있어 플랫폼 역할과 매핑할 수 있음
	CanMapRoles bool `json:"canMapRoles"`
	// CanIssue 실제 assume 에 성공하고 매핑도 있는 CSP Role 이 1개 이상. 확인하지 않았으면 null
	CanIssue *bool `json:"canIssue"`
	// LiveChecks CSP 실호출(④·⑦)을 수행할 권한이 있었는지 (PlatformAdmin)
	LiveChecks bool   `json:"liveChecks"`
	CheckedAt  string `json:"checkedAt"`
}
