package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/m-cmp/mc-iam-manager/constants"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInlineName = "mciam-t045-inline"

type deleteFixture struct {
	*bindingFixture
	managed, inline *model.CspPolicy
	master          *model.RoleMaster
}

// newDeleteFixture IAM 이 managed·inline 정책을 붙인 CspRole(출처 origin)과 그 Master 매핑
func newDeleteFixture(t *testing.T, origin string) *deleteFixture {
	t.Helper()
	f := newBindingFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	require.NoError(t, f.db.Model(f.role).Update("origin", origin).Error)
	f.role.Origin = origin
	d := &deleteFixture{bindingFixture: f}
	d.managed = f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, testManagedArn, nil)
	d.inline = f.policy(t, testInlineName, model.PolicyTypeInline, "", s3Doc())
	for _, p := range []*model.CspPolicy{d.managed, d.inline} {
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
	}
	d.master = &model.RoleMaster{Name: "t045-p1"}
	require.NoError(t, f.db.Create(d.master).Error)
	require.NoError(t, f.db.Create(&model.RoleMasterCspRoleMapping{RoleID: d.master.ID, CspRoleID: f.role.ID, AuthMethod: constants.AuthMethodOIDC}).Error)
	return d
}

func (d *deleteFixture) awsRole(extraAttached, extraInline, profiles []string) *fakeAwsRoleState {
	return d.api.addRole("mciam-t045-p1", append([]string{testManagedArn}, extraAttached...), append([]string{testInlineName}, extraInline...), profiles)
}

func (d *deleteFixture) counts(t *testing.T) (roles, policyMaps, masterMaps int64) {
	t.Helper()
	require.NoError(t, d.db.Model(&model.CspRole{}).Where("id = ?", d.role.ID).Count(&roles).Error)
	require.NoError(t, d.db.Model(&model.CspRolePolicyMapping{}).Where("csp_role_id = ?", d.role.ID).Count(&policyMaps).Error)
	require.NoError(t, d.db.Model(&model.RoleMasterCspRoleMapping{}).Where("csp_role_id = ?", d.role.ID).Count(&masterMaps).Error)
	return
}

func (d *deleteFixture) requireUntouched(t *testing.T) {
	t.Helper()
	r, p, m := d.counts(t)
	assert.Equal(t, [3]int64{1, 2, 1}, [3]int64{r, p, m}, "CspRole·정책 매핑·Master 매핑 유지")
}

func (d *deleteFixture) requireDeleted(t *testing.T) {
	t.Helper()
	r, p, m := d.counts(t)
	assert.Equal(t, [3]int64{0, 0, 0}, [3]int64{r, p, m}, "한 트랜잭션에서 모두 삭제")
	var master int64
	require.NoError(t, d.db.Model(&model.RoleMaster{}).Where("id = ?", d.master.ID).Count(&master).Error)
	assert.EqualValues(t, 1, master, "Master Role 은 유지")
}

func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// IAM 이 만든 Role: IAM 정책 해제 → DeleteRole → DB 정리
func TestDeleteCspRole_CreatedDetachesAndDeletesRole(t *testing.T) {
	d := newDeleteFixture(t, model.CspRoleOriginCreated)
	d.awsRole(nil, nil, nil)
	res, err := d.svc.DeleteCspRole(d.role.ID)
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.Contains(t, d.api.calls, "detach:mciam-t045-p1:"+testManagedArn)
	assert.Contains(t, d.api.calls, "delete:mciam-t045-p1:"+testInlineName)
	assert.Contains(t, d.api.calls, "deleteRole:mciam-t045-p1")
	assert.Nil(t, d.api.role("mciam-t045-p1"), "AWS Role 삭제")
	d.requireDeleted(t)
}

// IAM 이 만든 Role 에 IAM 밖 정책·instance profile 이 있으면 409 + 목록, 아무것도 바꾸지 않는다 (페이지 1개씩)
func TestDeleteCspRole_CreatedBlockedByUnmanaged(t *testing.T) {
	d := newDeleteFixture(t, model.CspRoleOriginCreated)
	d.awsRole([]string{"arn:aws:iam::aws:policy/ReadOnlyAccess", "arn:aws:iam::111111111111:policy/console-x"}, []string{"console-inline"}, []string{"ec2-profile"})
	_, err := d.svc.DeleteCspRole(d.role.ID)
	requireBindingError(t, err, http.StatusConflict, "conflict")
	be, _ := AsPolicyBindingError(err)
	assert.Equal(t, []string{"arn:aws:iam::111111111111:policy/console-x", "arn:aws:iam::aws:policy/ReadOnlyAccess"}, be.Details["unmanagedAttachedPolicies"])
	assert.Equal(t, []string{"console-inline"}, be.Details["unmanagedInlinePolicies"])
	assert.Equal(t, []string{"ec2-profile"}, be.Details["instanceProfiles"])
	for _, c := range d.api.calls {
		assert.True(t, strings.HasPrefix(c, "list"), "조회만: %s", c)
	}
	d.requireUntouched(t)
	assert.Len(t, d.api.role("mciam-t045-p1").attached, 3)
}

func TestDeleteCspRole_ListAccessDenied502(t *testing.T) {
	d := newDeleteFixture(t, model.CspRoleOriginCreated)
	d.awsRole(nil, nil, nil)
	d.api.listErr = awsDenied
	_, err := d.svc.DeleteCspRole(d.role.ID)
	requireBindingError(t, err, http.StatusBadGateway, "csp_error")
	assert.Contains(t, err.Error(), "iam:ListAttachedRolePolicies")
	assert.Contains(t, err.Error(), "nothing was changed")
	d.requireUntouched(t)
	assert.False(t, hasCall(d.api.calls, "detach:"))
}

// 해제 실패 → detach_failed, CspRole·Master 매핑 유지 → 재요청으로 이어서 삭제
func TestDeleteCspRole_DetachFailureThenRetry(t *testing.T) {
	d := newDeleteFixture(t, model.CspRoleOriginCreated)
	d.awsRole(nil, nil, nil)
	d.api.detachErr = awsDenied
	res, err := d.svc.DeleteCspRole(d.role.ID)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, model.PolicyBindingDetachFailed, res.Result)
	assert.NotEmpty(t, res.Warnings)
	assert.Equal(t, model.PolicyAwsStatusDetachFailed, d.mapping(t, d.role.ID, d.managed.ID).AwsStatus)
	assert.Nil(t, d.mapping(t, d.role.ID, d.inline.ID), "해제에 성공한 inline 연결은 지운다")
	assert.False(t, hasCall(d.api.calls, "deleteRole:"))
	r, _, m := d.counts(t)
	assert.Equal(t, [2]int64{1, 1}, [2]int64{r, m}, "CspRole·Master 매핑 유지")

	d.api.detachErr = nil
	res, err = d.svc.DeleteCspRole(d.role.ID)
	require.NoError(t, err)
	assert.Nil(t, res)
	d.requireDeleted(t)
}

func TestDeleteCspRole_DeleteConflict409(t *testing.T) {
	d := newDeleteFixture(t, model.CspRoleOriginCreated)
	d.awsRole(nil, nil, nil)
	d.api.deleteRoleErr = &iamtypes.DeleteConflictException{Message: aws.String("attached")}
	_, err := d.svc.DeleteCspRole(d.role.ID)
	requireBindingError(t, err, http.StatusConflict, "conflict")
	r, _, m := d.counts(t)
	assert.Equal(t, [2]int64{1, 1}, [2]int64{r, m}, "DB 유지")
}

// AWS 에 Role 이 없으면 DB 만 정리 (IAM-BUG-052 유지)
func TestDeleteCspRole_AwsRoleGone(t *testing.T) {
	d := newDeleteFixture(t, model.CspRoleOriginCreated)
	res, err := d.svc.DeleteCspRole(d.role.ID)
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.False(t, hasCall(d.api.calls, "deleteRole:"))
	d.requireDeleted(t)
}

// 이미 있던 Role 을 등록: IAM 정책만 해제하고 AWS Role·IAM 밖 정책은 남긴다
func TestDeleteCspRole_RegisteredKeepsAwsRole(t *testing.T) {
	for _, origin := range []string{model.CspRoleOriginRegistered, ""} {
		d := newDeleteFixture(t, origin)
		d.awsRole([]string{"arn:aws:iam::aws:policy/ReadOnlyAccess"}, nil, []string{"ec2-profile"})
		res, err := d.svc.DeleteCspRole(d.role.ID)
		require.NoError(t, err, "origin=%q", origin)
		assert.Nil(t, res)
		st := d.api.role("mciam-t045-p1")
		require.NotNil(t, st, "AWS Role 유지")
		assert.Equal(t, []string{"arn:aws:iam::aws:policy/ReadOnlyAccess"}, st.attached, "IAM 밖 정책 유지, IAM 정책만 해제")
		assert.Empty(t, st.inline)
		assert.False(t, hasCall(d.api.calls, "deleteRole:"))
		assert.False(t, hasCall(d.api.calls, "list"), "사전 확인은 created 만")
		d.requireDeleted(t)
	}
}

// IAM 정책이 없는 등록 Role 은 AWS 를 부르지 않는다 (소속 Config 를 정할 수 없어도 된다)
func TestDeleteCspRole_RegisteredWithoutIamPoliciesNoAws(t *testing.T) {
	f := newBindingFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	seedIdpConfig(t, f.db, "aws-oidc-2", "aws", model.AuthMethodOIDC, true, map[string]string{"role_arn": "arn:aws:iam::111111111111:role/mciam-mgmt2"})
	legacy := seedBindingRole(t, f.db, "mciam-t045-legacy", nil) // Config NULL + 활성 OIDC 2개
	p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
	require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: legacy.ID, CspPolicyID: p.ID}).Error) // #218 이전 DB 전용 행
	res, err := f.svc.DeleteCspRole(legacy.ID)
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.api.calls)
	_, gerr := f.svc.cspRoleRepo.GetRoleByID(legacy.ID)
	assert.Error(t, gerr)
}

func TestDeleteCspRole_ManagementRole409AndNotFound(t *testing.T) {
	f := newBindingFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	mgmt := seedBindingRole(t, f.db, "mciam-platformadmin", &f.cfg.ID)
	_, err := f.svc.DeleteCspRole(mgmt.ID)
	requireBindingError(t, err, http.StatusConflict, "protected")
	assert.Empty(t, f.api.calls)
	_, err = f.svc.DeleteCspRole(9999)
	requireBindingError(t, err, http.StatusNotFound, "not_found")
}

// AWS 이름은 저장된 ARN 에서 얻는다 (레코드 이름이 바뀐 이전 데이터)
func TestDeleteCspRole_UsesArnName(t *testing.T) {
	d := newDeleteFixture(t, model.CspRoleOriginCreated)
	require.NoError(t, d.db.Model(d.role).Update("name", "renamed-record").Error)
	d.awsRole(nil, nil, nil)
	_, err := d.svc.DeleteCspRole(d.role.ID)
	require.NoError(t, err)
	assert.Contains(t, d.api.calls, "deleteRole:mciam-t045-p1")
}

// Config 해석: ① NULL ② 비활성 ③ SAML ④ NULL + 활성 OIDC 0·2개
func TestResolveBindingIdpConfig_Rules(t *testing.T) {
	f := newBindingFixture(t)
	acct := f.cfg.CspAccountID
	role := func(cfgID *uint, account *uint) *model.CspRole {
		return &model.CspRole{ID: 7, Name: "mciam-t045-x", CspType: "aws", CspIdpConfigID: cfgID, CspAccountID: account}
	}
	got, err := f.svc.resolveBindingIdpConfig(role(&f.cfg.ID, nil), false)
	require.NoError(t, err)
	assert.Equal(t, f.cfg.ID, got.ID, "소속 활성 OIDC")

	got, err = f.svc.resolveBindingIdpConfig(role(nil, nil), false)
	require.NoError(t, err, "① NULL + 활성 1개")
	assert.Equal(t, f.cfg.ID, got.ID)

	inactive := &model.CspIdpConfig{Name: "old", CspAccountID: acct, AuthMethod: model.AuthMethodOIDC, IsActive: true, Config: map[string]string{"role_arn": "arn:aws:iam::111111111111:role/mciam-old"}}
	require.NoError(t, f.db.Create(inactive).Error)
	require.NoError(t, f.db.Model(inactive).Update("is_active", false).Error)
	got, err = f.svc.resolveBindingIdpConfig(role(&inactive.ID, nil), false)
	require.NoError(t, err, "② 비활성 → 같은 계정의 활성 OIDC")
	assert.Equal(t, f.cfg.ID, got.ID)

	saml := &model.CspIdpConfig{Name: "saml", CspAccountID: acct, AuthMethod: model.AuthMethodSAML, IsActive: true, Config: map[string]string{}}
	require.NoError(t, f.db.Create(saml).Error)
	got, err = f.svc.resolveBindingIdpConfig(role(&saml.ID, nil), false)
	require.NoError(t, err, "③ SAML → 같은 계정의 활성 OIDC")
	assert.Equal(t, f.cfg.ID, got.ID)

	// ④ 다른 계정의 OIDC 가 하나 더 생기면 NULL·계정 모름은 400, 계정을 알면 그 계정 것
	other := seedIdpConfig(t, f.db, "aws-oidc-2", "aws", model.AuthMethodOIDC, true, map[string]string{"role_arn": "arn:aws:iam::222222222222:role/mciam-mgmt2"})
	_, err = f.svc.resolveBindingIdpConfig(role(nil, nil), false)
	requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
	assert.Contains(t, err.Error(), "PUT /api/roles/csp/id/7")
	got, err = f.svc.resolveBindingIdpConfig(role(nil, &other.CspAccountID), false)
	require.NoError(t, err)
	assert.Equal(t, other.ID, got.ID)
	got, err = f.svc.resolveBindingIdpConfig(role(&saml.ID, nil), false)
	require.NoError(t, err, "SAML 의 계정으로 좁힌다")
	assert.Equal(t, f.cfg.ID, got.ID)

	// 활성 OIDC 0개(계정 기준)
	empty := &model.CspAccount{Name: "empty", CspType: "aws"}
	require.NoError(t, f.db.Create(empty).Error)
	_, err = f.svc.resolveBindingIdpConfig(role(nil, &empty.ID), false)
	requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
}

// 규칙 2: 새 권한을 붙이는 작업(attach·inline 문서 반영)은 소속 Config 가 비활성이면 400, SAML 이면 같은 계정 OIDC 로 대체.
// 정리 작업(detach·CspRole 삭제)은 비활성도 대체한다.
func TestBindingConfigRules_GrantVsCleanup(t *testing.T) {
	newRoleWithConfig := func(t *testing.T, f *bindingFixture, method model.AuthMethodType, active bool) *model.CspRole {
		cfg := &model.CspIdpConfig{Name: "t045-" + string(method), CspAccountID: f.cfg.CspAccountID, AuthMethod: method, IsActive: true,
			Config: map[string]string{"role_arn": testManagementRoleArn}}
		require.NoError(t, f.db.Create(cfg).Error)
		if !active {
			require.NoError(t, f.db.Model(cfg).Update("is_active", false).Error)
		}
		return seedBindingRole(t, f.db, "mciam-t045-cfg", &cfg.ID)
	}

	t.Run("attach: 소속 Config 비활성 → 400, 행·AWS 호출 없음", func(t *testing.T) {
		f := newBindingFixture(t)
		r := newRoleWithConfig(t, f, model.AuthMethodOIDC, false)
		p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
		_, err := f.svc.AttachPolicyToRole(r.ID, p.ID)
		requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
		assert.Contains(t, err.Error(), "is inactive")
		assert.Contains(t, err.Error(), "PUT /api/roles/csp/id/")
		assert.Empty(t, f.api.calls)
		assert.Nil(t, f.mapping(t, r.ID, p.ID))
	})
	t.Run("attach: 소속 Config SAML → 같은 계정 OIDC 로 대체", func(t *testing.T) {
		f := newBindingFixture(t)
		r := newRoleWithConfig(t, f, model.AuthMethodSAML, true)
		p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
		res, err := f.svc.AttachPolicyToRole(r.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingApplied, res.Result)
	})
	t.Run("detach: 소속 Config 비활성 → 대체해서 해제", func(t *testing.T) {
		f := newBindingFixture(t)
		r := newRoleWithConfig(t, f, model.AuthMethodOIDC, false)
		p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: r.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
		res, err := f.svc.DetachPolicyFromRole(r.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetached, res.Result)
	})
	t.Run("CspRole 삭제: 소속 Config 비활성 → 대체해서 정리", func(t *testing.T) {
		f := newBindingFixture(t)
		require.NoError(t, f.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
		r := newRoleWithConfig(t, f, model.AuthMethodOIDC, false)
		p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: r.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
		res, err := f.svc.DeleteCspRole(r.ID)
		require.NoError(t, err)
		assert.Nil(t, res)
		assert.Contains(t, f.api.calls, "detach:mciam-t045-cfg:"+testManagedArn)
	})
	t.Run("inline 문서 수정: 소속 Config 비활성 → 저장, 그 연결 failed + warnings", func(t *testing.T) {
		f := newBindingFixture(t)
		r := newRoleWithConfig(t, f, model.AuthMethodOIDC, false)
		p := f.policy(t, testInlineName, model.PolicyTypeInline, "", s3Doc())
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: r.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
		res, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc("ec2:DescribeRegions")})
		require.NoError(t, err)
		require.Len(t, res.BindingResults, 1)
		assert.Equal(t, model.PolicyBindingFailed, res.BindingResults[0].Result)
		assert.NotEmpty(t, res.Warnings)
		m := f.mapping(t, r.ID, p.ID)
		assert.Equal(t, model.PolicyAwsStatusFailed, m.AwsStatus)
		assert.Contains(t, m.LastError, "is inactive")
		assert.Empty(t, f.api.calls, "AWS 에 쓰지 않는다")
	})
}
