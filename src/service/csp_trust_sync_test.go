package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTrustOidcAud = "mciam-oidc-Client"
	testTrustAudKey  = "mcmp.example.com/auth/realms/mciam:aud"
)

func boolPtr(v bool) *bool { return &v }

func requireTrustSyncError(t *testing.T, err error, status int, code string) *TrustSyncError {
	t.Helper()
	te, ok := AsTrustSyncError(err)
	require.True(t, ok, "TrustSyncError 여야 함: %v", err)
	assert.Equal(t, status, te.Status, te.Msg)
	assert.Equal(t, code, te.Code, te.Msg)
	return te
}

func (f *fakeAwsRoleAPI) trustDoc(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	doc, err := decodeTrustDoc(aws.ToString(f.roles[name].AssumeRolePolicyDocument))
	require.NoError(t, err)
	return doc
}

func TestUpsertTrustStatements(t *testing.T) {
	exp := newTrustExpectation("OIDC", oidcTestConfig())
	t.Run("없으면 추가, 다른 statement 보존", func(t *testing.T) {
		doc, _ := decodeTrustDoc(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
		changes := upsertTrustStatements(doc, []trustExpectation{exp})
		require.Len(t, changes, 1)
		assert.Equal(t, "statement_added", changes[0].Change)
		sts := doc["Statement"].([]interface{})
		require.Len(t, sts, 2)
		assert.Equal(t, "ec2.amazonaws.com", sts[0].(map[string]interface{})["Principal"].(map[string]interface{})["Service"])
		assert.Equal(t, model.TrustPolicyOK, evaluateTrustPolicy("x", mustJSON(doc), []trustExpectation{exp}).Status)
	})
	t.Run("Action·aud 만 고치고 다른 Action·Condition 키 보존, 다른 연산자의 aud 제거", func(t *testing.T) {
		doc, _ := decodeTrustDoc(`{"Statement":{"Effect":"Allow","Principal":{"Federated":"` + testOidcProviderArn + `"},"Action":"sts:TagSession","Condition":{"StringEquals":{"` + testTrustAudKey + `":"wrong"},"StringLike":{"` + testTrustAudKey + `":"*","x:sub":"user-*"}}}}`)
		changes := upsertTrustStatements(doc, []trustExpectation{exp})
		kinds := []string{}
		for _, c := range changes {
			kinds = append(kinds, c.Change)
		}
		assert.ElementsMatch(t, []string{"action_added", "aud_removed", "aud_set"}, kinds)
		st := doc["Statement"].([]interface{})[0].(map[string]interface{})
		assert.Equal(t, []interface{}{"sts:TagSession", "sts:AssumeRoleWithWebIdentity"}, st["Action"])
		cond := st["Condition"].(map[string]interface{})
		assert.Equal(t, testTrustOidcAud, cond["StringEquals"].(map[string]interface{})[testTrustAudKey])
		assert.Equal(t, map[string]interface{}{"x:sub": "user-*"}, cond["StringLike"], "다른 Condition 키 보존")
	})
	t.Run("SAML statement 추가", func(t *testing.T) {
		doc, _ := decodeTrustDoc(oidcOkDoc)
		saml := newTrustExpectation("SAML", samlTestConfig())
		changes := upsertTrustStatements(doc, []trustExpectation{newTrustExpectation("OIDC", oidcTestConfig()), saml})
		require.Len(t, changes, 1)
		assert.Equal(t, "SAML", changes[0].AuthMethod)
		assert.Equal(t, model.TrustPolicyOK, evaluateTrustPolicy("x", mustJSON(doc), []trustExpectation{exp, saml}).Status)
	})
}

type trustSyncFixture struct {
	svc  *CspRoleService
	api  *fakeAwsRoleAPI
	cfg  *model.CspIdpConfig
	role *model.CspRole
}

func newTrustSyncFixture(t *testing.T, origin, trustDoc string) *trustSyncFixture {
	t.Helper()
	svc, api, cfg := newTargetTestService(t, nil)
	require.NoError(t, svc.db.AutoMigrate(&model.RoleMaster{}, &model.RoleMasterCspRoleMapping{}))
	role := &model.CspRole{Name: "mciam-t045-sy", CspType: "aws", Status: "created", Origin: origin,
		IamIdentifier: "arn:aws:iam::111111111111:role/mciam-t045-sy", CspIdpConfigID: &cfg.ID, CspAccountID: &cfg.CspAccountID}
	require.NoError(t, svc.db.Create(role).Error)
	if trustDoc != "" {
		api.roles["mciam-t045-sy"] = fakeAwsRole("mciam-t045-sy", trustDoc)
	}
	return &trustSyncFixture{svc: svc, api: api, cfg: cfg, role: role}
}

const noAudDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"` + testOidcProviderArn + `"},"Action":"sts:AssumeRoleWithWebIdentity"},{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

func TestSyncTargetRoleTrustPolicy(t *testing.T) {
	ctx := context.TODO()
	t.Run("dryRun 기본: 변경안만, 쓰지 않음", func(t *testing.T) {
		f := newTrustSyncFixture(t, model.CspRoleOriginCreated, noAudDoc)
		res, err := f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, &model.TrustPolicySyncRequest{})
		require.NoError(t, err)
		assert.True(t, res.DryRun)
		assert.False(t, res.Applied)
		assert.Equal(t, model.TrustPolicyMismatch, res.Verdict)
		require.Len(t, res.Changes, 1)
		assert.Equal(t, "aud_set", res.Changes[0].Change)
		assert.NotNil(t, res.Before)
		assert.NotNil(t, res.After)
		assert.Empty(t, f.api.updateCalls)
	})
	t.Run("dryRun=false: 반영, 다른 statement 보존", func(t *testing.T) {
		f := newTrustSyncFixture(t, model.CspRoleOriginCreated, noAudDoc)
		res, err := f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, &model.TrustPolicySyncRequest{DryRun: boolPtr(false)})
		require.NoError(t, err)
		assert.True(t, res.Applied)
		require.Len(t, f.api.updateCalls, 1)
		doc := f.api.trustDoc(t, "mciam-t045-sy")
		assert.Len(t, doc["Statement"].([]interface{}), 2)
		check := evaluateTrustPolicy("x", mustJSON(doc), []trustExpectation{newTrustExpectation("OIDC", f.cfg)})
		assert.Equal(t, model.TrustPolicyOK, check.Status)
		assert.Len(t, check.Extra, 1, "ec2 statement 보존")
	})
	t.Run("이미 ok 면 registered 라도 200, 쓰지 않음", func(t *testing.T) {
		f := newTrustSyncFixture(t, model.CspRoleOriginRegistered, oidcOkDoc)
		res, err := f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, &model.TrustPolicySyncRequest{DryRun: boolPtr(false)})
		require.NoError(t, err)
		assert.False(t, res.Applied)
		assert.Empty(t, res.Changes)
		assert.Empty(t, f.api.updateCalls)
	})
	t.Run("registered·빈 origin 에 바꿀 것이 있으면 409 registered_role(변경안 포함), allowRegistered 로 반영", func(t *testing.T) {
		for _, origin := range []string{model.CspRoleOriginRegistered, ""} {
			f := newTrustSyncFixture(t, origin, noAudDoc)
			_, err := f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, &model.TrustPolicySyncRequest{DryRun: boolPtr(false)})
			te := requireTrustSyncError(t, err, http.StatusConflict, "registered_role")
			require.NotNil(t, te.Result)
			assert.Len(t, te.Result.Changes, 1)
			assert.Contains(t, te.Msg, "allowRegistered=true")
			assert.Empty(t, f.api.updateCalls)

			// dryRun 은 registered 여도 변경안을 보여 준다
			res, err := f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, &model.TrustPolicySyncRequest{})
			require.NoError(t, err)
			assert.Len(t, res.Changes, 1)

			res, err = f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, &model.TrustPolicySyncRequest{DryRun: boolPtr(false), AllowRegistered: true})
			require.NoError(t, err)
			assert.True(t, res.Applied)
		}
	})
	t.Run("관리용 Role 409, 비활성 Config 400, AWS 에 없음 404, 기대값 불가 400", func(t *testing.T) {
		f := newTrustSyncFixture(t, model.CspRoleOriginCreated, "")
		mgmt := &model.CspRole{Name: "mciam-platformadmin", CspType: "aws", Status: "created", CspIdpConfigID: &f.cfg.ID}
		require.NoError(t, f.svc.db.Create(mgmt).Error)
		_, err := f.svc.SyncTargetRoleTrustPolicy(ctx, mgmt.ID, nil)
		requireTrustSyncError(t, err, http.StatusConflict, "protected")

		_, err = f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, nil)
		requireTrustSyncError(t, err, http.StatusNotFound, "not_found")

		_, err = f.svc.SyncTargetRoleTrustPolicy(ctx, 9999, nil)
		requireTrustSyncError(t, err, http.StatusNotFound, "not_found")

		require.NoError(t, f.svc.db.Model(f.cfg).Update("is_active", false).Error)
		_, err = f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, nil)
		te := requireTrustSyncError(t, err, http.StatusBadRequest, "invalid_request")
		assert.Contains(t, te.Msg, "is inactive")
	})
	t.Run("Master 매핑에 SAML 이 있는데 SAML Config 가 없으면 400", func(t *testing.T) {
		f := newTrustSyncFixture(t, model.CspRoleOriginCreated, oidcOkDoc)
		require.NoError(t, f.svc.db.Create(&model.RoleMasterCspRoleMapping{RoleID: 1, CspRoleID: f.role.ID, AuthMethod: "SAML"}).Error)
		_, err := f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, nil)
		requireTrustSyncError(t, err, http.StatusBadRequest, "invalid_request")
	})
	t.Run("UpdateAssumeRolePolicy 실패 502", func(t *testing.T) {
		f := newTrustSyncFixture(t, model.CspRoleOriginCreated, noAudDoc)
		f.api.updateErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
		_, err := f.svc.SyncTargetRoleTrustPolicy(ctx, f.role.ID, &model.TrustPolicySyncRequest{DryRun: boolPtr(false)})
		requireTrustSyncError(t, err, http.StatusBadGateway, "csp_error")
	})
}

// Config 일괄: 그 Config 소속 Role 만, 그 Config 의 방식 statement 만. 한 항목이 실패해도 계속
func TestIdpConfigTrustPolicy_Bulk(t *testing.T) {
	ctx := context.TODO()
	f := newTrustSyncFixture(t, model.CspRoleOriginCreated, noAudDoc) // would_change / applied
	mk := func(name, origin, doc string) *model.CspRole {
		r := &model.CspRole{Name: name, CspType: "aws", Status: "created", Origin: origin, IamIdentifier: "arn:aws:iam::111111111111:role/" + name, CspIdpConfigID: &f.cfg.ID}
		require.NoError(t, f.svc.db.Create(r).Error)
		if doc != "" {
			f.api.roles[name] = fakeAwsRole(name, doc)
		}
		return r
	}
	mk("mciam-t045-ok", model.CspRoleOriginCreated, oidcOkDoc)
	mk("mciam-t045-reg", model.CspRoleOriginRegistered, noAudDoc)
	mk("mciam-t045-gone", model.CspRoleOriginCreated, "")
	mgmt := &model.CspRole{Name: "mciam-platformadmin", CspType: "aws", Status: "created", CspIdpConfigID: &f.cfg.ID}
	require.NoError(t, f.svc.db.Create(mgmt).Error)
	other := seedIdpConfig(t, f.svc.db, "other", "aws", model.AuthMethodOIDC, true, map[string]string{"role_arn": testManagementRoleArn})
	outside := &model.CspRole{Name: "mciam-t045-outside", CspType: "aws", Status: "created", CspIdpConfigID: &other.ID}
	require.NoError(t, f.svc.db.Create(outside).Error)

	statusOf := func(res *model.TrustPolicyBulkResult) map[string]string {
		m := map[string]string{}
		for _, it := range res.Items {
			m[it.RoleName] = it.Status
		}
		return m
	}

	check, err := f.svc.IdpConfigTrustPolicy(ctx, f.cfg.ID, false, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"mciam-t045-sy": "mismatch", "mciam-t045-ok": "ok", "mciam-t045-reg": "mismatch", "mciam-t045-gone": "not_found", "mciam-platformadmin": "protected"}, statusOf(check), "다른 Config 소속은 대상이 아니다")

	dry, err := f.svc.IdpConfigTrustPolicy(ctx, f.cfg.ID, true, &model.TrustPolicySyncRequest{})
	require.NoError(t, err)
	assert.True(t, dry.DryRun)
	assert.Equal(t, map[string]string{"mciam-t045-sy": "would_change", "mciam-t045-ok": "unchanged", "mciam-t045-reg": "would_change", "mciam-t045-gone": "not_found", "mciam-platformadmin": "protected"}, statusOf(dry))
	assert.Empty(t, f.api.updateCalls)

	applied, err := f.svc.IdpConfigTrustPolicy(ctx, f.cfg.ID, true, &model.TrustPolicySyncRequest{DryRun: boolPtr(false)})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"mciam-t045-sy": "applied", "mciam-t045-ok": "unchanged", "mciam-t045-reg": "registered_role", "mciam-t045-gone": "not_found", "mciam-platformadmin": "protected"}, statusOf(applied), "부분 실패여도 나머지는 처리")
	assert.Len(t, f.api.updateCalls, 1)
	assert.NotEmpty(t, applied.Warnings)
	for _, it := range applied.Items {
		if it.RoleName == "mciam-t045-reg" {
			require.NotNil(t, it.Sync, "registered_role 항목에도 변경안")
			assert.Len(t, it.Sync.Changes, 1)
		}
	}

	require.NoError(t, f.svc.db.Model(f.cfg).Update("is_active", false).Error)
	_, err = f.svc.IdpConfigTrustPolicy(ctx, f.cfg.ID, true, nil)
	requireTrustSyncError(t, err, http.StatusBadRequest, "invalid_request")
	_, err = f.svc.IdpConfigTrustPolicy(ctx, f.cfg.ID, false, nil)
	assert.NoError(t, err, "비활성이어도 check 는 진행")
	_, err = f.svc.IdpConfigTrustPolicy(ctx, 9999, false, nil)
	requireTrustSyncError(t, err, http.StatusNotFound, "not_found")
}

func TestGetAwsRoleView(t *testing.T) {
	svc, api, cfg := newTargetTestService(t, nil)
	api.roles["mciam-t045-v"] = fakeAwsRole("mciam-t045-v", oidcOkDoc)
	view, err := svc.GetAwsRoleView(context.TODO(), cfg.CspAccountID, "mciam-t045-v")
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::111111111111:role/mciam-t045-v", view.Arn)
	b, _ := json.Marshal(view.TrustPolicy)
	assert.Contains(t, string(b), "Federated", "신뢰 정책 원문 그대로")
	_, err = svc.GetAwsRoleView(context.TODO(), cfg.CspAccountID, "missing")
	requireTrustSyncError(t, err, http.StatusNotFound, "not_found")
	_, err = svc.GetAwsRoleView(context.TODO(), 9999, "mciam-t045-v")
	requireTrustSyncError(t, err, http.StatusBadRequest, "invalid_request")
}
