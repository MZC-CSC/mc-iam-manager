package service

import (
	"testing"
	"time"

	"github.com/m-cmp/mc-iam-manager/model/mcmpapi"
	"github.com/m-cmp/mc-iam-manager/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMcmpApiService(t *testing.T) *mcmpApiService {
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&mcmpapi.McmpApiServiceMeta{}))
	return &mcmpApiService{db: db, repo: repository.NewMcmpApiRepository(db)}
}

// TestShouldUpdateService_UnchangedMeta_Skips: IAM-TECH-021 회귀 방지.
// 레지스트리 YAML을 고쳐도 서비스의 _meta(version/generatedAt)를 갱신하지 않으면
// syncServicesAndActions가 해당 서비스의 액션 전체를 조용히 스킵한다(IAM-TECH-020이
// 배포+수동 동기화 후에도 반영되지 않았던 원인). generatedAt이 그대로면 false를
// 반환해야 한다.
func TestShouldUpdateService_UnchangedMeta_Skips(t *testing.T) {
	s := newTestMcmpApiService(t)

	generatedAt := time.Date(2026, 6, 1, 2, 11, 43, 0, time.UTC)
	meta := &mcmpapi.McmpApiServiceMeta{
		ServiceName: "mc-iam-manager",
		Version:     "0.5.2",
		GeneratedAt: generatedAt,
	}
	require.NoError(t, s.repo.UpsertServiceMeta(s.db, meta))

	shouldUpdate, err := s.shouldUpdateService("mc-iam-manager", &mcmpapi.McmpApiServiceMeta{
		ServiceName: "mc-iam-manager",
		Version:     "0.5.2",
		GeneratedAt: generatedAt,
	})
	require.NoError(t, err)
	assert.False(t, shouldUpdate, "version/generatedAt 동일 시 스킵되어야 함")
}

// TestShouldUpdateService_GeneratedAtBumped_Updates: generatedAt만 바뀌어도
// (version 동일) 갱신 대상으로 인식해야 한다 — IAM-TECH-021의 수정 사항.
func TestShouldUpdateService_GeneratedAtBumped_Updates(t *testing.T) {
	s := newTestMcmpApiService(t)

	oldGeneratedAt := time.Date(2026, 6, 1, 2, 11, 43, 0, time.UTC)
	meta := &mcmpapi.McmpApiServiceMeta{
		ServiceName: "mc-iam-manager",
		Version:     "0.5.2",
		GeneratedAt: oldGeneratedAt,
	}
	require.NoError(t, s.repo.UpsertServiceMeta(s.db, meta))

	newGeneratedAt := oldGeneratedAt.Add(24 * time.Hour)
	shouldUpdate, err := s.shouldUpdateService("mc-iam-manager", &mcmpapi.McmpApiServiceMeta{
		ServiceName: "mc-iam-manager",
		Version:     "0.5.2",
		GeneratedAt: newGeneratedAt,
	})
	require.NoError(t, err)
	assert.True(t, shouldUpdate, "generatedAt이 바뀌면 갱신 대상으로 인식해야 함")
}

// TestShouldUpdateService_NewService_AlwaysUpdates: DB에 메타가 아예 없으면
// (신규 서비스) 항상 갱신 대상.
func TestShouldUpdateService_NewService_AlwaysUpdates(t *testing.T) {
	s := newTestMcmpApiService(t)

	shouldUpdate, err := s.shouldUpdateService("brand-new-service", &mcmpapi.McmpApiServiceMeta{
		ServiceName: "brand-new-service",
		Version:     "0.1.0",
		GeneratedAt: time.Now(),
	})
	require.NoError(t, err)
	assert.True(t, shouldUpdate, "DB에 없는 신규 서비스는 항상 갱신 대상이어야 함")
}
