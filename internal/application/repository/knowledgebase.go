package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

var ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")

// knowledgeBaseRepository implements the KnowledgeBaseRepository interface
type knowledgeBaseRepository struct {
	db *gorm.DB
}

// NewKnowledgeBaseRepository creates a new knowledge base repository
func NewKnowledgeBaseRepository(db *gorm.DB) interfaces.KnowledgeBaseRepository {
	return &knowledgeBaseRepository{db: db}
}

// CreateKnowledgeBase creates a new knowledge base
func (r *knowledgeBaseRepository) CreateKnowledgeBase(ctx context.Context, kb *types.KnowledgeBase) error {
	if kb == nil || kb.TenantID == 0 {
		return ErrTenantNotFound
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockActiveTenant(tx, kb.TenantID); err != nil {
			return err
		}
		return tx.Create(kb).Error
	})
}

// GetKnowledgeBaseByID gets a knowledge base by id (no tenant scope; caller must enforce isolation where needed)
func (r *knowledgeBaseRepository) GetKnowledgeBaseByID(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	var kb types.KnowledgeBase
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&kb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKnowledgeBaseNotFound
		}
		return nil, err
	}
	return &kb, nil
}

// GetKnowledgeBaseByIDAndTenant gets a knowledge base by id only if it belongs to the given tenant (enforces tenant isolation)
func (r *knowledgeBaseRepository) GetKnowledgeBaseByIDAndTenant(ctx context.Context, id string, tenantID uint64) (*types.KnowledgeBase, error) {
	var kb types.KnowledgeBase
	if err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, tenantID).First(&kb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKnowledgeBaseNotFound
		}
		return nil, err
	}
	return &kb, nil
}

// GetKnowledgeBaseByIDAndTenantUnscoped 读取包含软删除行的知识库快照。
// 仅供删除任务恢复旧 payload 的存储绑定，调用方仍必须携带租户范围。
func (r *knowledgeBaseRepository) GetKnowledgeBaseByIDAndTenantUnscoped(
	ctx context.Context,
	id string,
	tenantID uint64,
) (*types.KnowledgeBase, error) {
	var kb types.KnowledgeBase
	if err := r.db.WithContext(ctx).Unscoped().
		Where("id = ? AND tenant_id = ?", id, tenantID).
		First(&kb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKnowledgeBaseNotFound
		}
		return nil, err
	}
	return &kb, nil
}

// GetKnowledgeBaseByIDs gets knowledge bases by multiple ids
func (r *knowledgeBaseRepository) GetKnowledgeBaseByIDs(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	if len(ids) == 0 {
		return []*types.KnowledgeBase{}, nil
	}
	var kbs []*types.KnowledgeBase
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&kbs).Error; err != nil {
		return nil, err
	}
	return kbs, nil
}

// ListKnowledgeBases lists all knowledge bases
func (r *knowledgeBaseRepository) ListKnowledgeBases(ctx context.Context) ([]*types.KnowledgeBase, error) {
	var kbs []*types.KnowledgeBase
	if err := r.db.WithContext(ctx).Find(&kbs).Error; err != nil {
		return nil, err
	}
	return kbs, nil
}

// ListKnowledgeBasesByTenantID lists all knowledge bases by tenant id.
//
// Ordering used to also include `is_pinned DESC, pinned_at DESC` so the
// repository would return tenant-wide pinned rows first. That column is
// no longer the source of truth (see migration 000050) — pin state is
// now per (user, kb) and applied by the service layer after enrichment.
// We keep `created_at DESC` here so callers that don't enrich (chat
// pipeline, agent editor, IM commands) still get a stable ordering.
func (r *knowledgeBaseRepository) ListKnowledgeBasesByTenantID(
	ctx context.Context, tenantID uint64,
) ([]*types.KnowledgeBase, error) {
	var kbs []*types.KnowledgeBase
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND is_temporary = ?", tenantID, false).
		Order("created_at DESC").Find(&kbs).Error; err != nil {
		return nil, err
	}
	return kbs, nil
}

// userKBPinRow mirrors the user_kb_pins table. Kept local to the
// repository because it never escapes the package; callers see the
// higher-level map[kb_id]pinned_at returned by ListUserKBPinIDs.
type userKBPinRow struct {
	TenantID uint64    `gorm:"column:tenant_id"`
	UserID   string    `gorm:"column:user_id"`
	KBID     string    `gorm:"column:kb_id"`
	PinnedAt time.Time `gorm:"column:pinned_at"`
}

func (userKBPinRow) TableName() string { return "user_kb_pins" }

// SetUserKBPin upserts (pinned=true) or deletes (pinned=false) the row
// for the given (tenant, user, kb) triple. The returned pinned_at is
// nil when pinned=false; otherwise it carries the timestamp written
// to the row (either the existing one if the row already existed, or
// the current time on insert) so the caller can stamp the response
// without a follow-up SELECT.
func (r *knowledgeBaseRepository) SetUserKBPin(
	ctx context.Context, tenantID uint64, userID string, kbID string, pinned bool,
) (*time.Time, error) {
	if userID == "" {
		return nil, errors.New("user_kb_pins: empty user_id")
	}
	if !pinned {
		err := r.db.WithContext(ctx).
			Where("tenant_id = ? AND user_id = ? AND kb_id = ?", tenantID, userID, kbID).
			Delete(&userKBPinRow{}).Error
		if err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Upsert with idempotent INSERT … ON CONFLICT DO NOTHING. We then
	// SELECT to learn whether an existing row's pinned_at survived (so
	// repeated calls return a stable timestamp instead of bumping it).
	row := userKBPinRow{
		TenantID: tenantID,
		UserID:   userID,
		KBID:     kbID,
		PinnedAt: time.Now(),
	}
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND kb_id = ?", tenantID, userID, kbID).
		Attrs(userKBPinRow{PinnedAt: row.PinnedAt}).
		FirstOrCreate(&row).Error; err != nil {
		return nil, err
	}
	pa := row.PinnedAt
	return &pa, nil
}

// ListUserKBPinIDs returns every KB id this user has personally pinned
// in this tenant, mapped to its pinned_at. Returns an empty map (not
// nil) when there are no pins, so callers can do `len(m) == 0` checks
// without a nil guard.
func (r *knowledgeBaseRepository) ListUserKBPinIDs(
	ctx context.Context, tenantID uint64, userID string,
) (map[string]time.Time, error) {
	out := make(map[string]time.Time)
	if userID == "" {
		return out, nil
	}
	var rows []userKBPinRow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.KBID] = row.PinnedAt
	}
	return out, nil
}

// UpdateKnowledgeBase updates a knowledge base
func (r *knowledgeBaseRepository) UpdateKnowledgeBase(ctx context.Context, kb *types.KnowledgeBase) error {
	return r.db.WithContext(ctx).Save(kb).Error
}

// DeleteKnowledgeBase deletes a knowledge base
func (r *knowledgeBaseRepository) DeleteKnowledgeBase(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.KnowledgeBase{}).Error
}

// ListDeletedKnowledgeBasesWithActiveKnowledge 返回仍有清理残留的已删除知识库。
// 除知识和目录外，分享、数据源及持久任务也必须触发恢复，空知识库同样不能漏扫。
func (r *knowledgeBaseRepository) ListDeletedKnowledgeBasesWithActiveKnowledge(
	ctx context.Context,
	limit int,
) ([]*types.KnowledgeBase, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var knowledgeBases []*types.KnowledgeBase
	err := r.db.WithContext(ctx).Unscoped().
		Where("knowledge_bases.deleted_at IS NOT NULL").
		Where(`EXISTS (
			SELECT 1 FROM knowledges
			WHERE knowledges.knowledge_base_id = knowledge_bases.id
			  AND knowledges.deleted_at IS NULL
		) OR EXISTS (
			SELECT 1 FROM knowledge_folders
			WHERE knowledge_folders.knowledge_base_id = knowledge_bases.id
			  AND knowledge_folders.deleted_at IS NULL
		) OR EXISTS (
			SELECT 1 FROM kb_shares
			WHERE kb_shares.knowledge_base_id = knowledge_bases.id
			  AND kb_shares.deleted_at IS NULL
		) OR EXISTS (
			SELECT 1 FROM data_sources
			WHERE data_sources.knowledge_base_id = knowledge_bases.id
			  AND (
				data_sources.deleted_at IS NULL
				OR EXISTS (
					SELECT 1 FROM sync_logs
					WHERE sync_logs.data_source_id = data_sources.id
					  AND sync_logs.status IN ('running', 'pending')
				)
			  )
		) OR EXISTS (
			SELECT 1 FROM task_pending_ops
			WHERE task_pending_ops.scope = 'knowledge_base'
			  AND task_pending_ops.scope_id = knowledge_bases.id
		)`).
		Order("knowledge_bases.deleted_at ASC").
		Limit(limit).
		Find(&knowledgeBases).Error
	return knowledgeBases, err
}

// ListKnowledgeBaseCleanupDataSourceIDs 返回仍需停止任务或同步日志的数据源。
// 查询包含软删除数据源，使恢复任务能够补偿请求路径已经删除记录但未取消日志的情况。
func (r *knowledgeBaseRepository) ListKnowledgeBaseCleanupDataSourceIDs(
	ctx context.Context,
	knowledgeBaseID string,
) ([]string, error) {
	var dataSourceIDs []string
	err := r.db.WithContext(ctx).
		Table("data_sources").
		Distinct("data_sources.id").
		Where("data_sources.knowledge_base_id = ?", knowledgeBaseID).
		Where(`data_sources.deleted_at IS NULL OR EXISTS (
			SELECT 1 FROM sync_logs
			WHERE sync_logs.data_source_id = data_sources.id
			  AND sync_logs.status IN ('running', 'pending')
		)`).
		Order("data_sources.id ASC").
		Pluck("data_sources.id", &dataSourceIDs).Error
	return dataSourceIDs, err
}

// CountByVectorStoreID counts active knowledge bases that are bound to the
// given vector store within a tenant scope.
//
// Soft-delete filter is applied automatically by GORM because KnowledgeBase
// has a gorm.DeletedAt column — we deliberately do not add an explicit
// `deleted_at IS NULL` predicate to keep the single source of truth on the
// auto-scope.
//
// Pass db == nil to use the repository's default db handle; pass a *gorm.DB
// bound to a transaction (e.g., from db.Transaction) to share the same
// write-lock context as the caller. Query column order matches the
// composite index idx_knowledge_bases_tenant_vector_store(tenant_id,
// vector_store_id).
func (r *knowledgeBaseRepository) CountByVectorStoreID(
	ctx context.Context, db *gorm.DB, tenantID uint64, storeID string,
) (int64, error) {
	if db == nil {
		db = r.db
	}
	var count int64
	err := db.WithContext(ctx).
		Model(&types.KnowledgeBase{}).
		Where("tenant_id = ? AND vector_store_id = ?", tenantID, storeID).
		Count(&count).Error
	return count, err
}

// CountByModelID 统计直接引用模型的活动知识库，以及仍有待清理知识的知识库。
// 后者包含已软删除知识库，避免异步向量清理完成前删除其嵌入模型。
func (r *knowledgeBaseRepository) CountByModelID(
	ctx context.Context, tenantID uint64, modelID string,
) (int64, error) {
	var activeKnowledgeBaseIDs []string
	query := r.db.WithContext(ctx).
		Model(&types.KnowledgeBase{}).
		Where("tenant_id = ?", tenantID)
	query = scopeKnowledgeBasesByModelID(query, modelID)
	if err := query.Pluck("id", &activeKnowledgeBaseIDs).Error; err != nil {
		return 0, err
	}

	var knowledgeBoundIDs []string
	if err := r.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Distinct("knowledge_base_id").
		Where("tenant_id = ? AND embedding_model_id = ?", tenantID, modelID).
		Pluck("knowledge_base_id", &knowledgeBoundIDs).Error; err != nil {
		return 0, err
	}

	referenced := make(map[string]struct{}, len(activeKnowledgeBaseIDs)+len(knowledgeBoundIDs))
	for _, knowledgeBaseID := range activeKnowledgeBaseIDs {
		referenced[knowledgeBaseID] = struct{}{}
	}
	for _, knowledgeBaseID := range knowledgeBoundIDs {
		referenced[knowledgeBaseID] = struct{}{}
	}
	return int64(len(referenced)), nil
}
