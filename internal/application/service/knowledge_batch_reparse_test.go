package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type reparseFailureKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	knowledge   *types.Knowledge
	updateCalls int
}

func (r *reparseFailureKnowledgeRepo) GetKnowledgeByID(
	_ context.Context,
	_ uint64,
	_ string,
) (*types.Knowledge, error) {
	return r.knowledge, nil
}

func (r *reparseFailureKnowledgeRepo) UpdateKnowledge(
	_ context.Context,
	_ *types.Knowledge,
) error {
	r.updateCalls++
	return nil
}

func (r *reparseFailureKnowledgeRepo) UpdateKnowledgeColumn(
	_ context.Context,
	_ string,
	_ string,
	_ interface{},
) error {
	return nil
}

func (r *reparseFailureKnowledgeRepo) ClaimKnowledgeReparse(
	_ context.Context,
	_ uint64,
	_ string,
	_ time.Time,
) (bool, string, error) {
	status := r.knowledge.ParseStatus
	switch status {
	case types.ParseStatusCompleted, types.ParseStatusFailed, types.ParseStatusCancelled:
		r.knowledge.ParseStatus = types.ParseStatusProcessing
		return true, types.ParseStatusProcessing, nil
	default:
		return false, status, nil
	}
}

func (r *reparseFailureKnowledgeRepo) StageKnowledgeReparsePending(
	_ context.Context,
	_ uint64,
	_ string,
	embeddingModelID string,
	metadata types.JSON,
	updatedAt time.Time,
) (bool, string, error) {
	if r.knowledge.ParseStatus != types.ParseStatusProcessing &&
		r.knowledge.ParseStatus != types.ParseStatusPending {
		return false, r.knowledge.ParseStatus, nil
	}
	r.updateCalls++
	r.knowledge.ParseStatus = types.ParseStatusPending
	r.knowledge.EnableStatus = "disabled"
	r.knowledge.EmbeddingModelID = embeddingModelID
	r.knowledge.Metadata = metadata
	r.knowledge.UpdatedAt = updatedAt
	return true, types.ParseStatusPending, nil
}

func (r *reparseFailureKnowledgeRepo) CancelKnowledgeProcessing(
	context.Context, uint64, string, time.Time,
) (bool, string, error) {
	return false, r.knowledge.ParseStatus, nil
}

func (r *reparseFailureKnowledgeRepo) FailKnowledgeProcessing(
	_ context.Context,
	_ uint64,
	_ string,
	errorMessage string,
	_ time.Time,
) (bool, string, error) {
	r.updateCalls++
	r.knowledge.ParseStatus = types.ParseStatusFailed
	r.knowledge.ErrorMessage = errorMessage
	return true, types.ParseStatusFailed, nil
}

type reparseFailureKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *reparseFailureKBService) GetKnowledgeBaseByID(
	_ context.Context,
	_ string,
) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type failingReparseTaskEnqueuer struct {
	err error
}

func (e failingReparseTaskEnqueuer) Enqueue(
	_ *asynq.Task,
	_ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	return nil, e.err
}

func TestReparseKnowledgeManualEnqueueFailureIsVisible(t *testing.T) {
	enqueueErr := errors.New("queue unavailable")
	knowledge := &types.Knowledge{
		ID:              "knowledge-1",
		TenantID:        7,
		KnowledgeBaseID: "kb-1",
		Type:            types.KnowledgeTypeManual,
		ParseStatus:     types.ParseStatusCompleted,
		EnableStatus:    "enabled",
	}
	require.NoError(t, knowledge.SetManualMetadata(
		types.NewManualKnowledgeMetadata("# content", types.ManualKnowledgeStatusPublish, 1),
	))
	repo := &reparseFailureKnowledgeRepo{knowledge: knowledge}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &reparseFailureKBService{kb: &types.KnowledgeBase{ID: "kb-1"}},
		task:      failingReparseTaskEnqueuer{err: enqueueErr},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))

	got, err := svc.ReparseKnowledge(ctx, knowledge.ID, nil)

	require.Error(t, err)
	require.Same(t, knowledge, got)
	require.Equal(t, types.ParseStatusFailed, knowledge.ParseStatus)
	require.Equal(t, "disabled", knowledge.EnableStatus)
	require.Equal(t, "Failed to enqueue processing task", knowledge.ErrorMessage)
	require.GreaterOrEqual(t, repo.updateCalls, 2, "pending and failed states must both be persisted")
}

func TestRunKnowledgeListReparseSubmissionsReportsPartialFailure(t *testing.T) {
	firstErr := errors.New("first failed")
	secondErr := errors.New("second failed")
	var attempted []string

	outcome, err := runKnowledgeListReparseSubmissions(
		[]string{"ok-1", "bad-1", "ok-2", "bad-2"},
		func(id string) error {
			attempted = append(attempted, id)
			switch id {
			case "bad-1":
				return firstErr
			case "bad-2":
				return secondErr
			default:
				return nil
			}
		},
	)

	require.Equal(t, []string{"ok-1", "bad-1", "ok-2", "bad-2"}, attempted)
	require.Equal(t, knowledgeListReparseOutcome{Submitted: 2, Failed: 2}, outcome)
	require.ErrorIs(t, err, asynq.SkipRetry)
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.ErrorContains(t, err, "knowledge bad-1")
	require.ErrorContains(t, err, "knowledge bad-2")
}

func TestRunKnowledgeListReparseSubmissionsSucceeds(t *testing.T) {
	outcome, err := runKnowledgeListReparseSubmissions(
		[]string{"knowledge-1", "knowledge-2"},
		func(string) error { return nil },
	)

	require.NoError(t, err)
	require.Equal(t, knowledgeListReparseOutcome{Submitted: 2}, outcome)
}
