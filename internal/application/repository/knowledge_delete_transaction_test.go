package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type transactionalKnowledgeDeleter interface {
	DeleteKnowledgeListAndAdjustStorage(context.Context, uint64, string, []string) error
}

type knowledgeDeleteClaimer interface {
	ClaimKnowledgeListForKBDelete(context.Context, uint64, string, []string) ([]*types.Knowledge, error)
}

type guardedKnowledgeMover interface {
	ClaimKnowledgeForMove(
		context.Context,
		uint64,
		string,
		string,
		string,
		string,
		string,
	) (*types.Knowledge, bool, bool, error)
	StageClaimedKnowledgeMove(context.Context, *types.Knowledge, string) (bool, error)
	CompleteClaimedKnowledgeMove(context.Context, *types.Knowledge, string) (bool, error)
	FailClaimedKnowledgeMove(context.Context, uint64, string, string, string) (bool, error)
}

type deletedKnowledgeBaseRecoveryLister interface {
	ListDeletedKnowledgeBasesWithActiveKnowledge(context.Context, int) ([]*types.KnowledgeBase, error)
}

type deletedKnowledgeBaseCleanupDataSourceLister interface {
	ListKnowledgeBaseCleanupDataSourceIDs(context.Context, string) ([]string, error)
}

func newKnowledgeDeleteTransactionDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.TenantMember{},
		&types.KnowledgeBase{},
		&types.Knowledge{},
		&types.KnowledgeFolder{},
		&types.KnowledgeBaseShare{},
		&types.DataSource{},
		&types.SyncLog{},
		&types.TaskPendingOp{},
	))
	return db
}

func TestDeleteKnowledgeListAndAdjustStorageIsAtomicAndIdempotent(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active", StorageUsed: 1000}
	require.NoError(t, db.Create(tenant).Error)
	knowledgeList := []*types.Knowledge{
		{
			ID:                  "knowledge-1",
			TenantID:            tenant.ID,
			KnowledgeBaseID:     "kb-1",
			Type:                "file",
			ParseStatus:         types.ParseStatusCompleted,
			StorageSize:         100,
			SourceFileQuotaSize: 20,
		},
		{
			ID:                  "knowledge-2",
			TenantID:            tenant.ID,
			KnowledgeBaseID:     "kb-1",
			Type:                "file",
			ParseStatus:         types.ParseStatusCompleted,
			StorageSize:         200,
			SourceFileQuotaSize: 30,
		},
	}
	require.NoError(t, db.Create(&knowledgeList).Error)
	require.NoError(t, db.Create(&types.KnowledgeFolder{
		ID:              "folder-1",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-1",
		Path:            "docs",
	}).Error)
	repo := NewKnowledgeRepository(db)
	deleter, ok := repo.(transactionalKnowledgeDeleter)
	require.True(t, ok)
	claimer := repo.(knowledgeDeleteClaimer)
	_, err := claimer.ClaimKnowledgeListForKBDelete(
		context.Background(), tenant.ID, "kb-1", []string{"knowledge-1", "knowledge-2"},
	)
	require.NoError(t, err)

	require.NoError(t, deleter.DeleteKnowledgeListAndAdjustStorage(
		context.Background(), tenant.ID, "kb-1", []string{"knowledge-1", "knowledge-2"},
	))
	require.NoError(t, deleter.DeleteKnowledgeListAndAdjustStorage(
		context.Background(), tenant.ID, "kb-1", []string{"knowledge-1", "knowledge-2"},
	))

	var activeKnowledge int64
	require.NoError(t, db.Model(&types.Knowledge{}).Count(&activeKnowledge).Error)
	assert.Zero(t, activeKnowledge)
	var activeFolders int64
	require.NoError(t, db.Model(&types.KnowledgeFolder{}).Count(&activeFolders).Error)
	assert.Zero(t, activeFolders)
	var persistedTenant types.Tenant
	require.NoError(t, db.First(&persistedTenant, tenant.ID).Error)
	assert.Equal(t, int64(650), persistedTenant.StorageUsed)
}

func TestDeleteKnowledgeListAndAdjustStorageRollsBackWhenTenantIsMissing(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	knowledge := &types.Knowledge{
		ID:                  "knowledge-orphan",
		TenantID:            99,
		KnowledgeBaseID:     "kb-1",
		Type:                "file",
		ParseStatus:         types.ParseStatusCompleted,
		SourceFileQuotaSize: 100,
	}
	require.NoError(t, db.Create(knowledge).Error)
	require.NoError(t, db.Create(&types.KnowledgeFolder{
		ID:              "folder-orphan",
		TenantID:        99,
		KnowledgeBaseID: "kb-1",
		Path:            "docs",
	}).Error)
	repo := NewKnowledgeRepository(db)
	deleter, ok := repo.(transactionalKnowledgeDeleter)
	require.True(t, ok)

	err := deleter.DeleteKnowledgeListAndAdjustStorage(
		context.Background(), 99, "kb-1", []string{knowledge.ID},
	)

	require.Error(t, err)
	var activeKnowledge int64
	require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", knowledge.ID).Count(&activeKnowledge).Error)
	assert.Equal(t, int64(1), activeKnowledge)
	var activeFolders int64
	require.NoError(t, db.Model(&types.KnowledgeFolder{}).Where("id = ?", "folder-orphan").Count(&activeFolders).Error)
	assert.Equal(t, int64(1), activeFolders)
}

func TestDeleteKnowledgeListAndAdjustStorageRollsBackWhenQuotaUpdateFails(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active", StorageUsed: 500}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID:                  "knowledge-quota-failure",
		TenantID:            tenant.ID,
		KnowledgeBaseID:     "kb-1",
		Type:                "file",
		ParseStatus:         types.ParseStatusCompleted,
		SourceFileQuotaSize: 100,
	}
	require.NoError(t, db.Create(knowledge).Error)
	require.NoError(t, db.Create(&types.KnowledgeFolder{
		ID:              "folder-quota-failure",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-1",
		Path:            "docs",
	}).Error)
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(
		"test:fail_tenant_quota_update",
		func(tx *gorm.DB) {
			if tx.Statement.Table == "tenants" {
				tx.AddError(errors.New("forced tenant quota update failure"))
			}
		},
	))
	repo := NewKnowledgeRepository(db)
	deleter := repo.(transactionalKnowledgeDeleter)
	claimer := repo.(knowledgeDeleteClaimer)
	_, err := claimer.ClaimKnowledgeListForKBDelete(
		context.Background(), tenant.ID, "kb-1", []string{knowledge.ID},
	)
	require.NoError(t, err)

	err = deleter.DeleteKnowledgeListAndAdjustStorage(
		context.Background(), tenant.ID, "kb-1", []string{knowledge.ID},
	)

	require.ErrorContains(t, err, "forced tenant quota update failure")
	var activeKnowledge int64
	require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", knowledge.ID).Count(&activeKnowledge).Error)
	assert.Equal(t, int64(1), activeKnowledge)
	var activeFolders int64
	require.NoError(t, db.Model(&types.KnowledgeFolder{}).
		Where("id = ?", "folder-quota-failure").Count(&activeFolders).Error)
	assert.Equal(t, int64(1), activeFolders)
	var persistedTenant types.Tenant
	require.NoError(t, db.First(&persistedTenant, tenant.ID).Error)
	assert.Equal(t, int64(500), persistedTenant.StorageUsed)
}

func TestDeleteKnowledgeListAndAdjustStorageDoesNotDeleteMovedKnowledge(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active", StorageUsed: 500}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID:                  "knowledge-moved",
		TenantID:            tenant.ID,
		KnowledgeBaseID:     "kb-target",
		Type:                "file",
		ParseStatus:         types.ParseStatusCompleted,
		SourceFileQuotaSize: 100,
	}
	require.NoError(t, db.Create(knowledge).Error)
	repo := NewKnowledgeRepository(db)
	deleter := repo.(transactionalKnowledgeDeleter)

	require.NoError(t, deleter.DeleteKnowledgeListAndAdjustStorage(
		context.Background(), tenant.ID, "kb-source", []string{knowledge.ID},
	))

	var activeKnowledge int64
	require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", knowledge.ID).Count(&activeKnowledge).Error)
	assert.Equal(t, int64(1), activeKnowledge)
	var persistedTenant types.Tenant
	require.NoError(t, db.First(&persistedTenant, tenant.ID).Error)
	assert.Equal(t, int64(500), persistedTenant.StorageUsed)
}

func TestDeleteKnowledgeListAndAdjustStorageSupportsCrossKnowledgeBaseBatch(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active", StorageUsed: 500}
	require.NoError(t, db.Create(tenant).Error)
	knowledgeList := []*types.Knowledge{
		{
			ID: "knowledge-cross-a", TenantID: tenant.ID, KnowledgeBaseID: "kb-a",
			ParseStatus: types.ParseStatusDeleting, StorageSize: 100, SourceFileQuotaSize: 20,
		},
		{
			ID: "knowledge-cross-b", TenantID: tenant.ID, KnowledgeBaseID: "kb-b",
			ParseStatus: types.ParseStatusDeleting, StorageSize: 50, SourceFileQuotaSize: 30,
		},
	}
	require.NoError(t, db.Create(&knowledgeList).Error)
	require.NoError(t, db.Create(&types.KnowledgeFolder{
		ID: "folder-kept", TenantID: tenant.ID, KnowledgeBaseID: "kb-a", Path: "docs",
	}).Error)
	deleter := NewKnowledgeRepository(db).(transactionalKnowledgeDeleter)

	require.NoError(t, deleter.DeleteKnowledgeListAndAdjustStorage(
		context.Background(), tenant.ID, "", []string{
			"knowledge-cross-a", "knowledge-cross-b", "knowledge-cross-a",
		},
	))
	require.NoError(t, deleter.DeleteKnowledgeListAndAdjustStorage(
		context.Background(), tenant.ID, "", []string{"knowledge-cross-a", "knowledge-cross-b"},
	))

	var active int64
	require.NoError(t, db.Model(&types.Knowledge{}).Count(&active).Error)
	assert.Zero(t, active)
	var folders int64
	require.NoError(t, db.Model(&types.KnowledgeFolder{}).Count(&folders).Error)
	assert.Equal(t, int64(1), folders, "跨知识库删除不能清理任一知识库的目录")
	var persistedTenant types.Tenant
	require.NoError(t, db.First(&persistedTenant, tenant.ID).Error)
	assert.Equal(t, int64(300), persistedTenant.StorageUsed)
}

func TestKnowledgeDeleteClaimPreventsMoveBeforeAnySideEffect(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID:              "knowledge-race",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-source",
		Type:            "file",
		ParseStatus:     types.ParseStatusCompleted,
	}
	require.NoError(t, db.Create(knowledge).Error)
	repo := NewKnowledgeRepository(db)
	claimer := repo.(knowledgeDeleteClaimer)
	mover := repo.(guardedKnowledgeMover)

	claimed, err := claimer.ClaimKnowledgeListForKBDelete(
		context.Background(), tenant.ID, "kb-source", []string{knowledge.ID},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, types.ParseStatusDeleting, claimed[0].ParseStatus)

	_, alreadyCompleted, moveClaimed, err := mover.ClaimKnowledgeForMove(
		context.Background(),
		tenant.ID,
		knowledge.ID,
		"kb-source",
		"kb-target",
		"move-task-after-delete",
		"reuse_vectors",
	)
	require.NoError(t, err)
	assert.False(t, alreadyCompleted)
	assert.False(t, moveClaimed)

	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	assert.Equal(t, "kb-source", persisted.KnowledgeBaseID)
	assert.Equal(t, types.ParseStatusDeleting, persisted.ParseStatus)
}

func TestKnowledgeMoveClaimPreventsKnowledgeBaseDelete(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID:              "knowledge-moving",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-source",
		Type:            "file",
		ParseStatus:     types.ParseStatusCompleted,
	}
	require.NoError(t, db.Create(knowledge).Error)
	repo := NewKnowledgeRepository(db)
	mover := repo.(guardedKnowledgeMover)
	deleter := repo.(knowledgeDeleteClaimer)

	claimedKnowledge, alreadyCompleted, moveClaimed, err := mover.ClaimKnowledgeForMove(
		context.Background(),
		tenant.ID,
		knowledge.ID,
		"kb-source",
		"kb-target",
		"move-task-first",
		"reuse_vectors",
	)
	require.NoError(t, err)
	require.True(t, moveClaimed)
	assert.False(t, alreadyCompleted)
	assert.Equal(t, types.ParseStatusMoving, claimedKnowledge.ParseStatus)

	_, err = deleter.ClaimKnowledgeListForKBDelete(
		context.Background(), tenant.ID, "kb-source", []string{knowledge.ID},
	)
	require.Error(t, err)

	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	assert.Equal(t, "kb-source", persisted.KnowledgeBaseID)
	assert.Equal(t, types.ParseStatusMoving, persisted.ParseStatus)
}

func TestKnowledgeMoveClaimIsOwnedAndRetryable(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.Knowledge{
		ID:              "knowledge-owned-move",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-source",
		Type:            "file",
		ParseStatus:     types.ParseStatusCompleted,
		Metadata:        types.JSON(`{"existing_internal_key":"preserved"}`),
		CustomMetadata:  types.JSON(`{"user_key":"preserved"}`),
	}).Error)
	mover := NewKnowledgeRepository(db).(guardedKnowledgeMover)

	claimed, completed, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-owned-move",
		"kb-source", "kb-target", "move-owner", "reuse_vectors",
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, completed)

	retried, completed, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-owned-move",
		"kb-source", "kb-target", "move-owner", "reuse_vectors",
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, completed)
	assert.Equal(t, claimed.ID, retried.ID)

	_, completed, ok, err = mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-owned-move",
		"kb-source", "kb-other", "move-competitor", "reuse_vectors",
	)
	require.NoError(t, err)
	assert.False(t, completed)
	assert.False(t, ok)

	claimed.KnowledgeBaseID = "kb-target"
	claimed.ParseStatus = types.ParseStatusCompleted
	updated, err := mover.CompleteClaimedKnowledgeMove(
		context.Background(), claimed, "move-owner",
	)
	require.NoError(t, err)
	require.True(t, updated)

	completedKnowledge, completed, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-owned-move",
		"kb-source", "kb-target", "move-owner", "reuse_vectors",
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, completed)
	assert.Equal(t, "kb-target", completedKnowledge.KnowledgeBaseID)
	metadata, err := completedKnowledge.Metadata.Map()
	require.NoError(t, err)
	assert.Equal(t, "preserved", metadata["existing_internal_key"])
	customMetadata, err := completedKnowledge.CustomMetadata.Map()
	require.NoError(t, err)
	assert.Equal(t, "preserved", customMetadata["user_key"])
}

func TestFailedKnowledgeMoveReleasesMovingStateForDelete(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.Knowledge{
		ID:              "knowledge-failed-move",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-source",
		Type:            "file",
		ParseStatus:     types.ParseStatusCompleted,
	}).Error)
	repo := NewKnowledgeRepository(db)
	mover := repo.(guardedKnowledgeMover)
	deleter := repo.(knowledgeDeleteClaimer)

	_, _, claimed, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-failed-move",
		"kb-source", "kb-target", "move-owner", "reuse_vectors",
	)
	require.NoError(t, err)
	require.True(t, claimed)

	failed, err := mover.FailClaimedKnowledgeMove(
		context.Background(), tenant.ID, "knowledge-failed-move", "move-competitor", "错误任务",
	)
	require.NoError(t, err)
	assert.False(t, failed)

	failed, err = mover.FailClaimedKnowledgeMove(
		context.Background(), tenant.ID, "knowledge-failed-move", "move-owner", "向量复制失败",
	)
	require.NoError(t, err)
	require.True(t, failed)

	claimedForDelete, err := deleter.ClaimKnowledgeListForKBDelete(
		context.Background(), tenant.ID, "kb-source", []string{"knowledge-failed-move"},
	)
	require.NoError(t, err)
	require.Len(t, claimedForDelete, 1)
	assert.Equal(t, types.ParseStatusDeleting, claimedForDelete[0].ParseStatus)
}

func TestFailClaimedKnowledgeMoveTruncatesUTF8Safely(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID: "knowledge-utf8-failure", TenantID: tenant.ID, KnowledgeBaseID: "kb-source",
		ParseStatus: types.ParseStatusCompleted,
	}
	require.NoError(t, db.Create(knowledge).Error)
	mover := NewKnowledgeRepository(db).(guardedKnowledgeMover)
	_, _, claimed, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, knowledge.ID,
		"kb-source", "kb-target", "move-utf8", "reparse",
	)
	require.NoError(t, err)
	require.True(t, claimed)

	failed, err := mover.FailClaimedKnowledgeMove(
		context.Background(), tenant.ID, knowledge.ID, "move-utf8", strings.Repeat("错", 1000),
	)
	require.NoError(t, err)
	require.True(t, failed)
	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	require.True(t, utf8.ValidString(persisted.ErrorMessage))
	require.LessOrEqual(t, len(persisted.ErrorMessage), 2000)
	require.NotContains(t, persisted.ErrorMessage, string(utf8.RuneError))
}

func TestReparseMoveStagingResumesWithSameOwner(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.Knowledge{
		ID:               "knowledge-reparse-stage",
		TenantID:         tenant.ID,
		KnowledgeBaseID:  "kb-source",
		Type:             "file",
		ParseStatus:      types.ParseStatusCompleted,
		EmbeddingModelID: "embed-source",
		FilePath:         "local://source.md",
	}).Error)
	mover := NewKnowledgeRepository(db).(guardedKnowledgeMover)

	claimed, completed, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-reparse-stage",
		"kb-source", "kb-target", "move-reparse-owner", "reparse",
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, completed)
	claimed.KnowledgeBaseID = "kb-target"
	claimed.EmbeddingModelID = "embed-target"
	claimed.ParseStatus = types.ParseStatusMoving
	staged, err := mover.StageClaimedKnowledgeMove(
		context.Background(), claimed, "move-reparse-owner",
	)
	require.NoError(t, err)
	require.True(t, staged)

	resumed, completed, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-reparse-stage",
		"kb-source", "kb-target", "move-reparse-owner", "reparse",
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, completed)
	assert.Equal(t, "kb-target", resumed.KnowledgeBaseID)
	assert.Equal(t, types.ParseStatusMoving, resumed.ParseStatus)

	resumed.ParseStatus = types.ParseStatusPending
	completedMove, err := mover.CompleteClaimedKnowledgeMove(
		context.Background(), resumed, "move-reparse-owner",
	)
	require.NoError(t, err)
	require.True(t, completedMove)

	finalKnowledge, completed, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, "knowledge-reparse-stage",
		"kb-source", "kb-target", "move-reparse-owner", "reparse",
	)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, completed)
	assert.Equal(t, types.ParseStatusPending, finalKnowledge.ParseStatus)
}

func TestKnowledgeMoveAndDeleteConcurrentClaimsHaveSingleWinner(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Create(&types.Knowledge{
		ID:              "knowledge-concurrent-claim",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-source",
		Type:            "file",
		ParseStatus:     types.ParseStatusCompleted,
	}).Error)
	repo := NewKnowledgeRepository(db)
	mover := repo.(guardedKnowledgeMover)
	deleter := repo.(knowledgeDeleteClaimer)

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var moveWon bool
	var moveErr error
	var deleteWon bool
	var deleteErr error
	go func() {
		defer wait.Done()
		<-start
		_, _, moveWon, moveErr = mover.ClaimKnowledgeForMove(
			context.Background(), tenant.ID, "knowledge-concurrent-claim",
			"kb-source", "kb-target", "move-concurrent", "reuse_vectors",
		)
	}()
	go func() {
		defer wait.Done()
		<-start
		claimed, err := deleter.ClaimKnowledgeListForKBDelete(
			context.Background(), tenant.ID, "kb-source", []string{"knowledge-concurrent-claim"},
		)
		deleteErr = err
		deleteWon = err == nil && len(claimed) == 1
	}()
	close(start)
	wait.Wait()

	if moveErr != nil {
		require.Error(t, moveErr)
	}
	if deleteErr != nil {
		require.Error(t, deleteErr)
	}
	assert.NotEqual(t, moveWon, deleteWon)

	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", "knowledge-concurrent-claim").Error)
	if moveWon {
		assert.Equal(t, types.ParseStatusMoving, persisted.ParseStatus)
	} else {
		assert.Equal(t, types.ParseStatusDeleting, persisted.ParseStatus)
	}
}

func TestListDeletedKnowledgeBasesWithActiveKnowledgeFindsRecoveryWork(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	knowledgeBase := &types.KnowledgeBase{ID: "kb-recovery", TenantID: tenant.ID, Name: "recovery"}
	require.NoError(t, db.Create(knowledgeBase).Error)
	knowledge := &types.Knowledge{
		ID:              "knowledge-recovery",
		TenantID:        tenant.ID,
		KnowledgeBaseID: knowledgeBase.ID,
		Type:            "file",
		ParseStatus:     types.ParseStatusCompleted,
	}
	require.NoError(t, db.Create(knowledge).Error)
	require.NoError(t, db.Delete(knowledgeBase).Error)
	repo := NewKnowledgeBaseRepository(db)
	lister := repo.(deletedKnowledgeBaseRecoveryLister)

	pending, err := lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)

	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, knowledgeBase.ID, pending[0].ID)
	require.NoError(t, db.Delete(knowledge).Error)
	require.NoError(t, db.Create(&types.KnowledgeFolder{
		ID:              "folder-recovery",
		TenantID:        tenant.ID,
		KnowledgeBaseID: knowledgeBase.ID,
		Path:            "empty",
	}).Error)
	pending, err = lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, db.Delete(&types.KnowledgeFolder{}, "id = ?", "folder-recovery").Error)
	pending, err = lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)
	require.NoError(t, err)
	assert.Empty(t, pending)

	share := &types.KnowledgeBaseShare{
		ID:              "share-recovery",
		KnowledgeBaseID: knowledgeBase.ID,
		OrganizationID:  "org-recovery",
		SharedByUserID:  "user-recovery",
		SourceTenantID:  tenant.ID,
		Permission:      types.OrgRoleViewer,
	}
	require.NoError(t, db.Create(share).Error)
	pending, err = lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, db.Delete(share).Error)

	dataSource := &types.DataSource{
		ID:              "data-source-recovery",
		TenantID:        tenant.ID,
		KnowledgeBaseID: knowledgeBase.ID,
		Name:            "恢复数据源",
		Type:            types.ConnectorTypeRSS,
		Status:          types.DataSourceStatusActive,
	}
	require.NoError(t, db.Create(dataSource).Error)
	pending, err = lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, db.Delete(dataSource).Error)
	require.NoError(t, db.Create(&types.SyncLog{
		ID:           "sync-log-recovery",
		DataSourceID: dataSource.ID,
		TenantID:     tenant.ID,
		Status:       "pending",
	}).Error)
	pending, err = lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	dataSourceLister := repo.(deletedKnowledgeBaseCleanupDataSourceLister)
	dataSourceIDs, err := dataSourceLister.ListKnowledgeBaseCleanupDataSourceIDs(
		context.Background(),
		knowledgeBase.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{dataSource.ID}, dataSourceIDs)
	require.NoError(t, db.Model(&types.SyncLog{}).
		Where("id = ?", "sync-log-recovery").
		Update("status", types.SyncLogStatusCanceled).Error)

	require.NoError(t, db.Exec(
		`INSERT INTO task_pending_ops
			(tenant_id, task_type, scope, scope_id, op, dedup_key, payload, fail_count, enqueued_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		tenant.ID,
		"wiki:ingest",
		types.TaskScopeKnowledgeBase,
		knowledgeBase.ID,
		"ingest",
		"knowledge-recovery",
		"{}",
		0,
	).Error)
	pending, err = lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, db.Exec(
		"DELETE FROM task_pending_ops WHERE scope = ? AND scope_id = ?",
		types.TaskScopeKnowledgeBase,
		knowledgeBase.ID,
	).Error)

	pending, err = lister.ListDeletedKnowledgeBasesWithActiveKnowledge(context.Background(), 100)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestCreateKnowledgeBaseRejectsSoftDeletedTenantInsideTransaction(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	require.NoError(t, db.Delete(tenant).Error)
	repo := NewKnowledgeBaseRepository(db)

	err := repo.CreateKnowledgeBase(context.Background(), &types.KnowledgeBase{
		ID:               "kb-after-tenant-delete",
		TenantID:         tenant.ID,
		Name:             "不应创建",
		EmbeddingModelID: "embed",
	})

	require.ErrorIs(t, err, ErrTenantNotFound)
	var count int64
	require.NoError(t, db.Unscoped().Model(&types.KnowledgeBase{}).
		Where("id = ?", "kb-after-tenant-delete").Count(&count).Error)
	assert.Zero(t, count)
}

func TestCreateKnowledgeBaseAndDeleteTenantConcurrentTransactionsStayConsistent(t *testing.T) {
	db := newKnowledgeDeleteTransactionDB(t)
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	knowledgeBaseRepo := NewKnowledgeBaseRepository(db)
	tenantRepo := NewTenantRepository(db)

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var createErr error
	var deleteErr error
	go func() {
		defer wait.Done()
		<-start
		createErr = knowledgeBaseRepo.CreateKnowledgeBase(context.Background(), &types.KnowledgeBase{
			ID:               "kb-tenant-race",
			TenantID:         tenant.ID,
			Name:             "并发知识库",
			EmbeddingModelID: "embed",
		})
	}()
	go func() {
		defer wait.Done()
		<-start
		deleteErr = tenantRepo.DeleteTenant(context.Background(), tenant.ID)
	}()
	close(start)
	wait.Wait()

	assert.NotEqual(t, createErr == nil, deleteErr == nil)
	var activeKnowledgeBases int64
	require.NoError(t, db.Model(&types.KnowledgeBase{}).
		Where("id = ?", "kb-tenant-race").Count(&activeKnowledgeBases).Error)
	var activeTenants int64
	require.NoError(t, db.Model(&types.Tenant{}).
		Where("id = ?", tenant.ID).Count(&activeTenants).Error)
	if createErr == nil {
		require.ErrorIs(t, deleteErr, ErrTenantHasKnowledgeBase)
		assert.Equal(t, int64(1), activeKnowledgeBases)
		assert.Equal(t, int64(1), activeTenants)
	} else {
		require.NoError(t, deleteErr)
		require.ErrorIs(t, createErr, ErrTenantNotFound)
		assert.Zero(t, activeKnowledgeBases)
		assert.Zero(t, activeTenants)
	}
}
