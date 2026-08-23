package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type manualEnqueueRecorder struct {
	task *asynq.Task
	info *asynq.TaskInfo
	err  error
}

func (r *manualEnqueueRecorder) Enqueue(
	task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	r.task = task
	return r.info, r.err
}

func TestEnqueueManualProcessingTreatsDeterministicTaskConflictAsSuccess(t *testing.T) {
	for _, conflict := range []error{asynq.ErrTaskIDConflict, asynq.ErrDuplicateTask} {
		t.Run(conflict.Error(), func(t *testing.T) {
			recorder := &manualEnqueueRecorder{err: conflict}
			service := &knowledgeService{task: recorder}
			knowledge := &types.Knowledge{
				ID:              "knowledge-1",
				TenantID:        1,
				KnowledgeBaseID: "kb-1",
			}

			taskID, err := service.enqueueManualProcessing(
				context.Background(),
				knowledge,
				"# 手工知识",
				true,
				manualProcessingEnqueueConfig{
					TaskID:  "manual-move-knowledge-1",
					Options: []asynq.Option{asynq.Queue(types.QueueDefault)},
				},
			)

			require.NoError(t, err)
			require.Equal(t, "manual-move-knowledge-1", taskID)
			require.NotNil(t, recorder.task)
			require.Equal(t, types.TypeManualProcess, recorder.task.Type())
			var payload types.ManualProcessPayload
			require.NoError(t, json.Unmarshal(recorder.task.Payload(), &payload))
			require.Equal(t, knowledge.ID, payload.KnowledgeID)
			require.Equal(t, "# 手工知识", payload.Content)
			require.True(t, payload.NeedCleanup)
		})
	}
}

func TestEnqueueManualProcessingDoesNotHideConflictWithoutDeterministicTaskID(t *testing.T) {
	recorder := &manualEnqueueRecorder{err: asynq.ErrTaskIDConflict}
	service := &knowledgeService{task: recorder}

	_, err := service.enqueueManualProcessing(
		context.Background(),
		&types.Knowledge{ID: "knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1"},
		"content",
		true,
	)

	require.Error(t, err)
	require.True(t, errors.Is(err, asynq.ErrTaskIDConflict))
}
