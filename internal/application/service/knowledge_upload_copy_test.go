package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type preparedUploadFileServiceStub struct {
	*createKnowledgeFileServiceStub
	content    []byte
	writeCalls int
}

func (s *preparedUploadFileServiceStub) PrepareReaderPath(
	_ context.Context, _ int64, _ string, _ string, _ uint64, knowledgeID string,
) (string, error) {
	return "stored/" + knowledgeID, nil
}

func (s *preparedUploadFileServiceStub) SaveReaderTo(
	_ context.Context, reader io.Reader, _ int64, _ string, _ string,
	_ uint64, _ string, filePath string,
) error {
	content, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.writeCalls++
	s.deletedPath = filePath
	s.content = content
	return nil
}

func (s *preparedUploadFileServiceStub) FinalizeReaderPath(
	_ context.Context, _ int64, _ string, _ string, _ uint64, _ string, filePath string,
) (string, error) {
	return filePath, nil
}

func (s *preparedUploadFileServiceStub) SaveReader(
	ctx context.Context, reader io.Reader, size int64, fileName, contentType string,
	tenantID uint64, knowledgeID string,
) (string, error) {
	path, err := s.PrepareReaderPath(ctx, size, fileName, contentType, tenantID, knowledgeID)
	if err != nil {
		return "", err
	}
	return path, s.SaveReaderTo(ctx, reader, size, fileName, contentType, tenantID, knowledgeID, path)
}

func (s *preparedUploadFileServiceStub) GetFile(_ context.Context, filePath string) (io.ReadCloser, error) {
	if filePath != s.deletedPath || len(s.content) == 0 {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(s.content)), nil
}

func TestValidateKnowledgeUploadPartRangeChecksEnd(t *testing.T) {
	session := &types.KnowledgeUploadSession{FileSize: 10, ChunkSize: 4}

	size, err := validateKnowledgeUploadPartRange(session, 0, 0, 3, 10)
	require.NoError(t, err)
	require.Equal(t, int64(4), size)

	size, err = validateKnowledgeUploadPartRange(session, 2, 8, 9, 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), size)

	_, err = validateKnowledgeUploadPartRange(session, 0, 0, 0, 10)
	require.Error(t, err)

	_, err = validateKnowledgeUploadPartRange(session, 2, 8, 10, 10)
	require.Error(t, err)
}

func TestKnowledgeUploadTempRootStaysUnderPersistentRoot(t *testing.T) {
	persistentRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_BASE_DIR", persistentRoot)
	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", "sessions")

	root, err := knowledgeUploadTempRoot()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(persistentRoot, "sessions"), root)

	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", filepath.Join(persistentRoot, "..", "escape"))
	_, err = knowledgeUploadTempRoot()
	require.Error(t, err)

	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", persistentRoot)
	_, err = knowledgeUploadTempRoot()
	require.Error(t, err)
}

func TestKnowledgeUploadChunkSizeRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB", "17")
	_, err := knowledgeUploadChunkBytes()
	require.Error(t, err)

	t.Setenv("KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB", "4")
	size, err := knowledgeUploadChunkBytes()
	require.NoError(t, err)
	require.Equal(t, int64(4*1024*1024), size)
}

func TestValidateKnowledgeUploadTempStorageDetectsDifferentReplicaRoots(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	persistentRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_BASE_DIR", persistentRoot)
	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", "replica-a")
	require.NoError(t, validateKnowledgeUploadTempStorage(context.Background(), client))

	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", "replica-b")
	err := validateKnowledgeUploadTempStorage(context.Background(), client)
	require.ErrorContains(t, err, "not shared across app replicas")
}

func TestFinishKnowledgeUploadCleanupRetriesFailedFileRemoval(t *testing.T) {
	persistentRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_BASE_DIR", persistentRoot)
	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", "upload-sessions")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upload.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeUploadSession{}, &types.KnowledgeUploadPart{}))
	repo := apprepo.NewKnowledgeRepository(db)

	session := &types.KnowledgeUploadSession{
		ID: "upload-cleanup", TenantID: 71, KnowledgeBaseID: "kb-1", UserID: "user-1",
		FileName: "file.pdf", FileSize: 1, MIMEType: "application/pdf", ChunkSize: 1,
		Status: types.KnowledgeUploadCancelledCleanupPending, Options: types.JSON(`{}`),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	tempRoot, err := knowledgeUploadTempRoot()
	require.NoError(t, err)
	session.TempPath = filepath.Join(tempRoot, "71", session.ID+".part")
	require.NoError(t, os.MkdirAll(session.TempPath, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(session.TempPath, "blocks-remove"), []byte("x"), 0o600))
	require.NoError(t, repo.CreateKnowledgeUploadSession(context.Background(), session))
	require.NoError(t, db.Create(&types.KnowledgeUploadPart{SessionID: session.ID, PartNumber: 0, PartSize: 1}).Error)

	service := &knowledgeService{repo: repo}
	err = service.finishKnowledgeUploadCleanup(context.Background(), session)
	require.Error(t, err)
	var persisted types.KnowledgeUploadSession
	require.NoError(t, db.First(&persisted, "id = ?", session.ID).Error)
	require.Equal(t, types.KnowledgeUploadCancelledCleanupPending, persisted.Status)
	require.NotEmpty(t, persisted.ErrorMessage)
	var partCount int64
	require.NoError(t, db.Model(&types.KnowledgeUploadPart{}).Count(&partCount).Error)
	require.Zero(t, partCount)

	require.NoError(t, os.RemoveAll(session.TempPath))
	require.NoError(t, service.finishKnowledgeUploadCleanup(context.Background(), session))
	require.NoError(t, db.First(&persisted, "id = ?", session.ID).Error)
	require.Equal(t, types.KnowledgeUploadCancelled, persisted.Status)
	require.Empty(t, persisted.ErrorMessage)
}

func TestExpiredKnowledgeUploadCleanupDeletesUnboundFinalFile(t *testing.T) {
	persistentRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_BASE_DIR", persistentRoot)
	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", "upload-sessions")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upload-final.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Knowledge{},
		&types.KnowledgeUploadSession{},
		&types.KnowledgeUploadPart{},
	))
	repo := apprepo.NewKnowledgeRepository(db)

	session := &types.KnowledgeUploadSession{
		ID: "upload-final-cleanup", TenantID: 72, KnowledgeBaseID: "kb-1", UserID: "user-1",
		FileName: "file.pdf", FileSize: 1, MIMEType: "application/pdf", ChunkSize: 1,
		Status: types.KnowledgeUploadExpiredCleanupPending, Options: types.JSON(`{}`),
		FinalFilePath: "stored/upload-final-cleanup", FinalizeStage: types.KnowledgeUploadFinalizeFileStored,
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	tempRoot, err := knowledgeUploadTempRoot()
	require.NoError(t, err)
	session.TempPath = filepath.Join(tempRoot, "72", session.ID+".part")
	require.NoError(t, os.MkdirAll(filepath.Dir(session.TempPath), 0o700))
	require.NoError(t, os.WriteFile(session.TempPath, []byte("x"), 0o600))
	require.NoError(t, repo.CreateKnowledgeUploadSession(context.Background(), session))

	fileSvc := &createKnowledgeFileServiceStub{}
	service := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 72}},
		fileSvc:   fileSvc,
	}
	require.NoError(t, service.finishKnowledgeUploadCleanup(
		context.WithValue(context.Background(), types.TenantIDContextKey, uint64(72)), session,
	))
	require.Equal(t, 1, fileSvc.deleteCalls)
	require.Equal(t, "stored/upload-final-cleanup", fileSvc.deletedPath)

	var persisted types.KnowledgeUploadSession
	require.NoError(t, db.First(&persisted, "id = ?", session.ID).Error)
	require.Equal(t, types.KnowledgeUploadExpired, persisted.Status)
	require.Empty(t, persisted.FinalFilePath)
	require.Equal(t, types.KnowledgeUploadFinalizeNone, persisted.FinalizeStage)
}

func TestEnsureKnowledgeUploadFinalFileResumesPreparedPath(t *testing.T) {
	persistentRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_BASE_DIR", persistentRoot)
	t.Setenv("KNOWLEDGE_UPLOAD_TEMP_DIR", "upload-sessions")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upload-resume.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeUploadSession{}, &types.KnowledgeUploadPart{}))
	repo := apprepo.NewKnowledgeRepository(db)

	session := &types.KnowledgeUploadSession{
		ID: "upload-resume-final", TenantID: 73, KnowledgeBaseID: "kb-1", UserID: "user-1",
		FileName: "file.pdf", FileSize: 7, MIMEType: "application/pdf", ChunkSize: 7,
		Status: types.KnowledgeUploadCompleting, Options: types.JSON(`{}`),
		FinalFilePath: "stored/upload-resume-final", FinalizeStage: types.KnowledgeUploadFinalizeFilePrepared,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	tempRoot, err := knowledgeUploadTempRoot()
	require.NoError(t, err)
	session.TempPath = filepath.Join(tempRoot, "73", session.ID+".part")
	require.NoError(t, os.MkdirAll(filepath.Dir(session.TempPath), 0o700))
	require.NoError(t, os.WriteFile(session.TempPath, []byte("payload"), 0o600))
	require.NoError(t, repo.CreateKnowledgeUploadSession(context.Background(), session))

	fileSvc := &preparedUploadFileServiceStub{createKnowledgeFileServiceStub: &createKnowledgeFileServiceStub{}}
	service := &knowledgeService{repo: repo}
	path, err := service.ensureKnowledgeUploadFinalFile(context.Background(), session, fileSvc)
	require.NoError(t, err)
	require.Equal(t, "stored/upload-resume-final", path)
	require.Equal(t, 1, fileSvc.writeCalls)
	require.Equal(t, []byte("payload"), fileSvc.content)

	persisted, err := repo.GetKnowledgeUploadSession(context.Background(), 73, "kb-1", "user-1", session.ID)
	require.NoError(t, err)
	require.Equal(t, types.KnowledgeUploadFinalizeFileStored, persisted.FinalizeStage)

	path, err = service.ensureKnowledgeUploadFinalFile(context.Background(), persisted, fileSvc)
	require.NoError(t, err)
	require.Equal(t, "stored/upload-resume-final", path)
	require.Equal(t, 1, fileSvc.writeCalls)
}

func TestCopyKnowledgeUploadPartWritesCompleteDataAndReturnsDigest(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("knowledge-upload-part\n"), 4097)
	expectedDigest := sha256.Sum256(payload)
	var destination bytes.Buffer

	written, digest, err := copyKnowledgeUploadPart(context.Background(), &destination, bytes.NewReader(payload))

	require.NoError(t, err)
	require.Equal(t, int64(len(payload)), written)
	require.Equal(t, hex.EncodeToString(expectedDigest[:]), digest)
	require.Equal(t, payload, destination.Bytes())
}

func TestKnowledgeUploadRedisLockPreservesAppError(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	service := &knowledgeService{redisClient: client}

	err := service.withKnowledgeUploadLock(context.Background(), "upload-1", func(context.Context) error {
		return werrors.NewConflictError("上传状态冲突")
	})

	appErr, ok := werrors.IsAppError(err)
	require.True(t, ok)
	require.Equal(t, werrors.ErrConflict, appErr.Code)
}

func TestCopyKnowledgeUploadPartCanceledContextDoesNotWrite(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var destination bytes.Buffer

	written, digest, err := copyKnowledgeUploadPart(ctx, &destination, bytes.NewReader([]byte("not written")))

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, written)
	require.Empty(t, digest)
	require.Zero(t, destination.Len())
}
