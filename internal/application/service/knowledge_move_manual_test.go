package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
		ParseStatus:     types.ParseStatusMoving,
	}
	require.NoError(t, knowledge.SetManualMetadata(
		types.NewManualKnowledgeMetadata("# 正文", types.ManualKnowledgeStatusPublish, 1),
	))
	repo := &moveWikiKnowledgeRepo{
		knowledge:  knowledge,
		claimOwner: "move-1",
		allowClaim: true,
	}
	return &knowledgeService{repo: repo, task: queue}, repo
}

func TestManualReparseMoveDurablyEnqueuesBeforeCompletingClaim(t *testing.T) {
	queue := &manualMoveTaskQueue{}
	service, repo := newStagedManualMove(t, queue)
	staged := *repo.knowledge

	err := service.moveKnowledgeReparse(
		context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7)),
		&staged,
		&types.KnowledgeBase{ID: "kb-source"},
		&types.KnowledgeBase{ID: "kb-target"},
		"move-1",
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
}

func TestManualReparseMoveKeepsClaimWhenEnqueueFails(t *testing.T) {
	queue := &manualMoveTaskQueue{err: errors.New("redis unavailable")}
	service, repo := newStagedManualMove(t, queue)
	staged := *repo.knowledge

	err := service.moveKnowledgeReparse(
		context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7)),
		&staged,
		&types.KnowledgeBase{ID: "kb-source"},
		&types.KnowledgeBase{ID: "kb-target"},
		"move-1",
	)

	require.ErrorContains(t, err, "enqueue moved manual knowledge processing")
	assert.Equal(t, types.ParseStatusMoving, repo.knowledge.ParseStatus)
}

func TestManualReparseMoveTreatsDeterministicTaskConflictAsSuccess(t *testing.T) {
	queue := &manualMoveTaskQueue{err: asynq.ErrTaskIDConflict}
	service, repo := newStagedManualMove(t, queue)
	staged := *repo.knowledge

	err := service.moveKnowledgeReparse(
		context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7)),
		&staged,
		&types.KnowledgeBase{ID: "kb-source"},
		&types.KnowledgeBase{ID: "kb-target"},
		"move-1",
	)

	require.NoError(t, err)
	assert.Equal(t, types.ParseStatusPending, repo.knowledge.ParseStatus)
}
