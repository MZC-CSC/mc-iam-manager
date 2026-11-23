package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeAwsPolicyAPI AWS 정책 연결 호출을 기록한다. 각 err 를 채우면 그 호출이 실패한다.
type fakeAwsPolicyAPI struct {
	calls                                   []string
	attachErr, detachErr, putErr, deleteErr error
	lastPutDoc, lastPutName, lastAttachArn  string
}

func (f *fakeAwsPolicyAPI) AttachRolePolicy(_ context.Context, in *iam.AttachRolePolicyInput, _ ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error) {
	f.calls = append(f.calls, "attach:"+aws.ToString(in.RoleName)+":"+aws.ToString(in.PolicyArn))
	f.lastAttachArn = aws.ToString(in.PolicyArn)
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	return &iam.AttachRolePolicyOutput{}, nil
}

func (f *fakeAwsPolicyAPI) DetachRolePolicy(_ context.Context, in *iam.DetachRolePolicyInput, _ ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error) {
	f.calls = append(f.calls, "detach:"+aws.ToString(in.RoleName)+":"+aws.ToString(in.PolicyArn))
	if f.detachErr != nil {
		return nil, f.detachErr
	}
	return &iam.DetachRolePolicyOutput{}, nil
}

func (f *fakeAwsPolicyAPI) PutRolePolicy(_ context.Context, in *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	f.calls = append(f.calls, "put:"+aws.ToString(in.RoleName)+":"+aws.ToString(in.PolicyName))
	f.lastPutDoc, f.lastPutName = aws.ToString(in.PolicyDocument), aws.ToString(in.PolicyName)
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &iam.PutRolePolicyOutput{}, nil
}

func (f *fakeAwsPolicyAPI) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	f.calls = append(f.calls, "delete:"+aws.ToString(in.RoleName)+":"+aws.ToString(in.PolicyName))
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &iam.DeleteRolePolicyOutput{}, nil
}

type bindingFixture struct {
	svc  *CspPolicyService
	db   *gorm.DB
	api  *fakeAwsPolicyAPI
	cfg  *model.CspIdpConfig
	role *model.CspRole
}

func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	db := setupTestDB(t)
	cfg := seedIdpConfig(t, db, "aws-oidc", "aws", model.AuthMethodOIDC, true,
		map[string]string{"role_arn": testManagementRoleArn, "oidc_provider_arn": testOidcProviderArn, "audience": "mciam-oidc-Client"})
	svc := NewCspPolicyService(db, nil)
	api := &fakeAwsPolicyAPI{}
	svc.awsPolicyClient = func(*model.CspIdpConfig) (awsPolicyAPI, error) { return api, nil }
	role := seedBindingRole(t, db, "mciam-t045-p1", &cfg.ID)
	return &bindingFixture{svc: svc, db: db, api: api, cfg: cfg, role: role}
}

func seedBindingRole(t *testing.T, db *gorm.DB, name string, cfgID *uint) *model.CspRole {
	t.Helper()
	role := &model.CspRole{Name: name, CspType: "aws", Status: "created", IamIdentifier: "arn:aws:iam::111111111111:role/" + name, CspIdpConfigID: cfgID}
	require.NoError(t, db.Create(role).Error)
	return role
}

func (f *bindingFixture) policy(t *testing.T, name string, typ model.PolicyType, arn string, doc map[string]interface{}) *model.CspPolicy {
	t.Helper()
	p := &model.CspPolicy{Name: name, CspAccountID: f.cfg.CspAccountID, PolicyType: typ, PolicyArn: arn, PolicyDoc: doc}
	require.NoError(t, f.db.Create(p).Error)
	return p
}

func (f *bindingFixture) mapping(t *testing.T, roleID, policyID uint) *model.CspRolePolicyMapping {
	t.Helper()
	var m model.CspRolePolicyMapping
	err := f.db.Where("csp_role_id = ? AND csp_policy_id = ?", roleID, policyID).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	require.NoError(t, err)
	return &m
}

func (f *bindingFixture) mappingCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&model.CspRolePolicyMapping{}).Count(&n).Error)
	return n
}

func requireBindingError(t *testing.T, err error, status int, code string) {
	t.Helper()
	be, ok := AsPolicyBindingError(err)
	require.True(t, ok, "PolicyBindingError 여야 함: %v", err)
	assert.Equal(t, status, be.Status, be.Msg)
	assert.Equal(t, code, be.Code, be.Msg)
}

const testManagedArn = "arn:aws:iam::111111111111:policy/mciam-t045-policy"

var noSuchEntity = &iamtypes.NoSuchEntityException{Message: aws.String("not found")}
var awsDenied = &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized"}

func TestAttachPolicy_ManagedAppliedInAws(t *testing.T) {
	f := newBindingFixture(t)
	p := f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, testManagedArn, nil)

	res, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PolicyBindingApplied, res.Result)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, []string{"attach:mciam-t045-p1:" + testManagedArn}, f.api.calls)
	m := f.mapping(t, f.role.ID, p.ID)
	require.NotNil(t, m)
	assert.Equal(t, model.PolicyAwsStatusApplied, m.AwsStatus)
	assert.NotNil(t, m.AppliedAt)

	// 다시 요청해도 멱등 (행 1개, AWS 재반영)
	res, err = f.svc.AttachPolicyToRole(f.role.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PolicyBindingApplied, res.Result)
	assert.Len(t, f.api.calls, 2)
	assert.EqualValues(t, 1, f.mappingCount(t))
}

func TestAttachPolicy_InlineSendsDocumentAsIs(t *testing.T) {
	f := newBindingFixture(t)
	doc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []interface{}{map[string]interface{}{
			"Sid": "ReadOnlyS3", "Effect": "Allow", "Action": "s3:GetObject", "Resource": "*",
			"Condition": map[string]interface{}{"StringEquals": map[string]interface{}{"aws:RequestedRegion": "ap-northeast-2"}},
		}},
	}
	p := f.policy(t, "mciam-t045-inline", model.PolicyTypeInline, "", doc)

	res, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PolicyBindingApplied, res.Result)
	assert.Equal(t, "mciam-t045-inline", f.api.lastPutName)
	var sent map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(f.api.lastPutDoc), &sent))
	assert.Equal(t, doc, sent, "Sid·Condition·문자열 Action 이 그대로 전달돼야 한다")
}

func TestAttachPolicy_AwsFailureRecordedAndRetried(t *testing.T) {
	f := newBindingFixture(t)
	p := f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, "arn:aws:iam::111111111111:policy/does-not-exist", nil)
	f.api.attachErr = noSuchEntity

	res, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
	require.NoError(t, err, "AWS 실패는 요청 오류가 아니다")
	assert.Equal(t, model.PolicyBindingFailed, res.Result)
	assert.Equal(t, "csp_error", res.ErrorCode)
	require.Len(t, res.Warnings, 1)
	m := f.mapping(t, f.role.ID, p.ID)
	require.NotNil(t, m)
	assert.Equal(t, model.PolicyAwsStatusFailed, m.AwsStatus)
	assert.Contains(t, m.LastError, "not found")
	assert.Nil(t, m.AppliedAt)

	// 원인을 고치고 다시 요청하면 applied
	f.api.attachErr = nil
	res, err = f.svc.AttachPolicyToRole(f.role.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PolicyBindingApplied, res.Result)
	m = f.mapping(t, f.role.ID, p.ID)
	assert.Equal(t, model.PolicyAwsStatusApplied, m.AwsStatus)
	assert.Empty(t, m.LastError)
	assert.NotNil(t, m.AppliedAt)
	assert.EqualValues(t, 1, f.mappingCount(t))
}

func TestAttachPolicy_LegacyDbOnlyMappingReapplied(t *testing.T) {
	f := newBindingFixture(t)
	p := f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, testManagedArn, nil)
	// 기존 구현이 남긴 DB 전용 행 (aws_status 빈 값)
	require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID}).Error)

	res, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PolicyBindingApplied, res.Result)
	assert.Len(t, f.api.calls, 1)
	assert.Equal(t, model.PolicyAwsStatusApplied, f.mapping(t, f.role.ID, p.ID).AwsStatus)
}

func TestDetachPolicy(t *testing.T) {
	t.Run("managed 해제 후 행 삭제", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, testManagedArn, nil)
		_, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		require.NoError(t, err)

		res, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetached, res.Result)
		assert.Equal(t, "detach:mciam-t045-p1:"+testManagedArn, f.api.calls[1])
		assert.Nil(t, f.mapping(t, f.role.ID, p.ID))
	})
	t.Run("inline 은 DeleteRolePolicy", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-inline", model.PolicyTypeInline, "", map[string]interface{}{"Version": "2012-10-17", "Statement": []interface{}{}})
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
		res, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetached, res.Result)
		assert.Equal(t, []string{"delete:mciam-t045-p1:mciam-t045-inline"}, f.api.calls)
	})
	t.Run("AWS NoSuchEntity 는 성공 (DB 전용 행 정리)", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, testManagedArn, nil)
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID}).Error)
		f.api.detachErr = noSuchEntity
		res, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetached, res.Result)
		assert.Nil(t, f.mapping(t, f.role.ID, p.ID))
	})
	t.Run("AWS 실패는 detach_failed 로 남기고 재요청으로 해제", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, testManagedArn, nil)
		_, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		require.NoError(t, err)
		f.api.detachErr = awsDenied

		res, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetachFailed, res.Result)
		require.Len(t, res.Warnings, 1)
		m := f.mapping(t, f.role.ID, p.ID)
		require.NotNil(t, m)
		assert.Equal(t, model.PolicyAwsStatusDetachFailed, m.AwsStatus)
		assert.Contains(t, m.LastError, "AccessDenied")
		assert.NotNil(t, m.AppliedAt, "applied_at 은 유지")

		f.api.detachErr = nil
		res, err = f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetached, res.Result)
		assert.Nil(t, f.mapping(t, f.role.ID, p.ID))
	})
	t.Run("매핑이 없으면 404, AWS 호출 없음", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-policy", model.PolicyTypeManaged, testManagedArn, nil)
		_, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		requireBindingError(t, err, http.StatusNotFound, "not_found")
		assert.Empty(t, f.api.calls)
	})
	t.Run("custom 은 AWS 호출 없이 행만 삭제", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-custom", model.PolicyTypeCustom, "", nil)
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID}).Error)
		res, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetached, res.Result)
		assert.Empty(t, f.api.calls)
		assert.Nil(t, f.mapping(t, f.role.ID, p.ID))
	})
	t.Run("거부 목록 정책도 해제는 허용", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "AdministratorAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/AdministratorAccess", nil)
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID}).Error)
		res, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingDetached, res.Result)
	})
}

// 요청 오류는 기록 없이 거절한다 (AWS 호출 0, 매핑 행 0)
func TestPolicyBinding_RejectedRequestsWriteNothing(t *testing.T) {
	adminDoc := func(action interface{}) map[string]interface{} {
		return map[string]interface{}{"Version": "2012-10-17", "Statement": []interface{}{map[string]interface{}{"Effect": "Allow", "Action": action, "Resource": "*"}}}
	}
	cases := []struct {
		name   string
		setup  func(t *testing.T, f *bindingFixture) (roleID, policyID uint)
		status int
		code   string
	}{
		{"Role 없음", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return 9999, f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil).ID
		}, http.StatusNotFound, "not_found"},
		{"정책 없음", func(t *testing.T, f *bindingFixture) (uint, uint) { return f.role.ID, 9999 }, http.StatusNotFound, "not_found"},
		{"관리용 Role", func(t *testing.T, f *bindingFixture) (uint, uint) {
			r := seedBindingRole(t, f.db, "mciam-platformadmin", &f.cfg.ID)
			return r.ID, f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil).ID
		}, http.StatusConflict, "protected"},
		{"mciam-* 가 아닌 Role", func(t *testing.T, f *bindingFixture) (uint, uint) {
			r := seedBindingRole(t, f.db, "shared-role", &f.cfg.ID)
			return r.ID, f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil).ID
		}, http.StatusForbidden, "forbidden_policy"},
		{"aws 가 아닌 Role", func(t *testing.T, f *bindingFixture) (uint, uint) {
			r := &model.CspRole{Name: "mciam-t045-gcp", CspType: "gcp", Status: "created"}
			require.NoError(t, f.db.Create(r).Error)
			return r.ID, f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil).ID
		}, http.StatusBadRequest, "invalid_request"},
		{"AdministratorAccess", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "AdministratorAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/AdministratorAccess", nil).ID
		}, http.StatusForbidden, "forbidden_policy"},
		{"IAMFullAccess", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "IAMFullAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/IAMFullAccess", nil).ID
		}, http.StatusForbidden, "forbidden_policy"},
		{"PowerUserAccess", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "PowerUserAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/PowerUserAccess", nil).ID
		}, http.StatusForbidden, "forbidden_policy"},
		{"inline Action *", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "mciam-t045-star", model.PolicyTypeInline, "", adminDoc("*")).ID
		}, http.StatusForbidden, "forbidden_policy"},
		{"inline Action iam:* (배열)", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "mciam-t045-iam", model.PolicyTypeInline, "", adminDoc([]interface{}{"s3:GetObject", "IAM:*"})).ID
		}, http.StatusForbidden, "forbidden_policy"},
		{"inline NotAction", func(t *testing.T, f *bindingFixture) (uint, uint) {
			doc := map[string]interface{}{"Statement": map[string]interface{}{"Effect": "Allow", "NotAction": "s3:*", "Resource": "*"}}
			return f.role.ID, f.policy(t, "mciam-t045-notaction", model.PolicyTypeInline, "", doc).ID
		}, http.StatusForbidden, "forbidden_policy"},
		{"custom 타입", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "c", model.PolicyTypeCustom, testManagedArn, nil).ID
		}, http.StatusBadRequest, "invalid_request"},
		{"managed ARN 없음", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "m", model.PolicyTypeManaged, "", nil).ID
		}, http.StatusBadRequest, "invalid_request"},
		{"inline 문서 없음", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "mciam-t045-nodoc", model.PolicyTypeInline, "", nil).ID
		}, http.StatusBadRequest, "invalid_request"},
		{"inline 이름 규칙 위반", func(t *testing.T, f *bindingFixture) (uint, uint) {
			return f.role.ID, f.policy(t, "bad name", model.PolicyTypeInline, "", adminDoc("s3:GetObject")).ID
		}, http.StatusBadRequest, "invalid_request"},
		{"계정 불일치", func(t *testing.T, f *bindingFixture) (uint, uint) {
			other := &model.CspAccount{Name: "other", CspType: "aws", AccountInfo: map[string]string{"account_id": "222222222222"}}
			require.NoError(t, f.db.Create(other).Error)
			p := &model.CspPolicy{Name: "p", CspAccountID: other.ID, PolicyType: model.PolicyTypeManaged, PolicyArn: "arn:aws:iam::222222222222:policy/x"}
			require.NoError(t, f.db.Create(p).Error)
			return f.role.ID, p.ID
		}, http.StatusBadRequest, "invalid_request"},
		{"소속 Config 없음 + 활성 Config 2개", func(t *testing.T, f *bindingFixture) (uint, uint) {
			seedIdpConfig(t, f.db, "aws-oidc-2", "aws", model.AuthMethodOIDC, true, map[string]string{"role_arn": "arn:aws:iam::111111111111:role/mciam-mgmt2"})
			r := seedBindingRole(t, f.db, "mciam-t045-nocfg", nil)
			return r.ID, f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil).ID
		}, http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBindingFixture(t)
			roleID, policyID := tc.setup(t, f)
			_, err := f.svc.AttachPolicyToRole(roleID, policyID)
			requireBindingError(t, err, tc.status, tc.code)
			assert.Empty(t, f.api.calls)
			assert.Zero(t, f.mappingCount(t))
		})
	}
}

func TestPolicyBinding_DetachGuards(t *testing.T) {
	f := newBindingFixture(t)
	mgmt := seedBindingRole(t, f.db, "mciam-platformadmin", &f.cfg.ID)
	shared := seedBindingRole(t, f.db, "shared-role", &f.cfg.ID)
	p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
	for _, r := range []*model.CspRole{mgmt, shared} {
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: r.ID, CspPolicyID: p.ID}).Error)
	}
	_, err := f.svc.DetachPolicyFromRole(mgmt.ID, p.ID)
	requireBindingError(t, err, http.StatusConflict, "protected")
	_, err = f.svc.DetachPolicyFromRole(shared.ID, p.ID)
	requireBindingError(t, err, http.StatusForbidden, "forbidden_policy")
	assert.Empty(t, f.api.calls)
	assert.EqualValues(t, 2, f.mappingCount(t))
}

func TestPolicyBinding_RoleWithoutConfigUsesOnlyActiveConfig(t *testing.T) {
	f := newBindingFixture(t)
	r := seedBindingRole(t, f.db, "mciam-t045-nocfg", nil)
	p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
	res, err := f.svc.AttachPolicyToRole(r.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PolicyBindingApplied, res.Result)
}

// DB 쓰기가 실패하면 500, AWS 는 반대 호출로 되돌린다
func TestPolicyBinding_DbFailureCompensates(t *testing.T) {
	failMappingWrites := func(t *testing.T, db *gorm.DB, op string) {
		boom := func(tx *gorm.DB) {
			if tx.Statement.Table == "mcmp_csp_role_policy_mappings" {
				_ = tx.AddError(errors.New("db down"))
			}
		}
		switch op {
		case "create":
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_mapping_create", boom))
		case "update":
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:fail_mapping_update", boom))
		case "delete":
			require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:fail_mapping_delete", boom))
		}
	}

	t.Run("attach: 새 매핑 저장 실패 → detach 보상", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
		failMappingWrites(t, f.db, "create")
		_, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		requireBindingError(t, err, http.StatusInternalServerError, "db_error")
		assert.Contains(t, err.Error(), "reverted")
		assert.Equal(t, []string{"attach:mciam-t045-p1:" + testManagedArn, "detach:mciam-t045-p1:" + testManagedArn}, f.api.calls)
	})
	t.Run("attach: 이미 applied 인 매핑은 보상하지 않는다", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
		failMappingWrites(t, f.db, "update")
		_, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		requireBindingError(t, err, http.StatusInternalServerError, "db_error")
		assert.Equal(t, []string{"attach:mciam-t045-p1:" + testManagedArn}, f.api.calls)
	})
	t.Run("detach: 행 삭제 실패 → 재연결 보상", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-inline", model.PolicyTypeInline, "", map[string]interface{}{"Version": "2012-10-17", "Statement": []interface{}{}})
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
		failMappingWrites(t, f.db, "delete")
		_, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		requireBindingError(t, err, http.StatusInternalServerError, "db_error")
		assert.Contains(t, err.Error(), "reverted")
		assert.Equal(t, []string{"delete:mciam-t045-p1:mciam-t045-inline", "put:mciam-t045-p1:mciam-t045-inline"}, f.api.calls)
	})
	t.Run("detach: AWS 에 없던(NoSuchEntity) 경우는 보상하지 않는다", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "p", model.PolicyTypeManaged, testManagedArn, nil)
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID}).Error)
		f.api.detachErr = noSuchEntity
		failMappingWrites(t, f.db, "delete")
		_, err := f.svc.DetachPolicyFromRole(f.role.ID, p.ID)
		requireBindingError(t, err, http.StatusInternalServerError, "db_error")
		assert.Len(t, f.api.calls, 1)
	})
}

func TestGetRolePoliciesWithStatus(t *testing.T) {
	f := newBindingFixture(t)
	ok := f.policy(t, "ok", model.PolicyTypeManaged, testManagedArn, nil)
	bad := f.policy(t, "bad", model.PolicyTypeManaged, "arn:aws:iam::111111111111:policy/missing", nil)
	_, err := f.svc.AttachPolicyToRole(f.role.ID, ok.ID)
	require.NoError(t, err)
	f.api.attachErr = noSuchEntity
	_, err = f.svc.AttachPolicyToRole(f.role.ID, bad.ID)
	require.NoError(t, err)

	list, err := f.svc.GetRolePoliciesWithStatus(f.role.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	byName := map[string]*model.CspRolePolicy{}
	for _, p := range list {
		byName[p.Name] = p
	}
	assert.Equal(t, model.PolicyAwsStatusApplied, byName["ok"].AwsStatus)
	assert.Equal(t, model.PolicyAwsStatusFailed, byName["bad"].AwsStatus)
	assert.NotEmpty(t, byName["bad"].LastError)

	// JSON 은 정책 필드와 상태 필드를 한 객체로 펼친다
	b, err := json.Marshal(byName["bad"])
	require.NoError(t, err)
	var flat map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &flat))
	assert.Equal(t, "bad", flat["name"])
	assert.Equal(t, "failed", flat["aws_status"])
}
