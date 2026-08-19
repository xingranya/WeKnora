package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupKnowledgeUploadTestRepository(t *testing.T) interfaces.KnowledgeRepository {
	repo, _ := setupKnowledgeUploadTestRepositoryAndDB(t)
	return repo
}

func setupKnowledgeUploadTestRepositoryAndDB(t *testing.T) (interfaces.KnowledgeRepository, *gorm.DB) {
	t.Helper()

	dsn := "file:knowledge-upload-" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.Knowledge{},
		&types.KnowledgeUploadSession{},
		&types.KnowledgeUploadPart{},
	))
	return NewKnowledgeRepository(db), db
}

func newKnowledgeUploadTestSession(
	id string,
	tenantID uint64,
	kbID string,
	userID string,
	fileSize int64,
	status types.KnowledgeUploadStatus,
) *types.KnowledgeUploadSession {
	return &types.KnowledgeUploadSession{
		ID:              id,
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		UserID:          userID,
		FileName:        id + ".txt",
		FileSize:        fileSize,
		MIMEType:        "text/plain",
		FolderPath:      "",
		ChunkSize:       1024,
		Status:          status,
		TempPath:        "/tmp/" + id,
		Options:         types.JSON(`{}`),
		ExpiresAt:       time.Now().Add(time.Hour),
	}
}

func TestGetKnowledgeUploadSessionBindsAllOwnerDimensions(t *testing.T) {
	repo := setupKnowledgeUploadTestRepository(t)
	ctx := context.Background()
	session := newKnowledgeUploadTestSession(
		"upload-1", 11, "kb-1", "user-1", 1024, types.KnowledgeUploadCreated,
	)
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

	got, err := repo.GetKnowledgeUploadSession(ctx, 11, "kb-1", "user-1", "upload-1")
	require.NoError(t, err)
	require.Equal(t, session.ID, got.ID)
	require.Equal(t, session.TenantID, got.TenantID)
	require.Equal(t, session.KnowledgeBaseID, got.KnowledgeBaseID)
	require.Equal(t, session.UserID, got.UserID)

	tests := []struct {
		name     string
		tenantID uint64
		kbID     string
		userID   string
		uploadID string
	}{
		{name: "错误租户", tenantID: 12, kbID: "kb-1", userID: "user-1", uploadID: "upload-1"},
		{name: "错误知识库", tenantID: 11, kbID: "kb-2", userID: "user-1", uploadID: "upload-1"},
		{name: "错误用户", tenantID: 11, kbID: "kb-1", userID: "user-2", uploadID: "upload-1"},
		{name: "错误上传会话", tenantID: 11, kbID: "kb-1", userID: "user-1", uploadID: "upload-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := repo.GetKnowledgeUploadSession(ctx, tt.tenantID, tt.kbID, tt.userID, tt.uploadID)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			require.Nil(t, found)
		})
	}
}

func TestCreateKnowledgeUploadSessionWithQuotaBoundaries(t *testing.T) {
	t.Run("用户第五个活跃会话可创建且第六个被拒绝", func(t *testing.T) {
		repo := setupKnowledgeUploadTestRepository(t)
		ctx := context.Background()

		for range 4 {
			session := newKnowledgeUploadTestSession(
				uuid.NewString(), 21, "kb-1", "user-1", 1, types.KnowledgeUploadCreated,
			)
			require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))
		}

		fifth := newKnowledgeUploadTestSession(
			"upload-fifth", 21, "kb-1", "user-1", 1, types.KnowledgeUploadCreated,
		)
		require.NoError(t, repo.CreateKnowledgeUploadSessionWithQuota(ctx, fifth, 5, 1<<40))

		count, err := repo.CountActiveKnowledgeUploadSessions(ctx, 21, "user-1")
		require.NoError(t, err)
		require.EqualValues(t, 5, count)

		sixth := newKnowledgeUploadTestSession(
			"upload-sixth", 21, "kb-1", "user-1", 1, types.KnowledgeUploadCreated,
		)
		err = repo.CreateKnowledgeUploadSessionWithQuota(ctx, sixth, 5, 1<<40)
		require.ErrorIs(t, err, types.ErrUploadUserQuota)
		_, err = repo.GetKnowledgeUploadSession(ctx, 21, "kb-1", "user-1", sixth.ID)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("租户恰好十吉字节可创建且超过一字节被拒绝", func(t *testing.T) {
		const tenGiB int64 = 10 * 1024 * 1024 * 1024
		const oneMiB int64 = 1024 * 1024

		repo := setupKnowledgeUploadTestRepository(t)
		ctx := context.Background()
		existing := newKnowledgeUploadTestSession(
			"upload-existing", 22, "kb-1", "user-1", tenGiB-oneMiB, types.KnowledgeUploadUploading,
		)
		require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, existing))

		atLimit := newKnowledgeUploadTestSession(
			"upload-at-limit", 22, "kb-1", "user-2", oneMiB, types.KnowledgeUploadCreated,
		)
		require.NoError(t, repo.CreateKnowledgeUploadSessionWithQuota(ctx, atLimit, 5, tenGiB))

		total, err := repo.SumActiveKnowledgeUploadBytes(ctx, 22)
		require.NoError(t, err)
		require.Equal(t, tenGiB, total)

		overLimit := newKnowledgeUploadTestSession(
			"upload-over-limit", 22, "kb-1", "user-3", 1, types.KnowledgeUploadCreated,
		)
		err = repo.CreateKnowledgeUploadSessionWithQuota(ctx, overLimit, 5, tenGiB)
		require.ErrorIs(t, err, types.ErrUploadTenantQuota)
		_, err = repo.GetKnowledgeUploadSession(ctx, 22, "kb-1", "user-3", overLimit.ID)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})
}

func TestCompletingKnowledgeUploadCannotBeCancelledButCanExpire(t *testing.T) {
	repo := setupKnowledgeUploadTestRepository(t)
	ctx := context.Background()
	session := newKnowledgeUploadTestSession(
		"upload-completing", 31, "kb-1", "user-1", 1024, types.KnowledgeUploadCompleting,
	)
	session.ExpiresAt = time.Now().Add(-time.Hour)
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

	_, err := repo.CancelKnowledgeUploadSession(ctx, 31, "kb-1", "user-1", session.ID)
	require.ErrorIs(t, err, types.ErrUploadSessionState)

	expired, err := repo.ExpireKnowledgeUploadSession(ctx, session)
	require.NoError(t, err)
	require.True(t, expired)

	persisted, err := repo.GetKnowledgeUploadSession(ctx, 31, "kb-1", "user-1", session.ID)
	require.NoError(t, err)
	require.Equal(t, types.KnowledgeUploadExpiredCleanupPending, persisted.Status)
}

func TestCreateKnowledgeWithFileHashLockRechecksAndReservesQuota(t *testing.T) {
	repo, db := setupKnowledgeUploadTestRepositoryAndDB(t)
	ctx := context.Background()
	tenant := &types.Tenant{ID: 61, Name: "quota", StorageQuota: 100, StorageUsed: 10}
	require.NoError(t, db.Create(tenant).Error)

	creator := repo.(interface {
		CreateKnowledgeWithFileHashLock(context.Context, *types.Knowledge, int64) (*types.Knowledge, bool, error)
	})
	first := &types.Knowledge{
		ID: "knowledge-1", TenantID: tenant.ID, KnowledgeBaseID: "kb-1", Type: "file",
		FileName: "a.pdf", FileType: "pdf", FileHash: "same-hash", ParseStatus: types.ParseStatusPending,
	}
	require.NoError(t, first.SetSourceFileQuotaBytes(80))
	existing, created, err := creator.CreateKnowledgeWithFileHashLock(ctx, first, 80)
	require.NoError(t, err)
	require.True(t, created)
	require.Nil(t, existing)

	duplicate := &types.Knowledge{
		ID: "knowledge-2", TenantID: tenant.ID, KnowledgeBaseID: "kb-1", Type: "file",
		FileName: "b.pdf", FileType: "pdf", FileHash: "same-hash", ParseStatus: types.ParseStatusPending,
	}
	existing, created, err = creator.CreateKnowledgeWithFileHashLock(ctx, duplicate, 80)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, existing.ID)

	overQuota := &types.Knowledge{
		ID: "knowledge-3", TenantID: tenant.ID, KnowledgeBaseID: "kb-1", Type: "file",
		FileName: "c.pdf", FileType: "pdf", FileHash: "other-hash", ParseStatus: types.ParseStatusPending,
	}
	_, created, err = creator.CreateKnowledgeWithFileHashLock(ctx, overQuota, 11)
	require.Error(t, err)
	require.False(t, created)

	var persistedTenant types.Tenant
	require.NoError(t, db.First(&persistedTenant, tenant.ID).Error)
	require.Equal(t, int64(90), persistedTenant.StorageUsed)
	var count int64
	require.NoError(t, db.Model(&types.Knowledge{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestKnowledgeUploadHashLockKeyIsDeterministicAndScoped(t *testing.T) {
	key := knowledgeUploadHashLockKey(1, "kb", "PDF", "hash")
	require.Equal(t, key, knowledgeUploadHashLockKey(1, "kb", "pdf", "hash"))
	require.NotEqual(t, key, knowledgeUploadHashLockKey(2, "kb", "pdf", "hash"))
	require.NotEqual(t, key, knowledgeUploadHashLockKey(1, "other", "pdf", "hash"))
}

func TestListExpiredKnowledgeUploadSessionsIncludesCleanupPendingImmediately(t *testing.T) {
	repo := setupKnowledgeUploadTestRepository(t)
	ctx := context.Background()
	session := newKnowledgeUploadTestSession(
		"upload-cleanup", 62, "kb-1", "user-1", 1, types.KnowledgeUploadCompletedCleanupPending,
	)
	session.ExpiresAt = time.Now().Add(time.Hour)
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

	sessions, err := repo.ListExpiredKnowledgeUploadSessions(ctx, time.Now(), 10)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, session.ID, sessions[0].ID)
}

func TestListExpiredKnowledgeUploadSessionsIncludesCompletingBeforeTTL(t *testing.T) {
	repo := setupKnowledgeUploadTestRepository(t)
	ctx := context.Background()
	session := newKnowledgeUploadTestSession(
		"upload-recover", 64, "kb-1", "user-1", 1, types.KnowledgeUploadCompleting,
	)
	session.ExpiresAt = time.Now().Add(24 * time.Hour)
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

	sessions, err := repo.ListExpiredKnowledgeUploadSessions(ctx, time.Now(), 10)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, session.ID, sessions[0].ID)
}

func TestUpdateKnowledgeUploadSessionPersistsFinalizeState(t *testing.T) {
	repo := setupKnowledgeUploadTestRepository(t)
	ctx := context.Background()
	session := newKnowledgeUploadTestSession(
		"upload-finalize", 63, "kb-1", "user-1", 1, types.KnowledgeUploadCompleting,
	)
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

	session.FinalFilePath = "minio://files/63/upload-finalize/source.pdf"
	session.FinalizeStage = types.KnowledgeUploadFinalizeFileStored
	require.NoError(t, repo.UpdateKnowledgeUploadSession(ctx, session))

	persisted, err := repo.GetKnowledgeUploadSession(ctx, 63, "kb-1", "user-1", session.ID)
	require.NoError(t, err)
	require.Equal(t, session.FinalFilePath, persisted.FinalFilePath)
	require.Equal(t, types.KnowledgeUploadFinalizeFileStored, persisted.FinalizeStage)
}

func TestClaimKnowledgeUploadForCompletionClaimsWritableStatesOnce(t *testing.T) {
	tests := []struct {
		name   string
		status types.KnowledgeUploadStatus
	}{
		{name: "已创建", status: types.KnowledgeUploadCreated},
		{name: "上传中", status: types.KnowledgeUploadUploading},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := setupKnowledgeUploadTestRepository(t)
			ctx := context.Background()
			session := newKnowledgeUploadTestSession(
				"upload-claim", 41, "kb-1", "user-1", 1024, tt.status,
			)
			session.ReceivedBytes = session.FileSize
			require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

			claimed, err := repo.ClaimKnowledgeUploadForCompletion(ctx, 41, "kb-1", "user-1", session.ID)
			require.NoError(t, err)
			require.Equal(t, types.KnowledgeUploadCompleting, claimed.Status)

			persisted, err := repo.GetKnowledgeUploadSession(ctx, 41, "kb-1", "user-1", session.ID)
			require.NoError(t, err)
			require.Equal(t, types.KnowledgeUploadCompleting, persisted.Status)

			_, err = repo.ClaimKnowledgeUploadForCompletion(ctx, 41, "kb-1", "user-1", session.ID)
			require.ErrorIs(t, err, types.ErrUploadSessionState)
		})
	}
}

func TestClaimKnowledgeUploadForCompletionRejectsIncompleteSession(t *testing.T) {
	repo := setupKnowledgeUploadTestRepository(t)
	ctx := context.Background()
	session := newKnowledgeUploadTestSession(
		"upload-incomplete", 42, "kb-1", "user-1", 2048, types.KnowledgeUploadUploading,
	)
	session.ReceivedBytes = 1024
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

	_, err := repo.ClaimKnowledgeUploadForCompletion(ctx, 42, "kb-1", "user-1", session.ID)
	require.ErrorIs(t, err, types.ErrUploadIncomplete)
	persisted, err := repo.GetKnowledgeUploadSession(ctx, 42, "kb-1", "user-1", session.ID)
	require.NoError(t, err)
	require.Equal(t, types.KnowledgeUploadUploading, persisted.Status)
}

func TestConfirmKnowledgeUploadPartRejectsInvalidStateAndOutOfOrderPart(t *testing.T) {
	t.Run("完成中状态拒绝分片", func(t *testing.T) {
		repo := setupKnowledgeUploadTestRepository(t)
		ctx := context.Background()
		session := newKnowledgeUploadTestSession(
			"upload-invalid-state", 51, "kb-1", "user-1", 2048, types.KnowledgeUploadCompleting,
		)
		require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

		part := &types.KnowledgeUploadPart{
			SessionID: session.ID, PartNumber: 1, PartOffset: 0, PartSize: 1024, SHA256: "sha-1",
		}
		_, err := repo.ConfirmKnowledgeUploadPart(ctx, 51, "kb-1", "user-1", session.ID, part)
		require.ErrorIs(t, err, types.ErrUploadSessionState)

		_, err = repo.GetKnowledgeUploadPart(ctx, session.TenantID, session.KnowledgeBaseID, session.UserID, session.ID, part.PartNumber)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		persisted, err := repo.GetKnowledgeUploadSession(ctx, 51, "kb-1", "user-1", session.ID)
		require.NoError(t, err)
		require.Equal(t, types.KnowledgeUploadCompleting, persisted.Status)
		require.Zero(t, persisted.ReceivedBytes)
	})

	t.Run("非连续偏移拒绝分片", func(t *testing.T) {
		repo := setupKnowledgeUploadTestRepository(t)
		ctx := context.Background()
		session := newKnowledgeUploadTestSession(
			"upload-out-of-order", 52, "kb-1", "user-1", 2048, types.KnowledgeUploadCreated,
		)
		require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

		part := &types.KnowledgeUploadPart{
			SessionID: session.ID, PartNumber: 2, PartOffset: 1024, PartSize: 1024, SHA256: "sha-2",
		}
		_, err := repo.ConfirmKnowledgeUploadPart(ctx, 52, "kb-1", "user-1", session.ID, part)
		require.ErrorIs(t, err, types.ErrUploadPartOutOfOrder)

		_, err = repo.GetKnowledgeUploadPart(ctx, session.TenantID, session.KnowledgeBaseID, session.UserID, session.ID, part.PartNumber)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		persisted, err := repo.GetKnowledgeUploadSession(ctx, 52, "kb-1", "user-1", session.ID)
		require.NoError(t, err)
		require.Equal(t, types.KnowledgeUploadCreated, persisted.Status)
		require.Zero(t, persisted.ReceivedBytes)
	})
}
