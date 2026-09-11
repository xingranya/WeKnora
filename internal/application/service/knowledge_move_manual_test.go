package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type manualMoveTaskQueue struct {
	interfaces.TaskEnqueuer
	task *asynq.Task
	err  error
}

func (q *manualMoveTaskQueue) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.task = task
	if q.err != nil {
		return nil, q.err
	}
	return &asynq.TaskInfo{ID: "manual-move-task", Type: task.Type()}, nil
}

func newStagedManualMove(t *testing.T, queue *manualMoveTaskQueue) (*knowledgeService, *moveWikiKnowledgeRepo) {
	t.Helper()
	knowledge := &types.Knowledge{
		ID:              "manual-1",
		TenantID:        7,
		KnowledgeBaseID: "kb-target",
		Type:            types.KnowledgeTypeManual,
		ParseStatus:     types.ParseStatusPending,
	}
	require.NoError(t, knowledge.SetManualMetadata(
		types.NewManualKnowledgeMetadata("# 正文", types.ManualKnowledgeStatusPublish, 1),
	))
	require.NoError(t, setTransferState(knowledge, knowledgeTransferState{
		TaskID: "move-1", Operation: access.KBTransferMove,
		SourceKB: "kb-source", TargetKB: "kb-target", SourceID: knowledge.ID,
		Mode: "reparse", Phase: "reparse_pending",
	}))
	repo := &moveWikiKnowledgeRepo{
		knowledge:  knowledge,
		claimOwner: "move-1",
		allowClaim: true,
	}
	return &knowledgeService{repo: repo, task: queue}, repo
}

func TestManualReparseMoveDurablyEnqueuesBeforeAcknowledgingTransfer(t *testing.T) {
	queue := &manualMoveTaskQueue{}
	service, repo := newStagedManualMove(t, queue)
	staged := *repo.knowledge

	err := service.enqueueMovedKnowledge(
		manualMoveContext(t),
		&staged,
		&types.KnowledgeBase{ID: "kb-source", TenantID: 7},
		&types.KnowledgeBase{ID: "kb-target", TenantID: 7},
	)

	require.NoError(t, err)
	require.NotNil(t, queue.task)
	assert.Equal(t, types.TypeManualProcess, queue.task.Type())
	var payload types.ManualProcessPayload
	require.NoError(t, json.Unmarshal(queue.task.Payload(), &payload))
	assert.Equal(t, "manual-1", payload.KnowledgeID)
	assert.Equal(t, "kb-target", payload.KnowledgeBaseID)
	assert.Equal(t, "# 正文", payload.Content)
	assert.False(t, payload.NeedCleanup)
	assert.Equal(t, types.ParseStatusPending, repo.knowledge.ParseStatus)
	state, stateErr := transferState(repo.knowledge)
	require.NoError(t, stateErr)
	assert.Equal(t, "done", state.Phase)
}

func TestManualReparseMoveKeepsRecoveryMarkerWhenEnqueueFails(t *testing.T) {
	queue := &manualMoveTaskQueue{err: errors.New("redis unavailable")}
	service, repo := newStagedManualMove(t, queue)
	staged := *repo.knowledge

	err := service.enqueueMovedKnowledge(
		manualMoveContext(t),
		&staged,
		&types.KnowledgeBase{ID: "kb-source", TenantID: 7},
		&types.KnowledgeBase{ID: "kb-target", TenantID: 7},
	)

	require.ErrorContains(t, err, "redis unavailable")
	state, stateErr := transferState(repo.knowledge)
	require.NoError(t, stateErr)
	assert.Equal(t, "reparse_pending", state.Phase)
	assert.Equal(t, "move-1", state.TaskID)
	assert.Equal(t, types.ParseStatusPending, repo.knowledge.ParseStatus)
}

func TestManualReparseMoveTreatsDeterministicTaskConflictAsSuccess(t *testing.T) {
	queue := &manualMoveTaskQueue{err: asynq.ErrTaskIDConflict}
	service, repo := newStagedManualMove(t, queue)
	staged := *repo.knowledge

	err := service.enqueueMovedKnowledge(
		manualMoveContext(t),
		&staged,
		&types.KnowledgeBase{ID: "kb-source", TenantID: 7},
		&types.KnowledgeBase{ID: "kb-target", TenantID: 7},
	)

	require.NoError(t, err)
	assert.Equal(t, types.ParseStatusPending, repo.knowledge.ParseStatus)
}

func manualMoveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := access.WithKBTransferTask(context.Background(),
		&types.KnowledgeBase{ID: "kb-source", TenantID: 7},
		&types.KnowledgeBase{ID: "kb-target", TenantID: 7},
		7, access.KBTransferMove, "move-1", false)
	require.NoError(t, err)
	return ctx
}
