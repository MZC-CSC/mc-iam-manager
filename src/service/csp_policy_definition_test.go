package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func s3Doc(actions ...interface{}) map[string]interface{} {
	var action interface{} = "s3:ListAllMyBuckets"
	if len(actions) == 1 {
		action = actions[0]
	}
	return map[string]interface{}{"Version": "2012-10-17", "Statement": []interface{}{map[string]interface{}{"Effect": "Allow", "Action": action, "Resource": "*"}}}
}

func (f *bindingFixture) policyCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&model.CspPolicy{}).Count(&n).Error)
	return n
}

func (f *bindingFixture) reload(t *testing.T, id uint) *model.CspPolicy {
	t.Helper()
	var p model.CspPolicy
	err := f.db.First(&p, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	require.NoError(t, err)
	return &p
}

func failPolicyWrites(t *testing.T, db *gorm.DB) {
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_policy_create", func(tx *gorm.DB) {
		if tx.Statement.Table == "mcmp_csp_policies" {
			_ = tx.AddError(errors.New("db down"))
		}
	}))
}

func TestPolicyTypeForArn(t *testing.T) {
	assert.Equal(t, model.PolicyTypeManaged, model.PolicyTypeForArn("arn:aws:iam::aws:policy/ReadOnlyAccess"))
	assert.Equal(t, model.PolicyTypeManaged, model.PolicyTypeForArn("arn:aws:iam::aws:policy/service-role/AWSLambdaRole"))
	assert.Equal(t, model.PolicyTypeManaged, model.PolicyTypeForArn("arn:aws-cn:iam::aws:policy/ReadOnlyAccess"))
	assert.Equal(t, model.PolicyTypeCustom, model.PolicyTypeForArn("arn:aws:iam::111111111111:policy/mciam-x"))
	assert.Equal(t, model.PolicyTypeCustom, model.PolicyTypeForArn("arn:aws:iam::111111111111:policy/service-role/x"))
}

// custom: ARN 없이 문서로 만들면 AWS 에 mciam-* 로 생성
func TestCreateCustomPolicy_CreatesInAws(t *testing.T) {
	f := newBindingFixture(t)
	doc := s3Doc()
	p, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "t045-shared", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyDoc: doc, Description: "t045"})
	require.NoError(t, err)
	assert.Equal(t, "mciam-t045-shared", p.Name, "접두사를 붙인다")
	assert.Equal(t, "arn:aws:iam::111111111111:policy/mciam-t045-shared", p.PolicyArn)
	assert.Equal(t, []string{"getPolicy:" + p.PolicyArn, "createPolicy:mciam-t045-shared"}, f.api.calls)
	var sent map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(f.api.policies[p.PolicyArn].defaultDoc()), &sent))
	assert.Equal(t, doc, sent)
	assert.Equal(t, doc, f.reload(t, p.ID).PolicyDoc)
}

func TestCreateCustomPolicy_Rejections(t *testing.T) {
	t.Run("AWS 에 같은 이름 → 409, 아무것도 안 만듦", func(t *testing.T) {
		f := newBindingFixture(t)
		f.api.addPolicy("mciam-t045-dup", "arn:aws:iam::111111111111:policy/mciam-t045-dup", `{}`, 1)
		_, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "mciam-t045-dup", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyDoc: s3Doc()})
		requireBindingError(t, err, http.StatusConflict, "conflict")
		assert.Zero(t, f.policyCount(t))
		assert.NotContains(t, f.api.calls, "createPolicy:mciam-t045-dup")
	})
	t.Run("거부 문서 → 403, AWS 호출 없음", func(t *testing.T) {
		f := newBindingFixture(t)
		_, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "t045-admin", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyDoc: s3Doc("*")})
		requireBindingError(t, err, http.StatusForbidden, "forbidden_policy")
		assert.Empty(t, f.api.calls)
	})
	t.Run("문서 없음 → 400", func(t *testing.T) {
		f := newBindingFixture(t)
		_, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "t045-nodoc", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom})
		requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
	})
	t.Run("CreatePolicy 실패 → 502, DB 없음", func(t *testing.T) {
		f := newBindingFixture(t)
		f.api.createPolicyErr = awsDenied
		_, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "t045-denied", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyDoc: s3Doc()})
		requireBindingError(t, err, http.StatusBadGateway, "csp_error")
		assert.Zero(t, f.policyCount(t))
	})
	t.Run("DB 저장 실패 → DeletePolicy 로 보상, 500", func(t *testing.T) {
		f := newBindingFixture(t)
		failPolicyWrites(t, f.db)
		_, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "t045-dbfail", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyDoc: s3Doc()})
		requireBindingError(t, err, http.StatusInternalServerError, "db_error")
		assert.Contains(t, f.api.calls, "deletePolicy:arn:aws:iam::111111111111:policy/mciam-t045-dbfail")
		assert.Empty(t, f.api.policies)
	})
}

func TestCreatePolicy_RegisterAndTypeRules(t *testing.T) {
	f := newBindingFixture(t)
	acct := f.cfg.CspAccountID
	// custom ARN 등록: IAM 밖 이름도 등록(연결·해제용)은 된다
	p, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "CodeBuildPolicy", CspAccountID: acct, PolicyType: model.PolicyTypeCustom, PolicyArn: "arn:aws:iam::111111111111:policy/CodeBuildPolicy"})
	require.NoError(t, err)
	assert.Empty(t, f.api.calls, "등록은 AWS 에 쓰지 않는다")
	assert.Equal(t, model.PolicyTypeCustom, p.PolicyType)

	cases := []struct {
		name string
		req  model.CreateCspPolicyRequest
	}{
		{"다른 계정 ARN", model.CreateCspPolicyRequest{Name: "x", PolicyType: model.PolicyTypeCustom, PolicyArn: "arn:aws:iam::222222222222:policy/x"}},
		{"이름과 ARN 불일치", model.CreateCspPolicyRequest{Name: "y", PolicyType: model.PolicyTypeCustom, PolicyArn: "arn:aws:iam::111111111111:policy/x"}},
		{"custom 에 AWS 관리형 ARN", model.CreateCspPolicyRequest{Name: "ReadOnlyAccess", PolicyType: model.PolicyTypeCustom, PolicyArn: "arn:aws:iam::aws:policy/ReadOnlyAccess"}},
		{"ARN 등록에 문서", model.CreateCspPolicyRequest{Name: "x", PolicyType: model.PolicyTypeCustom, PolicyArn: "arn:aws:iam::111111111111:policy/x", PolicyDoc: s3Doc()}},
		{"managed 에 고객 관리형 ARN", model.CreateCspPolicyRequest{Name: "x", PolicyType: model.PolicyTypeManaged, PolicyArn: "arn:aws:iam::111111111111:policy/x"}},
		{"managed ARN 없음", model.CreateCspPolicyRequest{Name: "x", PolicyType: model.PolicyTypeManaged}},
		{"managed 에 문서", model.CreateCspPolicyRequest{Name: "ReadOnlyAccess", PolicyType: model.PolicyTypeManaged, PolicyArn: "arn:aws:iam::aws:policy/ReadOnlyAccess", PolicyDoc: s3Doc()}},
		{"inline 문서 없음", model.CreateCspPolicyRequest{Name: "mciam-t045-i", PolicyType: model.PolicyTypeInline}},
		{"inline 에 ARN", model.CreateCspPolicyRequest{Name: "mciam-t045-i", PolicyType: model.PolicyTypeInline, PolicyArn: "arn:aws:iam::111111111111:policy/x", PolicyDoc: s3Doc()}},
	}
	for _, tc := range cases {
		req := tc.req
		req.CspAccountID = acct
		_, err := f.svc.CreateCspPolicy(&req)
		requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
	}
	_, err = f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "mciam-t045-i", CspAccountID: acct, PolicyType: model.PolicyTypeInline, PolicyDoc: s3Doc([]interface{}{"iam:*"})})
	requireBindingError(t, err, http.StatusForbidden, "forbidden_policy")
	_, err = f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "ReadOnlyAccess", CspAccountID: acct, PolicyType: model.PolicyTypeManaged, PolicyArn: "arn:aws:iam::aws:policy/ReadOnlyAccess"})
	require.NoError(t, err)
	_, err = f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "ReadOnlyAccess", CspAccountID: acct, PolicyType: model.PolicyTypeManaged, PolicyArn: "arn:aws:iam::aws:policy/ReadOnlyAccess"})
	requireBindingError(t, err, http.StatusConflict, "conflict")
	assert.Empty(t, f.api.calls)
}

func (f *bindingFixture) createCustom(t *testing.T, name string) *model.CspPolicy {
	t.Helper()
	p, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: name, CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyDoc: s3Doc()})
	require.NoError(t, err)
	f.api.calls = nil
	return p
}

// custom 문서 수정: 새 기본 버전. 버전 5개면 가장 오래된 비기본 버전부터 지운다
func TestUpdateCustomPolicy_NewDefaultVersion(t *testing.T) {
	f := newBindingFixture(t)
	p := f.createCustom(t, "mciam-t045-v")
	newDoc := s3Doc([]interface{}{"s3:ListAllMyBuckets", "ec2:DescribeInstances"})
	res, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: newDoc})
	require.NoError(t, err)
	assert.Equal(t, newDoc, res.PolicyDoc)
	assert.Equal(t, newDoc, f.reload(t, p.ID).PolicyDoc)
	var awsDoc map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(f.api.policies[p.PolicyArn].defaultDoc()), &awsDoc))
	assert.Equal(t, newDoc, awsDoc)

	// 버전을 5개까지 채운 뒤 한 번 더 → 가장 오래된 v1 삭제 후 생성, 5개 유지
	for i := 0; i < 3; i++ {
		_, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc()})
		require.NoError(t, err)
	}
	require.Len(t, f.api.policies[p.PolicyArn].versions, 5)
	f.api.calls = nil
	_, err = f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: newDoc})
	require.NoError(t, err)
	assert.Contains(t, f.api.calls, "deletePolicyVersion:v1")
	assert.Len(t, f.api.policies[p.PolicyArn].versions, 5)
}

func TestUpdatePolicy_Rejections(t *testing.T) {
	t.Run("custom: mciam-* 아님 403, 거부 문서 403, AWS 실패 502 + DB 불변", func(t *testing.T) {
		f := newBindingFixture(t)
		outside, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "CodeBuildPolicy", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyArn: "arn:aws:iam::111111111111:policy/CodeBuildPolicy"})
		require.NoError(t, err)
		_, err = f.svc.UpdateCspPolicy(outside.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc()})
		requireBindingError(t, err, http.StatusForbidden, "forbidden_policy")
		assert.Empty(t, f.api.calls)

		p := f.createCustom(t, "mciam-t045-r")
		_, err = f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc("*")})
		requireBindingError(t, err, http.StatusForbidden, "forbidden_policy")
		f.api.createVersionErr = awsDenied
		_, err = f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc("ec2:DescribeInstances"), Description: "changed"})
		requireBindingError(t, err, http.StatusBadGateway, "csp_error")
		got := f.reload(t, p.ID)
		assert.Equal(t, s3Doc(), got.PolicyDoc, "DB 문서 불변")
		assert.Empty(t, got.Description, "다른 필드도 불변")
	})
	t.Run("이름·ARN 변경 규칙", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.createCustom(t, "mciam-t045-n")
		_, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{Name: "mciam-t045-renamed"})
		requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
		_, err = f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyArn: "arn:aws:iam::111111111111:policy/other"})
		requireBindingError(t, err, http.StatusBadRequest, "invalid_request")

		m := f.policy(t, "ReadOnlyAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/ReadOnlyAccess", nil)
		_, err = f.svc.UpdateCspPolicy(m.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc()})
		requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: m.ID, AwsStatus: model.PolicyAwsStatusApplied}).Error)
		_, err = f.svc.UpdateCspPolicy(m.ID, &model.UpdateCspPolicyRequest{PolicyArn: "arn:aws:iam::aws:policy/IAMReadOnlyAccess"})
		requireBindingError(t, err, http.StatusConflict, "conflict")
		// 설명은 연결 중에도 바꿀 수 있다
		_, err = f.svc.UpdateCspPolicy(m.ID, &model.UpdateCspPolicyRequest{Description: "label"})
		require.NoError(t, err)
		assert.Equal(t, "label", f.reload(t, m.ID).Description)
		assert.Equal(t, "arn:aws:iam::aws:policy/ReadOnlyAccess", f.reload(t, m.ID).PolicyArn)
	})
}

// inline 문서 수정: DB 갱신 후 연결된 Role 1개에 저장, 실패는 매핑 상태로
func TestUpdateInlinePolicy(t *testing.T) {
	t.Run("연결된 Role 에 새 문서 저장", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-in", model.PolicyTypeInline, "", s3Doc())
		_, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		require.NoError(t, err)
		newDoc := s3Doc("ec2:DescribeInstances")
		res, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: newDoc})
		require.NoError(t, err)
		require.Len(t, res.BindingResults, 1)
		assert.Equal(t, model.PolicyBindingApplied, res.BindingResults[0].Result)
		var sent map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(f.api.lastPutDoc), &sent))
		assert.Equal(t, newDoc, sent)
	})
	t.Run("Role 저장 실패 → 200 warnings, 매핑 failed, 재요청으로 맞춤", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-in", model.PolicyTypeInline, "", s3Doc())
		_, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		require.NoError(t, err)
		f.api.putErr = awsDenied
		res, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc("ec2:DescribeInstances")})
		require.NoError(t, err)
		assert.NotEmpty(t, res.Warnings)
		assert.Equal(t, s3Doc("ec2:DescribeInstances"), f.reload(t, p.ID).PolicyDoc, "DB 문서는 갱신")
		assert.Equal(t, model.PolicyAwsStatusFailed, f.mapping(t, f.role.ID, p.ID).AwsStatus)
		f.api.putErr = nil
		again, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingApplied, again.Result)
	})
	t.Run("여러 Role 에 연결된 기존 inline → 문서 수정 409, 연결 중 이름 변경 409", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-in", model.PolicyTypeInline, "", s3Doc())
		other := seedBindingRole(t, f.db, "mciam-t045-other", &f.cfg.ID)
		for _, r := range []uint{f.role.ID, other.ID} {
			require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: r, CspPolicyID: p.ID}).Error)
		}
		_, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc("ec2:DescribeInstances")})
		requireBindingError(t, err, http.StatusConflict, "conflict")
		_, err = f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{Name: "mciam-t045-in2"})
		requireBindingError(t, err, http.StatusConflict, "conflict")
		assert.Empty(t, f.api.calls)
		assert.Equal(t, s3Doc(), f.reload(t, p.ID).PolicyDoc)
	})
	t.Run("연결 없는 inline 은 DB 만", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-in", model.PolicyTypeInline, "", s3Doc())
		_, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{Name: "mciam-t045-in2", PolicyDoc: s3Doc("ec2:DescribeInstances")})
		require.NoError(t, err)
		assert.Empty(t, f.api.calls)
		assert.Equal(t, "mciam-t045-in2", f.reload(t, p.ID).Name)
	})
}

// custom 은 ARN 으로 연결·해제 (여러 Role 공유)
func TestCustomPolicyBinding(t *testing.T) {
	f := newBindingFixture(t)
	p := f.createCustom(t, "mciam-t045-c")
	other := seedBindingRole(t, f.db, "mciam-t045-other", &f.cfg.ID)
	for _, r := range []uint{f.role.ID, other.ID} {
		res, err := f.svc.AttachPolicyToRole(r, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingApplied, res.Result)
	}
	assert.Equal(t, []string{"attach:mciam-t045-p1:" + p.PolicyArn, "attach:mciam-t045-other:" + p.PolicyArn}, f.api.calls)
	res, err := f.svc.DetachPolicyFromRole(other.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PolicyBindingDetached, res.Result)
	assert.Equal(t, "detach:mciam-t045-other:"+p.PolicyArn, f.api.calls[2])
}

func TestDeletePolicy(t *testing.T) {
	t.Run("custom mciam-*: 비기본 버전 → 정책 삭제 → DB 삭제", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.createCustom(t, "mciam-t045-d")
		_, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyDoc: s3Doc("ec2:DescribeInstances")})
		require.NoError(t, err)
		require.NoError(t, f.svc.DeleteCspPolicy(p.ID))
		assert.Nil(t, f.reload(t, p.ID))
		assert.Empty(t, f.api.policies)
		assert.Contains(t, f.api.calls, "deletePolicyVersion:v1")
	})
	t.Run("IAM 밖 연결(AttachmentCount>0) → 409, 유지", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.createCustom(t, "mciam-t045-d")
		f.api.policies[p.PolicyArn].attachments = 1
		err := f.svc.DeleteCspPolicy(p.ID)
		requireBindingError(t, err, http.StatusConflict, "conflict")
		assert.NotNil(t, f.reload(t, p.ID))
		assert.NotEmpty(t, f.api.policies)
	})
	t.Run("IAM 매핑 있음 → 409, AWS 호출 없음", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.createCustom(t, "mciam-t045-d")
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID}).Error)
		err := f.svc.DeleteCspPolicy(p.ID)
		requireBindingError(t, err, http.StatusConflict, "conflict")
		assert.Empty(t, f.api.calls)
	})
	t.Run("AWS 에 이미 없음 → DB 만 삭제", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.createCustom(t, "mciam-t045-d")
		delete(f.api.policies, p.PolicyArn)
		require.NoError(t, f.svc.DeleteCspPolicy(p.ID))
		assert.Nil(t, f.reload(t, p.ID))
	})
	t.Run("AWS 실패 → 502, 유지", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.createCustom(t, "mciam-t045-d")
		f.api.deletePolicyErr = awsDenied
		err := f.svc.DeleteCspPolicy(p.ID)
		requireBindingError(t, err, http.StatusBadGateway, "csp_error")
		assert.NotNil(t, f.reload(t, p.ID))
	})
	t.Run("IAM 밖 custom·managed·inline 은 등록만 해제 (AWS 호출 없음)", func(t *testing.T) {
		f := newBindingFixture(t)
		outside, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "CodeBuildPolicy", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyArn: "arn:aws:iam::111111111111:policy/CodeBuildPolicy"})
		require.NoError(t, err)
		m := f.policy(t, "ReadOnlyAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/ReadOnlyAccess", nil)
		i := f.policy(t, "mciam-t045-in", model.PolicyTypeInline, "", s3Doc())
		for _, id := range []uint{outside.ID, m.ID, i.ID} {
			require.NoError(t, f.svc.DeleteCspPolicy(id))
		}
		assert.Empty(t, f.api.calls)
		assert.Zero(t, f.policyCount(t))
	})
	t.Run("없는 정책 404", func(t *testing.T) {
		f := newBindingFixture(t)
		requireBindingError(t, f.svc.DeleteCspPolicy(999), http.StatusNotFound, "not_found")
	})
}

func TestAccountPolicyClient_NeedsOneOidcConfig(t *testing.T) {
	f := newBindingFixture(t)
	seed := &model.CspIdpConfig{Name: "aws-oidc-2", CspAccountID: f.cfg.CspAccountID, AuthMethod: model.AuthMethodOIDC, IsActive: true, Config: map[string]string{"role_arn": "arn:aws:iam::111111111111:role/mciam-mgmt2"}}
	require.NoError(t, f.db.Create(seed).Error)
	_, err := f.svc.CreateCspPolicy(&model.CreateCspPolicyRequest{Name: "t045-two", CspAccountID: f.cfg.CspAccountID, PolicyType: model.PolicyTypeCustom, PolicyDoc: s3Doc()})
	requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
	assert.Empty(t, f.api.calls)
	_ = aws.String
}

// IAM-BUG-058: AWS 에 붙지 않은(failed) 연결만 있으면 ARN·inline 이름을 바꿀 수 있다
func TestUpdatePolicy_FailedBindingsDoNotBlockChange(t *testing.T) {
	t.Run("managed: 없는 ARN 으로 failed → ARN 수정 허용 → 재요청 applied", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "AmazonS3ReadOnlyAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/DoesNotExist", nil)
		f.api.attachErr = noSuchEntity
		res, err := f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		require.NoError(t, err)
		require.Equal(t, model.PolicyBindingFailed, res.Result)
		f.api.attachErr = nil

		_, err = f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyArn: "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"})
		require.NoError(t, err)
		res, err = f.svc.AttachPolicyToRole(f.role.ID, p.ID)
		require.NoError(t, err)
		assert.Equal(t, model.PolicyBindingApplied, res.Result)
		assert.Equal(t, "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess", f.api.lastAttachArn)
	})
	t.Run("detach_failed·상태 빈 이전 연결은 AWS 에 남아 있을 수 있어 409", func(t *testing.T) {
		for _, status := range []string{model.PolicyAwsStatusDetachFailed, ""} {
			f := newBindingFixture(t)
			p := f.policy(t, "ReadOnlyAccess", model.PolicyTypeManaged, "arn:aws:iam::aws:policy/ReadOnlyAccess", nil)
			require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID, AwsStatus: status}).Error)
			_, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{PolicyArn: "arn:aws:iam::aws:policy/IAMReadOnlyAccess"})
			requireBindingError(t, err, http.StatusConflict, "conflict")
		}
	})
	t.Run("inline: failed 연결만 있으면 이름 변경 허용", func(t *testing.T) {
		f := newBindingFixture(t)
		p := f.policy(t, "mciam-t045-in", model.PolicyTypeInline, "", s3Doc())
		require.NoError(t, f.db.Create(&model.CspRolePolicyMapping{CspRoleID: f.role.ID, CspPolicyID: p.ID, AwsStatus: model.PolicyAwsStatusFailed}).Error)
		_, err := f.svc.UpdateCspPolicy(p.ID, &model.UpdateCspPolicyRequest{Name: "mciam-t045-in2"})
		require.NoError(t, err)
		assert.Equal(t, "mciam-t045-in2", f.reload(t, p.ID).Name)
	})
}

func TestPolicyLookups_NotFoundErrorCode(t *testing.T) {
	f := newBindingFixture(t)
	_, err := f.svc.GetCspPolicyByID(999)
	requireBindingError(t, err, http.StatusNotFound, "not_found")
	_, err = f.svc.GetPolicyDocument(nil, 999)
	requireBindingError(t, err, http.StatusNotFound, "not_found")
	p := f.policy(t, "mciam-t045-c", model.PolicyTypeCustom, "", nil)
	_, err = f.svc.GetPolicyDocument(nil, p.ID)
	requireBindingError(t, err, http.StatusNotFound, "not_found")
	_, err = f.svc.SyncPoliciesFromCloud(nil, &model.SyncPoliciesRequest{CspAccountID: 999})
	requireBindingError(t, err, http.StatusBadRequest, "invalid_request")
}
