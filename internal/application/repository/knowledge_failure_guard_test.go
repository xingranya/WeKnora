package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFailKnowledgeProcessingPreservesLifecycleClaims(t *testing.T) {
	statuses := []string{
		types.ParseStatusMoving,
		types.ParseStatusDeleting,
		types.ParseStatusCancelled,
	}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:failure-guard-%s?mode=memory&cache=shared", status)))
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&types.Knowledge{}))
			knowledge := &types.Knowledge{
				ID: "knowledge-1", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: status,
			}
			require.NoError(t, db.Create(knowledge).Error)
			repo := &knowledgeRepository{db: db}

			applied, current, err := repo.FailKnowledgeProcessing(
				context.Background(), 7, knowledge.ID, "parse failed", time.Now(),
			)

			require.NoError(t, err)
			assert.False(t, applied)
			assert.Equal(t, status, current)
			var persisted types.Knowledge
			require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
			assert.Equal(t, status, persisted.ParseStatus)
			assert.Empty(t, persisted.ErrorMessage)
		})
	}
}

func TestFailKnowledgeProcessingUpdatesMutableState(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:failure-guard-processing?mode=memory&cache=shared"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}))
	knowledge := &types.Knowledge{
		ID: "knowledge-1", TenantID: 7, KnowledgeBaseID: "kb-1", ParseStatus: types.ParseStatusProcessing,
	}
	require.NoError(t, db.Create(knowledge).Error)
	repo := &knowledgeRepository{db: db}

	applied, current, err := repo.FailKnowledgeProcessing(
		context.Background(), 7, knowledge.ID, "parse failed", time.Now(),
	)

	require.NoError(t, err)
	assert.True(t, applied)
	assert.Equal(t, types.ParseStatusFailed, current)
	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	assert.Equal(t, types.ParseStatusFailed, persisted.ParseStatus)
	assert.Equal(t, "parse failed", persisted.ErrorMessage)
}
