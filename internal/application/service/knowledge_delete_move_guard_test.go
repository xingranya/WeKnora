package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type movingKnowledgeSingleDeleteRepo struct {
	interfaces.KnowledgeRepository
	updateCalled bool
}

func (r *movingKnowledgeSingleDeleteRepo) GetKnowledgeByID(
	context.Context,
	uint64,
	string,
) (*types.Knowledge, error) {
	return &types.Knowledge{
		ID:              "knowledge-moving-single-delete",
		TenantID:        1,
		KnowledgeBaseID: "kb-source",
		ParseStatus:     types.ParseStatusMoving,
	}, nil
}

func (r *movingKnowledgeSingleDeleteRepo) GetKnowledgeBatch(
	ctx context.Context,
	tenantID uint64,
	ids []string,
) ([]*types.Knowledge, error) {
	knowledge, err := r.GetKnowledgeByID(ctx, tenantID, ids[0])
	return []*types.Knowledge{knowledge}, err
}

func (r *movingKnowledgeSingleDeleteRepo) UpdateKnowledge(
	context.Context,
	*types.Knowledge,
) error {
	r.updateCalled = true
	return nil
}

func (r *movingKnowledgeSingleDeleteRepo) ClaimKnowledgeListForKBDelete(
	context.Context,
	uint64,
	string,
	[]string,
) ([]*types.Knowledge, error) {
	return nil, types.ErrKnowledgeMoveInProgress
}

func TestDeleteKnowledgeStopsBeforeSideEffectsWhenMoveOwnsClaim(t *testing.T) {
	repo := &movingKnowledgeSingleDeleteRepo{}
	svc := &knowledgeService{repo: repo}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))

	err := svc.DeleteKnowledge(ctx, "knowledge-moving-single-delete")

	require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
	assert.False(t, repo.updateCalled)
}

func TestDeleteKnowledgeListStopsBeforeSideEffectsWhenMoveOwnsClaim(t *testing.T) {
	repo := &movingKnowledgeSingleDeleteRepo{}
	svc := &knowledgeService{repo: repo}
	ctx := context.WithValue(context.Background(), types.TenantInfoContextKey, &types.Tenant{ID: 1})

	err := svc.DeleteKnowledgeList(ctx, []string{"knowledge-moving-single-delete"})

	require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
	assert.False(t, repo.updateCalled)
}
