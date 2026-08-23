package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type atomicKnowledgeStorageRepository interface {
	ReserveSourceFileQuota(
		context.Context, uint64, string, int64,
	) (applied bool, delta int64, err error)
	FinalizeIndexedKnowledge(
		context.Context, *types.Knowledge,
	) (applied bool, delta int64, err error)
	ResetIndexedKnowledgeStorage(
		context.Context, uint64, string, string,
	) (applied bool, delta int64, err error)
}

func TestResetIndexedKnowledgeStorageIsAtomicIdempotentAndKeepsSourceQuota(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(
		t, db, 150, 1000, 100, 20, types.ParseStatusProcessing,
	)
	repo := newAtomicKnowledgeStorageRepository(t, db)

	applied, delta, err := repo.ResetIndexedKnowledgeStorage(
		context.Background(), tenant.ID, knowledge.ID, "",
	)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, int64(-100), delta)
	applied, delta, err = repo.ResetIndexedKnowledgeStorage(
		context.Background(), tenant.ID, knowledge.ID, "",
	)
	require.NoError(t, err)
	require.True(t, applied)
	require.Zero(t, delta)

	persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, int64(50), persistedTenant.StorageUsed)
	require.Zero(t, persistedKnowledge.StorageSize)
	require.Equal(t, int64(20), persistedKnowledge.SourceFileQuotaBytes())
}

func TestResetIndexedKnowledgeStorageRequiresMoveOwner(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active", StorageUsed: 100}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID:              "move-reset",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-source",
		ParseStatus:     types.ParseStatusCompleted,
		StorageSize:     100,
	}
	require.NoError(t, db.Create(knowledge).Error)
	repo := NewKnowledgeRepository(db)
	mover := repo.(guardedKnowledgeMover)
	claimed, _, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, knowledge.ID,
		"kb-source", "kb-target", "move-owner", "reparse",
	)
	require.NoError(t, err)
	require.True(t, ok)
	storageRepo := repo.(atomicKnowledgeStorageRepository)

	_, _, err = storageRepo.ResetIndexedKnowledgeStorage(
		context.Background(), tenant.ID, knowledge.ID, "wrong-owner",
	)
	require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
	applied, delta, err := storageRepo.ResetIndexedKnowledgeStorage(
		context.Background(), tenant.ID, knowledge.ID, "move-owner",
	)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, int64(-100), delta)
	claimed.StorageSize = 0
	claimed.KnowledgeBaseID = "kb-target"
	staged, err := mover.StageClaimedKnowledgeMove(context.Background(), claimed, "move-owner")
	require.NoError(t, err)
	require.True(t, staged)

	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	require.Zero(t, persisted.StorageSize)
	require.Equal(t, types.ParseStatusMoving, persisted.ParseStatus)
	require.Contains(t, string(persisted.Metadata), "move-owner")
}

func newKnowledgeStorageTransactionDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.Knowledge{}))
	return db
}

func newAtomicKnowledgeStorageRepository(t *testing.T, db *gorm.DB) atomicKnowledgeStorageRepository {
	t.Helper()
	repo, ok := NewKnowledgeRepository(db).(atomicKnowledgeStorageRepository)
	require.True(t, ok, "knowledge repository must provide atomic storage finalization")
	return repo
}

func seedKnowledgeStorageRows(
	t *testing.T, db *gorm.DB, storageUsed, storageQuota, indexBytes, sourceBytes int64, status string,
) (*types.Tenant, *types.Knowledge) {
	t.Helper()
	tenant := &types.Tenant{
		Name:         "atomic-storage",
		Status:       "active",
		StorageUsed:  storageUsed,
		StorageQuota: storageQuota,
	}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID:                   uuid.NewString(),
		TenantID:             tenant.ID,
		KnowledgeBaseID:      uuid.NewString(),
		Type:                 "file",
		ParseStatus:          status,
		EnableStatus:         "disabled",
		SummaryStatus:        types.SummaryStatusNone,
		StorageSize:          indexBytes,
		SourceFileQuotaSize:  sourceBytes,
		PendingSubtasksCount: 3,
		Metadata:             types.JSON(`{"keep":"move-and-process-metadata"}`),
	}
	require.NoError(t, db.Create(knowledge).Error)
	return tenant, knowledge
}

func reloadStorageRows(
	t *testing.T, db *gorm.DB, tenantID uint64, knowledgeID string,
) (*types.Tenant, *types.Knowledge) {
	t.Helper()
	var tenant types.Tenant
	var knowledge types.Knowledge
	require.NoError(t, db.First(&tenant, tenantID).Error)
	require.NoError(t, db.First(&knowledge, "id = ?", knowledgeID).Error)
	return &tenant, &knowledge
}

func TestReserveSourceFileQuotaIsAtomicAndIdempotent(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(t, db, 10, 100, 0, 0, types.ParseStatusProcessing)
	repo := newAtomicKnowledgeStorageRepository(t, db)

	applied, delta, err := repo.ReserveSourceFileQuota(context.Background(), tenant.ID, knowledge.ID, 40)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, int64(40), delta)

	applied, delta, err = repo.ReserveSourceFileQuota(context.Background(), tenant.ID, knowledge.ID, 40)
	require.NoError(t, err)
	require.True(t, applied)
	require.Zero(t, delta)

	persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, int64(50), persistedTenant.StorageUsed)
	require.Equal(t, int64(40), persistedKnowledge.SourceFileQuotaBytes())
}

func TestReserveSourceFileQuotaConcurrentReplayCountsOnce(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(t, db, 10, 100, 0, 0, types.ParseStatusProcessing)
	repo := newAtomicKnowledgeStorageRepository(t, db)

	var wg sync.WaitGroup
	errorsByCall := make([]error, 2)
	for index := range errorsByCall {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errorsByCall[index] = repo.ReserveSourceFileQuota(
				context.Background(), tenant.ID, knowledge.ID, 40,
			)
		}()
	}
	wg.Wait()
	require.NoError(t, errorsByCall[0])
	require.NoError(t, errorsByCall[1])

	persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, int64(50), persistedTenant.StorageUsed)
	require.Equal(t, int64(40), persistedKnowledge.SourceFileQuotaBytes())
}

func TestReserveSourceFileQuotaRollsBackTenantWhenKnowledgeUpdateFails(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(t, db, 10, 100, 0, 0, types.ParseStatusProcessing)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_source_quota_marker
		BEFORE UPDATE OF source_file_quota_bytes ON knowledges
		BEGIN
			SELECT RAISE(ABORT, 'forced source quota marker failure');
		END;
	`).Error)
	repo := newAtomicKnowledgeStorageRepository(t, db)

	_, _, err := repo.ReserveSourceFileQuota(context.Background(), tenant.ID, knowledge.ID, 40)
	require.ErrorContains(t, err, "forced source quota marker failure")

	persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, int64(10), persistedTenant.StorageUsed)
	require.Zero(t, persistedKnowledge.SourceFileQuotaBytes())
}

func TestReserveSourceFileQuotaRejectsOverQuotaWithoutPartialWrite(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(t, db, 90, 100, 0, 0, types.ParseStatusProcessing)
	repo := newAtomicKnowledgeStorageRepository(t, db)

	_, _, err := repo.ReserveSourceFileQuota(context.Background(), tenant.ID, knowledge.ID, 20)
	require.Error(t, err)

	persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, int64(90), persistedTenant.StorageUsed)
	require.Zero(t, persistedKnowledge.SourceFileQuotaBytes())
}

func TestFinalizeIndexedKnowledgeIsAtomicIdempotentAndSelective(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(t, db, 10, 1000, 0, 20, types.ParseStatusProcessing)
	repo := newAtomicKnowledgeStorageRepository(t, db)
	now := time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)
	final := *knowledge
	final.ParseStatus = types.ParseStatusProcessing
	final.EnableStatus = "enabled"
	final.SummaryStatus = types.SummaryStatusNone
	final.StorageSize = 100
	final.ProcessedAt = &now
	final.UpdatedAt = now
	final.ErrorMessage = ""

	applied, delta, err := repo.FinalizeIndexedKnowledge(context.Background(), &final)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, int64(100), delta)
	applied, delta, err = repo.FinalizeIndexedKnowledge(context.Background(), &final)
	require.NoError(t, err)
	require.True(t, applied)
	require.Zero(t, delta)

	persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, int64(110), persistedTenant.StorageUsed)
	require.Equal(t, int64(100), persistedKnowledge.StorageSize)
	require.Equal(t, "enabled", persistedKnowledge.EnableStatus)
	require.Equal(t, types.ParseStatusProcessing, persistedKnowledge.ParseStatus)
	require.Equal(t, 3, persistedKnowledge.PendingSubtasksCount)
	require.JSONEq(t, string(knowledge.Metadata), string(persistedKnowledge.Metadata))
}

func TestFinalizeIndexedKnowledgeRollsBackTenantWhenKnowledgeUpdateFails(t *testing.T) {
	db := newKnowledgeStorageTransactionDB(t)
	tenant, knowledge := seedKnowledgeStorageRows(t, db, 10, 1000, 0, 0, types.ParseStatusProcessing)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_index_storage_update
		BEFORE UPDATE OF storage_size ON knowledges
		BEGIN
			SELECT RAISE(ABORT, 'forced index storage failure');
		END;
	`).Error)
	repo := newAtomicKnowledgeStorageRepository(t, db)
	final := *knowledge
	final.StorageSize = 100
	final.EnableStatus = "enabled"

	_, _, err := repo.FinalizeIndexedKnowledge(context.Background(), &final)
	require.ErrorContains(t, err, "forced index storage failure")

	persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
	require.Equal(t, int64(10), persistedTenant.StorageUsed)
	require.Zero(t, persistedKnowledge.StorageSize)
	require.Equal(t, "disabled", persistedKnowledge.EnableStatus)
}

func TestFinalizeIndexedKnowledgeRejectsOverQuotaAndMovingState(t *testing.T) {
	t.Run("over quota", func(t *testing.T) {
		db := newKnowledgeStorageTransactionDB(t)
		tenant, knowledge := seedKnowledgeStorageRows(t, db, 90, 100, 0, 0, types.ParseStatusProcessing)
		repo := newAtomicKnowledgeStorageRepository(t, db)
		final := *knowledge
		final.StorageSize = 20

		_, _, err := repo.FinalizeIndexedKnowledge(context.Background(), &final)
		require.Error(t, err)
		persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
		require.Equal(t, int64(90), persistedTenant.StorageUsed)
		require.Zero(t, persistedKnowledge.StorageSize)
	})

	t.Run("moving claim", func(t *testing.T) {
		db := newKnowledgeStorageTransactionDB(t)
		tenant, knowledge := seedKnowledgeStorageRows(t, db, 10, 100, 0, 0, types.ParseStatusMoving)
		repo := newAtomicKnowledgeStorageRepository(t, db)
		final := *knowledge
		final.ParseStatus = types.ParseStatusProcessing
		final.StorageSize = 20

		_, _, err := repo.FinalizeIndexedKnowledge(context.Background(), &final)
		require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
		persistedTenant, persistedKnowledge := reloadStorageRows(t, db, tenant.ID, knowledge.ID)
		require.Equal(t, int64(10), persistedTenant.StorageUsed)
		require.Zero(t, persistedKnowledge.StorageSize)
		require.Equal(t, types.ParseStatusMoving, persistedKnowledge.ParseStatus)
		require.JSONEq(t, string(knowledge.Metadata), string(persistedKnowledge.Metadata))
	})
}
