package service

import (
	"context"
	"net/http"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type lifecycleKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s lifecycleKBService) GetKnowledgeBaseByID(
	context.Context, string,
) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type lifecycleInterleavingRepo struct {
	interfaces.KnowledgeRepository
	beforeUserPatch func()
}

func (r *lifecycleInterleavingRepo) PatchKnowledgeUserFields(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	values map[string]interface{},
) (bool, string, error) {
	if r.beforeUserPatch != nil {
		r.beforeUserPatch()
	}
	return r.KnowledgeRepository.(interface {
		PatchKnowledgeUserFields(
			context.Context, uint64, string, map[string]interface{},
		) (bool, string, error)
	}).PatchKnowledgeUserFields(ctx, tenantID, knowledgeID, values)
}

func newKnowledgeLifecycleServiceDB(t *testing.T) (*gorm.DB, interfaces.KnowledgeRepository, *types.Tenant) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.Knowledge{},
		&types.KnowledgeTag{},
		&types.KnowledgeTagRelation{},
		&types.TaskPendingOp{},
	))
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	return db, apprepo.NewKnowledgeRepository(db), tenant
}

func requireConflictAppError(t *testing.T, err error) {
	t.Helper()
	appErr, ok := werrors.IsAppError(err)
	require.True(t, ok, "expected AppError, got %v", err)
	require.Equal(t, http.StatusConflict, appErr.HTTPCode)
}

func TestUpdateKnowledgeRejectsMoving(t *testing.T) {
	db, repo, tenant := newKnowledgeLifecycleServiceDB(t)
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: tenant.ID, KnowledgeBaseID: "kb-1",
		ParseStatus: types.ParseStatusMoving, Title: "旧标题",
		Metadata: types.JSON(`{"_weknora_move_claim":{"task_id":"owner"}}`),
	}
	require.NoError(t, db.Create(knowledge).Error)
	svc := &knowledgeService{repo: repo}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant.ID)

	err := svc.UpdateKnowledge(ctx, &types.Knowledge{ID: knowledge.ID, Title: "新标题"})

	requireConflictAppError(t, err)
}

func TestKnowledgeUserPatchInterleavingReturnsConflictAndKeepsMoveClaim(t *testing.T) {
	db, baseRepo, tenant := newKnowledgeLifecycleServiceDB(t)
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: tenant.ID, KnowledgeBaseID: "kb-1",
		ParseStatus: types.ParseStatusCompleted, Title: "旧标题",
	}
	require.NoError(t, db.Create(knowledge).Error)
	wrapped := &lifecycleInterleavingRepo{KnowledgeRepository: baseRepo}
	wrapped.beforeUserPatch = func() {
		require.NoError(t, db.Model(&types.Knowledge{}).
			Where("id = ?", knowledge.ID).
			Updates(map[string]interface{}{
				"parse_status": types.ParseStatusMoving,
				"metadata":     types.JSON(`{"_weknora_move_claim":{"task_id":"winner"}}`),
			}).Error)
	}
	svc := &knowledgeService{repo: wrapped}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant.ID)

	err := svc.UpdateKnowledge(ctx, &types.Knowledge{ID: knowledge.ID, Title: "陈旧标题"})

	requireConflictAppError(t, err)
	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	require.Equal(t, "旧标题", persisted.Title)
	require.Equal(t, types.ParseStatusMoving, persisted.ParseStatus)
	require.Contains(t, string(persisted.Metadata), "winner")
}

func TestCancelAndTagMutationReturnConflictForMovingKnowledge(t *testing.T) {
	db, repo, tenant := newKnowledgeLifecycleServiceDB(t)
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: tenant.ID, KnowledgeBaseID: "kb-1",
		ParseStatus: types.ParseStatusMoving,
		Metadata:    types.JSON(`{"_weknora_move_claim":{"task_id":"owner"}}`),
	}
	require.NoError(t, db.Create(knowledge).Error)
	svc := &knowledgeService{repo: repo}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant.ID)

	_, cancelErr := svc.CancelKnowledgeParse(ctx, knowledge.ID)
	requireConflictAppError(t, cancelErr)
	requireConflictAppError(t, svc.UpdateKnowledgeTag(ctx, knowledge.ID, nil))
	requireConflictAppError(t, svc.SetKnowledgeTags(ctx, knowledge.ID, nil))
	requireConflictAppError(t, svc.RequestKnowledgeSummaryRefresh(ctx, knowledge.ID))
	_, summaryErr := svc.RegenerateKnowledgeSummary(ctx, knowledge.ID)
	requireConflictAppError(t, summaryErr)
}

func TestManualKnowledgeUpdateRejectsMovingWithoutOverwritingMetadata(t *testing.T) {
	db, repo, tenant := newKnowledgeLifecycleServiceDB(t)
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: tenant.ID, KnowledgeBaseID: "kb-1",
		Type: types.KnowledgeTypeManual, ParseStatus: types.ParseStatusMoving,
		Metadata: types.JSON(`{
			"content":"旧正文","format":"markdown","status":"publish","version":1,
			"_weknora_move_claim":{"task_id":"owner","stage":"active"},
			"process_overrides":{"chunking":{"chunk_size":512}}
		}`),
	}
	require.NoError(t, db.Create(knowledge).Error)
	svc := &knowledgeService{
		repo: repo,
		kbService: lifecycleKBService{kb: &types.KnowledgeBase{
			ID: "kb-1", TenantID: tenant.ID, EmbeddingModelID: "embed-1",
		}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant.ID)

	_, err := svc.UpdateManualKnowledge(ctx, knowledge.ID, &types.ManualKnowledgePayload{
		Title: "新标题", Content: "新正文", Status: types.ManualKnowledgeStatusDraft,
	})

	requireConflictAppError(t, err)
	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	require.Contains(t, string(persisted.Metadata), "owner")
	require.Contains(t, string(persisted.Metadata), "process_overrides")
	require.Contains(t, string(persisted.Metadata), "旧正文")
}

func TestReparseRejectsMovingBeforeCleanup(t *testing.T) {
	db, repo, tenant := newKnowledgeLifecycleServiceDB(t)
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: tenant.ID, KnowledgeBaseID: "kb-1",
		Type: "file", ParseStatus: types.ParseStatusMoving, FilePath: "local://source.txt",
		Metadata: types.JSON(`{"_weknora_move_claim":{"task_id":"owner"}}`),
	}
	require.NoError(t, db.Create(knowledge).Error)
	svc := &knowledgeService{
		repo:      repo,
		kbService: lifecycleKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: tenant.ID}},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant.ID)

	_, err := svc.ReparseKnowledge(ctx, knowledge.ID, nil)

	requireConflictAppError(t, err)
}
