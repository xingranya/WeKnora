package service

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/common/redislock"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	defaultKnowledgeUploadMaxBytes       = int64(2 * 1024 * 1024 * 1024)
	defaultKnowledgeUploadChunkBytes     = int64(4 * 1024 * 1024)
	defaultKnowledgeUploadTTL            = 24 * time.Hour
	defaultKnowledgeUploadUserSessions   = int64(5)
	defaultKnowledgeUploadTenantInflight = int64(10 * 1024 * 1024 * 1024)
	knowledgeUploadRootIdentityKey       = "weknora:knowledge-upload:temp-root-identity"
	knowledgeUploadRootIdentityFile      = ".weknora-upload-root-id"
)

type knowledgeUploadRuntime struct {
	mu sync.Mutex
}

type knowledgeUploadAtomicCreator interface {
	CreateKnowledgeWithFileHashLock(
		ctx context.Context, knowledge *types.Knowledge, sourceFileQuotaBytes int64,
	) (existing *types.Knowledge, created bool, err error)
}

func (s *knowledgeService) withKnowledgeUploadLock(
	ctx context.Context, uploadID string, operation func(context.Context) error,
) error {
	runtime := s.uploadRuntime(uploadID)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if s.redisClient == nil {
		return operation(ctx)
	}
	err := redislock.WithRenewableLock(
		ctx,
		s.redisClient,
		"weknora:knowledge-upload:"+uploadID,
		2*time.Minute,
		30*time.Second,
		operation,
	)
	var appErr *werrors.AppError
	if errors.As(err, &appErr) && err.Error() == appErr.Error() {
		return appErr
	}
	return err
}

func envInt64(name string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func knowledgeUploadMaxBytes() int64 {
	return envInt64("KNOWLEDGE_UPLOAD_MAX_FILE_SIZE_MB", 2048) * 1024 * 1024
}

func knowledgeUploadChunkBytes() (int64, error) {
	raw := strings.TrimSpace(os.Getenv("KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB"))
	if raw == "" {
		return defaultKnowledgeUploadChunkBytes, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 || value > 16 {
		return 0, fmt.Errorf("KNOWLEDGE_UPLOAD_CHUNK_SIZE_MB must be an integer between 1 and 16")
	}
	return value * 1024 * 1024, nil
}

func knowledgeUploadTTL() time.Duration {
	return time.Duration(envInt64("KNOWLEDGE_UPLOAD_SESSION_TTL_HOURS", 24)) * time.Hour
}

func knowledgeUploadPersistentRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("LOCAL_STORAGE_BASE_DIR"))
	if root == "" {
		root = "/data/files"
	}
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve persistent upload root: %w", err)
	}
	return absRoot, nil

}

func knowledgeUploadTempRoot() (string, error) {
	persistentRoot, err := knowledgeUploadPersistentRoot()
	if err != nil {
		return "", err
	}
	configured := strings.TrimSpace(os.Getenv("KNOWLEDGE_UPLOAD_TEMP_DIR"))
	if configured == "" {
		configured = filepath.Join(persistentRoot, "upload-sessions")
	} else if !filepath.IsAbs(configured) {
		configured = filepath.Join(persistentRoot, configured)
	}
	tempRoot, err := secutils.SafePathUnderBase(persistentRoot, configured)
	if err != nil {
		return "", fmt.Errorf("KNOWLEDGE_UPLOAD_TEMP_DIR must stay under LOCAL_STORAGE_BASE_DIR: %w", err)
	}
	if tempRoot == persistentRoot {
		return "", fmt.Errorf("KNOWLEDGE_UPLOAD_TEMP_DIR must be a dedicated subdirectory of LOCAL_STORAGE_BASE_DIR")
	}
	return tempRoot, nil
}

func ensureKnowledgeUploadRootIdentity(tempRoot string) (string, error) {
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return "", fmt.Errorf("create upload temp root: %w", err)
	}
	identityPath := filepath.Join(tempRoot, knowledgeUploadRootIdentityFile)
	identity := uuid.NewString()
	file, err := os.OpenFile(identityPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		if _, writeErr := io.WriteString(file, identity); writeErr != nil {
			_ = file.Close()
			_ = os.Remove(identityPath)
			return "", fmt.Errorf("write upload temp root identity: %w", writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(identityPath)
			return "", fmt.Errorf("close upload temp root identity: %w", closeErr)
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create upload temp root identity: %w", err)
	}
	content, readErr := os.ReadFile(identityPath)
	if readErr != nil {
		return "", fmt.Errorf("read upload temp root identity: %w", readErr)
	}
	identity = strings.TrimSpace(string(content))
	if _, parseErr := uuid.Parse(identity); parseErr != nil {
		return "", fmt.Errorf("invalid upload temp root identity: %w", parseErr)
	}
	return identity, nil
}

func validateKnowledgeUploadTempStorage(ctx context.Context, redisClient *redis.Client) error {
	if redisClient == nil || strings.TrimSpace(os.Getenv("KNOWLEDGE_UPLOAD_TEMP_DIR")) == "" {
		return nil
	}
	tempRoot, err := knowledgeUploadTempRoot()
	if err != nil {
		return err
	}
	identity, err := ensureKnowledgeUploadRootIdentity(tempRoot)
	if err != nil {
		return err
	}
	created, err := redisClient.SetNX(ctx, knowledgeUploadRootIdentityKey, identity, 0).Result()
	if err != nil {
		return fmt.Errorf("validate shared upload temp root through Redis: %w", err)
	}
	if created {
		return nil
	}
	expected, err := redisClient.Get(ctx, knowledgeUploadRootIdentityKey).Result()
	if err != nil {
		return fmt.Errorf("read shared upload temp root identity: %w", err)
	}
	if expected != identity {
		return fmt.Errorf("KNOWLEDGE_UPLOAD_TEMP_DIR is not shared across app replicas; configure shared RWX storage or run a single app replica")
	}
	return nil
}

func (s *knowledgeService) uploadRuntime(uploadID string) *knowledgeUploadRuntime {
	entry, _ := s.uploadRuntimes.LoadOrStore(uploadID, &knowledgeUploadRuntime{})
	return entry.(*knowledgeUploadRuntime)
}

func (s *knowledgeService) InitializeKnowledgeUpload(
	ctx context.Context, kbID, userID string, init types.KnowledgeUploadInit,
) (*types.KnowledgeUploadSession, error) {
	if init.FileSize <= 0 || init.FileSize > knowledgeUploadMaxBytes() {
		return nil, werrors.NewBadRequestError(fmt.Sprintf("文件大小必须在 1 字节到 %dMB 之间", knowledgeUploadMaxBytes()/1024/1024))
	}
	fileName := filepath.Base(strings.TrimSpace(init.FileName))
	if fileName == "." || fileName == "" || !isValidFileType(fileName) {
		return nil, ErrInvalidFileType
	}
	safeName, ok := secutils.ValidateInput(fileName)
	if !ok {
		return nil, werrors.NewValidationError("文件名包含非法字符")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if kb.Type == types.KnowledgeBaseTypeFAQ {
		return nil, werrors.NewBadRequestError("FAQ 知识库不支持文件上传")
	}
	if err := s.checkStorageEngineConfigured(ctx, kb); err != nil {
		return nil, err
	}
	eff, err := resolveFileImportProcessConfig(ctx, kb, getFileType(safeName), init.Options.ProcessConfig, init.Options.EnableMultimodel)
	if err != nil {
		return nil, err
	}
	parserEngine := eff.ChunkingConfig.ResolveParserEngine(getFileType(safeName))
	parserMaxBytes := int64(100 * 1024 * 1024)
	if parserEngine == "" || parserEngine == docparser.BuiltinEngineName {
		parserMaxBytes = 50 * 1024 * 1024
	} else if parserEngine == docparser.MinerUEngineName {
		parserMaxBytes = knowledgeUploadMaxBytes()
	}
	if init.FileSize > parserMaxBytes {
		return nil, werrors.NewBadRequestError(fmt.Sprintf(
			"当前解析引擎最多支持 %dMB 文件，请选择支持大文件的解析引擎",
			parserMaxBytes/(1024*1024),
		))
	}
	tenantID := ctx.Value(types.TenantIDContextKey).(uint64)
	folderPath, err := normalizeTargetFolderPath(ctx, init.FolderPath)
	if err != nil {
		return nil, err
	}
	createdBy, _ := ctx.Value(types.UserIDContextKey).(string)
	optionsJSON, err := json.Marshal(init.Options)
	if err != nil {
		return nil, err
	}
	uploadID := uuid.NewString()
	chunkSize, err := knowledgeUploadChunkBytes()
	if err != nil {
		return nil, err
	}
	tempRoot, err := knowledgeUploadTempRoot()
	if err != nil {
		return nil, err
	}
	tempDir := filepath.Join(tempRoot, strconv.FormatUint(tenantID, 10))
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return nil, fmt.Errorf("create upload staging directory: %w", err)
	}
	tempPath := filepath.Join(tempDir, uploadID+".part")
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create upload staging file: %w", err)
	}
	_ = file.Close()
	session := &types.KnowledgeUploadSession{
		ID: uploadID, TenantID: tenantID, KnowledgeBaseID: kbID, UserID: userID,
		FileName: safeName, FileSize: init.FileSize, MIMEType: strings.TrimSpace(init.MIMEType),
		LastModified: init.LastModified, FolderPath: folderPath, ChunkSize: chunkSize,
		Status: types.KnowledgeUploadCreated, TempPath: tempPath, Options: types.JSON(optionsJSON),
		ExpiresAt: time.Now().Add(knowledgeUploadTTL()),
	}
	if session.MIMEType == "" {
		session.MIMEType = "application/octet-stream"
	}
	if err := s.repo.CreateKnowledgeUploadSessionWithQuota(ctx, session, defaultKnowledgeUploadUserSessions, defaultKnowledgeUploadTenantInflight); err != nil {
		cleanupErr := os.Remove(tempPath)
		if errors.Is(cleanupErr, os.ErrNotExist) {
			cleanupErr = nil
		}
		if errors.Is(err, types.ErrUploadUserQuota) {
			err = werrors.NewConflictError("未完成上传已达到 5 个，请先完成或取消现有任务")
		}
		if errors.Is(err, types.ErrUploadTenantQuota) {
			err = werrors.NewConflictError("当前空间未完成上传已超过 10GB")
		}
		return nil, errors.Join(err, cleanupErr)
	}
	if err := s.repo.EnsureKnowledgeFolderPath(ctx, tenantID, kbID, folderPath, createdBy); err != nil {
		session.Status = types.KnowledgeUploadCancelledCleanupPending
		stateErr := s.repo.UpdateKnowledgeUploadSession(ctx, session)
		cleanupErr := s.finishKnowledgeUploadCleanup(ctx, session)
		return nil, errors.Join(err, stateErr, cleanupErr)
	}
	return session, nil
}

func (s *knowledgeService) GetKnowledgeUpload(ctx context.Context, kbID, uploadID, userID string) (*types.KnowledgeUploadSession, error) {
	tenantID := ctx.Value(types.TenantIDContextKey).(uint64)
	session, err := s.repo.GetKnowledgeUploadSession(ctx, tenantID, kbID, userID, uploadID)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func copyKnowledgeUploadPart(ctx context.Context, dst io.Writer, src io.Reader) (int64, string, error) {
	hash := sha256.New()
	writer := io.MultiWriter(dst, hash)
	buffer := make([]byte, 64*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, "", err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			m, writeErr := writer.Write(buffer[:n])
			written += int64(m)
			if writeErr != nil {
				return written, "", writeErr
			}
			if m != n {
				return written, "", io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return written, "", readErr
		}
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *knowledgeService) WriteKnowledgeUploadPart(
	ctx context.Context, kbID, uploadID, userID string, partNumber int, offset, end, total int64, expectedSHA string, body io.Reader,
) (*types.KnowledgeUploadSession, error) {
	var result *types.KnowledgeUploadSession
	err := s.withKnowledgeUploadLock(ctx, uploadID, func(lockCtx context.Context) error {
		var writeErr error
		result, writeErr = s.writeKnowledgeUploadPart(lockCtx, kbID, uploadID, userID, partNumber, offset, end, total, expectedSHA, body)
		return writeErr
	})
	return result, err
}

func (s *knowledgeService) writeKnowledgeUploadPart(
	ctx context.Context, kbID, uploadID, userID string, partNumber int, offset, end, total int64, expectedSHA string, body io.Reader,
) (*types.KnowledgeUploadSession, error) {
	if partNumber < 0 || offset < 0 {
		return nil, werrors.NewBadRequestError("分片编号或偏移无效")
	}
	expectedSHA = strings.ToLower(strings.TrimSpace(expectedSHA))
	decoded, err := hex.DecodeString(expectedSHA)
	if err != nil || len(decoded) != sha256.Size {
		return nil, werrors.NewBadRequestError("X-Chunk-SHA256 无效")
	}
	session, err := s.GetKnowledgeUpload(ctx, kbID, uploadID, userID)
	if err != nil {
		return nil, err
	}
	if session.Status.Terminal() || session.Status == types.KnowledgeUploadCompleting {
		return nil, werrors.NewConflictError("上传会话已不可写")
	}
	expectedSize, err := validateKnowledgeUploadPartRange(session, partNumber, offset, end, total)
	if err != nil {
		return nil, err
	}
	if existing, partErr := s.repo.GetKnowledgeUploadPart(ctx, session.TenantID, kbID, userID, uploadID, partNumber); partErr == nil {
		if existing.PartOffset != offset || existing.PartSize != expectedSize || existing.SHA256 != expectedSHA {
			return nil, werrors.NewConflictError("分片与已确认内容冲突")
		}
		return session, nil
	} else if !errors.Is(partErr, gorm.ErrRecordNotFound) {
		return nil, partErr
	}
	if offset != session.ReceivedBytes {
		return nil, werrors.NewConflictError(fmt.Sprintf("分片必须从已确认偏移 %d 继续", session.ReceivedBytes))
	}
	file, err := os.OpenFile(session.TempPath, os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := file.Truncate(offset); err != nil {
		return nil, err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	written, actualSHA, err := copyKnowledgeUploadPart(ctx, file, io.LimitReader(body, expectedSize+1))
	if err != nil || written != expectedSize || actualSHA != expectedSHA {
		_ = file.Truncate(offset)
		if err != nil {
			return nil, err
		}
		if written != expectedSize {
			return nil, werrors.NewBadRequestError("分片大小不正确")
		}
		return nil, werrors.NewBadRequestError("分片 SHA256 校验失败")
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	part := &types.KnowledgeUploadPart{
		SessionID: uploadID, PartNumber: partNumber, PartOffset: offset,
		PartSize: expectedSize, SHA256: expectedSHA,
	}
	updated, err := s.repo.ConfirmKnowledgeUploadPart(ctx, session.TenantID, kbID, userID, uploadID, part)
	if err != nil {
		if errors.Is(err, types.ErrUploadPartConflict) || errors.Is(err, types.ErrUploadPartOutOfOrder) || errors.Is(err, types.ErrUploadSessionState) {
			_ = file.Truncate(offset)
			return nil, werrors.NewConflictError("上传分片与当前会话状态冲突")
		}
		_ = file.Truncate(offset)
		return nil, err
	}
	updated.ReceivedParts = append(session.ReceivedParts, partNumber)
	updated.ReceivedPartHashes = make(map[int]string, len(session.ReceivedPartHashes)+1)
	for number, hash := range session.ReceivedPartHashes {
		updated.ReceivedPartHashes[number] = hash
	}
	updated.ReceivedPartHashes[partNumber] = expectedSHA
	return updated, nil
}

func validateKnowledgeUploadPartRange(
	session *types.KnowledgeUploadSession, partNumber int, offset, end, total int64,
) (int64, error) {
	if session == nil || partNumber < 0 || total != session.FileSize ||
		offset != int64(partNumber)*session.ChunkSize || offset < 0 || offset >= total {
		return 0, werrors.NewBadRequestError("Content-Range 与上传会话不一致")
	}
	expectedSize := session.ChunkSize
	if remaining := total - offset; remaining < expectedSize {
		expectedSize = remaining
	}
	if end != offset+expectedSize-1 {
		return 0, werrors.NewBadRequestError("Content-Range 结束位置与分片大小不一致")
	}
	return expectedSize, nil
}

func calculatePathMD5(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func isKnowledgeUploadKnowledgeNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, apprepo.ErrKnowledgeNotFound)
}

func (s *knowledgeService) clearKnowledgeUploadFinalFile(
	ctx context.Context,
	session *types.KnowledgeUploadSession,
	fileSvc interfaces.FileService,
) error {
	if session.FinalFilePath == "" {
		return nil
	}
	if err := fileSvc.DeleteFile(ctx, session.FinalFilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete staged final file: %w", err)
	}
	session.FinalFilePath = ""
	session.FinalizeStage = types.KnowledgeUploadFinalizeNone
	if err := s.repo.UpdateKnowledgeUploadSession(ctx, session); err != nil {
		return fmt.Errorf("clear staged final file state: %w", err)
	}
	return nil
}

func (s *knowledgeService) ensureKnowledgeUploadFinalFile(
	ctx context.Context,
	session *types.KnowledgeUploadSession,
	fileSvc interfaces.FileService,
) (string, error) {
	prepared, ok := fileSvc.(interfaces.PreparedStreamingFileService)
	if !ok {
		return "", werrors.NewBadRequestError("当前存储后端不支持可恢复的流式上传")
	}
	if session.FinalFilePath != "" &&
		(session.FinalizeStage == types.KnowledgeUploadFinalizeFileStored ||
			session.FinalizeStage == types.KnowledgeUploadFinalizeKnowledgeCreated) {
		reader, err := fileSvc.GetFile(ctx, session.FinalFilePath)
		if err == nil {
			var probe [1]byte
			_, readErr := reader.Read(probe[:])
			closeErr := reader.Close()
			if (readErr == nil || errors.Is(readErr, io.EOF)) && closeErr == nil {
				return session.FinalFilePath, nil
			}
			err = errors.Join(readErr, closeErr)
		}
		return "", fmt.Errorf("stored final file is unavailable: %w", err)
	}

	if session.FinalFilePath == "" {
		filePath, err := prepared.PrepareReaderPath(
			ctx, session.FileSize, session.FileName, session.MIMEType, session.TenantID, session.ID,
		)
		if err != nil {
			return "", err
		}
		session.FinalFilePath = filePath
		session.FinalizeStage = types.KnowledgeUploadFinalizeFilePrepared
		if err := s.repo.UpdateKnowledgeUploadSession(ctx, session); err != nil {
			cleanupErr := fileSvc.DeleteFile(ctx, filePath)
			if cleanupErr == nil || errors.Is(cleanupErr, os.ErrNotExist) {
				cleanupErr = nil
				session.FinalFilePath = ""
				session.FinalizeStage = types.KnowledgeUploadFinalizeNone
			}
			return "", errors.Join(fmt.Errorf("persist prepared final file path: %w", err), cleanupErr)
		}
	}

	file, err := os.Open(session.TempPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if err := prepared.SaveReaderTo(
		ctx, file, session.FileSize, session.FileName, session.MIMEType,
		session.TenantID, session.ID, session.FinalFilePath,
	); err != nil {
		return "", err
	}
	finalPath, err := prepared.FinalizeReaderPath(
		ctx, session.FileSize, session.FileName, session.MIMEType,
		session.TenantID, session.ID, session.FinalFilePath,
	)
	if err != nil {
		return "", err
	}
	session.FinalFilePath = finalPath
	session.FinalizeStage = types.KnowledgeUploadFinalizeFileStored
	if err := s.repo.UpdateKnowledgeUploadSession(ctx, session); err != nil {
		return "", fmt.Errorf("persist stored final file stage: %w", err)
	}
	return session.FinalFilePath, nil
}

func (s *knowledgeService) createKnowledgeFromStagedUpload(
	ctx context.Context, session *types.KnowledgeUploadSession,
) (*types.Knowledge, error) {
	options, err := session.ParsedOptions()
	if err != nil {
		return nil, err
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, session.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	eff, err := resolveFileImportProcessConfig(ctx, kb, getFileType(session.FileName), options.ProcessConfig, options.EnableMultimodel)
	if err != nil {
		return nil, err
	}
	if existing, findErr := s.repo.GetKnowledgeByID(ctx, session.TenantID, session.ID); findErr == nil {
		if existing.KnowledgeBaseID != session.KnowledgeBaseID {
			return nil, werrors.NewConflictError("上传会话对应的知识记录不一致")
		}
		session.FinalFilePath = existing.FilePath
		session.FinalizeStage = types.KnowledgeUploadFinalizeKnowledgeCreated
		session.KnowledgeID = existing.ID
		if err := s.repo.UpdateKnowledgeUploadSession(ctx, session); err != nil {
			return existing, err
		}
		if err := s.setAndAttachKnowledgeTags(ctx, session.TenantID, session.KnowledgeBaseID, existing, options.TagIDs); err != nil {
			return nil, err
		}
		return s.enqueueStoredKnowledge(ctx, kb, existing, eff)
	} else if !isKnowledgeUploadKnowledgeNotFound(findErr) {
		return nil, findErr
	}
	hash, err := calculatePathMD5(session.TempPath)
	if err != nil {
		return nil, err
	}
	exists, existing, err := s.repo.CheckKnowledgeExists(ctx, session.TenantID, session.KnowledgeBaseID, &types.KnowledgeCheckParams{
		Type: "file", FileName: session.FileName, FileType: getFileType(session.FileName),
		FileSize: session.FileSize, FileHash: hash,
	})
	if err != nil {
		return nil, err
	}
	if exists {
		return existing, types.NewDuplicateFileError(existing)
	}
	if tenant, ok := ctx.Value(types.TenantInfoContextKey).(*types.Tenant); ok && tenant.StorageQuota > 0 &&
		(tenant.StorageUsed >= tenant.StorageQuota || session.FileSize > tenant.StorageQuota-tenant.StorageUsed) {
		return nil, types.NewStorageQuotaExceededError()
	}
	metadataBytes, err := json.Marshal(options.Metadata)
	if err != nil {
		return nil, err
	}
	knowledge := &types.Knowledge{
		ID: session.ID, TenantID: session.TenantID, KnowledgeBaseID: session.KnowledgeBaseID,
		Type: "file", Channel: defaultChannel(options.Channel), Title: session.FileName,
		FileName: session.FileName, FolderPath: session.FolderPath, FileType: getFileType(session.FileName),
		FileSize: session.FileSize, FileHash: hash, ParseStatus: "pending", EnableStatus: "disabled",
		EmbeddingModelID: kb.EmbeddingModelID, Metadata: types.JSON(metadataBytes),
	}
	if options.ProcessConfig != nil {
		if err := knowledge.SetProcessOverrides(options.ProcessConfig); err != nil {
			return nil, err
		}
	}
	if err := knowledge.SetSourceFileQuotaBytes(session.FileSize); err != nil {
		return nil, err
	}
	fileSvc := s.resolveFileService(ctx, kb)
	filePath, err := s.ensureKnowledgeUploadFinalFile(ctx, session, fileSvc)
	if err != nil {
		return nil, err
	}
	knowledge.FilePath = filePath
	creator, ok := s.repo.(knowledgeUploadAtomicCreator)
	if !ok {
		cleanupErr := s.clearKnowledgeUploadFinalFile(ctx, session, fileSvc)
		return nil, errors.Join(
			fmt.Errorf("knowledge repository does not support atomic upload finalization"),
			cleanupErr,
		)
	}
	existing, created, err := creator.CreateKnowledgeWithFileHashLock(ctx, knowledge, session.FileSize)
	if err != nil {
		cleanupErr := s.clearKnowledgeUploadFinalFile(ctx, session, fileSvc)
		return nil, errors.Join(err, cleanupErr)
	}
	if !created {
		cleanupErr := s.clearKnowledgeUploadFinalFile(ctx, session, fileSvc)
		duplicateErr := types.NewDuplicateFileError(existing)
		return existing, errors.Join(duplicateErr, cleanupErr)
	}
	if tenant, ok := ctx.Value(types.TenantInfoContextKey).(*types.Tenant); ok {
		tenant.StorageUsed += session.FileSize
	}
	session.FinalizeStage = types.KnowledgeUploadFinalizeKnowledgeCreated
	session.KnowledgeID = knowledge.ID
	if err := s.repo.UpdateKnowledgeUploadSession(ctx, session); err != nil {
		return knowledge, err
	}
	if err := s.setAndAttachKnowledgeTags(ctx, session.TenantID, session.KnowledgeBaseID, knowledge, options.TagIDs); err != nil {
		return nil, err
	}
	return s.enqueueStoredKnowledge(ctx, kb, knowledge, eff)
}

func (s *knowledgeService) CompleteKnowledgeUpload(ctx context.Context, kbID, uploadID, userID string) (*types.Knowledge, error) {
	var result *types.Knowledge
	err := s.withKnowledgeUploadLock(ctx, uploadID, func(lockCtx context.Context) error {
		var completeErr error
		result, completeErr = s.completeKnowledgeUpload(lockCtx, kbID, uploadID, userID)
		return completeErr
	})
	return result, err
}

func (s *knowledgeService) completeKnowledgeUpload(ctx context.Context, kbID, uploadID, userID string) (*types.Knowledge, error) {
	tenantID := ctx.Value(types.TenantIDContextKey).(uint64)
	session, err := s.GetKnowledgeUpload(ctx, kbID, uploadID, userID)
	if err != nil {
		return nil, err
	}
	if session.Status == types.KnowledgeUploadCompleted && session.KnowledgeID != "" {
		return s.repo.GetKnowledgeByID(ctx, session.TenantID, session.KnowledgeID)
	}
	if session.Status == types.KnowledgeUploadCompletedCleanupPending && session.KnowledgeID != "" {
		knowledge, getErr := s.repo.GetKnowledgeByID(ctx, session.TenantID, session.KnowledgeID)
		if getErr != nil {
			return nil, getErr
		}
		if cleanupErr := s.finishKnowledgeUploadCleanup(ctx, session); cleanupErr != nil {
			return knowledge, cleanupErr
		}
		return knowledge, nil
	}
	if session.Status.Terminal() {
		return nil, werrors.NewConflictError("上传会话已不可完成")
	}
	if session.ReceivedBytes != session.FileSize {
		return nil, werrors.NewConflictError("文件分片尚未全部上传")
	}
	info, err := os.Stat(session.TempPath)
	if err != nil || info.Size() != session.FileSize {
		return nil, werrors.NewConflictError("暂存文件不完整")
	}
	if session.Status != types.KnowledgeUploadCompleting {
		session, err = s.repo.ClaimKnowledgeUploadForCompletion(ctx, tenantID, kbID, userID, uploadID)
		if err != nil {
			if errors.Is(err, types.ErrUploadIncomplete) {
				return nil, werrors.NewConflictError("文件分片尚未全部上传")
			}
			if errors.Is(err, types.ErrUploadSessionState) {
				return nil, werrors.NewConflictError("上传会话已不可完成")
			}
			return nil, err
		}
	}
	knowledge, err := s.createKnowledgeFromStagedUpload(ctx, session)
	if err != nil {
		session.Status = types.KnowledgeUploadFailed
		session.ErrorMessage = err.Error()
		stateErr := s.repo.UpdateKnowledgeUploadSession(ctx, session)
		return knowledge, errors.Join(err, stateErr)
	}
	session.Status = types.KnowledgeUploadCompletedCleanupPending
	session.KnowledgeID = knowledge.ID
	session.ErrorMessage = ""
	if err := s.repo.UpdateKnowledgeUploadSession(ctx, session); err != nil {
		return nil, err
	}
	if err := s.finishKnowledgeUploadCleanup(ctx, session); err != nil {
		return knowledge, err
	}
	return knowledge, nil
}

func (s *knowledgeService) CancelKnowledgeUpload(ctx context.Context, kbID, uploadID, userID string) error {
	return s.withKnowledgeUploadLock(ctx, uploadID, func(lockCtx context.Context) error {
		return s.cancelKnowledgeUpload(lockCtx, kbID, uploadID, userID)
	})
}

func (s *knowledgeService) cancelKnowledgeUpload(ctx context.Context, kbID, uploadID, userID string) error {
	tenantID := ctx.Value(types.TenantIDContextKey).(uint64)
	session, err := s.repo.CancelKnowledgeUploadSession(ctx, tenantID, kbID, userID, uploadID)
	if err != nil {
		if errors.Is(err, types.ErrUploadSessionState) {
			return werrors.NewConflictError("上传会话已不可取消")
		}
		return err
	}
	if session.Status == types.KnowledgeUploadCancelled {
		s.uploadRuntimes.Delete(uploadID)
		return nil
	}
	return s.finishKnowledgeUploadCleanup(ctx, session)
}

func validateKnowledgeUploadSessionTempPath(session *types.KnowledgeUploadSession) (string, error) {
	if session == nil {
		return "", fmt.Errorf("upload session is required")
	}
	tempRoot, err := knowledgeUploadTempRoot()
	if err != nil {
		return "", err
	}
	resolved, err := secutils.SafePathUnderBase(tempRoot, session.TempPath)
	if err != nil {
		return "", fmt.Errorf("invalid upload temp path: %w", err)
	}
	if filepath.Base(resolved) != session.ID+".part" ||
		filepath.Base(filepath.Dir(resolved)) != strconv.FormatUint(session.TenantID, 10) {
		return "", fmt.Errorf("upload temp path does not match session ownership")
	}
	return resolved, nil
}

func (s *knowledgeService) cleanupKnowledgeUploadArtifacts(
	ctx context.Context, session *types.KnowledgeUploadSession,
) error {
	var cleanupErr error
	resolved, pathErr := validateKnowledgeUploadSessionTempPath(session)
	if pathErr != nil {
		cleanupErr = errors.Join(cleanupErr, pathErr)
	} else if removeErr := os.Remove(resolved); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove upload temp file: %w", removeErr))
	}
	if partsErr := s.repo.DeleteKnowledgeUploadParts(
		ctx, session.TenantID, session.KnowledgeBaseID, session.UserID, session.ID,
	); partsErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete upload part records: %w", partsErr))
	}
	if session.Status != types.KnowledgeUploadCompletedCleanupPending && session.FinalFilePath != "" {
		if _, getErr := s.repo.GetKnowledgeByID(ctx, session.TenantID, session.ID); isKnowledgeUploadKnowledgeNotFound(getErr) {
			kb, kbErr := s.kbService.GetKnowledgeBaseByID(ctx, session.KnowledgeBaseID)
			if kbErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("resolve upload storage for cleanup: %w", kbErr))
			} else {
				fileSvc := s.resolveFileService(ctx, kb)
				if fileErr := s.clearKnowledgeUploadFinalFile(ctx, session, fileSvc); fileErr != nil {
					cleanupErr = errors.Join(cleanupErr, fileErr)
				}
			}
		} else if getErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("check finalized upload knowledge: %w", getErr))
		}
	}
	return cleanupErr
}

func (s *knowledgeService) finishKnowledgeUploadCleanup(
	ctx context.Context, session *types.KnowledgeUploadSession,
) error {
	finalStatus, ok := session.Status.CleanupFinalStatus()
	if !ok {
		return fmt.Errorf("upload session status %q is not cleanup-pending", session.Status)
	}
	if cleanupErr := s.cleanupKnowledgeUploadArtifacts(ctx, session); cleanupErr != nil {
		session.ErrorMessage = cleanupErr.Error()
		stateErr := s.repo.UpdateKnowledgeUploadSession(ctx, session)
		return errors.Join(fmt.Errorf("upload cleanup remains pending: %w", cleanupErr), stateErr)
	}
	session.Status = finalStatus
	session.ErrorMessage = ""
	if err := s.repo.UpdateKnowledgeUploadSession(ctx, session); err != nil {
		return fmt.Errorf("finalize upload cleanup state: %w", err)
	}
	s.uploadRuntimes.Delete(session.ID)
	return nil
}

func (s *knowledgeService) CleanupExpiredKnowledgeUploads(ctx context.Context, limit int) (int, error) {
	sessions, err := s.repo.ListExpiredKnowledgeUploadSessions(ctx, time.Now(), limit)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	var cleanupErrors error
	for _, session := range sessions {
		cleanupErr := s.withKnowledgeUploadLock(ctx, session.ID, func(lockCtx context.Context) error {
			recoveryCtx := lockCtx
			if s.tenantService != nil {
				tenantInfo, tenantErr := s.tenantService.GetTenantByID(lockCtx, session.TenantID)
				if tenantErr != nil {
					return tenantErr
				}
				recoveryCtx = context.WithValue(recoveryCtx, types.TenantInfoContextKey, tenantInfo)
			}
			recoveryCtx = context.WithValue(recoveryCtx, types.TenantIDContextKey, session.TenantID)
			if session.Status == types.KnowledgeUploadCompleting ||
				session.FinalizeStage == types.KnowledgeUploadFinalizeKnowledgeCreated {
				if _, completeErr := s.completeKnowledgeUpload(
					recoveryCtx, session.KnowledgeBaseID, session.ID, session.UserID,
				); completeErr == nil {
					cleaned++
					return nil
				} else {
					refreshed, refreshErr := s.repo.GetKnowledgeUploadSession(
						lockCtx, session.TenantID, session.KnowledgeBaseID, session.UserID, session.ID,
					)
					if refreshErr != nil {
						return errors.Join(completeErr, refreshErr)
					}
					session = refreshed
					if session.FinalizeStage == types.KnowledgeUploadFinalizeKnowledgeCreated {
						return completeErr
					}
				}
			}
			if !session.Status.CleanupPending() {
				claimed, claimErr := s.repo.ExpireKnowledgeUploadSession(recoveryCtx, session)
				if claimErr != nil || !claimed {
					return claimErr
				}
				session.Status = types.KnowledgeUploadExpiredCleanupPending
			}
			if finishErr := s.finishKnowledgeUploadCleanup(recoveryCtx, session); finishErr != nil {
				return finishErr
			}
			cleaned++
			return nil
		})
		if cleanupErr != nil {
			cleanupErrors = errors.Join(cleanupErrors, fmt.Errorf("cleanup upload %s: %w", session.ID, cleanupErr))
			continue
		}
	}
	return cleaned, cleanupErrors
}

var _ = defaultKnowledgeUploadMaxBytes
var _ = defaultKnowledgeUploadChunkBytes
var _ = defaultKnowledgeUploadTTL
