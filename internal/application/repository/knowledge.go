package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrKnowledgeNotFound       = errors.New("knowledge not found")
	ErrKnowledgeMoveInProgress = types.ErrKnowledgeMoveInProgress
)

const knowledgeMoveClaimMetadataKey = "_weknora_move_claim"

const knowledgeMoveDispatchOp = "dispatch"

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

type knowledgeMoveClaim struct {
	TaskID                string `json:"task_id"`
	SourceKnowledgeBaseID string `json:"source_knowledge_base_id"`
	TargetKnowledgeBaseID string `json:"target_knowledge_base_id"`
	Mode                  string `json:"mode"`
	Stage                 string `json:"stage"`
}

const (
	knowledgeMoveClaimStageActive    = "active"
	knowledgeMoveClaimStageCompleted = "completed"
	knowledgeMoveClaimStageFailed    = "failed"
)

func decodeKnowledgeMoveClaim(metadata types.JSON) (*knowledgeMoveClaim, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &values); err != nil {
		return nil, fmt.Errorf("decode knowledge metadata: %w", err)
	}
	raw, ok := values[knowledgeMoveClaimMetadataKey]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var claim knowledgeMoveClaim
	if err := json.Unmarshal(raw, &claim); err != nil {
		return nil, fmt.Errorf("decode knowledge move claim: %w", err)
	}
	return &claim, nil
}

func encodeKnowledgeMoveClaim(metadata types.JSON, claim *knowledgeMoveClaim) (types.JSON, error) {
	values := make(map[string]json.RawMessage)
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &values); err != nil {
			return nil, fmt.Errorf("decode knowledge metadata: %w", err)
		}
	}
	if values == nil {
		values = make(map[string]json.RawMessage)
	}
	if claim == nil {
		delete(values, knowledgeMoveClaimMetadataKey)
	} else {
		raw, err := json.Marshal(claim)
		if err != nil {
			return nil, fmt.Errorf("encode knowledge move claim: %w", err)
		}
		values[knowledgeMoveClaimMetadataKey] = raw
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode knowledge metadata: %w", err)
	}
	return types.JSON(encoded), nil
}

// likeEscapeChar is the SQL ESCAPE character paired with escapeLikeKeyword.
const likeEscapeChar = `\`

// escapeLikeKeyword escapes SQL LIKE wildcards (%, _) in a keyword
// so they are treated as literal characters.
func escapeLikeKeyword(keyword string) string {
	keyword = strings.ReplaceAll(keyword, `\`, `\\`)
	keyword = strings.ReplaceAll(keyword, "%", `\%`)
	keyword = strings.ReplaceAll(keyword, "_", `\_`)
	return keyword
}

// omitFieldsOnUpdate defines fields to omit when updating knowledge.
//
// PendingSubtasksCount is deliberately omitted from every full-row Save:
// it is an orchestration counter owned exclusively by the atomic helpers
// SetFinalizing (seed), FinalizeSubtask (decrement+promote) and the
// explicit UpdateKnowledgeColumns resets (cancel/reparse). A generic
// UpdateKnowledge call persists the WHOLE in-memory struct, so any
// concurrent enrichment subtask that loaded the row, did slow work
// (e.g. an LLM call), then saved an unrelated field would otherwise
// write back the STALE counter it read at load time — clobbering the
// decrements other subtasks performed in the meantime. That made the
// counter jump back up and never reach zero (the "stuck
// pending_subtasks_count / never promoted to completed" bug). Omitting
// the column here means Save can never touch it.
var omitFieldsOnUpdate = []string{"DeletedAt", "PendingSubtasksCount"}

// knowledgeRepository implements knowledge base and knowledge repository interface
type knowledgeRepository struct {
	db *gorm.DB
}

// NewKnowledgeRepository creates a new knowledge repository
func NewKnowledgeRepository(db *gorm.DB) interfaces.KnowledgeRepository {
	return &knowledgeRepository{db: db}
}

// CreateKnowledge creates knowledge
func (r *knowledgeRepository) CreateKnowledge(ctx context.Context, knowledge *types.Knowledge) error {
	err := r.db.WithContext(ctx).Create(knowledge).Error
	return err
}

// GetKnowledgeByID gets knowledge
func (r *knowledgeRepository) GetKnowledgeByID(
	ctx context.Context,
	tenantID uint64,
	id string,
) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&knowledge).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKnowledgeNotFound
		}
		return nil, err
	}
	return &knowledge, nil
}

// GetKnowledgeByIDOnly returns knowledge by ID without tenant filter (for permission resolution).
func (r *knowledgeRepository) GetKnowledgeByIDOnly(ctx context.Context, id string) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&knowledge).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKnowledgeNotFound
		}
		return nil, err
	}
	return &knowledge, nil
}

// ListKnowledgeByKnowledgeBaseID lists all knowledge in a knowledge base
func (r *knowledgeRepository) ListKnowledgeByKnowledgeBaseID(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.Knowledge, error) {
	var knowledges []*types.Knowledge
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("created_at DESC").Find(&knowledges).Error; err != nil {
		return nil, err
	}
	return knowledges, nil
}

// applyKnowledgeListFilter applies the optional filter dimensions of
// KnowledgeListFilter to a GORM query. Tenant / knowledge base scoping must be
// applied by the caller before invoking this helper.
func applyKnowledgeListFilter(query *gorm.DB, filter types.KnowledgeListFilter) *gorm.DB {
	if len(filter.TagIDs) > 0 {
		query = query.Where(
			"knowledges.id IN (SELECT knowledge_id FROM knowledge_tag_relations WHERE tag_id IN (?))",
			filter.TagIDs,
		)
	}
	if filter.Keyword != "" {
		// Case-insensitive (LOWER … LIKE LOWER) so keyword search matches
		// regardless of the stored casing — consistent with the sibling
		// LOWER() filters in this file and with the client-side `search kb`
		// / `search sessions` filters. Plain LIKE is case-sensitive in
		// Postgres, which surprised callers searching with lowercase.
		escaped := strings.ToLower(escapeLikeKeyword(filter.Keyword))
		query = query.Where("(LOWER(file_name) LIKE ? OR LOWER(title) LIKE ?)", "%"+escaped+"%", "%"+escaped+"%")
	}
	// FileType and Source share the same special-case routing onto `type` for
	// the "manual" / "url" values, so callers can pick either control.
	applyTypeOrFileType := func(q *gorm.DB, val string) *gorm.DB {
		switch val {
		case "":
			return q
		case "manual", "url":
			return q.Where("type = ?", val)
		default:
			return q.Where("file_type = ?", val)
		}
	}
	query = applyTypeOrFileType(query, filter.FileType)
	if filter.Source != "" {
		switch filter.Source {
		case "manual", "url":
			query = query.Where("type = ?", filter.Source)
		default:
			query = query.Where("channel = ?", filter.Source)
		}
	}
	if filter.ParseStatus != "" {
		query = query.Where("parse_status = ?", filter.ParseStatus)
	} else {
		// Hide rows that are mid-deletion so an async delete never lingers in the
		// document list as if it were a normal entry (issue #2192). The delete
		// pipeline marks the row `deleting` before tearing down its resources; a
		// row whose delete task exhausts its retries is flipped to `failed` by the
		// dead-letter callback and stays visible so the failure remains actionable.
		query = query.Where("parse_status <> ?", types.ParseStatusDeleting)
	}
	if !filter.UpdatedFrom.IsZero() {
		query = query.Where("updated_at >= ?", filter.UpdatedFrom)
	}
	if !filter.UpdatedTo.IsZero() {
		query = query.Where("updated_at <= ?", filter.UpdatedTo)
	}
	switch filter.FolderScope {
	case types.FolderScopeExact:
		query = query.Where("folder_path = ?", filter.FolderPath)
	case types.FolderScopeSubtree:
		// An empty path means "the whole knowledge base", so no predicate is
		// needed; otherwise match the folder itself plus everything below it.
		if filter.FolderPath != "" {
			query = query.Where(
				"(folder_path = ? OR folder_path LIKE ? ESCAPE ?)",
				filter.FolderPath,
				escapeLikeKeyword(filter.FolderPath)+"/%",
				likeEscapeChar,
			)
		}
	}
	return query
}

// ListPagedKnowledgeByKnowledgeBaseID lists all knowledge in a knowledge base with pagination
func (r *knowledgeRepository) ListPagedKnowledgeByKnowledgeBaseID(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	page *types.Pagination,
	filter types.KnowledgeListFilter,
) ([]*types.Knowledge, int64, error) {
	var knowledges []*types.Knowledge
	var total int64

	scope := func(q *gorm.DB) *gorm.DB {
		return applyKnowledgeListFilter(
			q.Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID),
			filter,
		)
	}

	if err := scope(r.db.WithContext(ctx).Model(&types.Knowledge{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := scope(r.db.WithContext(ctx)).
		Order("created_at DESC").
		Offset(page.Offset()).
		Limit(page.Limit()).
		Find(&knowledges).Error; err != nil {
		return nil, 0, err
	}

	return knowledges, total, nil
}

// ListKnowledgeFolderCounts aggregates how many knowledge entries live directly
// in each folder of a knowledge base. Rows mid-deletion are excluded so the
// sidebar tree counts match the document list.
func (r *knowledgeRepository) ListKnowledgeFolderCounts(
	ctx context.Context,
	tenantID uint64,
	kbID string,
) ([]*types.KnowledgeFolderCount, error) {
	var counts []*types.KnowledgeFolderCount
	if err := r.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Select("folder_path AS folder_path, COUNT(*) AS count").
		Where("tenant_id = ? AND knowledge_base_id = ? AND parse_status <> ?",
			tenantID, kbID, types.ParseStatusDeleting).
		Group("folder_path").
		Find(&counts).Error; err != nil {
		return nil, err
	}
	return counts, nil
}

func (r *knowledgeRepository) ListKnowledgeFolders(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.KnowledgeFolder, error) {
	var folders []*types.KnowledgeFolder
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", tenantID, kbID).
		Order("path ASC").Find(&folders).Error
	return folders, err
}

func knowledgeFolderAncestors(folderPath string) []string {
	parts := strings.Split(types.NormalizeKnowledgeFolderPath(folderPath), "/")
	paths := make([]string, 0, len(parts))
	for index := range parts {
		path := strings.Join(parts[:index+1], "/")
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func (r *knowledgeRepository) EnsureKnowledgeFolderPath(
	ctx context.Context, tenantID uint64, kbID, folderPath, createdBy string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockKnowledgeUploadFolderMutation(tx, tenantID, kbID); err != nil {
			return err
		}
		return ensureKnowledgeFolderPathTx(tx, tenantID, kbID, folderPath, createdBy)
	})
}

func ensureKnowledgeFolderPathTx(
	tx *gorm.DB, tenantID uint64, kbID, folderPath, createdBy string,
) error {
	paths := knowledgeFolderAncestors(folderPath)
	if len(paths) == 0 {
		return nil
	}
	rows := make([]*types.KnowledgeFolder, 0, len(paths))
	for _, path := range paths {
		rows = append(rows, &types.KnowledgeFolder{
			ID: uuid.NewString(), TenantID: tenantID, KnowledgeBaseID: kbID,
			Path: path, CreatedBy: createdBy,
		})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func (r *knowledgeRepository) DeleteEmptyKnowledgeFolderTree(
	ctx context.Context, tenantID uint64, kbID, folderPath string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockKnowledgeUploadFolderMutation(tx, tenantID, kbID); err != nil {
			return err
		}
		activeUploads, err := countActiveKnowledgeUploadsInFolderTree(tx, tenantID, kbID, folderPath)
		if err != nil {
			return err
		}
		if activeUploads > 0 {
			return types.ErrKnowledgeFolderHasActiveUploads
		}
		var documentCount int64
		if err = tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL AND (folder_path = ? OR folder_path LIKE ? ESCAPE ?)",
				tenantID, kbID, folderPath, escapeLikeKeyword(folderPath)+"/%", likeEscapeChar).
			Count(&documentCount).Error; err != nil {
			return err
		}
		if documentCount > 0 {
			return types.ErrKnowledgeFolderNotEmpty
		}
		return tx.Where("tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL AND (path = ? OR path LIKE ? ESCAPE ?)",
			tenantID, kbID, folderPath, escapeLikeKeyword(folderPath)+"/%", likeEscapeChar).
			Delete(&types.KnowledgeFolder{}).Error
	})
}

func countActiveKnowledgeUploadsInFolderTree(
	tx *gorm.DB, tenantID uint64, kbID, folderPath string,
) (int64, error) {
	var count int64
	err := tx.Model(&types.KnowledgeUploadSession{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND status IN ? AND (folder_path = ? OR folder_path LIKE ? ESCAPE ?)",
			tenantID, kbID, activeUploadStatuses, folderPath, escapeLikeKeyword(folderPath)+"/%", likeEscapeChar).
		Count(&count).Error
	return count, err
}

// UpdateKnowledgeFolderPath files the given knowledge entries under folderPath.
// Only the display/navigation column is touched: chunks, embeddings and the
// stored file are unaffected, which is why re-filing needs no re-processing.
// Returns the number of affected rows.
func (r *knowledgeRepository) UpdateKnowledgeFolderPath(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	ids []string,
	folderPath string,
	createdBy string,
) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var affected int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockKnowledgeUploadFolderMutation(tx, tenantID, kbID); err != nil {
			return err
		}
		if err := ensureKnowledgeFolderPathTx(tx, tenantID, kbID, folderPath, createdBy); err != nil {
			return err
		}
		result := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND id IN (?)", tenantID, kbID, ids).
			Updates(map[string]interface{}{"folder_path": folderPath, "updated_at": time.Now()})
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}

// RenameKnowledgeFolderPath rewrites folder_path for a folder and every folder
// below it, which is how a folder rename or move is applied. Renaming onto an
// existing path merges the two folders. Returns the number of affected rows.
func (r *knowledgeRepository) RenameKnowledgeFolderPath(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	from string,
	to string,
	createdBy string,
) (int64, error) {
	if from == "" {
		return 0, errors.New("source folder path is required")
	}

	var affected int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockKnowledgeUploadFolderMutation(tx, tenantID, kbID); err != nil {
			return err
		}
		activeUploads, err := countActiveKnowledgeUploadsInFolderTree(tx, tenantID, kbID, from)
		if err != nil {
			return err
		}
		if activeUploads > 0 {
			return types.ErrKnowledgeFolderHasActiveUploads
		}
		if err := ensureKnowledgeFolderPathTx(tx, tenantID, kbID, to, createdBy); err != nil {
			return err
		}
		// 在同一事务快照中锁定目录与文档，避免并发上传只移动一半。
		var rows []*types.Knowledge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "folder_path").
			Where("tenant_id = ? AND knowledge_base_id = ? AND (folder_path = ? OR folder_path LIKE ? ESCAPE ?)",
				tenantID, kbID, from, escapeLikeKeyword(from)+"/%", likeEscapeChar).
			Find(&rows).Error; err != nil {
			return err
		}
		byTarget := map[string][]string{}
		for _, row := range rows {
			suffix := strings.TrimPrefix(row.FolderPath, from)
			target := types.NormalizeKnowledgeFolderPath(to + suffix)
			byTarget[target] = append(byTarget[target], row.ID)
		}

		var folders []*types.KnowledgeFolder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL AND (path = ? OR path LIKE ? ESCAPE ?)",
				tenantID, kbID, from, escapeLikeKeyword(from)+"/%", likeEscapeChar).
			Order("length(path) ASC").Find(&folders).Error; err != nil {
			return err
		}
		for _, folder := range folders {
			target := types.NormalizeKnowledgeFolderPath(to + strings.TrimPrefix(folder.Path, from))
			var existing int64
			if err := tx.Model(&types.KnowledgeFolder{}).
				Where("tenant_id = ? AND knowledge_base_id = ? AND path = ? AND deleted_at IS NULL AND id <> ?",
					tenantID, kbID, target, folder.ID).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				if err := tx.Delete(folder).Error; err != nil {
					return err
				}
				affected++
				continue
			}
			if err := tx.Model(folder).Update("path", target).Error; err != nil {
				return err
			}
			affected++
		}
		for target, targetIDs := range byTarget {
			result := tx.Model(&types.Knowledge{}).
				Where("tenant_id = ? AND knowledge_base_id = ? AND id IN (?)", tenantID, kbID, targetIDs).
				Updates(map[string]interface{}{"folder_path": target, "updated_at": time.Now()})
			if result.Error != nil {
				return result.Error
			}
			affected += result.RowsAffected
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// UpdateKnowledge updates knowledge
func (r *knowledgeRepository) UpdateKnowledge(ctx context.Context, knowledge *types.Knowledge) error {
	omit := omitFieldsOnUpdate
	// Legacy/unit-test schemas created before custom_metadata should continue
	// to support unrelated updates when the caller did not provide the field.
	if knowledge.CustomMetadata == nil {
		omit = append(append([]string{}, omitFieldsOnUpdate...), "custom_metadata")
	}
	err := r.db.WithContext(ctx).Omit(omit...).Save(knowledge).Error
	return err
}

// UpdateKnowledgeBatch updates knowledge items in batch
func (r *knowledgeRepository) UpdateKnowledgeBatch(ctx context.Context, knowledgeList []*types.Knowledge) error {
	if len(knowledgeList) == 0 {
		return nil
	}
	return r.db.Debug().WithContext(ctx).Omit(omitFieldsOnUpdate...).Save(knowledgeList).Error
}

// DeleteKnowledge 仅软删除已由原子声明进入 deleting 的知识。
func (r *knowledgeRepository) DeleteKnowledge(ctx context.Context, tenantID uint64, id string) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ? AND parse_status = ?", tenantID, id, types.ParseStatusDeleting).
		Delete(&types.Knowledge{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("knowledge delete claim is missing")
	}
	return nil
}

// DeleteKnowledgeList 仅软删除已由原子声明进入 deleting 的知识集合。
func (r *knowledgeRepository) DeleteKnowledgeList(ctx context.Context, tenantID uint64, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	uniqueIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	result := r.db.WithContext(ctx).
		Where(
			"tenant_id = ? AND id IN ? AND parse_status = ?",
			tenantID,
			uniqueIDs,
			types.ParseStatusDeleting,
		).
		Delete(&types.Knowledge{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(len(uniqueIDs)) {
		return fmt.Errorf(
			"knowledge delete claim set changed: expected=%d deleted=%d",
			len(uniqueIDs),
			result.RowsAffected,
		)
	}
	return nil
}

// DeleteKnowledgeListAndAdjustStorage 在同一事务中软删除知识并扣减实际配额。
// knowledgeBaseID 非空时限定单一知识库并同步清理其文件夹；为空时允许一次
// 跨知识库删除，但绝不清理文件夹。重试只处理仍有效的 deleting 行，因此不重复扣费。
func (r *knowledgeRepository) DeleteKnowledgeListAndAdjustStorage(
	ctx context.Context,
	tenantID uint64,
	knowledgeBaseID string,
	ids []string,
) error {
	uniqueIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 && knowledgeBaseID == "" {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tenant types.Tenant
		var knowledgeList []*types.Knowledge
		if len(uniqueIDs) > 0 {
			tenantQuery := tx.Where("id = ?", tenantID)
			if tx.Dialector.Name() == "postgres" {
				tenantQuery = tenantQuery.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := tenantQuery.First(&tenant).Error; err != nil {
				return err
			}

			query := tx.Where(
				"tenant_id = ? AND id IN ? AND parse_status = ?",
				tenantID,
				uniqueIDs,
				types.ParseStatusDeleting,
			)
			if knowledgeBaseID != "" {
				query = query.Where("knowledge_base_id = ?", knowledgeBaseID)
			}
			if tx.Dialector.Name() == "postgres" {
				query = query.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := query.Find(&knowledgeList).Error; err != nil {
				return err
			}
		}

		storageBytes := int64(0)
		activeIDs := make([]string, 0, len(knowledgeList))
		for _, knowledge := range knowledgeList {
			activeIDs = append(activeIDs, knowledge.ID)
			quotaBytes := knowledge.QuotaStorageBytes()
			if quotaBytes > math.MaxInt64-storageBytes {
				storageBytes = math.MaxInt64
			} else {
				storageBytes += quotaBytes
			}
		}
		if len(activeIDs) > 0 {
			deleteQuery := tx.Where(
				"tenant_id = ? AND id IN ? AND parse_status = ?",
				tenantID,
				activeIDs,
				types.ParseStatusDeleting,
			)
			if knowledgeBaseID != "" {
				deleteQuery = deleteQuery.Where("knowledge_base_id = ?", knowledgeBaseID)
			}
			result := deleteQuery.Delete(&types.Knowledge{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(activeIDs)) {
				return fmt.Errorf(
					"knowledge delete claim set changed: expected=%d deleted=%d",
					len(activeIDs),
					result.RowsAffected,
				)
			}
		}
		if knowledgeBaseID != "" {
			if err := tx.Where(
				"tenant_id = ? AND knowledge_base_id = ?",
				tenantID,
				knowledgeBaseID,
			).Delete(&types.KnowledgeFolder{}).Error; err != nil {
				return err
			}
		}

		if len(activeIDs) == 0 {
			return nil
		}
		if storageBytes >= tenant.StorageUsed {
			tenant.StorageUsed = 0
		} else {
			tenant.StorageUsed -= storageBytes
		}
		return tx.Model(&types.Tenant{}).Where("id = ?", tenantID).
			Update("storage_used", tenant.StorageUsed).Error
	})
}

// ClaimKnowledgeListForKBDelete 锁定并声明一批知识进入删除流程。
// 集合或所属知识库发生变化时整批失败，避免清理已经移动到其他知识库的数据。
func (r *knowledgeRepository) ClaimKnowledgeListForKBDelete(
	ctx context.Context,
	tenantID uint64,
	knowledgeBaseID string,
	ids []string,
) ([]*types.Knowledge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	uniqueIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return nil, nil
	}
	var claimed []*types.Knowledge
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&types.Knowledge{}).
			Where(
				"tenant_id = ? AND knowledge_base_id = ? AND id IN ? AND "+
					"(parse_status IS NULL OR parse_status <> ?)",
				tenantID,
				knowledgeBaseID,
				uniqueIDs,
				types.ParseStatusMoving,
			).
			Updates(map[string]interface{}{
				"parse_status": types.ParseStatusDeleting,
				"updated_at":   time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(uniqueIDs)) {
			return fmt.Errorf(
				"%w: expected=%d claimed=%d",
				ErrKnowledgeMoveInProgress,
				len(uniqueIDs),
				result.RowsAffected,
			)
		}
		return tx.Where(
			"tenant_id = ? AND knowledge_base_id = ? AND id IN ? AND parse_status = ?",
			tenantID,
			knowledgeBaseID,
			uniqueIDs,
			types.ParseStatusDeleting,
		).Find(&claimed).Error
	})
	return claimed, err
}

// ClaimKnowledgeForMove 在短事务中声明知识移动，不跨越任何外部存储调用持锁。
// 同一任务可以幂等恢复；其他移动任务以及已经开始的删除都会被拒绝。
func (r *knowledgeRepository) ClaimKnowledgeForMove(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	sourceKnowledgeBaseID string,
	targetKnowledgeBaseID string,
	taskID string,
	mode string,
) (*types.Knowledge, bool, bool, error) {
	if tenantID == 0 || knowledgeID == "" || sourceKnowledgeBaseID == "" ||
		targetKnowledgeBaseID == "" || taskID == "" || mode == "" {
		return nil, false, false, errors.New("knowledge move claim is incomplete")
	}
	var claimed *types.Knowledge
	alreadyCompleted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.Knowledge
		query := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		existingClaim, err := decodeKnowledgeMoveClaim(current.Metadata)
		if err != nil {
			return err
		}
		if existingClaim != nil && existingClaim.TaskID == taskID &&
			existingClaim.SourceKnowledgeBaseID == sourceKnowledgeBaseID &&
			existingClaim.TargetKnowledgeBaseID == targetKnowledgeBaseID &&
			existingClaim.Mode == mode {
			switch existingClaim.Stage {
			case knowledgeMoveClaimStageActive:
				if current.ParseStatus == types.ParseStatusMoving &&
					(current.KnowledgeBaseID == sourceKnowledgeBaseID ||
						current.KnowledgeBaseID == targetKnowledgeBaseID) {
					clone := current
					claimed = &clone
				}
			case knowledgeMoveClaimStageCompleted:
				if current.KnowledgeBaseID == targetKnowledgeBaseID {
					clone := current
					claimed = &clone
					alreadyCompleted = true
				}
			}
			return nil
		}
		if current.KnowledgeBaseID != sourceKnowledgeBaseID ||
			current.ParseStatus != types.ParseStatusCompleted {
			return nil
		}
		claim := &knowledgeMoveClaim{
			TaskID:                taskID,
			SourceKnowledgeBaseID: sourceKnowledgeBaseID,
			TargetKnowledgeBaseID: targetKnowledgeBaseID,
			Mode:                  mode,
			Stage:                 knowledgeMoveClaimStageActive,
		}
		metadata, err := encodeKnowledgeMoveClaim(current.Metadata, claim)
		if err != nil {
			return err
		}
		updates := map[string]interface{}{
			"parse_status": types.ParseStatusMoving,
			"metadata":     metadata,
			"updated_at":   time.Now(),
		}
		if current.SummaryStatus == types.SummaryStatusProcessing {
			updates["summary_status"] = types.SummaryStatusFailed
		}
		result := tx.Model(&types.Knowledge{}).
			Where(
				"tenant_id = ? AND id = ? AND knowledge_base_id = ? AND parse_status = ?",
				tenantID,
				knowledgeID,
				sourceKnowledgeBaseID,
				types.ParseStatusCompleted,
			).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		current.ParseStatus = types.ParseStatusMoving
		if current.SummaryStatus == types.SummaryStatusProcessing {
			current.SummaryStatus = types.SummaryStatusFailed
		}
		current.Metadata = metadata
		current.UpdatedAt = time.Now()
		claimed = &current
		return nil
	})
	if err != nil {
		return nil, false, false, err
	}
	return claimed, alreadyCompleted, claimed != nil, nil
}

func (r *knowledgeRepository) saveClaimedKnowledgeMove(
	ctx context.Context,
	knowledge *types.Knowledge,
	taskID string,
	stage string,
) (bool, error) {
	if knowledge == nil || knowledge.ID == "" || taskID == "" {
		return false, errors.New("knowledge move update is incomplete")
	}
	updated := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.Knowledge
		query := tx.Where("tenant_id = ? AND id = ?", knowledge.TenantID, knowledge.ID)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		claim, err := decodeKnowledgeMoveClaim(current.Metadata)
		if err != nil {
			return err
		}
		if claim == nil || claim.TaskID != taskID ||
			claim.Stage != knowledgeMoveClaimStageActive ||
			current.ParseStatus != types.ParseStatusMoving ||
			knowledge.KnowledgeBaseID != claim.TargetKnowledgeBaseID {
			return nil
		}
		claim.Stage = stage
		metadata, err := encodeKnowledgeMoveClaim(current.Metadata, claim)
		if err != nil {
			return err
		}
		knowledge.Metadata = metadata
		if stage == knowledgeMoveClaimStageActive {
			knowledge.ParseStatus = types.ParseStatusMoving
		} else if knowledge.ParseStatus == types.ParseStatusMoving {
			return errors.New("completed knowledge move requires a terminal parse status")
		}
		result := tx.Model(&types.Knowledge{}).
			Where(
				"tenant_id = ? AND id = ? AND parse_status = ?",
				knowledge.TenantID,
				knowledge.ID,
				types.ParseStatusMoving,
			).
			Updates(map[string]interface{}{
				"knowledge_base_id":  knowledge.KnowledgeBaseID,
				"embedding_model_id": knowledge.EmbeddingModelID,
				"parse_status":       knowledge.ParseStatus,
				"enable_status":      knowledge.EnableStatus,
				"description":        knowledge.Description,
				"storage_size":       max(knowledge.StorageSize, 0),
				"processed_at":       knowledge.ProcessedAt,
				"metadata":           metadata,
				"updated_at":         knowledge.UpdatedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		updated = true
		return nil
	})
	return updated, err
}

// StageClaimedKnowledgeMove 持久化移动中间态，并继续保留任务所有权。
func (r *knowledgeRepository) StageClaimedKnowledgeMove(
	ctx context.Context,
	knowledge *types.Knowledge,
	taskID string,
) (bool, error) {
	return r.saveClaimedKnowledgeMove(
		ctx,
		knowledge,
		taskID,
		knowledgeMoveClaimStageActive,
	)
}

// CompleteClaimedKnowledgeMove 完成移动并保留已完成凭据，供同一队列任务幂等重放。
func (r *knowledgeRepository) CompleteClaimedKnowledgeMove(
	ctx context.Context,
	knowledge *types.Knowledge,
	taskID string,
) (bool, error) {
	return r.saveClaimedKnowledgeMove(
		ctx,
		knowledge,
		taskID,
		knowledgeMoveClaimStageCompleted,
	)
}

// FailClaimedKnowledgeMove 只释放指定任务拥有的活动声明，竞争任务不能误清理。
func (r *knowledgeRepository) FailClaimedKnowledgeMove(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	taskID string,
	errorMessage string,
) (bool, error) {
	failed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.Knowledge
		query := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		claim, err := decodeKnowledgeMoveClaim(current.Metadata)
		if err != nil {
			return err
		}
		if claim == nil || claim.TaskID != taskID ||
			claim.Stage != knowledgeMoveClaimStageActive ||
			current.ParseStatus != types.ParseStatusMoving {
			return nil
		}
		claim.Stage = knowledgeMoveClaimStageFailed
		metadata, err := encodeKnowledgeMoveClaim(current.Metadata, claim)
		if err != nil {
			return err
		}
		errorMessage = truncateUTF8Bytes(errorMessage, 2000)
		result := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND id = ? AND parse_status = ?", tenantID, knowledgeID, types.ParseStatusMoving).
			Updates(map[string]interface{}{
				"parse_status":  types.ParseStatusFailed,
				"error_message": errorMessage,
				"metadata":      metadata,
				"updated_at":    time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		failed = result.RowsAffected == 1
		return nil
	})
	return failed, err
}

// ClaimKnowledgeMoveRecoveryOps 为丢失的 move 队列任务领取持久恢复意图。
// claimed_at 既是跨实例互斥租约，也是崩溃后重新领取的超时依据。
func (r *knowledgeRepository) ClaimKnowledgeMoveRecoveryOps(
	ctx context.Context,
	staleBefore time.Time,
	limit int,
) ([]*types.TaskPendingOp, error) {
	if limit <= 0 {
		return nil, nil
	}
	var claimed []*types.TaskPendingOp
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where(
			"task_type = ? AND scope = ? AND op = ? AND (claimed_at IS NULL OR claimed_at < ?)",
			types.TypeKnowledgeMove,
			types.TaskScopeKnowledgeBase,
			knowledgeMoveDispatchOp,
			staleBefore,
		).Order("id ASC").Limit(limit)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := query.Find(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}
		ids := make([]int64, 0, len(claimed))
		for _, op := range claimed {
			ids = append(ids, op.ID)
		}
		now := time.Now()
		if err := tx.Model(&types.TaskPendingOp{}).
			Where("id IN ?", ids).
			Update("claimed_at", now).Error; err != nil {
			return err
		}
		for _, op := range claimed {
			op.ClaimedAt = &now
		}
		return nil
	})
	return claimed, err
}

// ReleaseKnowledgeMoveRecoveryOps 释放本轮未能重新投递的恢复租约。
func (r *knowledgeRepository) ReleaseKnowledgeMoveRecoveryOps(
	ctx context.Context,
	ids []int64,
) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.TaskPendingOp{}).
		Where(
			"id IN ? AND task_type = ? AND scope = ? AND op = ?",
			ids,
			types.TypeKnowledgeMove,
			types.TaskScopeKnowledgeBase,
			knowledgeMoveDispatchOp,
		).
		Update("claimed_at", nil).Error
}

// KnowledgeMoveDispatchExists 检查指定移动任务的持久 dispatch 是否仍存在。
// context cancellation 的 worker 只能在 dispatch 已被知识库删除流程清理后
// 释放 move claim；查询错误由服务层按 fail-safe 保留处理。
func (r *knowledgeRepository) KnowledgeMoveDispatchExists(
	ctx context.Context,
	tenantID uint64,
	sourceKnowledgeBaseID string,
	taskID string,
) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.TaskPendingOp{}).
		Where(
			"tenant_id = ? AND task_type = ? AND scope = ? AND scope_id = ? AND op = ? AND dedup_key = ?",
			tenantID,
			types.TypeKnowledgeMove,
			types.TaskScopeKnowledgeBase,
			sourceKnowledgeBaseID,
			knowledgeMoveDispatchOp,
			taskID,
		).
		Count(&count).Error
	return count > 0, err
}

// GetKnowledgeBatch gets knowledge in batch
func (r *knowledgeRepository) GetKnowledgeBatch(
	ctx context.Context, tenantID uint64, ids []string,
) ([]*types.Knowledge, error) {
	var knowledge []*types.Knowledge
	if err := r.db.WithContext(ctx).Debug().
		Where("tenant_id = ? AND id IN ?", tenantID, ids).
		Find(&knowledge).Error; err != nil {
		return nil, err
	}
	return knowledge, nil
}

// CheckKnowledgeExists checks if knowledge already exists
func (r *knowledgeRepository) CheckKnowledgeExists(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	params *types.KnowledgeCheckParams,
) (bool, *types.Knowledge, error) {
	query := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND parse_status <> ?", tenantID, kbID, "failed")

	switch params.Type {
	case "file":
		// File content is only a duplicate within the same file type. This keeps
		// same-content documents with distinct formats (for example, .md and
		// .txt) available as separate knowledge items.
		if params.FileHash != "" {
			var knowledge types.Knowledge
			duplicateQuery := query.Where("type = ? AND file_hash = ?", "file", params.FileHash)
			if params.FileType != "" {
				duplicateQuery = duplicateQuery.Where("LOWER(file_type) = ?", strings.ToLower(params.FileType))
			}
			err := duplicateQuery.First(&knowledge).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return false, nil, nil
				}
				return false, nil, err
			}
			return true, &knowledge, nil
		}

		// If no hash or hash doesn't match, use filename, size, and file type.
		if params.FileName != "" && params.FileSize > 0 {
			var knowledge types.Knowledge
			duplicateQuery := query.Where(
				"type = ? AND file_name = ? AND file_size = ?",
				"file", params.FileName, params.FileSize,
			)
			if params.FileType != "" {
				duplicateQuery = duplicateQuery.Where("LOWER(file_type) = ?", strings.ToLower(params.FileType))
			}
			err := duplicateQuery.First(&knowledge).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return false, nil, nil
				}
				return false, nil, err
			}
			return true, &knowledge, nil
		}
	case "url":
		// If file hash exists, prioritize exact match using hash
		if params.FileHash != "" {
			var knowledge types.Knowledge
			err := query.Where("type = 'url' AND file_hash = ?", params.FileHash).First(&knowledge).Error
			if err == nil && knowledge.ID != "" {
				return true, &knowledge, nil
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil, err
			}
		}

		if params.URL != "" {
			var knowledge types.Knowledge
			err := query.Where("type = 'url' AND source = ?", params.URL).First(&knowledge).Error
			if err == nil && knowledge.ID != "" {
				return true, &knowledge, nil
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil, err
			}
		}
		return false, nil, nil
	}

	// No valid parameters, default to not existing
	return false, nil, nil
}

// AminusB returns the IDs of knowledge in A that have no counterpart in B,
// comparing by file_hash as a MULTISET rather than a plain set.
//
// A plain "file_hash NOT IN (SELECT file_hash FROM B)" only asks whether a
// hash exists in B at all, so once a KB accumulates several rows sharing the
// same file_hash (e.g. the same file ingested multiple times), the diff can
// never reconcile the *count* difference: two KBs with identical distinct-hash
// sets but different row counts produce an empty diff in both directions, and
// a clone target can never converge to the source. This also breaks on MySQL
// when B contains a NULL file_hash, because NOT IN then yields no rows at all.
//
// The multiset diff is computed in Go rather than SQL: we only pull
// (id, file_hash) for A plus per-hash counts for B, then keep A's surplus
// copies. This avoids window functions (unsupported on MySQL 5.7 / MariaDB)
// and the O(n^2) correlated-subquery ranking that would otherwise be needed
// there. Clone is a background job over at most a few thousand rows, so the
// two lightweight two-column reads are cheap.
//
// Rows with a NULL/empty file_hash carry no reliable identity (unparsed /
// passage knowledge), so they are always treated as present-only-in-A to
// avoid collapsing distinct rows into one.
func (r *knowledgeRepository) AminusB(
	ctx context.Context,
	Atenant uint64, A string,
	Btenant uint64, B string,
) ([]string, error) {
	type hashRow struct {
		ID       string
		FileHash string
	}
	// Order so the retained (matched) copies are the earliest ones and the
	// surplus we return is deterministic across runs.
	var aRows []hashRow
	if err := r.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Select("id, file_hash").
		Where("tenant_id = ? AND knowledge_base_id = ?", Atenant, A).
		Order("file_hash, created_at, id").
		Find(&aRows).Error; err != nil {
		return nil, err
	}

	type hashCount struct {
		FileHash string
		Cnt      int
	}
	var bCounts []hashCount
	if err := r.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Select("file_hash, COUNT(*) AS cnt").
		Where("tenant_id = ? AND knowledge_base_id = ?", Btenant, B).
		Group("file_hash").
		Find(&bCounts).Error; err != nil {
		return nil, err
	}

	// remaining[h] is how many copies of hash h in B are still unmatched.
	remaining := make(map[string]int, len(bCounts))
	for _, c := range bCounts {
		if c.FileHash != "" {
			remaining[c.FileHash] = c.Cnt
		}
	}

	knowledgeIDs := make([]string, 0)
	for _, row := range aRows {
		// NULL scans into "" here, so this also covers NULL hashes.
		if row.FileHash == "" {
			knowledgeIDs = append(knowledgeIDs, row.ID)
			continue
		}
		if remaining[row.FileHash] > 0 {
			remaining[row.FileHash]-- // matched by an existing copy in B
			continue
		}
		knowledgeIDs = append(knowledgeIDs, row.ID) // surplus copy in A
	}
	return knowledgeIDs, nil
}

func (r *knowledgeRepository) UpdateKnowledgeColumn(
	ctx context.Context,
	id string,
	column string,
	value interface{},
) error {
	err := r.db.WithContext(ctx).Model(&types.Knowledge{}).Where("id = ?", id).Update(column, value).Error
	return err
}

// UpdateKnowledgeColumns writes multiple columns in a single UPDATE so callers
// that flip related fields together (parse_status + error_message after
// dead-letter, for example) cannot leave the row half-updated when the second
// write fails.
func (r *knowledgeRepository) UpdateKnowledgeColumns(
	ctx context.Context,
	id string,
	values map[string]interface{},
) error {
	if len(values) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.Knowledge{}).Where("id = ?", id).Updates(values).Error
}

// lockKnowledgeForLifecycle 按租户读取并锁定知识行。生命周期状态的判断与写入
// 必须发生在同一事务中，不能依赖服务层早先读取的快照。
func lockKnowledgeForLifecycle(
	tx *gorm.DB,
	tenantID uint64,
	knowledgeID string,
) (*types.Knowledge, bool, error) {
	var current types.Knowledge
	query := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &current, true, nil
}

// updateLockedKnowledgeLifecycleColumns 使用锁内读取到的状态作为 CAS 条件，
// 只更新调用方拥有的列，避免陈旧结构体覆盖 metadata、移动声明或配额标记。
func updateLockedKnowledgeLifecycleColumns(
	tx *gorm.DB,
	current *types.Knowledge,
	values map[string]interface{},
) (bool, error) {
	if current == nil || len(values) == 0 {
		return false, nil
	}
	result := tx.Model(&types.Knowledge{}).
		Where(
			"tenant_id = ? AND id = ? AND parse_status = ?",
			current.TenantID,
			current.ID,
			current.ParseStatus,
		).
		Updates(values)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func knowledgeLifecycleStatusBlocked(status string) bool {
	switch status {
	case types.ParseStatusMoving, types.ParseStatusDeleting, types.ParseStatusCancelled:
		return true
	default:
		return false
	}
}

// StartKnowledgeProcessing 原子地把可执行状态迁移为 processing。
// completed/finalizing 属于其他已完成或收尾中的尝试，不允许旧队列任务重新开启。
func (r *knowledgeRepository) StartKnowledgeProcessing(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	startedAt time.Time,
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			status = types.ParseStatusDeleting
			return nil
		}
		status = current.ParseStatus
		switch status {
		case "", types.ParseStatusPending, types.ParseStatusProcessing,
			types.ParseStatusFailed, types.ManualKnowledgeStatusDraft:
		default:
			return nil
		}
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, map[string]interface{}{
			"parse_status":  types.ParseStatusProcessing,
			"error_message": "",
			"updated_at":    startedAt,
		})
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf("knowledge processing transition lost its lifecycle state")
		}
		applied = true
		status = types.ParseStatusProcessing
		return nil
	})
	return applied, status, err
}

// ClaimKnowledgeReparse 在任何外部清理前声明一次可重建尝试。
// 只有终态知识可进入该流程，moving/deleting 不能被陈旧 API 请求抢占。
func (r *knowledgeRepository) ClaimKnowledgeReparse(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	claimedAt time.Time,
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			status = types.ParseStatusDeleting
			return nil
		}
		status = current.ParseStatus
		switch status {
		case types.ParseStatusCompleted, types.ParseStatusFailed, types.ParseStatusCancelled:
		default:
			return nil
		}
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, map[string]interface{}{
			"parse_status":  types.ParseStatusProcessing,
			"error_message": "",
			"updated_at":    claimedAt,
		})
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf("knowledge reparse claim lost its lifecycle state")
		}
		applied = true
		status = types.ParseStatusProcessing
		return nil
	})
	return applied, status, err
}

// StageKnowledgeReparsePending 只允许当前 reparse 声明持有的 processing 行进入
// pending，并一次写入重建所需列，避免 cleanup 后的全行 Save 覆盖删除或移动。
func (r *knowledgeRepository) StageKnowledgeReparsePending(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	embeddingModelID string,
	metadata types.JSON,
	updatedAt time.Time,
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			status = types.ParseStatusDeleting
			return nil
		}
		status = current.ParseStatus
		if status != types.ParseStatusProcessing {
			return nil
		}
		mergedMetadata := current.Metadata
		if len(metadata) > 0 {
			var mergeErr error
			mergedMetadata, mergeErr = mergeManualKnowledgePatchMetadata(current.Metadata, metadata)
			if mergeErr != nil {
				return mergeErr
			}
		}
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, map[string]interface{}{
			"parse_status":           types.ParseStatusPending,
			"enable_status":          "disabled",
			"description":            "",
			"processed_at":           nil,
			"error_message":          "",
			"embedding_model_id":     embeddingModelID,
			"pending_subtasks_count": 0,
			"metadata":               mergedMetadata,
			"updated_at":             updatedAt,
		})
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf("knowledge reparse staging lost its lifecycle state")
		}
		applied = true
		status = types.ParseStatusPending
		return nil
	})
	return applied, status, err
}

// CancelKnowledgeProcessing 仅允许可取消状态原子迁移为 cancelled。
func (r *knowledgeRepository) CancelKnowledgeProcessing(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	cancelledAt time.Time,
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			status = types.ParseStatusDeleting
			return nil
		}
		status = current.ParseStatus
		if status == types.ParseStatusCancelled {
			return nil
		}
		switch status {
		case types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFinalizing:
		default:
			return nil
		}
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, map[string]interface{}{
			"parse_status":           types.ParseStatusCancelled,
			"error_message":          "用户已取消解析",
			"pending_subtasks_count": 0,
			"updated_at":             cancelledAt,
		})
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf("knowledge cancellation lost its lifecycle state")
		}
		applied = true
		status = types.ParseStatusCancelled
		return nil
	})
	return applied, status, err
}

// PersistFileURLSource 原子写入下载对象事实，只允许当前 processing 尝试拥有该写入。
func (r *knowledgeRepository) PersistFileURLSource(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	filePath string,
	fileName string,
	fileType string,
	fileSize int64,
	fileHash string,
	updatedAt time.Time,
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			status = types.ParseStatusDeleting
			return nil
		}
		status = current.ParseStatus
		if status != types.ParseStatusProcessing {
			return nil
		}
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, map[string]interface{}{
			"file_path":  filePath,
			"file_name":  fileName,
			"file_type":  fileType,
			"file_size":  max(fileSize, 0),
			"file_hash":  fileHash,
			"updated_at": updatedAt,
		})
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf("file URL source persistence lost its lifecycle state")
		}
		applied = true
		return nil
	})
	return applied, status, err
}

// KnowledgeHoldsFilePath 使用包含软删除行的当前事实判断对象是否仍归知识记录持有。
// 补偿删除必须先调用该方法，避免误删并发成功任务写入的稳定路径。
func (r *knowledgeRepository) KnowledgeHoldsFilePath(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	filePath string,
) (bool, error) {
	if filePath == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Unscoped().Model(&types.Knowledge{}).
		Where("tenant_id = ? AND id = ? AND file_path = ?", tenantID, knowledgeID, filePath).
		Count(&count).Error
	return count > 0, err
}

var manualKnowledgePatchMetadataKeys = []string{
	"content", "format", "status", "version", "updated_at", "process_overrides",
}

func mergeManualKnowledgePatchMetadata(current, patch types.JSON) (types.JSON, error) {
	currentValues := make(map[string]interface{})
	if len(current) > 0 {
		if err := json.Unmarshal(current, &currentValues); err != nil {
			return nil, fmt.Errorf("decode current manual metadata: %w", err)
		}
	}
	patchValues := make(map[string]interface{})
	if len(patch) > 0 {
		if err := json.Unmarshal(patch, &patchValues); err != nil {
			return nil, fmt.Errorf("decode manual metadata patch: %w", err)
		}
	}
	for _, key := range manualKnowledgePatchMetadataKeys {
		if value, ok := patchValues[key]; ok {
			currentValues[key] = value
		}
	}
	encoded, err := json.Marshal(currentValues)
	if err != nil {
		return nil, fmt.Errorf("encode merged manual metadata: %w", err)
	}
	return types.JSON(encoded), nil
}

// PatchKnowledgeUserFields 原子写入通用编辑接口允许的用户字段。
func (r *knowledgeRepository) PatchKnowledgeUserFields(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	values map[string]interface{},
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			status = types.ParseStatusDeleting
			return nil
		}
		status = current.ParseStatus
		if knowledgeLifecycleStatusBlocked(status) {
			return nil
		}
		allowed := make(map[string]interface{}, 4)
		for _, key := range []string{"title", "description", "custom_metadata", "updated_at"} {
			if value, ok := values[key]; ok {
				allowed[key] = value
			}
		}
		if len(allowed) == 0 {
			applied = true
			return nil
		}
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, allowed)
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf("knowledge user patch lost its lifecycle state")
		}
		applied = true
		return nil
	})
	return applied, status, err
}

// PatchManualKnowledge 原子写入手工知识可编辑字段，并把手工元数据合并到
// 锁内最新 metadata；移动声明等内部键永远不会被陈旧请求覆盖。
func (r *knowledgeRepository) PatchManualKnowledge(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	values map[string]interface{},
	metadata types.JSON,
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			status = types.ParseStatusDeleting
			return nil
		}
		status = current.ParseStatus
		if knowledgeLifecycleStatusBlocked(status) {
			return nil
		}
		mergedMetadata, mergeErr := mergeManualKnowledgePatchMetadata(current.Metadata, metadata)
		if mergeErr != nil {
			return mergeErr
		}
		allowed := make(map[string]interface{}, 16)
		for _, key := range []string{
			"title", "file_name", "file_type", "type", "source", "enable_status",
			"embedding_model_id", "parse_status", "description", "processed_at",
			"error_message", "pending_subtasks_count", "updated_at",
		} {
			if value, ok := values[key]; ok {
				allowed[key] = value
			}
		}
		allowed["metadata"] = mergedMetadata
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, allowed)
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf("manual knowledge patch lost its lifecycle state")
		}
		applied = true
		return nil
	})
	return applied, status, err
}

// UpdateKnowledgeSummaryIfCurrent 只在知识仍属于同一租户、知识库、解析状态和
// metadata 版本时写入摘要列。摘要刷新跨越 LLM 调用，必须用该版本栅栏丢弃旧结果。
func (r *knowledgeRepository) UpdateKnowledgeSummaryIfCurrent(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	knowledgeBaseID string,
	expectedParseStatus string,
	expectedMetadata types.JSON,
	expectedCustomMetadata types.JSON,
	values map[string]interface{},
) (bool, error) {
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, lockErr := lockKnowledgeForLifecycle(tx, tenantID, knowledgeID)
		if lockErr != nil {
			return lockErr
		}
		if !found || current.KnowledgeBaseID != knowledgeBaseID ||
			current.ParseStatus != expectedParseStatus ||
			string(current.Metadata) != string(expectedMetadata) ||
			string(current.CustomMetadata) != string(expectedCustomMetadata) ||
			knowledgeLifecycleStatusBlocked(current.ParseStatus) {
			return nil
		}
		allowed := make(map[string]interface{}, 4)
		for _, key := range []string{"description", "summary_status", "error_message", "updated_at"} {
			if value, ok := values[key]; ok {
				allowed[key] = value
			}
		}
		if len(allowed) == 0 {
			applied = true
			return nil
		}
		updated, updateErr := updateLockedKnowledgeLifecycleColumns(tx, current, allowed)
		if updateErr != nil {
			return updateErr
		}
		applied = updated
		return nil
	})
	return applied, err
}

// FailKnowledgeProcessing 在行锁内写入解析失败，且绝不覆盖并发移动、删除或取消声明。
// 返回当前持久状态，调用方据此区分应重试的 moving 与应正常停止的删除/取消。
func (r *knowledgeRepository) FailKnowledgeProcessing(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	errorMessage string,
	failedAt time.Time,
) (applied bool, status string, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.Knowledge
		query := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				status = types.ParseStatusDeleting
				return nil
			}
			return err
		}
		status = current.ParseStatus
		switch status {
		case types.ParseStatusMoving, types.ParseStatusDeleting, types.ParseStatusCancelled:
			return nil
		}
		result := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND id = ? AND parse_status = ?", tenantID, knowledgeID, status).
			Updates(map[string]interface{}{
				"parse_status":  types.ParseStatusFailed,
				"error_message": errorMessage,
				"updated_at":    failedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("knowledge processing failure update affected %d rows", result.RowsAffected)
		}
		applied = true
		status = types.ParseStatusFailed
		return nil
	})
	return applied, status, err
}

// lockTenantAndKnowledgeForStorage 按 tenant → knowledge 的固定顺序读取并锁定
// 配额事务涉及的两行。PostgreSQL 使用 FOR UPDATE；SQLite 由单写事务提供
// 等价串行化。知识已被删除时返回 found=false，调用方按预期终止处理。
func lockTenantAndKnowledgeForStorage(
	tx *gorm.DB, tenantID uint64, knowledgeID string,
) (tenant *types.Tenant, knowledge *types.Knowledge, found bool, err error) {
	tenant = &types.Tenant{}
	tenantQuery := tx.Where("id = ?", tenantID)
	if tx.Dialector.Name() == "postgres" {
		tenantQuery = tenantQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := tenantQuery.First(tenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, false, ErrTenantNotFound
		}
		return nil, nil, false, err
	}

	knowledge = &types.Knowledge{}
	knowledgeQuery := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID)
	if tx.Dialector.Name() == "postgres" {
		knowledgeQuery = knowledgeQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := knowledgeQuery.First(knowledge).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tenant, nil, false, nil
		}
		return nil, nil, false, err
	}
	return tenant, knowledge, true, nil
}

// knowledgeStorageMutationAllowed 保证计费事务不会覆盖删除、取消或移动声明。
// moving 必须普通重试；deleting/cancelled/missing 则由调用方正常短路。
func knowledgeStorageMutationAllowed(knowledge *types.Knowledge) (bool, error) {
	if knowledge == nil {
		return false, nil
	}
	switch knowledge.ParseStatus {
	case types.ParseStatusMoving:
		return false, types.ErrKnowledgeMoveInProgress
	case types.ParseStatusDeleting, types.ParseStatusCancelled:
		return false, nil
	default:
		return true, nil
	}
}

// applyKnowledgeStorageDelta 计算事务内的新空间用量并复查配额。
// 正增量禁止溢出或超过 quota；负增量最多把历史漂移值收敛到 0。
func applyKnowledgeStorageDelta(storageUsed, storageQuota, delta int64) (int64, error) {
	storageUsed = max(storageUsed, 0)
	if delta > 0 {
		if delta > math.MaxInt64-storageUsed {
			return 0, fmt.Errorf("tenant storage usage overflow")
		}
		if storageQuota > 0 &&
			(storageUsed >= storageQuota || delta > storageQuota-storageUsed) {
			return 0, types.NewStorageQuotaExceededError()
		}
		return storageUsed + delta, nil
	}
	if delta < 0 {
		if delta == math.MinInt64 || -delta >= storageUsed {
			return 0, nil
		}
		return storageUsed + delta, nil
	}
	return storageUsed, nil
}

// ReserveSourceFileQuota 在同一事务内更新 tenant.storage_used 与
// knowledge.source_file_quota_bytes。重试始终基于数据库当前 marker 计算
// delta；即使上一次 COMMIT 成功但响应丢失，下一次也会得到 delta=0。
func (r *knowledgeRepository) ReserveSourceFileQuota(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	targetBytes int64,
) (applied bool, delta int64, err error) {
	targetBytes = max(targetBytes, 0)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tenant, knowledge, found, lockErr := lockTenantAndKnowledgeForStorage(
			tx, tenantID, knowledgeID,
		)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			return nil
		}
		allowed, guardErr := knowledgeStorageMutationAllowed(knowledge)
		if guardErr != nil {
			return guardErr
		}
		if !allowed {
			return nil
		}

		currentBytes := knowledge.SourceFileQuotaBytes()
		delta = targetBytes - currentBytes
		if delta == 0 {
			applied = true
			return nil
		}
		newStorageUsed, quotaErr := applyKnowledgeStorageDelta(
			tenant.StorageUsed, tenant.StorageQuota, delta,
		)
		if quotaErr != nil {
			return quotaErr
		}
		if err := tx.Model(&types.Tenant{}).
			Where("id = ?", tenant.ID).
			Update("storage_used", newStorageUsed).Error; err != nil {
			return err
		}
		result := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).
			Updates(map[string]interface{}{
				"source_file_quota_bytes": targetBytes,
				"updated_at":              time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("source file quota marker update affected %d rows", result.RowsAffected)
		}
		applied = true
		return nil
	})
	return applied, delta, err
}

// ResetIndexedKnowledgeStorage 在同一事务中把索引占用归零并扣减租户用量。
// moveTaskID 为空时只允许普通重建状态；非空时必须仍由对应 active move claim
// 持有 moving 状态。重试始终以数据库 storage_size 计算差额，因此只扣一次。
func (r *knowledgeRepository) ResetIndexedKnowledgeStorage(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	moveTaskID string,
) (applied bool, delta int64, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tenant, knowledge, found, lockErr := lockTenantAndKnowledgeForStorage(
			tx, tenantID, knowledgeID,
		)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			return nil
		}

		if moveTaskID != "" {
			claim, claimErr := decodeKnowledgeMoveClaim(knowledge.Metadata)
			if claimErr != nil {
				return claimErr
			}
			if knowledge.ParseStatus != types.ParseStatusMoving || claim == nil ||
				claim.TaskID != moveTaskID || claim.Stage != knowledgeMoveClaimStageActive {
				return types.ErrKnowledgeMoveInProgress
			}
		} else {
			switch knowledge.ParseStatus {
			case types.ParseStatusMoving:
				return types.ErrKnowledgeMoveInProgress
			case types.ParseStatusDeleting, types.ParseStatusCancelled:
				return nil
			}
		}

		currentBytes := max(knowledge.StorageSize, 0)
		delta = -currentBytes
		newStorageUsed, quotaErr := applyKnowledgeStorageDelta(
			tenant.StorageUsed,
			tenant.StorageQuota,
			delta,
		)
		if quotaErr != nil {
			return quotaErr
		}
		if delta != 0 {
			if err := tx.Model(&types.Tenant{}).
				Where("id = ?", tenant.ID).
				Update("storage_used", newStorageUsed).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND id = ? AND parse_status = ?", tenantID, knowledgeID, knowledge.ParseStatus).
			Updates(map[string]interface{}{
				"storage_size": int64(0),
				"updated_at":   time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("indexed storage reset affected %d rows", result.RowsAffected)
		}
		applied = true
		return nil
	})
	return applied, delta, err
}

// FinalizeIndexedKnowledge 在同一事务内结算索引配额并写入解析最终状态。
// 只更新解析所有权字段，不触碰 metadata、move claim 或
// pending_subtasks_count，避免破坏移动状态机和原子子任务计数。
func (r *knowledgeRepository) FinalizeIndexedKnowledge(
	ctx context.Context,
	final *types.Knowledge,
) (applied bool, delta int64, err error) {
	if final == nil || final.ID == "" || final.TenantID == 0 {
		return false, 0, errors.New("indexed knowledge final state is incomplete")
	}
	targetStorageSize := max(final.StorageSize, 0)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tenant, current, found, lockErr := lockTenantAndKnowledgeForStorage(
			tx, final.TenantID, final.ID,
		)
		if lockErr != nil {
			return lockErr
		}
		if !found {
			return nil
		}
		allowed, guardErr := knowledgeStorageMutationAllowed(current)
		if guardErr != nil {
			return guardErr
		}
		if !allowed {
			return nil
		}
		if current.KnowledgeBaseID != final.KnowledgeBaseID {
			return types.ErrKnowledgeMoveInProgress
		}

		delta = targetStorageSize - max(current.StorageSize, 0)
		newStorageUsed, quotaErr := applyKnowledgeStorageDelta(
			tenant.StorageUsed, tenant.StorageQuota, delta,
		)
		if quotaErr != nil {
			return quotaErr
		}
		if delta != 0 {
			if err := tx.Model(&types.Tenant{}).
				Where("id = ?", tenant.ID).
				Update("storage_used", newStorageUsed).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND id = ?", final.TenantID, final.ID).
			Updates(map[string]interface{}{
				"parse_status":   final.ParseStatus,
				"summary_status": final.SummaryStatus,
				"enable_status":  final.EnableStatus,
				"storage_size":   targetStorageSize,
				"processed_at":   final.ProcessedAt,
				"updated_at":     final.UpdatedAt,
				"error_message":  final.ErrorMessage,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("indexed knowledge finalization affected %d rows", result.RowsAffected)
		}
		applied = true
		return nil
	})
	return applied, delta, err
}

// UpdateActiveDeletingKnowledgeColumns only touches rows that are still visible
// to normal queries and have not moved out of the transient deleting state.
func (r *knowledgeRepository) UpdateActiveDeletingKnowledgeColumns(
	ctx context.Context,
	id string,
	values map[string]interface{},
) (bool, error) {
	if len(values) == 0 {
		return false, nil
	}
	result := r.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Where("id = ? AND parse_status = ?", id, types.ParseStatusDeleting).
		Updates(values)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// FinalizeSubtask atomically decrements pending_subtasks_count and, when
// the counter reaches zero while parse_status is still 'finalizing',
// flips the row to 'completed' in the same statement so concurrent
// subtask completions can't race the promotion. Both this promotion and
// SetFinalizing clear error_message: a row that re-enters processing or
// finishes successfully must not keep displaying a failure from a
// previous attempt.
//
// Returns (newCount, promoted, error). promoted is true iff this caller
// was the one whose UPDATE flipped 'finalizing'→'completed'.
//
// The implementation is two statements (atomic decrement, then a guarded
// promote UPDATE) because GORM does not expose a portable RETURNING
// across PostgreSQL and SQLite. The promote UPDATE's WHERE clause
// (parse_status='finalizing' AND pending_subtasks_count=0) makes it
// safe to run from any number of concurrent callers — at most one wins.
func (r *knowledgeRepository) FinalizeSubtask(
	ctx context.Context, id string,
) (int, bool, error) {
	now := time.Now()
	// 1) Atomic decrement, clamped at zero. The `pending_subtasks_count > 0`
	//    guard is purely a safety net for accounting bugs — under normal
	//    operation each subtask handler decrements at most once per task,
	//    so the counter cannot go negative.
	res := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("id = ? AND pending_subtasks_count > 0", id).
		Updates(map[string]interface{}{
			"pending_subtasks_count": gorm.Expr("pending_subtasks_count - 1"),
			"updated_at":             now,
		})
	if res.Error != nil {
		return 0, false, res.Error
	}

	// 2) Guarded promote. EVERY caller unconditionally attempts this after
	//    decrementing — we must NOT gate it on a separate SELECT of the
	//    counter. That read can be served by a lagging read-replica (or a
	//    stale connection snapshot) and return a non-zero value even after
	//    the counter has truly reached zero on the primary; if every caller
	//    trusts that stale read, NONE of them runs the promote and the row
	//    is stranded in `finalizing` forever (the observed "stuck
	//    pending_subtasks_count" bug). The promote is a WRITE, so it executes
	//    on the primary and its `pending_subtasks_count = 0` WHERE clause is
	//    the single authoritative, atomic check on the live row: only the
	//    caller whose decrement actually brought the counter to zero matches,
	//    and cancel/delete cannot be clobbered by a late promote.
	promoteRes := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("id = ? AND parse_status = ? AND pending_subtasks_count = 0",
			id, types.ParseStatusFinalizing).
		Updates(map[string]interface{}{
			"parse_status":  types.ParseStatusCompleted,
			"error_message": "",
			"processed_at":  now,
			"updated_at":    now,
		})
	if promoteRes.Error != nil {
		return 0, false, promoteRes.Error
	}
	promoted := promoteRes.RowsAffected > 0

	// 3) Best-effort re-read of the new count for diagnostics/return value
	//    only. This read may be replica-stale and is intentionally NOT used
	//    to decide whether to promote (see above). A read failure here does
	//    not affect correctness, so we don't propagate it as an error.
	var snap struct {
		PendingSubtasksCount int `gorm:"column:pending_subtasks_count"`
	}
	if err := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Select("pending_subtasks_count").
		Where("id = ?", id).Take(&snap).Error; err != nil {
		return 0, promoted, nil
	}
	return snap.PendingSubtasksCount, promoted, nil
}

// SetFinalizing atomically transitions a row from 'processing' to
// 'finalizing' and seeds pending_subtasks_count. Used by
// KnowledgePostProcess.Handle as the single durable handoff between
// the synchronous parse stage and the asynchronous enrichment fan-out.
//
// The transition is conditional on parse_status='processing' so a row
// that the user cancelled / deleted between ProcessDocument finishing
// and post-process starting will NOT get hijacked into finalizing.
// Returns whether the transition happened.
func (r *knowledgeRepository) SetFinalizing(
	ctx context.Context, id string, expectedSubtasks int,
) (bool, error) {
	if expectedSubtasks < 0 {
		expectedSubtasks = 0
	}
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("id = ? AND parse_status = ?", id, types.ParseStatusProcessing).
		Updates(map[string]interface{}{
			"parse_status":           types.ParseStatusFinalizing,
			"pending_subtasks_count": expectedSubtasks,
			"error_message":          "",
			"updated_at":             now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// CountKnowledgeByKnowledgeBaseID counts the number of knowledge items in a knowledge base
func (r *knowledgeRepository) CountKnowledgeByKnowledgeBaseID(
	ctx context.Context,
	tenantID uint64,
	kbID string,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Count(&count).Error
	return count, err
}

// CountKnowledgeByStatus counts the number of knowledge items with the specified parse status
func (r *knowledgeRepository) CountKnowledgeByStatus(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	parseStatuses []string,
) (int64, error) {
	if len(parseStatuses) == 0 {
		return 0, nil
	}

	var count int64
	query := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Where("parse_status IN ?", parseStatuses)

	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}

	return count, nil
}

// SearchKnowledge searches knowledge items by keyword across the tenant
// If keyword is empty, returns recent files
// Only returns documents from document-type knowledge bases (excludes FAQ)
// Returns (results, hasMore, error)
// FindByMetadataKey finds a knowledge item by a key-value pair in the metadata JSON column.
// Uses Postgres jsonb operator: metadata->>'key' = 'value'.
func (r *knowledgeRepository) FindByMetadataKey(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	key string,
	value string,
) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", tenantID, kbID).
		Where("metadata->>? = ?", key, value).
		First(&knowledge).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &knowledge, nil
}

// FindByMetadataKeyPrefix finds knowledge items whose metadata[key] starts with
// the given prefix. Used to sweep an external node's attachment sub-items on re-sync.
func (r *knowledgeRepository) FindByMetadataKeyPrefix(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	key string,
	prefix string,
) ([]*types.Knowledge, error) {
	escaped := escapeLikeKeyword(prefix)
	var items []*types.Knowledge
	// The JSON key is embedded as a SQL literal (metadata->>'external_id'), NOT a
	// bind parameter. PostgreSQL only uses the expression index
	// idx_knowledges_kb_metadata_external_id (built on the literal expression
	// (metadata->>'external_id')) when that exact expression appears in the query;
	// a bound metadata->>$1 is a structurally different expression the planner
	// cannot match, so it would silently fall back to a heap scan. key is an
	// internal, caller-supplied field name (always "external_id"); single-quotes
	// are doubled defensively so the literal is always well-formed.
	//
	// The prefix pattern stays a bind parameter: an unnamed prepared statement is
	// custom-planned with the actual value, so LIKE 'prefix%' still extracts the
	// prefix and drives the index. The explicit ESCAPE '\' keeps backslash-escaped
	// wildcards (e.g. \_) literal on both PostgreSQL and SQLite.
	keyExpr := "metadata->>'" + strings.ReplaceAll(key, "'", "''") + "'"
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", tenantID, kbID).
		Where(keyExpr+" LIKE ? ESCAPE ?", escaped+"%", `\`).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// FindByDataSourceExternalID locates a synced knowledge item without allowing
// identical external IDs from two data sources to collide in one knowledge base.
func (r *knowledgeRepository) FindByDataSourceExternalID(
	ctx context.Context,
	tenantID uint64,
	kbID, dataSourceID, externalID string,
) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", tenantID, kbID).
		Where("metadata->>'datasource_id' = ? AND metadata->>'external_id' = ?", dataSourceID, externalID).
		First(&knowledge).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &knowledge, nil
}

// HardDeleteKnowledge physically removes a knowledge row. Call it AFTER
// DeleteKnowledge's soft-delete cascade so sync-internal deletions never
// become tombstones that block a later re-sync of the same external item.
func (r *knowledgeRepository) HardDeleteKnowledge(ctx context.Context, tenantID uint64, id string) error {
	return r.db.Unscoped().WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, id).
		Delete(&types.Knowledge{}).Error
}

// HardDeleteKnowledgeList is the batch counterpart of HardDeleteKnowledge.
func (r *knowledgeRepository) HardDeleteKnowledgeList(ctx context.Context, tenantID uint64, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Unscoped().WithContext(ctx).
		Where("tenant_id = ? AND id IN ?", tenantID, ids).
		Delete(&types.Knowledge{}).Error
}

func (r *knowledgeRepository) SearchKnowledge(
	ctx context.Context,
	tenantID uint64,
	keyword string,
	offset, limit int,
	fileTypes []string,
) ([]*types.Knowledge, bool, error) {
	// Use raw query to properly map knowledge_base_name
	type KnowledgeWithKBName struct {
		types.Knowledge
		KnowledgeBaseName string `gorm:"column:knowledge_base_name"`
	}

	var results []KnowledgeWithKBName
	query := r.db.WithContext(ctx).
		Table("knowledges").
		Select("knowledges.*, knowledge_bases.name as knowledge_base_name").
		Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledges.knowledge_base_id").
		Where("knowledges.tenant_id = ?", tenantID).
		Where("knowledge_bases.type = ?", types.KnowledgeBaseTypeDocument).
		Where("knowledges.deleted_at IS NULL")

	// If keyword is provided, filter by file_name or title (case-insensitive).
	if keyword != "" {
		escaped := strings.ToLower(escapeLikeKeyword(keyword))
		query = query.Where("(LOWER(knowledges.file_name) LIKE ? OR LOWER(knowledges.title) LIKE ?)", "%"+escaped+"%", "%"+escaped+"%")
	}

	// If fileTypes is provided, filter by file extension or type
	if len(fileTypes) > 0 {
		seen := make(map[string]bool)
		var uniquePatterns []string
		includeURL := false
		for _, ft := range fileTypes {
			ft = strings.ToLower(strings.TrimPrefix(ft, "."))
			if ft == "url" || ft == "html" {
				includeURL = true
				continue
			}
			pattern := "%." + ft
			if !seen[pattern] {
				seen[pattern] = true
				uniquePatterns = append(uniquePatterns, pattern)
			}
			// Handle common aliases
			var aliases []string
			switch ft {
			case "xlsx":
				aliases = []string{"%.xls"}
			case "xls":
				aliases = []string{"%.xlsx"}
			case "docx":
				aliases = []string{"%.doc"}
			case "doc":
				aliases = []string{"%.docx"}
			case "jpg":
				aliases = []string{"%.jpeg", "%.png"}
			case "jpeg":
				aliases = []string{"%.jpg", "%.png"}
			case "png":
				aliases = []string{"%.jpg", "%.jpeg"}
			}
			for _, alias := range aliases {
				if !seen[alias] {
					seen[alias] = true
					uniquePatterns = append(uniquePatterns, alias)
				}
			}
		}
		var orConditions []string
		var args []interface{}
		for _, p := range uniquePatterns {
			orConditions = append(orConditions, "LOWER(knowledges.file_name) LIKE ?")
			args = append(args, p)
		}
		if includeURL {
			orConditions = append(orConditions, "knowledges.type = ?")
			args = append(args, "url")
		}
		if len(orConditions) > 0 {
			query = query.Where("("+strings.Join(orConditions, " OR ")+")", args...)
		}
	}

	// Fetch limit+1 to check if there are more results
	err := query.Order("knowledges.created_at DESC").
		Offset(offset).
		Limit(limit + 1).
		Scan(&results).Error
	if err != nil {
		return nil, false, err
	}

	// Check if there are more results
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}

	// Convert to []*types.Knowledge
	knowledges := make([]*types.Knowledge, len(results))
	for i, r := range results {
		k := r.Knowledge
		k.KnowledgeBaseName = r.KnowledgeBaseName
		knowledges[i] = &k
	}
	return knowledges, hasMore, nil
}

// SearchKnowledgeInScopes searches knowledge items by keyword within the given (tenant_id, kb_id) scopes (e.g. own + shared KBs).
func (r *knowledgeRepository) SearchKnowledgeInScopes(
	ctx context.Context,
	scopes []types.KnowledgeSearchScope,
	keyword string,
	offset, limit int,
	fileTypes []string,
) ([]*types.Knowledge, bool, int64, error) {
	if len(scopes) == 0 {
		return nil, false, 0, nil
	}

	type KnowledgeWithKBName struct {
		types.Knowledge
		KnowledgeBaseName string `gorm:"column:knowledge_base_name"`
	}

	placeholders := make([]string, len(scopes))
	args := make([]interface{}, 0, len(scopes)*2)
	for i, s := range scopes {
		placeholders[i] = "(?,?)"
		args = append(args, s.TenantID, s.KBID)
	}
	scopeCondition := "(knowledges.tenant_id, knowledges.knowledge_base_id) IN (" + strings.Join(placeholders, ",") + ")"

	query := r.db.WithContext(ctx).
		Table("knowledges").
		Select("knowledges.*, knowledge_bases.name as knowledge_base_name").
		Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledges.knowledge_base_id AND knowledge_bases.tenant_id = knowledges.tenant_id").
		Where(scopeCondition, args...).
		Where("knowledge_bases.type = ?", types.KnowledgeBaseTypeDocument).
		Where("knowledges.deleted_at IS NULL")

	if keyword != "" {
		escaped := strings.ToLower(escapeLikeKeyword(keyword))
		query = query.Where("(LOWER(knowledges.file_name) LIKE ? OR LOWER(knowledges.title) LIKE ?)", "%"+escaped+"%", "%"+escaped+"%")
	}

	if len(fileTypes) > 0 {
		seen := make(map[string]bool)
		var uniquePatterns []string
		includeURL := false
		for _, ft := range fileTypes {
			ft = strings.ToLower(strings.TrimPrefix(ft, "."))
			if ft == "url" || ft == "html" {
				includeURL = true
				continue
			}
			pattern := "%." + ft
			if !seen[pattern] {
				seen[pattern] = true
				uniquePatterns = append(uniquePatterns, pattern)
			}
			var aliases []string
			switch ft {
			case "xlsx":
				aliases = []string{"%.xls"}
			case "xls":
				aliases = []string{"%.xlsx"}
			case "docx":
				aliases = []string{"%.doc"}
			case "doc":
				aliases = []string{"%.docx"}
			case "jpg":
				aliases = []string{"%.jpeg", "%.png"}
			case "jpeg":
				aliases = []string{"%.jpg", "%.png"}
			case "png":
				aliases = []string{"%.jpg", "%.jpeg"}
			}
			for _, alias := range aliases {
				if !seen[alias] {
					seen[alias] = true
					uniquePatterns = append(uniquePatterns, alias)
				}
			}
		}
		var orConditions []string
		var ftArgs []interface{}
		for _, p := range uniquePatterns {
			orConditions = append(orConditions, "LOWER(knowledges.file_name) LIKE ?")
			ftArgs = append(ftArgs, p)
		}
		if includeURL {
			orConditions = append(orConditions, "knowledges.type = ?")
			ftArgs = append(ftArgs, "url")
		}
		if len(orConditions) > 0 {
			query = query.Where("("+strings.Join(orConditions, " OR ")+")", ftArgs...)
		}
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, false, 0, err
	}

	var results []KnowledgeWithKBName
	err := query.Order("knowledges.created_at DESC").
		Offset(offset).
		Limit(limit + 1).
		Scan(&results).Error
	if err != nil {
		return nil, false, 0, err
	}

	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}

	knowledges := make([]*types.Knowledge, len(results))
	for i, r := range results {
		k := r.Knowledge
		k.KnowledgeBaseName = r.KnowledgeBaseName
		knowledges[i] = &k
	}
	return knowledges, hasMore, total, nil
}

// ListIDsByTagIDs returns all knowledge IDs that have any of the specified tag IDs (OR semantics)
func (r *knowledgeRepository) ListIDsByTagIDs(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	tagIDs []string,
) ([]string, error) {
	if len(tagIDs) == 0 {
		return nil, nil
	}
	var ids []string
	err := r.db.WithContext(ctx).Model(&types.Knowledge{}).
		Joins("JOIN knowledge_tag_relations ktr ON knowledges.id = ktr.knowledge_id").
		Where("knowledges.tenant_id = ? AND knowledges.knowledge_base_id = ? AND ktr.tag_id IN (?)",
			tenantID, kbID, tagIDs).
		Distinct("knowledges.id").
		Pluck("knowledges.id", &ids).Error
	return ids, err
}
