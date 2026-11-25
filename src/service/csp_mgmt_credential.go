package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/m-cmp/mc-iam-manager/model"
	"github.com/m-cmp/mc-iam-manager/repository"
)

// 관리용 자격증명 (IAM-TECH-045 M2)
//
// IAM 이 AWS 를 호출할 때는 IdP Config 의 관리용 Role(role_arn)을 AssumeRoleWithIdpConfig 로 assume 한다.
// 이전의 system 자격증명(첫 활성 Config 로 발급, TempCredential 캐시 키에 Config 없음)은 쓰지 않는다.
// 발급한 자격증명은 Config(ID·role_arn)별로 메모리에 캐시하고, 만료 5분 전까지 재사용한다.

const mgmtCredentialRefreshBefore = 5 * time.Minute

type mgmtCredentialEntry struct {
	cfg     aws.Config
	expires time.Time
}

type mgmtCredentialCache struct {
	mu      sync.Mutex
	entries map[string]mgmtCredentialEntry
}

var mgmtCredentials = &mgmtCredentialCache{entries: map[string]mgmtCredentialEntry{}}

// mgmtCredentialKey 캐시 키: Config 를 구분한다 (계정이 여럿이어도 다른 계정의 자격증명을 재사용하지 않는다)
func mgmtCredentialKey(cfg *model.CspIdpConfig) string {
	return fmt.Sprintf("%d|%s", cfg.ID, cfg.Config["role_arn"])
}

// get 캐시에 유효한 자격증명이 있으면 쓰고, 없으면 issue 로 발급해 넣는다
func (c *mgmtCredentialCache) get(key string, now time.Time, issue func() (aws.Config, time.Time, error)) (aws.Config, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Add(mgmtCredentialRefreshBefore).Before(e.expires) {
		c.mu.Unlock()
		return e.cfg, nil
	}
	c.mu.Unlock()
	awsCfg, expires, err := issue()
	if err != nil {
		return aws.Config{}, err
	}
	c.mu.Lock()
	c.entries[key] = mgmtCredentialEntry{cfg: awsCfg, expires: expires}
	c.mu.Unlock()
	return awsCfg, nil
}

// managementIamClient Config 의 관리용 Role 로 만든 IAM 클라이언트 (Config 별 캐시)
func managementIamClient(ctx context.Context, idpConfigService *CspIdpConfigService, cfg *model.CspIdpConfig) (*iam.Client, error) {
	if cfg == nil || cfg.Config == nil || cfg.Config["role_arn"] == "" {
		return nil, fmt.Errorf("IdP config has no role_arn for the management role")
	}
	awsCfg, err := mgmtCredentials.get(mgmtCredentialKey(cfg), time.Now(), func() (aws.Config, time.Time, error) {
		tempCred, err := idpConfigService.AssumeRoleWithIdpConfig(ctx, cfg.ID, cfg.Config["role_arn"], policyBindingSessionName, 3600)
		if err != nil {
			return aws.Config{}, time.Time{}, fmt.Errorf("failed to assume management role of IdP config %d: %w", cfg.ID, err)
		}
		awsCfg, err := config.LoadDefaultConfig(ctx,
			config.WithRegion(tempCred.Region),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(tempCred.AccessKeyId, tempCred.SecretAccessKey, tempCred.SessionToken)),
		)
		if err != nil {
			return aws.Config{}, time.Time{}, fmt.Errorf("failed to load AWS config: %w", err)
		}
		return awsCfg, tempCred.ExpiresAt, nil
	})
	if err != nil {
		return nil, err
	}
	return iam.NewFromConfig(awsCfg), nil
}

// usableManagementConfig 관리용 Role 을 assume 할 수 있는 Config 인가 (활성 aws OIDC, role_arn 있음)
func usableManagementConfig(cfg *model.CspIdpConfig) bool {
	return cfg != nil && cfg.IsActive && cfg.AuthMethod == model.AuthMethodOIDC && cfg.CspAccount != nil &&
		cfg.CspAccount.CspType == "aws" && cfg.Config != nil && cfg.Config["role_arn"] != ""
}

// managementConfigFor 대상 Role 의 소속 Config(SAML 일 수 있다)로 관리용 자격증명을 발급할 Config 를 정한다.
// 소속 Config 가 쓸 수 있으면 그것, 아니면 같은 CSP 계정의 활성 aws OIDC Config 가 1개일 때 그것 (IAM-BUG-057 의 대체 규칙과 같다).
func managementConfigFor(idpConfigRepo *repository.CspIdpConfigRepository, cfg *model.CspIdpConfig) (*model.CspIdpConfig, error) {
	if usableManagementConfig(cfg) {
		return cfg, nil
	}
	if cfg == nil {
		return nil, fmt.Errorf("IdP config is required to call AWS")
	}
	configs, err := idpConfigRepo.GetActiveByAccountID(cfg.CspAccountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list IdP configs: %w", err)
	}
	var candidates []*model.CspIdpConfig
	for _, c := range configs {
		if c.CspAccount == nil && cfg.CspAccount != nil {
			c.CspAccount = cfg.CspAccount
		}
		if usableManagementConfig(c) {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("IdP config %d (%s) cannot assume the management role and %d active aws OIDC IdP configs were found for CSP account %d",
			cfg.ID, cfg.Name, len(candidates), cfg.CspAccountID)
	}
	log.Printf("[WARN] IdP config %d (%s) cannot assume the management role, using active aws OIDC IdP config %d of the same account", cfg.ID, cfg.Name, candidates[0].ID)
	return candidates[0], nil
}
