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
				ID: "knowledge-1", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: status,
				Metadata: types.JSON(`{"_weknora_move_claim":{"task_id":"owner"}}`),
			}
			repo := &deadLetterLifecycleRepo{knowledge: knowledge}
			callback := newDeadLetterKnowledgeFailer(
				deadLetterLifecycleService{repo: repo}, nil,
			)
			payload, err := json.Marshal(types.DocumentProcessPayload{
				TenantID: 7, KnowledgeBaseID: knowledge.KnowledgeBaseID, KnowledgeID: knowledge.ID,
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

func (r *deadLetterLifecycleRepo) GetKnowledgeByID(context.Context, uint64, string) (*types.Knowledge, error) {
	copy := *r.knowledge
	return &copy, nil
}
func (r *deadLetterLifecycleRepo) UpdateKnowledgeForTransfer(_ context.Context, before, after *types.Knowledge) error {
	if r.knowledge.ParseStatus != before.ParseStatus || r.knowledge.KnowledgeBaseID != before.KnowledgeBaseID {
		return errors.New("knowledge changed during failure checkpoint")
	}
	*r.knowledge = *after
	return nil
}

func TestDeadLetterLegacyTaskRestoresOnlyUnmovedScope(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(map[bool]string{false: "original", true: "moved"}[moved], func(t *testing.T) {
			row := &types.Knowledge{ID: "knowledge-legacy", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusPending}
			if moved {
				row.Metadata = types.JSON(`{"_knowledge_transfer":{"phase":"done"}}`)
			}
			repo := &deadLetterLifecycleRepo{knowledge: row}
			payload, err := json.Marshal(types.DocumentProcessPayload{TenantID: 7, KnowledgeID: row.ID})
			require.NoError(t, err)
			newDeadLetterKnowledgeFailer(deadLetterLifecycleService{repo: repo}, nil)(context.Background(),
				asynq.NewTask(types.TypeDocumentProcess, payload), errors.New("worker exhausted"))
			if moved {
				require.Equal(t, types.ParseStatusPending, row.ParseStatus)
			} else {
				require.Equal(t, types.ParseStatusFailed, row.ParseStatus)
			}
		})
	}
}
