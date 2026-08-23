package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type moveRecoveryQueue struct {
	mu         sync.Mutex
	seen       map[string]struct{}
	tasks      []*asynq.Task
	enqueueErr error
}

func (q *moveRecoveryQueue) Enqueue(
	task *asynq.Task,
	opts ...asynq.Option,
) (*asynq.TaskInfo, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.enqueueErr != nil {
		return nil, q.enqueueErr
	}
	taskID := ""
	for _, option := range opts {
		if option.Type() == asynq.TaskIDOpt {
			taskID, _ = option.Value().(string)
		}
	}
	if taskID != "" {
		if _, exists := q.seen[taskID]; exists {
			return nil, asynq.ErrTaskIDConflict
		}
		q.seen[taskID] = struct{}{}
	}
	q.tasks = append(q.tasks, task)
	return &asynq.TaskInfo{ID: taskID, Type: task.Type(), Queue: types.QueueMaintenance}, nil
}

type moveRecoveryKBService struct {
	interfaces.KnowledgeBaseService
	byID map[string]*types.KnowledgeBase
}

type cancelledMoveChunkRepo struct {
	interfaces.ChunkRepository
}

type failingMoveDispatchLookupRepo struct {
	interfaces.KnowledgeRepository
}

func (failingMoveDispatchLookupRepo) KnowledgeMoveDispatchExists(
	context.Context, uint64, string, string,
) (bool, error) {
	return false, errors.New("database unavailable")
}

func (cancelledMoveChunkRepo) ListChunksByKnowledgeID(
	context.Context, uint64, string,
) ([]*types.Chunk, error) {
	return nil, context.Canceled
}

func (s moveRecoveryKBService) GetKnowledgeBaseByID(
	_ context.Context,
	id string,
) (*types.KnowledgeBase, error) {
	return s.byID[id], nil
}

func newMoveRecoveryDB(t *testing.T) (*gorm.DB, interfaces.KnowledgeRepository, interfaces.TaskPendingOpsRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.TaskPendingOp{},
	))
	return db, apprepo.NewKnowledgeRepository(db), apprepo.NewTaskPendingOpsRepository(db)
}

func TestLiteKnowledgeMoveProgressSupportsLifecycleAndExpiry(t *testing.T) {
	svc := &knowledgeService{}
	now := time.Now().Unix()
	for _, status := range []types.KBCloneTaskStatus{
		types.KBCloneStatusPending,
		types.KBCloneStatusCompleted,
		types.KBCloneStatusFailed,
	} {
		progress := &types.KnowledgeMoveProgress{
			TaskID: "move-" + string(status), Status: status, UpdatedAt: now,
		}
		require.NoError(t, svc.SaveKnowledgeMoveProgress(context.Background(), progress))
		got, err := svc.GetKnowledgeMoveProgress(context.Background(), progress.TaskID)
		require.NoError(t, err)
		require.Equal(t, status, got.Status)
		got.Message = "不能修改缓存"
		again, err := svc.GetKnowledgeMoveProgress(context.Background(), progress.TaskID)
		require.NoError(t, err)
		require.Empty(t, again.Message)
	}

	expired := &types.KnowledgeMoveProgress{
		TaskID: "move-expired", UpdatedAt: time.Now().Add(-knowledgeMoveProgressTTL - time.Minute).Unix(),
	}
	require.NoError(t, svc.SaveKnowledgeMoveProgress(context.Background(), expired))
	_, err := svc.GetKnowledgeMoveProgress(context.Background(), expired.TaskID)
	appErr, ok := werrors.IsAppError(err)
	require.True(t, ok)
	require.Equal(t, 404, appErr.HTTPCode)
}

func TestLiteFinalRetryReleasesMoveClaim(t *testing.T) {
	repo := &moveWikiKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID: "knowledge-1", TenantID: 7, KnowledgeBaseID: "kb-source",
			ParseStatus: types.ParseStatusMoving,
		},
		claimOwner: "move-1",
		allowClaim: true,
	}
	tenantRepo := &processReliabilityTenantRepo{tenant: &types.Tenant{ID: 7}}
	kbService := moveRecoveryKBService{byID: map[string]*types.KnowledgeBase{
		"kb-source": {ID: "kb-source", TenantID: 7, Type: "document", EmbeddingModelID: "embed"},
		"kb-target": {ID: "kb-target", TenantID: 7, Type: "faq", EmbeddingModelID: "embed"},
	}}
	svc := &knowledgeService{repo: repo, tenantRepo: tenantRepo, kbService: kbService}
	payload, err := json.Marshal(types.KnowledgeMovePayload{
		TenantID: 7, TaskID: "move-1", KnowledgeIDs: []string{"knowledge-1"},
		SourceKBID: "kb-source", TargetKBID: "kb-target", Mode: "reparse",
	})
	require.NoError(t, err)
	task := asynq.NewTask(types.TypeKnowledgeMove, payload)

	err = svc.ProcessKnowledgeMove(types.WithTaskRetryMetadata(context.Background(), 0, 1), task)
	require.Error(t, err)
	require.Equal(t, types.ParseStatusMoving, repo.knowledge.ParseStatus)

	err = svc.ProcessKnowledgeMove(types.WithTaskRetryMetadata(context.Background(), 1, 1), task)
	require.Error(t, err)
	require.Equal(t, types.ParseStatusFailed, repo.knowledge.ParseStatus)
}

func TestKnowledgeMoveDispatchSurvivesRestartAndDeduplicatesRecovery(t *testing.T) {
	db, knowledgeRepo, pendingRepo := newMoveRecoveryDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	sourceKB := &types.KnowledgeBase{ID: "kb-source", TenantID: tenant.ID, Name: "source"}
	require.NoError(t, db.Create(sourceKB).Error)
	payload := types.KnowledgeMovePayload{
		TenantID: tenant.ID, TaskID: "move-restart-1",
		KnowledgeIDs: []string{"knowledge-1", "knowledge-2"},
		SourceKBID:   sourceKB.ID, TargetKBID: "kb-target", Mode: "reparse",
	}
	producer := &knowledgeService{repo: knowledgeRepo, taskPendingRepo: pendingRepo}
	require.NoError(t, producer.PersistKnowledgeMoveDispatch(context.Background(), payload))

	queue := &moveRecoveryQueue{seen: make(map[string]struct{})}
	restarted := &knowledgeService{
		repo: knowledgeRepo, taskPendingRepo: pendingRepo, task: queue,
	}
	require.NoError(t, restarted.RecoverPendingKnowledgeMoves(context.Background(), 10, true))
	require.NoError(t, restarted.RecoverPendingKnowledgeMoves(context.Background(), 10, true))
	require.Len(t, queue.tasks, 1)
	var recovered types.KnowledgeMovePayload
	require.NoError(t, json.Unmarshal(queue.tasks[0].Payload(), &recovered))
	require.ElementsMatch(t, payload.KnowledgeIDs, recovered.KnowledgeIDs)

	var pending int64
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Count(&pending).Error)
	require.Equal(t, int64(1), pending, "worker 终态前必须保留 outbox")
}

func TestKnowledgeMoveTerminalSkipRetryDeletesDispatch(t *testing.T) {
	db, knowledgeRepo, pendingRepo := newMoveRecoveryDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID: "kb-source", TenantID: tenant.ID, Name: "source",
	}).Error)
	payload := types.KnowledgeMovePayload{
		TenantID: tenant.ID, TaskID: "move-terminal-1", KnowledgeIDs: []string{"knowledge-1"},
		SourceKBID: "kb-source", TargetKBID: "kb-target", Mode: "reparse",
	}
	svc := &knowledgeService{
		repo: knowledgeRepo, taskPendingRepo: pendingRepo,
		tenantRepo: &processReliabilityTenantRepo{tenant: tenant},
		kbService: moveRecoveryKBService{byID: map[string]*types.KnowledgeBase{
			"kb-source": {ID: "kb-source", TenantID: tenant.ID, Type: "document"},
			"kb-target": {ID: "kb-target", TenantID: tenant.ID + 1, Type: "document"},
		}},
	}
	require.NoError(t, svc.PersistKnowledgeMoveDispatch(context.Background(), payload))
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	err = svc.ProcessKnowledgeMove(context.Background(), asynq.NewTask(types.TypeKnowledgeMove, encoded))
	require.ErrorIs(t, err, asynq.SkipRetry)
	var pending int64
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Count(&pending).Error)
	require.Zero(t, pending)
}

func runCancelledMoveWithDispatch(
	t *testing.T,
	persistDispatch bool,
) (*gorm.DB, *types.Knowledge, error) {
	t.Helper()
	db, knowledgeRepo, pendingRepo := newMoveRecoveryDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID: "kb-source", TenantID: tenant.ID, Name: "source",
	}).Error)
	knowledge := &types.Knowledge{
		ID: "knowledge-cancelled-move", TenantID: tenant.ID,
		KnowledgeBaseID: "kb-source", ParseStatus: types.ParseStatusCompleted,
	}
	require.NoError(t, db.Create(knowledge).Error)
	payload := types.KnowledgeMovePayload{
		TenantID: tenant.ID, TaskID: "move-cancelled-1",
		KnowledgeIDs: []string{knowledge.ID}, SourceKBID: "kb-source",
		TargetKBID: "kb-target", Mode: "reuse_vectors",
	}
	svc := &knowledgeService{
		repo: knowledgeRepo, taskPendingRepo: pendingRepo,
		tenantRepo: &processReliabilityTenantRepo{tenant: tenant},
		kbService: moveRecoveryKBService{byID: map[string]*types.KnowledgeBase{
			"kb-source": {
				ID: "kb-source", TenantID: tenant.ID, Type: "document", EmbeddingModelID: "embed",
			},
			"kb-target": {
				ID: "kb-target", TenantID: tenant.ID, Type: "document", EmbeddingModelID: "embed",
			},
		}},
		chunkRepo: cancelledMoveChunkRepo{},
	}
	if persistDispatch {
		require.NoError(t, svc.PersistKnowledgeMoveDispatch(context.Background(), payload))
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	err = svc.ProcessKnowledgeMove(
		context.Background(), asynq.NewTask(types.TypeKnowledgeMove, encoded),
	)
	return db, knowledge, err
}

func TestShutdownCancellationWithDispatchPreservesMoveClaimAndOutbox(t *testing.T) {
	db, knowledge, err := runCancelledMoveWithDispatch(t, true)
	require.ErrorIs(t, err, context.Canceled)
	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	require.Equal(t, types.ParseStatusMoving, persisted.ParseStatus)
	require.Contains(t, string(persisted.Metadata), "move-cancelled-1")
	var pending int64
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Count(&pending).Error)
	require.Equal(t, int64(1), pending)
}

func TestKnowledgeBaseDeleteCancellationWithoutDispatchReleasesMoveClaim(t *testing.T) {
	db, knowledge, err := runCancelledMoveWithDispatch(t, false)
	require.ErrorIs(t, err, context.Canceled)
	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	require.Equal(t, types.ParseStatusFailed, persisted.ParseStatus)
	require.Contains(t, persisted.ErrorMessage, context.Canceled.Error())
	var pending int64
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Count(&pending).Error)
	require.Zero(t, pending)
}

func TestMoveCancellationDispatchLookupFailureFailsSafe(t *testing.T) {
	svc := &knowledgeService{repo: failingMoveDispatchLookupRepo{}}
	require.True(t, svc.preserveCancelledKnowledgeMove(
		context.Background(),
		types.KnowledgeMovePayload{
			TenantID: 1, TaskID: "move-1", SourceKBID: "kb-source",
		},
	))
}
