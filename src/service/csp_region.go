package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/m-cmp/mc-iam-manager/model"
)

// 임시자격증명 region 결정 (IAM-BUG-065, 2026-11-27 결정)
//
//   1. 요청 region 이 있으면 그 값 (source=request)
//   2. 없으면 대상 CspRole 이 속한 CSP 계정의 account_info.region (source=account)
//   3. 둘 다 없으면 (source=none)
//      - 호출에 region 이 필수인 CSP(tencent — STS 가 X-TC-Region 없으면 MissingParameter 로 거절): ErrRegionRequired (400)
//      - aws: STS 호출에만 글로벌 엔드포인트 리전(us-east-1)을 쓰고 응답 region 은 비운다. 경고를 남긴다
//      - 그 밖(alibaba·gcp·azure 등): region 없이 진행
//
// 실행 중에는 env AWS_REGION 이나 하드코딩 리전을 쓰지 않는다 (env 는 최초 설정에만).
// 발급·진단·readiness·연결 테스트가 이 함수 하나를 쓴다.

// CSP region 출처
const (
	RegionSourceRequest = "request"
	RegionSourceAccount = "account"
	RegionSourceNone    = "none"
)

// awsGlobalStsRegion region 이 정해지지 않은 aws 호출에 쓰는 글로벌 STS 엔드포인트 리전
const awsGlobalStsRegion = "us-east-1"

var (
	// ErrRegionRequired 호출에 region 이 필수인 CSP 인데 요청·계정 모두 region 이 없음 (400 region_required)
	ErrRegionRequired = errors.New("region is required: set region in the request or account_info.region of the CSP account")
	// ErrInvalidRegion region 형식 오류 (400 invalid_region)
	ErrInvalidRegion = errors.New("invalid region format")
)

// regionPattern CSP 리전 코드 형식만 본다 (예: ap-northeast-2, us-gov-west-1, cn-hangzhou, ap-guangzhou, asia-northeast3)
var regionPattern = regexp.MustCompile(`^[a-z]{2,}(-[a-z0-9]+){1,3}$`)

// regionRequiredForCall 호출에 region 이 꼭 필요한 CSP
func regionRequiredForCall(cspType string) bool {
	switch strings.ToLower(cspType) {
	case "tencent":
		return true
	}
	return false
}

// ValidateRegionFormat 리전 코드 형식 검증 (빈 값은 호출하는 쪽이 판단)
func ValidateRegionFormat(region string) error {
	if !regionPattern.MatchString(region) {
		return fmt.Errorf("%w: %q", ErrInvalidRegion, region)
	}
	return nil
}

// CspRegion region 결정 결과
type CspRegion struct {
	// Region 응답에 싣는 region (source=none 이면 빈 값)
	Region string
	// Source request|account|none
	Source string
	// CallRegion CSP 호출(SDK·헤더)에 쓰는 region. aws 는 비었을 때 글로벌 리전
	CallRegion string
	// Warning 사용자에게 알릴 내용 (계정 region 미설정 등)
	Warning string
}

// ResolveCspRegion 요청 region → 계정 region 순으로 정한다. account 는 nil 일 수 있다.
func ResolveCspRegion(cspType, requested string, account *model.CspAccount) (CspRegion, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if err := ValidateRegionFormat(requested); err != nil {
			return CspRegion{}, err
		}
		return CspRegion{Region: requested, Source: RegionSourceRequest, CallRegion: requested}, nil
	}
	if account != nil {
		if r := strings.TrimSpace(account.GetRegion()); r != "" {
			return CspRegion{Region: r, Source: RegionSourceAccount, CallRegion: r}, nil
		}
	}
	if regionRequiredForCall(cspType) {
		return CspRegion{}, ErrRegionRequired
	}
	res := CspRegion{Source: RegionSourceNone}
	if strings.EqualFold(cspType, "aws") {
		res.CallRegion = awsGlobalStsRegion
		res.Warning = fmt.Sprintf("CSP 계정 region 미설정 — 글로벌 STS(%s)로 발급했고 응답 region 은 비어 있습니다. 요청 region 또는 CSP 계정 account_info.region 을 설정하세요", awsGlobalStsRegion)
	}
	return res, nil
}

// cspRoleAccount CspRole 이 속한 CSP 계정 (CspRole.CspAccount, 없으면 소속 IdP Config 의 계정)
func cspRoleAccount(role *model.CspRole) *model.CspAccount {
	if role == nil {
		return nil
	}
	if role.CspAccount != nil {
		return role.CspAccount
	}
	if role.CspIdpConfig != nil && role.CspIdpConfig.CspAccount != nil {
		return role.CspIdpConfig.CspAccount
	}
	return nil
}
