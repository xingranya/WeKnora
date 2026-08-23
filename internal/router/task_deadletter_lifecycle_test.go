package router

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type deadLetterLifecycleRepo struct {
	interfaces.KnowledgeRepository
	knowledge *types.Knowledge
}

func (r *deadLetterLifecycleRepo) FailKnowledgeProcessing(
	_ context.Context,
	tenantID uint64,
	knowledgeID string,
	errorMessage string,
	_ time.Time,
) (bool, string, error) {
	if r.knowledge == nil || r.knowledge.TenantID != tenantID || r.knowledge.ID != knowledgeID {
		return false, types.ParseStatusDeleting, nil
	}
	switch r.knowledge.ParseStatus {
	case types.ParseStatusMoving, types.ParseStatusDeleting, types.ParseStatusCancelled:
		return false, r.knowledge.ParseStatus, nil
	}
	r.knowledge.ParseStatus = types.ParseStatusFailed
	r.knowledge.ErrorMessage = errorMessage
	return true, types.ParseStatusFailed, nil
}

type deadLetterLifecycleService struct {
	interfaces.KnowledgeService
	repo interfaces.KnowledgeRepository
}

func (s deadLetterLifecycleService) GetRepository() interfaces.KnowledgeRepository {
	return s.repo
}

func TestDeadLetterFailurePreservesLifecycleClaims(t *testing.T) {
	for _, status := range []string{
		types.ParseStatusMoving,
		types.ParseStatusDeleting,
		types.ParseStatusCancelled,
		types.ParseStatusPending,
		types.ParseStatusProcessing,
	} {
		t.Run(status, func(t *testing.T) {
			knowledge := &types.Knowledge{
				ID: "knowledge-1", TenantID: 7, ParseStatus: status,
				Metadata: types.JSON(`{"_weknora_move_claim":{"task_id":"owner"}}`),
			}
			repo := &deadLetterLifecycleRepo{knowledge: knowledge}
			callback := newDeadLetterKnowledgeFailer(
				deadLetterLifecycleService{repo: repo}, nil,
			)
			payload, err := json.Marshal(types.DocumentProcessPayload{
				TenantID: 7, KnowledgeID: knowledge.ID,
			})
			require.NoError(t, err)

			callback(
				context.Background(),
				asynq.NewTask(types.TypeDocumentProcess, payload),
				errors.New("worker exhausted"),
			)

			switch status {
			case types.ParseStatusPending, types.ParseStatusProcessing:
				require.Equal(t, types.ParseStatusFailed, knowledge.ParseStatus)
				require.Contains(t, knowledge.ErrorMessage, "worker exhausted")
			default:
				require.Equal(t, status, knowledge.ParseStatus)
				require.Empty(t, knowledge.ErrorMessage)
				require.Contains(t, string(knowledge.Metadata), "owner")
			}
		})
	}
}
