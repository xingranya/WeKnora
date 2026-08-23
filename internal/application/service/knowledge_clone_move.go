package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

type knowledgeMoveCoordinator interface {
	ClaimKnowledgeForMove(
		ctx context.Context,
		tenantID uint64,
		knowledgeID string,
		sourceKnowledgeBaseID string,
		targetKnowledgeBaseID string,
		taskID string,
		mode string,
	) (*types.Knowledge, bool, bool, error)
	StageClaimedKnowledgeMove(
		ctx context.Context,
		knowledge *types.Knowledge,
		taskID string,
	) (bool, error)
	CompleteClaimedKnowledgeMove(ctx context.Context, knowledge *types.Knowledge, taskID string) (bool, error)
	FailClaimedKnowledgeMove(
		ctx context.Context,
		tenantID uint64,
		knowledgeID string,
		taskID string,
		errorMessage string,
	) (bool, error)
}

// copyOwnedObject copies srcPath into a NEW object owned by the destination
// tenant, returning the new provider:// (resource) path.
//
// Extracted/embedded chunk images MUST land in the tenant's exports/ namespace,
// because GET /knowledge-bases/:id/files only serves objects that pass
// ValidateKBScopedStoragePath (i.e. {tenant}/exports/...). CopyFile writes to
// the knowledge-scoped upload layout ({tenant}/{knowledgeID}/...) used for raw
// source files, which the KB proxy rejects — so a clone that used CopyFile
// produced images that could no longer be rendered. Instead, read the source
// bytes and re-save them via SaveBytes, exactly mirroring how the original
// images were persisted during ingestion (see image_resolver.saveReferencedImage),
// so the copy is a genuine independent object in the servable namespace.
func copyOwnedObject(
	ctx context.Context,
	srcSvc, dstSvc interfaces.FileService,
	srcPath string,
	tenantID uint64,
	knowledgeID string,
) (string, error) {
	_ = knowledgeID // exports objects are tenant-scoped, not knowledge-scoped
	rc, err := srcSvc.GetFile(ctx, srcPath)
	if err != nil {
		return "", fmt.Errorf("read source image %q: %w", srcPath, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("buffer source image %q: %w", srcPath, err)
	}

	fileName := uuid.New().String() + imageExtForCopy(srcPath, data)
	newPath, err := dstSvc.SaveBytes(ctx, data, tenantID, fileName, false)
	if err != nil {
		return "", fmt.Errorf("save copied image for %q: %w", srcPath, err)
	}
	return newPath, nil
}

// imageExtForCopy resolves the file extension to use for a copied image. It
// prefers an image extension already present on the source path, then falls
// back to sniffing the content bytes, and finally defaults to ".png" (matching
// image_resolver's default) so the object is always served with a sane type.
func imageExtForCopy(srcPath string, data []byte) string {
	if ext := strings.ToLower(filepath.Ext(srcPath)); isImageExt(ext) {
		return ext
	}
	switch http.DetectContentType(data) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	}
	return ".png"
}

func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	default:
		return false
	}
}

// cloneChunkImageInfo parses a chunk's image_info JSON, copies every referenced
// object into a NEW object owned by (tenantID, knowledgeID), and returns the
// re-serialized image_info plus the list of newly-created object URLs (for
// rollback on failure). urlCache dedups identical source objects across chunks
// so the same source image is copied at most once per clone AND accumulates the
// full old->new URL mapping so callers can rewrite in-content Markdown image
// references (see rewriteContentImageURLs).
//
// An empty srcImageInfo yields ("", nil, nil). A JSON parse failure returns an
// error (the clone fails) rather than silently inheriting the shared-reference
// bug. When an image's OriginalURL points at the same object as its URL (the
// common case for extracted images), OriginalURL is rewritten to the new path
// too; an OriginalURL from a different/external source is preserved.
func cloneChunkImageInfo(
	ctx context.Context,
	dstSvc interfaces.FileService,
	srcImageInfo string,
	tenantID uint64,
	knowledgeID string,
	urlCache map[string]string,
) (newImageInfo string, copiedURLs []string, err error) {
	if srcImageInfo == "" {
		return "", nil, nil
	}

	var images []*types.ImageInfo
	if err := json.Unmarshal([]byte(srcImageInfo), &images); err != nil {
		return "", nil, fmt.Errorf("failed to parse chunk image_info JSON: %w", err)
	}

	for _, img := range images {
		if img == nil || img.URL == "" {
			continue
		}
		originalMatchedURL := img.OriginalURL == img.URL

		newURL, cached := urlCache[img.URL]
		if !cached {
			newURL, err = copyOwnedObject(ctx, dstSvc, dstSvc, img.URL, tenantID, knowledgeID)
			if err != nil {
				return "", copiedURLs, fmt.Errorf("failed to copy chunk image %q: %w", img.URL, err)
			}
			urlCache[img.URL] = newURL
			copiedURLs = append(copiedURLs, newURL)
		}

		if originalMatchedURL {
			img.OriginalURL = newURL
		}
		img.URL = newURL
	}

	out, err := json.Marshal(images)
	if err != nil {
		return "", copiedURLs, fmt.Errorf("failed to re-serialize chunk image_info: %w", err)
	}
	return string(out), copiedURLs, nil
}

// rewriteContentImageURLs replaces every occurrence of an old image URL with its
// new (copied) URL in content, using the old->new mapping accumulated in
// urlCache. It is the second half of the image deep-copy: chunk Content embeds
// image URLs as Markdown ![](url) references, but for document knowledge the
// image objects live in independent image_ocr/image_caption child chunks — the
// parent text chunk carries the ![](url) reference with an empty image_info. So
// the old->new mapping is only known after every chunk's image_info has been
// processed; this rewrite must therefore run as a final pass once urlCache is
// complete, over ALL cloned chunks, not per-chunk.
//
// Replacements are applied longest-old-URL first so a URL that is a prefix of
// another is not partially rewritten. Entries whose old==new are skipped.
func rewriteContentImageURLs(content string, urlCache map[string]string) string {
	if content == "" || len(urlCache) == 0 {
		return content
	}
	oldURLs := make([]string, 0, len(urlCache))
	for oldURL, newURL := range urlCache {
		if oldURL == "" || oldURL == newURL {
			continue
		}
		oldURLs = append(oldURLs, oldURL)
	}
	slices.SortFunc(oldURLs, func(a, b string) int { return len(b) - len(a) })
	for _, oldURL := range oldURLs {
		content = strings.ReplaceAll(content, oldURL, urlCache[oldURL])
	}
	return content
}

// cleanupCopiedObjects deletes objects that were newly created during a clone
// that subsequently failed, to avoid orphaning storage. It is best-effort:
// delete errors are logged but never returned (the original clone error wins).
func cleanupCopiedObjects(ctx context.Context, svc interfaces.FileService, paths []string) {
	if len(paths) == 0 || svc == nil {
		return
	}
	logger.Infof(ctx, "Cleaning up %d copied objects after clone failure", len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := svc.DeleteFile(ctx, p); err != nil {
			logger.Errorf(ctx, "Failed to clean up copied object %s: %v", p, err)
		}
	}
}

func (s *knowledgeService) CloneKnowledgeBase(ctx context.Context, srcID, dstID string) error {
	srcKB, dstKB, err := s.kbService.CopyKnowledgeBase(ctx, srcID, dstID)
	if err != nil {
		logger.Errorf(ctx, "Failed to copy knowledge base: %v", err)
		return err
	}

	addKnowledge, err := s.repo.AminusB(ctx, srcKB.TenantID, srcKB.ID, dstKB.TenantID, dstKB.ID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get knowledge: %v", err)
		return err
	}

	delKnowledge, err := s.repo.AminusB(ctx, dstKB.TenantID, dstKB.ID, srcKB.TenantID, srcKB.ID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get knowledge: %v", err)
		return err
	}
	logger.Infof(ctx, "Knowledge after update to add: %d, delete: %d", len(addKnowledge), len(delKnowledge))

	batch := 10
	g, gctx := errgroup.WithContext(ctx)
	for ids := range slices.Chunk(delKnowledge, batch) {
		g.Go(func() error {
			err := s.DeleteKnowledgeList(gctx, ids)
			if err != nil {
				logger.Errorf(gctx, "delete partial knowledge %v: %v", ids, err)
				return err
			}
			return nil
		})
	}
	err = g.Wait()
	if err != nil {
		logger.Errorf(ctx, "delete total knowledge %d: %v", len(delKnowledge), err)
		return err
	}

	// Copy context out of auto-stop task
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(batch)
	for _, knowledge := range addKnowledge {
		g.Go(func() error {
			srcKn, err := s.repo.GetKnowledgeByID(gctx, srcKB.TenantID, knowledge)
			if err != nil {
				logger.Errorf(gctx, "get knowledge %s: %v", knowledge, err)
				return err
			}
			err = s.cloneKnowledge(gctx, srcKn, dstKB)
			if err != nil {
				logger.Errorf(gctx, "clone knowledge %s: %v", knowledge, err)
				return err
			}
			return nil
		})
	}
	err = g.Wait()
	if err != nil {
		logger.Errorf(ctx, "add total knowledge %d: %v", len(addKnowledge), err)
		return err
	}
	return nil
}

// CloneChunk clone chunks from one knowledge to another
// This method transfers a chunk from a source knowledge document to a target knowledge document
// It handles the creation of new chunks in the target knowledge and updates the vector database accordingly
// Parameters:
//   - ctx: Context with authentication and request information
//   - src: Source knowledge document containing the chunk to move
//   - dst: Target knowledge document where the chunk will be moved
//
// Returns:
//   - error: Any error encountered during the move operation
//
// This method handles the chunk transfer logic, including creating new chunks in the target knowledge
// and updating the vector database representation of the moved chunks.
// It also ensures that the chunk's relationships (like pre and next chunk IDs) are maintained
// by mapping the source chunk IDs to the new target chunk IDs.
func (s *knowledgeService) CloneChunk(ctx context.Context, src, dst *types.Knowledge) (err error) {
	chunkPage := 1
	chunkPageSize := 100
	srcTodst := map[string]string{}
	tagIDMapping := map[string]string{} // srcTagID -> dstTagID
	targetChunks := make([]*types.Chunk, 0, 10)
	chunkType := []types.ChunkType{
		types.ChunkTypeText, types.ChunkTypeParentText, types.ChunkTypeSummary,
		types.ChunkTypeImageCaption, types.ChunkTypeImageOCR,
	}

	// Resolve the destination FileService so extracted images can be copied
	// into objects owned by the destination knowledge. urlCache dedups identical
	// source images across chunks; copiedURLs accumulates new objects so they can
	// be cleaned up if the clone fails partway through.
	dstKB, dstKBErr := s.kbService.GetKnowledgeBaseByID(ctx, dst.KnowledgeBaseID)
	if dstKBErr != nil {
		return fmt.Errorf("failed to load destination knowledge base for image copy: %w", dstKBErr)
	}
	dstSvc := s.resolveFileService(ctx, dstKB)
	urlCache := map[string]string{}
	var copiedURLs []string
	defer func() {
		if err != nil {
			cleanupCopiedObjects(ctx, dstSvc, copiedURLs)
		}
	}()

	for {
		sourceChunks, _, err := s.chunkRepo.ListPagedChunksByKnowledgeID(ctx,
			src.TenantID,
			src.ID,
			&types.Pagination{
				Page:     chunkPage,
				PageSize: chunkPageSize,
			},
			chunkType,
			nil,
			"",
			"",
			"",
			"",
			nil,
		)
		chunkPage++
		if err != nil {
			return err
		}
		if len(sourceChunks) == 0 {
			break
		}
		now := time.Now()
		for _, sourceChunk := range sourceChunks {
			// Map TagID to target knowledge base
			targetTagID := ""
			if sourceChunk.TagID != "" {
				if mappedTagID, ok := tagIDMapping[sourceChunk.TagID]; ok {
					targetTagID = mappedTagID
				} else {
					// Try to find or create the tag in target knowledge base
					targetTagID = s.getOrCreateTagInTarget(ctx, src.TenantID, dst.TenantID, dst.KnowledgeBaseID, sourceChunk.TagID, tagIDMapping)
				}
			}

			// Deep-copy extracted images into objects owned by the destination
			// knowledge so deleting the source never breaks this clone. Content
			// URL rewriting happens in a final pass below, once urlCache holds
			// the complete old->new mapping (image objects live in independent
			// child chunks, so a parent text chunk's ![](url) reference cannot be
			// rewritten until its child image chunk has been processed).
			newImageInfo, copied, copyErr := cloneChunkImageInfo(
				ctx, dstSvc, sourceChunk.ImageInfo, dst.TenantID, dst.ID, urlCache)
			if copyErr != nil {
				err = fmt.Errorf("clone chunk image copy failed: %w", copyErr)
				return err
			}
			copiedURLs = append(copiedURLs, copied...)

			targetChunk := &types.Chunk{
				ID:              uuid.New().String(),
				TenantID:        dst.TenantID,
				KnowledgeID:     dst.ID,
				KnowledgeBaseID: dst.KnowledgeBaseID,
				TagID:           targetTagID,
				Content:         sourceChunk.Content,
				ChunkIndex:      sourceChunk.ChunkIndex,
				IsEnabled:       sourceChunk.IsEnabled,
				Flags:           sourceChunk.Flags,
				Status:          sourceChunk.Status,
				StartAt:         sourceChunk.StartAt,
				EndAt:           sourceChunk.EndAt,
				PreChunkID:      sourceChunk.PreChunkID,
				NextChunkID:     sourceChunk.NextChunkID,
				ChunkType:       sourceChunk.ChunkType,
				ParentChunkID:   sourceChunk.ParentChunkID,
				Metadata:        sourceChunk.Metadata,
				ContentHash:     sourceChunk.ContentHash,
				ImageInfo:       newImageInfo,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			targetChunks = append(targetChunks, targetChunk)
			srcTodst[sourceChunk.ID] = targetChunk.ID
		}
	}
	for _, targetChunk := range targetChunks {
		// Rewrite in-content Markdown image URLs now that urlCache holds the
		// complete old->new mapping across all chunks. This fixes parent text
		// chunks whose ![](url) reference points at a source object copied while
		// processing an independent image_ocr/image_caption child chunk.
		targetChunk.Content = rewriteContentImageURLs(targetChunk.Content, urlCache)
		if val, ok := srcTodst[targetChunk.PreChunkID]; ok {
			targetChunk.PreChunkID = val
		} else {
			targetChunk.PreChunkID = ""
		}
		if val, ok := srcTodst[targetChunk.NextChunkID]; ok {
			targetChunk.NextChunkID = val
		} else {
			targetChunk.NextChunkID = ""
		}
		if val, ok := srcTodst[targetChunk.ParentChunkID]; ok {
			targetChunk.ParentChunkID = val
		} else {
			targetChunk.ParentChunkID = ""
		}
	}
	for chunks := range slices.Chunk(targetChunks, chunkPageSize) {
		err := s.chunkRepo.CreateChunks(ctx, chunks)
		if err != nil {
			return err
		}
	}

	tenantID := types.MustTenantIDFromContext(ctx)
	// Route CopyIndices via the source KB's bound store. This function does
	// not handle cross-store copies — embeddings written by different
	// VectorStore backends are not bit-compatible, so callers that allow
	// source/target KBs to bind to different stores must perform their own
	// cross-store migration before invoking this.
	var sourceStoreID *string
	if srcKB, loadErr := s.kbService.GetKnowledgeBaseByID(ctx, src.KnowledgeBaseID); loadErr == nil && srcKB != nil {
		sourceStoreID = srcKB.VectorStoreID
	}
	retrieveEngine, err := retriever.CreateRetrieveEngineForKB(
		ctx, s.retrieveEngine, s.ownership, tenantID, sourceStoreID)
	if err != nil {
		return err
	}
	embeddingModel, err := s.modelService.GetEmbeddingModel(ctx, dst.EmbeddingModelID)
	if err != nil {
		return err
	}
	if err := retrieveEngine.CopyIndices(ctx, src.KnowledgeBaseID, dst.KnowledgeBaseID,
		map[string]string{src.ID: dst.ID},
		srcTodst,
		embeddingModel.GetDimensions(),
		dst.Type,
	); err != nil {
		return err
	}
	return nil
}

const (
	kbCloneProgressKeyPrefix = "kb_clone_progress:"
	kbCloneProgressTTL       = 24 * time.Hour
)

// getKBCloneProgressKey returns the Redis key for storing KB clone progress
func getKBCloneProgressKey(taskID string) string {
	return kbCloneProgressKeyPrefix + taskID
}

// ProcessKBClone handles Asynq knowledge base clone tasks
func (s *knowledgeService) ProcessKBClone(ctx context.Context, t *asynq.Task) error {
	var payload types.KBClonePayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal KB clone payload: %w", err)
	}
	ctx = payload.Initiator.Apply(ctx)
	ctx = withKBActivityTask(ctx, payload.TaskID, kbActivityTrigger(ctx))

	// Add tenant ID to context
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)

	// Get tenant info and add to context
	tenantInfo, err := s.tenantRepo.GetTenantByID(ctx, payload.TenantID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get tenant info: %v", err)
		return fmt.Errorf("failed to get tenant info: %w", err)
	}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenantInfo)

	// 同时兼容 Redis Asynq 与 Lite executor，缺失元数据时不能误判最终轮。
	retryCount, maxRetry, hasRetryMetadata := taskRetryMetadata(ctx)
	isLastRetry := hasRetryMetadata && retryCount >= maxRetry

	logger.Infof(ctx, "Processing KB clone task: %s, source: %s, target: %s, retry: %d/%d",
		payload.TaskID, payload.SourceID, payload.TargetID, retryCount, maxRetry)

	// Helper function to handle errors - only mark as failed on last retry
	handleError := func(progress *types.KBCloneProgress, err error, message string) {
		if isLastRetry {
			progress.Status = types.KBCloneStatusFailed
			progress.Error = err.Error()
			progress.Message = message
			progress.UpdatedAt = time.Now().Unix()
			_ = s.saveKBCloneProgress(ctx, progress)
			recordKBActivity(ctx, s.audit, payload.TenantID, payload.TargetID, types.AuditActionKBCloneFailed,
				"knowledge_base", payload.TargetID, types.AuditOutcomeFailed,
				map[string]any{"source_kb_id": payload.SourceID, "task_id": payload.TaskID})
		}
	}

	// Update progress to processing
	progress := &types.KBCloneProgress{
		TaskID:    payload.TaskID,
		SourceID:  payload.SourceID,
		TargetID:  payload.TargetID,
		Status:    types.KBCloneStatusProcessing,
		Progress:  0,
		Message:   "Starting knowledge base clone...",
		UpdatedAt: time.Now().Unix(),
	}
	if err := s.saveKBCloneProgress(ctx, progress); err != nil {
		logger.Errorf(ctx, "Failed to update KB clone progress: %v", err)
	}

	// Get source and target knowledge bases
	srcKB, dstKB, err := s.kbService.CopyKnowledgeBase(ctx, payload.SourceID, payload.TargetID)
	if err != nil {
		logger.Errorf(ctx, "Failed to copy knowledge base: %v", err)
		handleError(progress, err, "Failed to copy knowledge base configuration")
		return err
	}
	if retryCount == 0 {
		recordKBActivity(ctx, s.audit, payload.TenantID, payload.TargetID, types.AuditActionKBCloneStarted,
			"knowledge_base", payload.TargetID, types.AuditOutcomeAccepted,
			map[string]any{"source_kb_id": payload.SourceID, "task_id": payload.TaskID})
	}

	// Use different sync strategies based on knowledge base type
	if srcKB.Type == types.KnowledgeBaseTypeFAQ {
		if err := s.cloneFAQKnowledgeBase(ctx, srcKB, dstKB, progress, handleError); err != nil {
			return err
		}
		recordKBActivity(ctx, s.audit, payload.TenantID, payload.TargetID, types.AuditActionKBCloneCompleted,
			"knowledge_base", payload.TargetID, types.AuditOutcomeSuccess,
			map[string]any{"source_kb_id": payload.SourceID, "task_id": payload.TaskID, "total": progress.Total})
		return nil
	}

	// Document type: use Knowledge-level diff based on file_hash
	addKnowledge, err := s.repo.AminusB(ctx, srcKB.TenantID, srcKB.ID, dstKB.TenantID, dstKB.ID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get knowledge to add: %v", err)
		handleError(progress, err, "Failed to calculate knowledge difference")
		return err
	}

	delKnowledge, err := s.repo.AminusB(ctx, dstKB.TenantID, dstKB.ID, srcKB.TenantID, srcKB.ID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get knowledge to delete: %v", err)
		handleError(progress, err, "Failed to calculate knowledge difference")
		return err
	}

	totalOperations := len(addKnowledge) + len(delKnowledge)
	progress.Total = totalOperations
	progress.Message = fmt.Sprintf("Found %d knowledge to add, %d to delete", len(addKnowledge), len(delKnowledge))
	progress.UpdatedAt = time.Now().Unix()
	_ = s.saveKBCloneProgress(ctx, progress)

	logger.Infof(ctx, "Knowledge after update to add: %d, delete: %d", len(addKnowledge), len(delKnowledge))

	processedCount := 0
	batch := 10

	// Delete knowledge in target that doesn't exist in source
	g, gctx := errgroup.WithContext(ctx)
	for ids := range slices.Chunk(delKnowledge, batch) {
		g.Go(func() error {
			err := s.DeleteKnowledgeList(gctx, ids)
			if err != nil {
				logger.Errorf(gctx, "delete partial knowledge %v: %v", ids, err)
				return err
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		logger.Errorf(ctx, "delete total knowledge %d: %v", len(delKnowledge), err)
		handleError(progress, err, "Failed to delete knowledge")
		return err
	}

	processedCount += len(delKnowledge)
	if totalOperations > 0 {
		progress.Progress = processedCount * 100 / totalOperations
	}
	progress.Processed = processedCount
	progress.Message = fmt.Sprintf("Deleted %d knowledge, cloning %d...", len(delKnowledge), len(addKnowledge))
	progress.UpdatedAt = time.Now().Unix()
	_ = s.saveKBCloneProgress(ctx, progress)

	// Clone knowledge from source to target
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(batch)
	for _, knowledge := range addKnowledge {
		g.Go(func() error {
			srcKn, err := s.repo.GetKnowledgeByID(gctx, srcKB.TenantID, knowledge)
			if err != nil {
				logger.Errorf(gctx, "get knowledge %s: %v", knowledge, err)
				return err
			}
			err = s.cloneKnowledge(gctx, srcKn, dstKB)
			if err != nil {
				logger.Errorf(gctx, "clone knowledge %s: %v", knowledge, err)
				return err
			}

			// Update progress
			processedCount++
			if totalOperations > 0 {
				progress.Progress = processedCount * 100 / totalOperations
			}
			progress.Processed = processedCount
			progress.Message = fmt.Sprintf("Cloned %d/%d knowledge", processedCount-len(delKnowledge), len(addKnowledge))
			progress.UpdatedAt = time.Now().Unix()
			_ = s.saveKBCloneProgress(ctx, progress)

			return nil
		})
	}
	if err := g.Wait(); err != nil {
		logger.Errorf(ctx, "add total knowledge %d: %v", len(addKnowledge), err)
		handleError(progress, err, "Failed to clone knowledge")
		return err
	}

	// Mark as completed
	progress.Status = types.KBCloneStatusCompleted
	progress.Progress = 100
	progress.Processed = totalOperations
	progress.Message = "Knowledge base clone completed successfully"
	progress.UpdatedAt = time.Now().Unix()
	if err := s.saveKBCloneProgress(ctx, progress); err != nil {
		logger.Errorf(ctx, "Failed to update KB clone progress to completed: %v", err)
	}

	logger.Infof(ctx, "KB clone task completed: %s", payload.TaskID)
	recordKBActivity(ctx, s.audit, payload.TenantID, payload.TargetID, types.AuditActionKBCloneCompleted,
		"knowledge_base", payload.TargetID, types.AuditOutcomeSuccess,
		map[string]any{"source_kb_id": payload.SourceID, "task_id": payload.TaskID, "total": totalOperations})
	return nil
}

// cloneFAQKnowledgeBase handles FAQ knowledge base cloning with chunk-level incremental sync
func (s *knowledgeService) cloneFAQKnowledgeBase(
	ctx context.Context,
	srcKB, dstKB *types.KnowledgeBase,
	progress *types.KBCloneProgress,
	handleError func(*types.KBCloneProgress, error, string),
) (retErr error) {
	// Deep-copy extracted FAQ images into objects owned by the destination KB.
	// urlCache dedups identical source images across chunks; copiedURLs tracks
	// new objects for best-effort cleanup if the clone fails partway through.
	dstSvc := s.resolveFileService(ctx, dstKB)
	imageURLCache := map[string]string{}
	var copiedImageURLs []string
	defer func() {
		if retErr != nil {
			cleanupCopiedObjects(ctx, dstSvc, copiedImageURLs)
		}
	}()

	// Get source FAQ knowledge first (FAQ KB has exactly one Knowledge entry)
	srcKnowledgeList, err := s.repo.ListKnowledgeByKnowledgeBaseID(ctx, srcKB.TenantID, srcKB.ID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get source FAQ knowledge: %v", err)
		handleError(progress, err, "Failed to get source FAQ knowledge")
		return err
	}
	if len(srcKnowledgeList) == 0 {
		// Source has no FAQ knowledge, nothing to clone
		progress.Status = types.KBCloneStatusCompleted
		progress.Progress = 100
		progress.Message = "Source FAQ knowledge base is empty"
		progress.UpdatedAt = time.Now().Unix()
		_ = s.saveKBCloneProgress(ctx, progress)
		return nil
	}
	srcKnowledge := srcKnowledgeList[0]

	// Get chunk-level differences based on content_hash.
	diff, err := s.chunkRepo.FAQChunkDiff(ctx, srcKB.TenantID, srcKB.ID, dstKB.TenantID, dstKB.ID)
	if err != nil {
		logger.Errorf(ctx, "Failed to calculate FAQ chunk difference: %v", err)
		handleError(progress, err, "Failed to calculate FAQ chunk difference")
		return err
	}
	chunksToAdd := diff.ChunksToAdd
	chunksToDelete := diff.ChunksToDelete

	tagIDMapping := map[string]string{}
	resolveFAQTag := func(srcTagID string) string {
		if srcTagID == "" {
			return ""
		}
		if id, ok := tagIDMapping[srcTagID]; ok {
			return id
		}
		return s.getOrCreateTagInTarget(ctx, srcKB.TenantID, dstKB.TenantID, dstKB.ID, srcTagID, tagIDMapping)
	}

	syncPlan, err := s.buildFAQStatusSyncPlan(ctx, srcKB.TenantID, dstKB.TenantID, diff.MatchedPairs, resolveFAQTag)
	if err != nil {
		logger.Errorf(ctx, "Failed to build FAQ status sync plan: %v", err)
		handleError(progress, err, "Failed to build FAQ status sync plan")
		return err
	}
	chunksToUpdate := syncPlan.Pairs

	totalOperations := len(chunksToAdd) + len(chunksToDelete) + len(chunksToUpdate)
	progress.Total = totalOperations
	progress.Message = fmt.Sprintf(
		"Found %d FAQ entries to add, %d to delete, %d to update status",
		len(chunksToAdd), len(chunksToDelete), len(chunksToUpdate),
	)
	progress.UpdatedAt = time.Now().Unix()
	_ = s.saveKBCloneProgress(ctx, progress)

	logger.Infof(ctx, "FAQ chunks to add: %d, delete: %d, update status: %d",
		len(chunksToAdd), len(chunksToDelete), len(chunksToUpdate))

	if totalOperations == 0 {
		progress.Status = types.KBCloneStatusCompleted
		progress.Progress = 100
		progress.Message = "FAQ knowledge base is already in sync"
		progress.UpdatedAt = time.Now().Unix()
		_ = s.saveKBCloneProgress(ctx, progress)
		return nil
	}

	// Route the FAQ clone through the source KB's bound store. Same
	// constraint as CloneChunk: callers must ensure source and target share
	// the same VectorStore (cross-store FAQ clone is not handled here).
	var sourceStoreID *string
	if srcKB != nil {
		sourceStoreID = srcKB.VectorStoreID
	}
	retrieveEngine, err := retriever.CreateRetrieveEngineForKB(
		ctx, s.retrieveEngine, s.ownership, types.MustTenantIDFromContext(ctx), sourceStoreID)
	if err != nil {
		logger.Errorf(ctx, "Failed to init retrieve engine: %v", err)
		handleError(progress, err, "Failed to initialize retrieve engine")
		return err
	}

	// Get embedding model
	embeddingModel, err := s.modelService.GetEmbeddingModel(ctx, dstKB.EmbeddingModelID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get embedding model: %v", err)
		handleError(progress, err, "Failed to get embedding model")
		return err
	}

	processedCount := 0

	// Delete FAQ chunks that don't exist in source
	if len(chunksToDelete) > 0 {
		// Delete from vector store
		if err := retrieveEngine.DeleteByChunkIDList(ctx, chunksToDelete, embeddingModel.GetDimensions(), types.KnowledgeTypeFAQ); err != nil {
			logger.Errorf(ctx, "Failed to delete FAQ chunks from vector store: %v", err)
			handleError(progress, err, "Failed to delete FAQ entries from vector store")
			return err
		}
		// Delete from database
		if err := s.chunkRepo.DeleteChunks(ctx, dstKB.TenantID, chunksToDelete); err != nil {
			logger.Errorf(ctx, "Failed to delete FAQ chunks from database: %v", err)
			handleError(progress, err, "Failed to delete FAQ entries from database")
			return err
		}
		processedCount += len(chunksToDelete)
		if totalOperations > 0 {
			progress.Progress = processedCount * 100 / totalOperations
		}
		progress.Processed = processedCount
		progress.Message = fmt.Sprintf("Deleted %d FAQ entries, adding %d...", len(chunksToDelete), len(chunksToAdd))
		progress.UpdatedAt = time.Now().Unix()
		_ = s.saveKBCloneProgress(ctx, progress)
	}

	// Get or create the FAQ knowledge entry in destination
	dstKnowledge, err := s.getOrCreateFAQKnowledge(ctx, dstKB, srcKnowledge)
	if err != nil {
		logger.Errorf(ctx, "Failed to get or create FAQ knowledge: %v", err)
		handleError(progress, err, "Failed to prepare FAQ knowledge entry")
		return err
	}

	// Clone FAQ chunks from source to destination
	batch := 50
	for i := 0; i < len(chunksToAdd); i += batch {
		end := i + batch
		if end > len(chunksToAdd) {
			end = len(chunksToAdd)
		}
		batchIDs := chunksToAdd[i:end]

		// Get source chunks
		srcChunks, err := s.chunkRepo.ListChunksByID(ctx, srcKB.TenantID, batchIDs)
		if err != nil {
			logger.Errorf(ctx, "Failed to get source FAQ chunks: %v", err)
			handleError(progress, err, "Failed to get source FAQ entries")
			return err
		}

		// Create new chunks for destination
		newChunks := make([]*types.Chunk, 0, len(srcChunks))
		for _, srcChunk := range srcChunks {
			// Map TagID to target knowledge base
			targetTagID := ""
			if srcChunk.TagID != "" {
				targetTagID = resolveFAQTag(srcChunk.TagID)
			}

			// Deep-copy extracted images into objects owned by the destination
			// FAQ knowledge so deleting the source never breaks this clone.
			newImageInfo, copied, copyErr := cloneChunkImageInfo(
				ctx, dstSvc, srcChunk.ImageInfo, dstKB.TenantID, dstKnowledge.ID, imageURLCache)
			if copyErr != nil {
				logger.Errorf(ctx, "Failed to copy FAQ chunk images: %v", copyErr)
				handleError(progress, copyErr, "Failed to copy FAQ entry images")
				retErr = copyErr
				return retErr
			}
			copiedImageURLs = append(copiedImageURLs, copied...)

			newChunk := &types.Chunk{
				ID:              uuid.New().String(),
				TenantID:        dstKB.TenantID,
				KnowledgeID:     dstKnowledge.ID,
				KnowledgeBaseID: dstKB.ID,
				TagID:           targetTagID,
				Content:         rewriteContentImageURLs(srcChunk.Content, imageURLCache),
				ChunkIndex:      srcChunk.ChunkIndex,
				IsEnabled:       srcChunk.IsEnabled,
				Flags:           srcChunk.Flags,
				ChunkType:       types.ChunkTypeFAQ,
				Metadata:        srcChunk.Metadata,
				ContentHash:     srcChunk.ContentHash,
				ImageInfo:       newImageInfo,
				Status:          int(types.ChunkStatusStored), // Initially stored, will be indexed
				CreatedAt:       time.Now(),
				UpdatedAt:       time.Now(),
			}
			newChunks = append(newChunks, newChunk)
		}

		// Save to database
		if err := s.chunkRepo.CreateChunks(ctx, newChunks); err != nil {
			logger.Errorf(ctx, "Failed to create FAQ chunks: %v", err)
			handleError(progress, err, "Failed to create FAQ entries")
			return err
		}

		// Index in vector store using existing method
		// This will index standard question + similar questions based on FAQConfig
		if err := s.indexFAQChunks(ctx, dstKB, dstKnowledge, newChunks, embeddingModel, false, false); err != nil {
			logger.Errorf(ctx, "Failed to index FAQ chunks: %v", err)
			handleError(progress, err, "Failed to index FAQ entries")
			return err
		}

		// Update chunk status to indexed
		for _, chunk := range newChunks {
			chunk.Status = int(types.ChunkStatusIndexed)
		}
		if err := s.chunkService.UpdateChunks(ctx, newChunks); err != nil {
			logger.Warnf(ctx, "Failed to update FAQ chunks status: %v", err)
			// Don't fail the whole operation for status update failure
		}

		processedCount += len(batchIDs)
		if totalOperations > 0 {
			progress.Progress = processedCount * 100 / totalOperations
		}
		progress.Processed = processedCount
		progress.Message = fmt.Sprintf("Added %d/%d FAQ entries", processedCount-len(chunksToDelete), len(chunksToAdd))
		progress.UpdatedAt = time.Now().Unix()
		_ = s.saveKBCloneProgress(ctx, progress)
	}

	for i := 0; i < len(chunksToUpdate); i += batch {
		end := i + batch
		if end > len(chunksToUpdate) {
			end = len(chunksToUpdate)
		}
		if err := s.syncFAQChunkStatusBatch(
			ctx, dstKB, chunksToUpdate[i:end], syncPlan.SrcByID, syncPlan.DstByID, resolveFAQTag); err != nil {
			logger.Errorf(ctx, "Failed to sync FAQ status fields: %v", err)
			handleError(progress, err, "Failed to sync FAQ status fields")
			return err
		}
		processedCount += end - i
		if totalOperations > 0 {
			progress.Progress = processedCount * 100 / totalOperations
		}
		progress.Processed = processedCount
		progress.Message = fmt.Sprintf("Updated %d/%d FAQ status fields", processedCount-len(chunksToDelete)-len(chunksToAdd), len(chunksToUpdate))
		progress.UpdatedAt = time.Now().Unix()
		_ = s.saveKBCloneProgress(ctx, progress)
	}

	// Mark as completed
	progress.Status = types.KBCloneStatusCompleted
	progress.Progress = 100
	progress.Processed = totalOperations
	progress.Message = "FAQ knowledge base clone completed successfully"
	progress.UpdatedAt = time.Now().Unix()
	if err := s.saveKBCloneProgress(ctx, progress); err != nil {
		logger.Errorf(ctx, "Failed to update KB clone progress to completed: %v", err)
	}

	return nil
}

// getOrCreateFAQKnowledge gets or creates the FAQ knowledge entry for a knowledge base
// If srcKnowledge is provided, it will copy relevant fields from source when creating new knowledge
func (s *knowledgeService) getOrCreateFAQKnowledge(ctx context.Context, kb *types.KnowledgeBase, srcKnowledge *types.Knowledge) (*types.Knowledge, error) {
	// FAQ knowledge base should have exactly one Knowledge entry
	knowledgeList, err := s.repo.ListKnowledgeByKnowledgeBaseID(ctx, kb.TenantID, kb.ID)
	if err != nil {
		return nil, err
	}

	if len(knowledgeList) > 0 {
		return knowledgeList[0], nil
	}

	// Create a new FAQ knowledge entry, copying from source if available
	knowledge := &types.Knowledge{
		ID:               uuid.New().String(),
		TenantID:         kb.TenantID,
		KnowledgeBaseID:  kb.ID,
		Type:             types.KnowledgeTypeFAQ,
		Channel:          types.ChannelWeb,
		Title:            "FAQ",
		ParseStatus:      "completed",
		EnableStatus:     "enabled",
		EmbeddingModelID: kb.EmbeddingModelID,
	}

	// Copy additional fields from source knowledge if available
	if srcKnowledge != nil {
		knowledge.Title = srcKnowledge.Title
		knowledge.Description = srcKnowledge.Description
		knowledge.Source = srcKnowledge.Source
		knowledge.Channel = srcKnowledge.Channel
		knowledge.Metadata = srcKnowledge.Metadata
	}

	if err := s.repo.CreateKnowledge(ctx, knowledge); err != nil {
		return nil, err
	}
	return knowledge, nil
}

// saveKBCloneProgress saves the KB clone progress to Redis
func (s *knowledgeService) saveKBCloneProgress(ctx context.Context, progress *types.KBCloneProgress) error {
	key := getKBCloneProgressKey(progress.TaskID)
	data, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("failed to marshal progress: %w", err)
	}
	return s.redisClient.Set(ctx, key, data, kbCloneProgressTTL).Err()
}

// SaveKBCloneProgress saves the KB clone progress to Redis (public method for handler use)
func (s *knowledgeService) SaveKBCloneProgress(ctx context.Context, progress *types.KBCloneProgress) error {
	return s.saveKBCloneProgress(ctx, progress)
}

// GetKBCloneProgress retrieves the progress of a knowledge base clone task
func (s *knowledgeService) GetKBCloneProgress(ctx context.Context, taskID string) (*types.KBCloneProgress, error) {
	key := getKBCloneProgressKey(taskID)
	data, err := s.redisClient.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, werrors.NewNotFoundError("KB clone task not found")
		}
		return nil, fmt.Errorf("failed to get progress from Redis: %w", err)
	}

	var progress types.KBCloneProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return nil, fmt.Errorf("failed to unmarshal progress: %w", err)
	}
	return &progress, nil
}

const (
	knowledgeMoveProgressKeyPrefix = "knowledge_move_progress:"
	knowledgeMoveProgressTTL       = 24 * time.Hour
	knowledgeMoveRecoveryLease     = 10 * time.Minute
	knowledgeMoveDispatchOp        = "dispatch"
)

// PersistKnowledgeMoveDispatch 在软删除门禁事务中写入完整 move 调度意图。
func (s *knowledgeService) PersistKnowledgeMoveDispatch(
	ctx context.Context,
	payload types.KnowledgeMovePayload,
) error {
	guarded, ok := s.taskPendingRepo.(interfaces.TaskPendingOpsKnowledgeBaseGuard)
	if !ok {
		return errors.New("task pending repository does not support guarded move dispatch")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	accepted, err := guarded.EnqueueIfKnowledgeBaseActive(ctx, &types.TaskPendingOp{
		TenantID: payload.TenantID,
		TaskType: types.TypeKnowledgeMove,
		Scope:    types.TaskScopeKnowledgeBase,
		ScopeID:  payload.SourceKBID,
		Op:       knowledgeMoveDispatchOp,
		DedupKey: payload.TaskID,
		Payload:  payloadBytes,
	})
	if err != nil {
		return err
	}
	if !accepted {
		return werrors.NewConflictError("源知识库已不可写，无法创建移动任务")
	}
	return nil
}

func (s *knowledgeService) deleteKnowledgeMoveDispatch(
	ctx context.Context,
	payload types.KnowledgeMovePayload,
) error {
	if s.taskPendingRepo == nil {
		return nil
	}
	return s.taskPendingRepo.DeleteByDedupKey(
		ctx,
		types.TypeKnowledgeMove,
		types.TaskScopeKnowledgeBase,
		payload.SourceKBID,
		payload.TaskID,
		knowledgeMoveDispatchOp,
	)
}

type knowledgeMoveRecoveryRepository interface {
	ClaimKnowledgeMoveRecoveryOps(
		context.Context, time.Time, int,
	) ([]*types.TaskPendingOp, error)
	ReleaseKnowledgeMoveRecoveryOps(context.Context, []int64) error
}

type knowledgeMoveDispatchLookup interface {
	KnowledgeMoveDispatchExists(context.Context, uint64, string, string) (bool, error)
}

// preserveCancelledKnowledgeMove 按持久 dispatch 判断 context cancellation
// 是否只是可重试的执行中断。仓储能力缺失或查询失败时 fail-safe 保留。
func (s *knowledgeService) preserveCancelledKnowledgeMove(
	ctx context.Context,
	payload types.KnowledgeMovePayload,
) bool {
	lookup, ok := s.repo.(knowledgeMoveDispatchLookup)
	if !ok {
		return true
	}
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	exists, err := lookup.KnowledgeMoveDispatchExists(
		lookupCtx,
		payload.TenantID,
		payload.SourceKBID,
		payload.TaskID,
	)
	if err != nil {
		logger.Warnf(ctx,
			"ProcessKnowledgeMove: dispatch lookup failed during cancellation; preserving claim: %v",
			err,
		)
		return true
	}
	return exists
}

// RecoverPendingKnowledgeMoves 从持久 outbox 重建丢失的 move 队列任务。
// force 仅用于进程启动：Redis TaskID 或 Lite 进程内 TaskID 会阻止并发重复投递。
func (s *knowledgeService) RecoverPendingKnowledgeMoves(
	ctx context.Context,
	limit int,
	force bool,
) error {
	recoveryRepo, ok := s.repo.(knowledgeMoveRecoveryRepository)
	if !ok {
		return errors.New("knowledge repository does not support move recovery")
	}
	if s.task == nil {
		return errors.New("knowledge move task enqueuer is unavailable")
	}
	staleBefore := time.Now().Add(-knowledgeMoveRecoveryLease)
	if force {
		staleBefore = time.Now().Add(time.Second)
	}
	ops, err := recoveryRepo.ClaimKnowledgeMoveRecoveryOps(ctx, staleBefore, limit)
	if err != nil || len(ops) == 0 {
		return err
	}

	type recoveryGroup struct {
		payload types.KnowledgeMovePayload
		opIDs   []int64
		seen    map[string]struct{}
	}
	groups := make(map[string]*recoveryGroup)
	var recoveryErr error
	for _, op := range ops {
		var payload types.KnowledgeMovePayload
		if op == nil || json.Unmarshal(op.Payload, &payload) != nil ||
			payload.TaskID == "" || len(payload.KnowledgeIDs) == 0 ||
			op.TenantID != payload.TenantID || op.ScopeID != payload.SourceKBID ||
			op.DedupKey != payload.TaskID {
			if op != nil {
				_ = recoveryRepo.ReleaseKnowledgeMoveRecoveryOps(ctx, []int64{op.ID})
			}
			recoveryErr = errors.Join(recoveryErr, errors.New("invalid knowledge move recovery payload"))
			continue
		}
		group := groups[payload.TaskID]
		if group == nil {
			group = &recoveryGroup{payload: payload, seen: make(map[string]struct{})}
			group.payload.KnowledgeIDs = nil
			groups[payload.TaskID] = group
		}
		if group.payload.TenantID != payload.TenantID ||
			group.payload.SourceKBID != payload.SourceKBID ||
			group.payload.TargetKBID != payload.TargetKBID ||
			group.payload.Mode != payload.Mode {
			_ = recoveryRepo.ReleaseKnowledgeMoveRecoveryOps(ctx, []int64{op.ID})
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("inconsistent move recovery payload for task %s", payload.TaskID))
			continue
		}
		for _, knowledgeID := range payload.KnowledgeIDs {
			if _, exists := group.seen[knowledgeID]; !exists {
				group.seen[knowledgeID] = struct{}{}
				group.payload.KnowledgeIDs = append(group.payload.KnowledgeIDs, knowledgeID)
			}
		}
		group.opIDs = append(group.opIDs, op.ID)
	}

	for taskID, group := range groups {
		if inspector, ok := s.taskInspector.(interfaces.RuntimeTaskInspector); ok {
			existing, supported, inspectErr := inspector.GetRuntimeTask(
				ctx,
				types.QueueMaintenance,
				taskID,
			)
			if inspectErr != nil {
				_ = recoveryRepo.ReleaseKnowledgeMoveRecoveryOps(ctx, group.opIDs)
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("inspect move task %s: %w", taskID, inspectErr))
				continue
			}
			if supported && existing != nil {
				switch existing.State {
				case types.RuntimeTaskPending, types.RuntimeTaskActive,
					types.RuntimeTaskScheduled, types.RuntimeTaskRetry:
					continue
				case types.RuntimeTaskArchived, types.RuntimeTaskCompleted:
					deleted, deleteErr := inspector.ForceDeleteRuntimeTask(
						ctx, types.QueueMaintenance, taskID,
					)
					if deleteErr != nil || !deleted {
						_ = recoveryRepo.ReleaseKnowledgeMoveRecoveryOps(ctx, group.opIDs)
						if deleteErr == nil {
							deleteErr = errors.New("runtime task deletion is unavailable")
						}
						recoveryErr = errors.Join(recoveryErr, fmt.Errorf("remove terminal move task %s: %w", taskID, deleteErr))
						continue
					}
				}
			}
		}
		_ = s.saveKnowledgeMoveProgress(ctx, &types.KnowledgeMoveProgress{
			TaskID:     taskID,
			SourceKBID: group.payload.SourceKBID,
			TargetKBID: group.payload.TargetKBID,
			Status:     types.KBCloneStatusPending,
			Total:      len(group.payload.KnowledgeIDs),
			Message:    "Task recovered, waiting to start...",
			CreatedAt:  time.Now().Unix(),
			UpdatedAt:  time.Now().Unix(),
		})
		payloadBytes, marshalErr := json.Marshal(group.payload)
		if marshalErr != nil {
			_ = recoveryRepo.ReleaseKnowledgeMoveRecoveryOps(ctx, group.opIDs)
			recoveryErr = errors.Join(recoveryErr, marshalErr)
			continue
		}
		task := asynq.NewTask(types.TypeKnowledgeMove, payloadBytes)
		_, enqueueErr := s.task.Enqueue(
			task,
			asynq.TaskID(taskID),
			asynq.Queue(types.QueueMaintenance),
			asynq.MaxRetry(3),
			asynq.Timeout(2*time.Hour),
		)
		if enqueueErr == nil || errors.Is(enqueueErr, asynq.ErrTaskIDConflict) ||
			errors.Is(enqueueErr, asynq.ErrDuplicateTask) {
			continue
		}
		if releaseErr := recoveryRepo.ReleaseKnowledgeMoveRecoveryOps(ctx, group.opIDs); releaseErr != nil {
			enqueueErr = errors.Join(enqueueErr, releaseErr)
		}
		recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover move task %s: %w", taskID, enqueueErr))
	}
	return recoveryErr
}

func getKnowledgeMoveProgressKey(taskID string) string {
	return knowledgeMoveProgressKeyPrefix + taskID
}

func (s *knowledgeService) saveKnowledgeMoveProgress(ctx context.Context, progress *types.KnowledgeMoveProgress) error {
	if progress == nil || progress.TaskID == "" {
		return errors.New("knowledge move progress requires a task ID")
	}
	if progress.UpdatedAt == 0 {
		progress.UpdatedAt = time.Now().Unix()
	}
	if s.redisClient == nil {
		copy := *progress
		s.memMoveProgress.Store(progress.TaskID, &copy)
		return nil
	}
	key := getKnowledgeMoveProgressKey(progress.TaskID)
	data, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("failed to marshal move progress: %w", err)
	}
	return s.redisClient.Set(ctx, key, data, knowledgeMoveProgressTTL).Err()
}

// SaveKnowledgeMoveProgress saves the knowledge move progress to Redis (public method for handler use)
func (s *knowledgeService) SaveKnowledgeMoveProgress(ctx context.Context, progress *types.KnowledgeMoveProgress) error {
	return s.saveKnowledgeMoveProgress(ctx, progress)
}

// GetKnowledgeMoveProgress retrieves the progress of a knowledge move task
func (s *knowledgeService) GetKnowledgeMoveProgress(ctx context.Context, taskID string) (*types.KnowledgeMoveProgress, error) {
	if s.redisClient == nil {
		value, ok := s.memMoveProgress.Load(taskID)
		if !ok {
			return nil, werrors.NewNotFoundError("Knowledge move task not found")
		}
		progress, ok := value.(*types.KnowledgeMoveProgress)
		if !ok || progress == nil {
			s.memMoveProgress.Delete(taskID)
			return nil, werrors.NewNotFoundError("Knowledge move task not found")
		}
		if progress.UpdatedAt > 0 && time.Since(time.Unix(progress.UpdatedAt, 0)) > knowledgeMoveProgressTTL {
			s.memMoveProgress.Delete(taskID)
			return nil, werrors.NewNotFoundError("Knowledge move task not found")
		}
		copy := *progress
		return &copy, nil
	}
	key := getKnowledgeMoveProgressKey(taskID)
	data, err := s.redisClient.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, werrors.NewNotFoundError("Knowledge move task not found")
		}
		return nil, fmt.Errorf("failed to get move progress from Redis: %w", err)
	}

	var progress types.KnowledgeMoveProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return nil, fmt.Errorf("failed to unmarshal move progress: %w", err)
	}
	return &progress, nil
}

// ProcessKnowledgeMove handles Asynq knowledge move tasks
func (s *knowledgeService) ProcessKnowledgeMove(ctx context.Context, t *asynq.Task) (retErr error) {
	var payload types.KnowledgeMovePayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal knowledge move payload: %w", err)
	}
	ctx = payload.Initiator.Apply(ctx)
	ctx = withKBActivityTask(ctx, payload.TaskID, kbActivityTrigger(ctx))

	// Add tenant ID to context
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)
	// 同时兼容 Redis Asynq 与 Lite executor；元数据缺失时不能误判最后一次。
	retryCount, maxRetry, hasRetryMetadata := taskRetryMetadata(ctx)
	isLastRetry := hasRetryMetadata && retryCount >= maxRetry
	isCancellation := func(cause error) bool {
		return cause != nil && (errors.Is(cause, context.Canceled) || ctx.Err() != nil)
	}
	shouldPreserveCancellation := func(cause error) bool {
		if !isCancellation(cause) {
			return false
		}
		return s.preserveCancelledKnowledgeMove(ctx, payload)
	}
	defer func() {
		if shouldPreserveCancellation(retErr) {
			return
		}
		terminal := retErr == nil || isLastRetry || errors.Is(retErr, asynq.SkipRetry) ||
			errors.Is(retErr, context.Canceled) || ctx.Err() != nil
		if !terminal {
			return
		}
		if retErr != nil {
			if coordinator, ok := s.repo.(knowledgeMoveCoordinator); ok {
				failCtx := ctx
				if ctx.Err() != nil {
					failCtx = context.WithoutCancel(ctx)
				}
				for _, knowledgeID := range payload.KnowledgeIDs {
					if _, failErr := coordinator.FailClaimedKnowledgeMove(
						failCtx,
						payload.TenantID,
						knowledgeID,
						payload.TaskID,
						retErr.Error(),
					); failErr != nil {
						retErr = errors.Join(retErr, fmt.Errorf("release terminal move claim %s: %w", knowledgeID, failErr))
					}
				}
			}
		}
		cleanupCtx := ctx
		if ctx.Err() != nil {
			cleanupCtx = context.WithoutCancel(ctx)
		}
		if cleanupErr := s.deleteKnowledgeMoveDispatch(cleanupCtx, payload); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("delete knowledge move dispatch: %w", cleanupErr))
		}
	}()

	// Get tenant info and add to context
	tenantInfo, err := s.tenantRepo.GetTenantByID(ctx, payload.TenantID)
	if err != nil {
		logger.Errorf(ctx, "ProcessKnowledgeMove: failed to get tenant info: %v", err)
		return fmt.Errorf("failed to get tenant info: %w", err)
	}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenantInfo)

	logger.Infof(ctx, "ProcessKnowledgeMove: task=%s, source=%s, target=%s, mode=%s, count=%d, retry=%d/%d",
		payload.TaskID, payload.SourceKBID, payload.TargetKBID, payload.Mode, len(payload.KnowledgeIDs), retryCount, maxRetry)

	// Helper function to handle errors - only mark as failed on last retry
	handleError := func(progress *types.KnowledgeMoveProgress, err error, message string) {
		preserveCancellation := shouldPreserveCancellation(err)
		if (isLastRetry || (isCancellation(err) && !preserveCancellation)) && !preserveCancellation {
			progress.Status = types.KBCloneStatusFailed
			progress.Error = err.Error()
			progress.Message = message
			progress.UpdatedAt = time.Now().Unix()
			_ = s.saveKnowledgeMoveProgress(ctx, progress)
			for _, kbID := range []string{payload.SourceKBID, payload.TargetKBID} {
				recordKBActivity(ctx, s.audit, payload.TenantID, kbID, types.AuditActionKnowledgeMoveFailed,
					"knowledge_move", payload.TaskID, types.AuditOutcomeFailed,
					map[string]any{"source_kb_id": payload.SourceKBID, "target_kb_id": payload.TargetKBID,
						"task_id": payload.TaskID, "count": len(payload.KnowledgeIDs), "mode": payload.Mode})
			}
		}
	}
	if retryCount == 0 {
		for _, kbID := range []string{payload.SourceKBID, payload.TargetKBID} {
			recordKBActivity(ctx, s.audit, payload.TenantID, kbID, types.AuditActionKnowledgeMoveStarted,
				"knowledge_move", payload.TaskID, types.AuditOutcomeAccepted,
				map[string]any{"source_kb_id": payload.SourceKBID, "target_kb_id": payload.TargetKBID,
					"task_id": payload.TaskID, "count": len(payload.KnowledgeIDs), "mode": payload.Mode})
		}
	}

	// Update progress to processing
	progress := &types.KnowledgeMoveProgress{
		TaskID:     payload.TaskID,
		SourceKBID: payload.SourceKBID,
		TargetKBID: payload.TargetKBID,
		Status:     types.KBCloneStatusProcessing,
		Total:      len(payload.KnowledgeIDs),
		Progress:   0,
		Message:    "Starting knowledge move...",
		UpdatedAt:  time.Now().Unix(),
	}
	_ = s.saveKnowledgeMoveProgress(ctx, progress)
	moveCoordinator, ok := s.repo.(knowledgeMoveCoordinator)
	if !ok {
		return errors.New("knowledge repository does not support move coordination")
	}
	releaseMoveClaims := func(cause error) {
		if errors.Is(cause, context.Canceled) || ctx.Err() != nil || !isLastRetry {
			return
		}
		failCtx := ctx
		if ctx.Err() != nil {
			failCtx = context.WithoutCancel(ctx)
		}
		for _, knowledgeID := range payload.KnowledgeIDs {
			if _, failErr := moveCoordinator.FailClaimedKnowledgeMove(
				failCtx,
				payload.TenantID,
				knowledgeID,
				payload.TaskID,
				cause.Error(),
			); failErr != nil {
				logger.Warnf(ctx, "ProcessKnowledgeMove: failed to release claim for %s: %v", knowledgeID, failErr)
			}
		}
	}

	// Get source and target knowledge bases
	sourceKB, err := s.kbService.GetKnowledgeBaseByID(ctx, payload.SourceKBID)
	if err != nil {
		releaseMoveClaims(err)
		handleError(progress, err, "Failed to get source knowledge base")
		return err
	}
	targetKB, err := s.kbService.GetKnowledgeBaseByID(ctx, payload.TargetKBID)
	if err != nil {
		releaseMoveClaims(err)
		handleError(progress, err, "Failed to get target knowledge base")
		return err
	}
	if sourceKB == nil || targetKB == nil ||
		sourceKB.TenantID != payload.TenantID || targetKB.TenantID != payload.TenantID {
		err := fmt.Errorf(
			"knowledge move tenant mismatch: payload=%d source=%d target=%d",
			payload.TenantID,
			func() uint64 {
				if sourceKB == nil {
					return 0
				}
				return sourceKB.TenantID
			}(),
			func() uint64 {
				if targetKB == nil {
					return 0
				}
				return targetKB.TenantID
			}(),
		)
		handleError(progress, err, "Knowledge base tenant mismatch")
		return errors.Join(asynq.SkipRetry, err)
	}

	// Validate compatibility
	if sourceKB.Type != targetKB.Type {
		err := fmt.Errorf("type mismatch: source=%s, target=%s", sourceKB.Type, targetKB.Type)
		releaseMoveClaims(err)
		handleError(progress, err, "Source and target knowledge bases must be the same type")
		return err
	}
	if sourceKB.EmbeddingModelID != targetKB.EmbeddingModelID {
		err := fmt.Errorf("embedding model mismatch: source=%s, target=%s", sourceKB.EmbeddingModelID, targetKB.EmbeddingModelID)
		releaseMoveClaims(err)
		handleError(progress, err, "Source and target must use the same embedding model")
		return err
	}

	// Process each knowledge item
	var moveErrors error
	for i, knowledgeID := range payload.KnowledgeIDs {
		err := s.moveOneKnowledge(
			ctx,
			knowledgeID,
			sourceKB,
			targetKB,
			payload.Mode,
			payload.TaskID,
		)
		if err != nil {
			logger.Errorf(ctx, "ProcessKnowledgeMove: failed to move knowledge %s: %v", knowledgeID, err)
			progress.Failed++
			moveErrors = errors.Join(moveErrors, fmt.Errorf("move knowledge %s: %w", knowledgeID, err))
			if isLastRetry && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				failCtx := ctx
				if ctx.Err() != nil {
					failCtx = context.WithoutCancel(ctx)
				}
				if _, failErr := moveCoordinator.FailClaimedKnowledgeMove(
					failCtx,
					payload.TenantID,
					knowledgeID,
					payload.TaskID,
					err.Error(),
				); failErr != nil {
					moveErrors = errors.Join(
						moveErrors,
						fmt.Errorf("release failed move claim for %s: %w", knowledgeID, failErr),
					)
				}
			}
		}
		progress.Processed = i + 1
		if progress.Total > 0 {
			progress.Progress = progress.Processed * 100 / progress.Total
		}
		progress.Message = fmt.Sprintf("Moved %d/%d knowledge items", progress.Processed, progress.Total)
		progress.UpdatedAt = time.Now().Unix()
		_ = s.saveKnowledgeMoveProgress(ctx, progress)
	}
	if moveErrors != nil {
		progress.Error = moveErrors.Error()
		preserveCancellation := shouldPreserveCancellation(moveErrors)
		if (isLastRetry || (isCancellation(moveErrors) && !preserveCancellation)) && !preserveCancellation {
			progress.Status = types.KBCloneStatusFailed
			progress.Message = fmt.Sprintf("Knowledge move failed: %d/%d items failed", progress.Failed, progress.Total)
			handleError(progress, moveErrors, progress.Message)
		} else {
			progress.Status = types.KBCloneStatusProcessing
			progress.Message = fmt.Sprintf("Knowledge move will retry: %d/%d items failed", progress.Failed, progress.Total)
			progress.UpdatedAt = time.Now().Unix()
			_ = s.saveKnowledgeMoveProgress(ctx, progress)
		}
		return fmt.Errorf("knowledge move task incomplete: %w", moveErrors)
	}

	// Mark as completed
	progress.Status = types.KBCloneStatusCompleted
	progress.Message = fmt.Sprintf("Knowledge move completed: %d/%d succeeded", progress.Processed, progress.Total)
	progress.Progress = 100
	progress.UpdatedAt = time.Now().Unix()
	_ = s.saveKnowledgeMoveProgress(ctx, progress)

	logger.Infof(ctx, "ProcessKnowledgeMove: task=%s completed, processed=%d, failed=%d", payload.TaskID, progress.Processed, progress.Failed)
	for _, kbID := range []string{payload.SourceKBID, payload.TargetKBID} {
		recordKBActivity(ctx, s.audit, payload.TenantID, kbID, types.AuditActionKnowledgeMoveCompleted,
			"knowledge_move", payload.TaskID, types.AuditOutcomeSuccess,
			map[string]any{"source_kb_id": payload.SourceKBID, "target_kb_id": payload.TargetKBID,
				"task_id": payload.TaskID, "count": progress.Total, "failed": progress.Failed, "mode": payload.Mode})
	}
	return nil
}

// moveOneKnowledge moves a single knowledge item from source KB to target KB.
func (s *knowledgeService) moveOneKnowledge(
	ctx context.Context,
	knowledgeID string,
	sourceKB, targetKB *types.KnowledgeBase,
	mode string,
	taskID string,
) error {
	tenantID := ctx.Value(types.TenantIDContextKey).(uint64)
	if sourceKB == nil || targetKB == nil || sourceKB.ID == "" || targetKB.ID == "" {
		return errors.New("source and target knowledge bases are required")
	}
	if mode != "reuse_vectors" && mode != "reparse" {
		return fmt.Errorf("unknown move mode: %s", mode)
	}

	// 跨存储复用向量必须在声明及任何副作用前拒绝。
	if mode == "reuse_vectors" && !sourceKB.SharesStoreWith(targetKB) {
		return fmt.Errorf(
			"reuse_vectors move across different vector stores is not supported "+
				"(source KB %s, target KB %s); use reparse mode", sourceKB.ID, targetKB.ID)
	}

	coordinator, ok := s.repo.(knowledgeMoveCoordinator)
	if !ok {
		return errors.New("knowledge repository does not support move coordination")
	}
	knowledge, alreadyCompleted, claimed, err := coordinator.ClaimKnowledgeForMove(
		ctx,
		tenantID,
		knowledgeID,
		sourceKB.ID,
		targetKB.ID,
		taskID,
		mode,
	)
	if err != nil {
		return fmt.Errorf("claim knowledge move: %w", err)
	}
	if !claimed || knowledge == nil {
		return errors.New("knowledge move was not claimed because another move or deletion owns it")
	}
	if alreadyCompleted {
		return nil
	}
	if knowledge.KnowledgeBaseID == targetKB.ID {
		switch mode {
		case "reuse_vectors":
			return s.moveKnowledgeReuseVectors(ctx, knowledge, sourceKB, targetKB, taskID)
		case "reparse":
			return s.moveKnowledgeReparse(ctx, knowledge, sourceKB, targetKB, taskID)
		}
		return errors.New("knowledge move has an unsupported staged target mode")
	}
	if knowledge.KnowledgeBaseID != sourceKB.ID || knowledge.ParseStatus != types.ParseStatusMoving {
		return errors.New("knowledge move claim returned an invalid source state")
	}

	// 源 Wiki 收敛必须在持久声明成功后、知识归属仍指向源库时执行。
	if sourceKB.IsWikiEnabled() {
		s.cleanupWikiOnKnowledgeDelete(ctx, knowledge)
	}

	switch mode {
	case "reuse_vectors":
		return s.moveKnowledgeReuseVectors(ctx, knowledge, sourceKB, targetKB, taskID)
	case "reparse":
		return s.moveKnowledgeReparse(ctx, knowledge, sourceKB, targetKB, taskID)
	}
	return nil
}

// moveKnowledgeReuseVectors moves knowledge by copying vector indices and updating DB references.
func (s *knowledgeService) moveKnowledgeReuseVectors(
	ctx context.Context,
	knowledge *types.Knowledge,
	sourceKB, targetKB *types.KnowledgeBase,
	taskID string,
) error {
	tenantID := ctx.Value(types.TenantIDContextKey).(uint64)
	if !sourceKB.SharesStoreWith(targetKB) {
		return fmt.Errorf(
			"reuse_vectors move across different vector stores is not supported "+
				"(source KB %s, target KB %s); use reparse mode", sourceKB.ID, targetKB.ID)
	}

	moveCoordinator, ok := s.repo.(knowledgeMoveCoordinator)
	if !ok {
		return errors.New("knowledge repository does not support move coordination")
	}
	if knowledge.KnowledgeBaseID == sourceKB.ID {
		oldChunks, err := s.chunkRepo.ListChunksByKnowledgeID(ctx, tenantID, knowledge.ID)
		if err != nil {
			return fmt.Errorf("failed to list chunks: %w", err)
		}
		chunkIDMapping := make(map[string]string, len(oldChunks))
		for _, chunk := range oldChunks {
			chunkIDMapping[chunk.ID] = chunk.ID
		}

		if len(chunkIDMapping) > 0 && knowledge.EmbeddingModelID != "" {
			retrieveEngine, err := retriever.CreateRetrieveEngineForKB(
				ctx,
				s.retrieveEngine,
				s.ownership,
				tenantID,
				sourceKB.VectorStoreID,
			)
			if err != nil {
				return fmt.Errorf("failed to init retrieve engine: %w", err)
			}
			embeddingModel, err := s.modelService.GetEmbeddingModel(ctx, knowledge.EmbeddingModelID)
			if err != nil {
				return fmt.Errorf("failed to get embedding model: %w", err)
			}
			knowledgeIDMapping := map[string]string{knowledge.ID: knowledge.ID}
			if err := retrieveEngine.CopyIndices(
				ctx,
				sourceKB.ID,
				targetKB.ID,
				knowledgeIDMapping,
				chunkIDMapping,
				embeddingModel.GetDimensions(),
				sourceKB.Type,
			); err != nil {
				return fmt.Errorf("failed to copy indices: %w", err)
			}
			if err := retrieveEngine.DeleteByKnowledgeIDList(
				ctx,
				[]string{knowledge.ID},
				embeddingModel.GetDimensions(),
				sourceKB.Type,
			); err != nil {
				return fmt.Errorf("failed to delete source indices: %w", err)
			}
		}

		if err := s.chunkRepo.MoveChunksByKnowledgeID(ctx, tenantID, knowledge.ID, targetKB.ID); err != nil {
			return fmt.Errorf("failed to move chunks: %w", err)
		}
		if err := s.repo.DeleteKnowledgeTagRelations(ctx, knowledge.ID); err != nil {
			return fmt.Errorf("failed to clear knowledge tag relations: %w", err)
		}
		knowledge.KnowledgeBaseID = targetKB.ID
		knowledge.ParseStatus = types.ParseStatusMoving
		knowledge.UpdatedAt = time.Now()
		staged, err := moveCoordinator.StageClaimedKnowledgeMove(ctx, knowledge, taskID)
		if err != nil {
			return fmt.Errorf("stage reused-vector knowledge move: %w", err)
		}
		if !staged {
			return errors.New("knowledge move claim was lost before reuse-vector staging")
		}
	} else if knowledge.KnowledgeBaseID != targetKB.ID || knowledge.ParseStatus != types.ParseStatusMoving {
		return errors.New("reuse-vector move has an invalid staged state")
	}

	if targetKB.IsWikiEnabled() {
		accepted, err := EnqueueWikiIngest(
			ctx,
			s.task,
			s.taskPendingRepo,
			tenantID,
			targetKB.ID,
			knowledge.ID,
		)
		if err != nil {
			return fmt.Errorf("enqueue target wiki ingest: %w", err)
		}
		if !accepted {
			return errors.New("target knowledge base rejected wiki ingest during move")
		}
	}
	knowledge.ParseStatus = types.ParseStatusCompleted
	knowledge.UpdatedAt = time.Now()
	updated, err := moveCoordinator.CompleteClaimedKnowledgeMove(ctx, knowledge, taskID)
	if err != nil {
		return fmt.Errorf("failed to update knowledge: %w", err)
	}
	if !updated {
		return errors.New("knowledge move claim was lost before completion")
	}

	return nil
}

// moveKnowledgeReparse moves knowledge to target KB and re-parses it with target KB's configuration.
func (s *knowledgeService) moveKnowledgeReparse(
	ctx context.Context,
	knowledge *types.Knowledge,
	sourceKB, targetKB *types.KnowledgeBase,
	taskID string,
) error {
	tenantID := ctx.Value(types.TenantIDContextKey).(uint64)
	moveCoordinator, ok := s.repo.(knowledgeMoveCoordinator)
	if !ok {
		return errors.New("knowledge repository does not support move coordination")
	}

	if knowledge.KnowledgeBaseID == sourceKB.ID {
		if err := s.cleanupKnowledgeResources(ctx, knowledge, taskID); err != nil {
			return fmt.Errorf("cleanup source knowledge resources: %w", err)
		}
		if err := s.repo.DeleteKnowledgeTagRelations(ctx, knowledge.ID); err != nil {
			return fmt.Errorf("failed to clear knowledge tag relations: %w", err)
		}
		knowledge.KnowledgeBaseID = targetKB.ID
		knowledge.EmbeddingModelID = targetKB.EmbeddingModelID
		knowledge.ParseStatus = types.ParseStatusMoving
		knowledge.EnableStatus = "disabled"
		knowledge.Description = ""
		knowledge.ProcessedAt = nil
		knowledge.UpdatedAt = time.Now()
		staged, err := moveCoordinator.StageClaimedKnowledgeMove(ctx, knowledge, taskID)
		if err != nil {
			return fmt.Errorf("stage knowledge move for reparse: %w", err)
		}
		if !staged {
			return errors.New("knowledge move claim was lost before reparse staging")
		}
	}
	if knowledge.KnowledgeBaseID != targetKB.ID || knowledge.ParseStatus != types.ParseStatusMoving {
		return errors.New("knowledge reparse move has an invalid staged state")
	}

	if knowledge.IsManual() {
		meta, err := knowledge.ManualMetadata()
		if err != nil || meta == nil {
			return fmt.Errorf("failed to get manual metadata for reparse: %w", err)
		}
		manualTaskID := fmt.Sprintf("knowledge-move-manual-%s-%s", taskID, knowledge.ID)
		if _, err := s.enqueueManualProcessing(
			ctx,
			knowledge,
			meta.Content,
			false,
			manualProcessingEnqueueConfig{
				TaskID:  manualTaskID,
				Options: []asynq.Option{asynq.ProcessIn(time.Second)},
			},
		); err != nil {
			return fmt.Errorf("enqueue moved manual knowledge processing: %w", err)
		}
		knowledge.ParseStatus = types.ParseStatusPending
		completed, err := moveCoordinator.CompleteClaimedKnowledgeMove(ctx, knowledge, taskID)
		if err != nil {
			return fmt.Errorf("complete manual knowledge move: %w", err)
		}
		if !completed {
			return errors.New("manual knowledge move claim was lost before completion")
		}
		return nil
	}

	if knowledge.FilePath == "" {
		return errors.New("moved knowledge has no source file for reparse")
	}
	enableMultimodel := targetKB.IsMultimodalEnabled()
	enableQuestionGeneration := false
	questionCount := 3
	if targetKB.QuestionGenerationConfig != nil && targetKB.QuestionGenerationConfig.Enabled {
		enableQuestionGeneration = true
		if targetKB.QuestionGenerationConfig.QuestionCount > 0 {
			questionCount = targetKB.QuestionGenerationConfig.QuestionCount
		}
	}

	lang := types.LanguageFromContextOrDefault(ctx)
	taskPayload := types.DocumentProcessPayload{
		TenantID:                 tenantID,
		KnowledgeID:              knowledge.ID,
		KnowledgeBaseID:          targetKB.ID,
		FilePath:                 knowledge.FilePath,
		FileName:                 knowledge.FileName,
		FileType:                 getFileType(knowledge.FileName),
		EnableMultimodel:         enableMultimodel,
		EnableQuestionGeneration: enableQuestionGeneration,
		QuestionCount:            questionCount,
		Language:                 lang,
	}

	langfuse.InjectTracing(ctx, &taskPayload)
	payloadBytes, err := json.Marshal(taskPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal document process payload: %w", err)
	}

	if s.task == nil {
		return errors.New("document process task enqueuer is unavailable")
	}
	reparseTaskID := fmt.Sprintf("knowledge-move-reparse-%s-%s", taskID, knowledge.ID)
	enqueueOptions := documentProcessTaskOptions(
		s.config,
		asynq.TaskID(reparseTaskID),
		asynq.ProcessIn(time.Second),
	)
	task := asynq.NewTask(
		types.TypeDocumentProcess,
		payloadBytes,
	)
	info, err := s.task.Enqueue(task, enqueueOptions...)
	if err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) && !errors.Is(err, asynq.ErrDuplicateTask) {
		return fmt.Errorf("failed to enqueue document process task: %w", err)
	}
	if err == nil {
		logger.Infof(ctx, "moveKnowledgeReparse: enqueued reparse task id=%s for knowledge=%s", info.ID, knowledge.ID)
	}

	knowledge.ParseStatus = types.ParseStatusPending
	completed, err := moveCoordinator.CompleteClaimedKnowledgeMove(ctx, knowledge, taskID)
	if err != nil {
		return fmt.Errorf("complete knowledge move after reparse enqueue: %w", err)
	}
	if !completed {
		return errors.New("knowledge move claim was lost after reparse enqueue")
	}
	return nil
}

// getOrCreateTagInTarget finds or creates a tag in the target knowledge base based on the source tag.
// It looks up the source tag by ID, then tries to find a tag with the same name in the target KB.
// If not found, it creates a new tag with the same properties.
// The mapping is cached in tagIDMapping for subsequent lookups.
func (s *knowledgeService) getOrCreateTagInTarget(
	ctx context.Context,
	srcTenantID, dstTenantID uint64,
	dstKnowledgeBaseID string,
	srcTagID string,
	tagIDMapping map[string]string,
) string {
	// Get source tag
	srcTag, err := s.tagRepo.GetByID(ctx, srcTenantID, srcTagID)
	if err != nil || srcTag == nil {
		logger.Warnf(ctx, "Failed to get source tag %s: %v", srcTagID, err)
		tagIDMapping[srcTagID] = "" // Cache empty result to avoid repeated lookups
		return ""
	}

	// Try to find existing tag with same name in target KB
	dstTag, err := s.tagRepo.GetByName(ctx, dstTenantID, dstKnowledgeBaseID, srcTag.Name)
	if err == nil && dstTag != nil {
		tagIDMapping[srcTagID] = dstTag.ID
		return dstTag.ID
	}

	// Create new tag in target KB
	// "未分类" tag should have the lowest sort order to appear first
	sortOrder := srcTag.SortOrder
	if srcTag.Name == types.UntaggedTagName {
		sortOrder = -1
	}
	newTag := &types.KnowledgeTag{
		ID:              uuid.New().String(),
		TenantID:        dstTenantID,
		KnowledgeBaseID: dstKnowledgeBaseID,
		Name:            srcTag.Name,
		Color:           srcTag.Color,
		SortOrder:       sortOrder,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := s.tagRepo.Create(ctx, newTag); err != nil {
		logger.Warnf(ctx, "Failed to create tag %s in target KB: %v", srcTag.Name, err)
		tagIDMapping[srcTagID] = "" // Cache empty result
		return ""
	}

	tagIDMapping[srcTagID] = newTag.ID
	logger.Infof(ctx, "Created tag %s (ID: %s) in target KB %s", newTag.Name, newTag.ID, dstKnowledgeBaseID)
	return newTag.ID
}
