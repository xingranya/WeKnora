package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStartKnowledgeProcessingRejectsLifecycleClaimsAndPreservesMetadata(t *testing.T) {
	for _, status := range []string{
		types.ParseStatusMoving,
		types.ParseStatusDeleting,
		types.ParseStatusCancelled,
		types.ParseStatusCompleted,
		types.ParseStatusFinalizing,
	} {
		t.Run(status, func(t *testing.T) {
			db := newKnowledgeStorageTransactionDB(t)
			tenant, knowledge := seedKnowledgeStorageRows(t, db, 10, 1000, 25, 5, status)
			knowledge.Metadata = types.JSON(`{"_weknora_move_claim":{"task_id":"owner"},"keep":true}`)
			require.NoError(t, db.Model(&types.Knowledge{}).
				Where("id = ?", knowledge.ID).
				Update("metadata", knowledge.Metadata).Error)
			repo := NewKnowledgeRepository(db).(*knowledgeRepository)

			applied, current, err := repo.StartKnowledgeProcessing(
				context.Background(), tenant.ID, knowledge.ID, time.Now(),
			)

			require.NoError(t, err)
			require.False(t, applied)
			require.Equal(t, status, current)
			_, persisted := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
			require.Equal(t, status, persisted.ParseStatus)
			require.JSONEq(t, string(knowledge.Metadata), string(persisted.Metadata))
		})
	}
}

func TestStartKnowledgeProcessingUsesCurrentDatabaseStateNotStaleSnapshot(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(
		t, db, 10, 1000, 25, 5, types.ParseStatusPending,
	)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)
	stale := *knowledge
	require.Equal(t, types.ParseStatusPending, stale.ParseStatus)
	require.NoError(t, db.Model(&types.Knowledge{}).
		Where("id = ?", knowledge.ID).
		Updates(map[string]interface{}{
			"parse_status": types.ParseStatusMoving,
			"metadata":     types.JSON(`{"_weknora_move_claim":{"task_id":"winner"}}`),
		}).Error)

	applied, status, err := repo.StartKnowledgeProcessing(
		context.Background(), tenant.ID, stale.ID, time.Now(),
	)

	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, types.ParseStatusMoving, status)
	_, persisted := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, types.ParseStatusMoving, persisted.ParseStatus)
	require.Contains(t, string(persisted.Metadata), "winner")
}

func TestPersistFileURLSourceRejectsDeleteClaim(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(
		t, db, 0, 1000, 0, 0, types.ParseStatusProcessing,
	)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)
	require.NoError(t, db.Model(&types.Knowledge{}).
		Where("id = ?", knowledge.ID).
		Update("parse_status", types.ParseStatusDeleting).Error)

	applied, status, err := repo.PersistFileURLSource(
		context.Background(), tenant.ID, knowledge.ID,
		"storage://source/file.txt", "file.txt", "txt", 12, "hash", time.Now(),
	)

	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, types.ParseStatusDeleting, status)
	_, persisted := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Empty(t, persisted.FilePath)
	require.Equal(t, types.ParseStatusDeleting, persisted.ParseStatus)
}

func TestCancelKnowledgeProcessingStateMatrix(t *testing.T) {
	for _, status := range []string{
		types.ParseStatusPending,
		types.ParseStatusProcessing,
		types.ParseStatusFinalizing,
		types.ParseStatusMoving,
		types.ParseStatusDeleting,
		types.ParseStatusCompleted,
		types.ParseStatusFailed,
	} {
		t.Run(status, func(t *testing.T) {
			db := newKnowledgeStorageTransactionDB(t)
			tenant, knowledge := seedKnowledgeStorageRows(t, db, 0, 1000, 0, 0, status)
			repo := NewKnowledgeRepository(db).(*knowledgeRepository)

			applied, current, err := repo.CancelKnowledgeProcessing(
				context.Background(), tenant.ID, knowledge.ID, time.Now(),
			)

			require.NoError(t, err)
			_, persisted := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
			if status == types.ParseStatusPending || status == types.ParseStatusProcessing ||
				status == types.ParseStatusFinalizing {
				require.True(t, applied)
				require.Equal(t, types.ParseStatusCancelled, current)
				require.Equal(t, types.ParseStatusCancelled, persisted.ParseStatus)
				require.Zero(t, persisted.PendingSubtasksCount)
				return
			}
			require.False(t, applied)
			require.Equal(t, status, current)
			require.Equal(t, status, persisted.ParseStatus)
		})
	}
}

func TestManualPatchMergesReservedMetadataAndRejectsMoving(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(
		t, db, 0, 1000, 0, 0, types.ParseStatusCompleted,
	)
	knowledge.Metadata = types.JSON(`{
		"_weknora_move_claim":{"task_id":"old-owner","stage":"completed"},
		"process_overrides":{"chunking":{"chunk_size":512}},
		"content":"old"
	}`)
	require.NoError(t, db.Model(&types.Knowledge{}).
		Where("id = ?", knowledge.ID).
		Update("metadata", knowledge.Metadata).Error)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)

	applied, _, err := repo.PatchManualKnowledge(
		context.Background(), tenant.ID, knowledge.ID,
		map[string]interface{}{
			"title":        "新标题",
			"parse_status": types.ParseStatusPending,
			"updated_at":   time.Now(),
		},
		types.JSON(`{"content":"new","format":"markdown","status":"publish","version":2,"updated_at":"now"}`),
	)
	require.NoError(t, err)
	require.True(t, applied)
	_, persisted := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Contains(t, string(persisted.Metadata), "old-owner")
	require.Contains(t, string(persisted.Metadata), "process_overrides")
	require.Contains(t, string(persisted.Metadata), `"content":"new"`)

	require.NoError(t, db.Model(&types.Knowledge{}).
		Where("id = ?", knowledge.ID).
		Update("parse_status", types.ParseStatusMoving).Error)
	applied, status, err := repo.PatchManualKnowledge(
		context.Background(), tenant.ID, knowledge.ID,
		map[string]interface{}{"title": "不得写入"},
		types.JSON(`{"content":"stale"}`),
	)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, types.ParseStatusMoving, status)
}
