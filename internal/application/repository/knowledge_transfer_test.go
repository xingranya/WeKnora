package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTransferCheckpointReturnsStoredTimestampPrecision(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	// Emulate a database that stores fewer fractional digits than time.Now.
	require.NoError(t, db.Exec(`CREATE TRIGGER truncate_checkpoint_time AFTER UPDATE ON knowledges
        BEGIN UPDATE knowledges SET updated_at = strftime('%Y-%m-%d %H:%M:%S', NEW.updated_at) || '+00:00'
        WHERE id = NEW.id; END`).Error)
	row := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "source", ParseStatus: types.ParseStatusCompleted}
	require.NoError(t, db.Omit("custom_metadata").Create(row).Error)
	repo := NewKnowledgeRepository(db)
	before, err := repo.GetKnowledgeByID(context.Background(), 7, "doc")
	require.NoError(t, err)
	after := *before
	after.ParseStatus = types.ParseStatusProcessing
	require.NoError(t, repo.UpdateKnowledgeForTransfer(context.Background(), before, &after))
	require.Zero(t, after.UpdatedAt.Nanosecond())
	final := after
	final.KnowledgeBaseID = "target"
	final.ParseStatus = types.ParseStatusCompleted
	require.NoError(t, repo.UpdateKnowledgeForTransfer(context.Background(), &after, &final))
}

func TestTransferCheckpointPreservesCompanyMoveAndSourceQuota(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}))
	require.NoError(t, db.Create(&types.Tenant{ID: 7, Name: "tenant", StorageUsed: 12}).Error)
	row := &types.Knowledge{
		ID: "company-doc", TenantID: 7, KnowledgeBaseID: "source",
		ParseStatus: types.ParseStatusCompleted, SummaryStatus: types.SummaryStatusProcessing,
		StorageSize: 5, SourceFileQuotaSize: 7,
	}
	require.NoError(t, db.Omit("custom_metadata").Create(row).Error)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)
	ctx := context.Background()
	before, err := repo.GetKnowledgeByID(ctx, 7, row.ID)
	require.NoError(t, err)
	moving := *before
	moving.ParseStatus = types.ParseStatusMoving
	moving.Metadata = types.JSON(`{"_knowledge_transfer":{"task_id":"move-1","operation":"move","source_kb":"source","target_kb":"target","source_id":"company-doc","mode":"reparse","phase":"moving"}}`)
	require.NoError(t, repo.UpdateKnowledgeForTransfer(ctx, before, &moving))
	require.Equal(t, types.SummaryStatusFailed, moving.SummaryStatus)
	claim, err := decodeKnowledgeMoveClaim(moving.Metadata)
	require.NoError(t, err)
	require.Equal(t, "move-1", claim.TaskID)
	require.Equal(t, knowledgeMoveClaimStageActive, claim.Stage)
	_, err = repo.ClaimKnowledgeListForKBDelete(ctx, 7, "source", []string{row.ID})
	require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
	applied, _, err := repo.PatchKnowledgeUserFields(ctx, 7, row.ID, map[string]interface{}{"title": "陈旧写入"})
	require.NoError(t, err)
	require.False(t, applied)
	pending := moving
	pending.KnowledgeBaseID = "target"
	pending.ParseStatus = types.ParseStatusPending
	pending.StorageSize = 0
	pending.Metadata = types.JSON(`{"_knowledge_transfer":{"task_id":"move-1","operation":"move","source_kb":"source","target_kb":"target","source_id":"company-doc","mode":"reparse","phase":"reparse_pending"}}`)
	require.NoError(t, repo.UpdateKnowledgeForTransfer(ctx, &moving, &pending))
	claim, err = decodeKnowledgeMoveClaim(pending.Metadata)
	require.NoError(t, err)
	require.Equal(t, knowledgeMoveClaimStageCompleted, claim.Stage)
	var tenant types.Tenant
	require.NoError(t, db.First(&tenant, 7).Error)
	require.EqualValues(t, 7, tenant.StorageUsed)
	stored, err := repo.GetKnowledgeByID(ctx, 7, row.ID)
	require.NoError(t, err)
	require.EqualValues(t, 7, stored.SourceFileQuotaSize)
	require.Error(t, repo.UpdateKnowledgeForTransfer(ctx, &moving, &pending))
	require.NoError(t, db.First(&tenant, 7).Error)
	require.EqualValues(t, 7, tenant.StorageUsed)
}
