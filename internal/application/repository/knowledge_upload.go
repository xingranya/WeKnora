package repository

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var activeUploadStatuses = []types.KnowledgeUploadStatus{
	types.KnowledgeUploadCreated,
	types.KnowledgeUploadUploading,
	types.KnowledgeUploadCompleting,
	types.KnowledgeUploadFailed,
}

var expirableUploadStatuses = []types.KnowledgeUploadStatus{
	types.KnowledgeUploadCreated,
	types.KnowledgeUploadUploading,
	types.KnowledgeUploadCompleting,
	types.KnowledgeUploadFailed,
}

var cleanupPendingUploadStatuses = []types.KnowledgeUploadStatus{
	types.KnowledgeUploadCompletedCleanupPending,
	types.KnowledgeUploadCancelledCleanupPending,
	types.KnowledgeUploadExpiredCleanupPending,
}

// lockKnowledgeUploadFolderMutation 串行化同一知识库内的上传会话创建与目录变更。
// SQLite 测试依赖事务自身的单写者语义，生产 PostgreSQL 使用事务级 advisory lock。
func lockKnowledgeUploadFolderMutation(tx *gorm.DB, tenantID uint64, kbID string) error {
	if tx.Dialector.Name() != "postgres" && tx.Dialector.Name() != "mysql" {
		return nil
	}
	var lockedKnowledgeBase types.KnowledgeBase
	return tx.Model(&types.KnowledgeBase{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("id = ? AND tenant_id = ?", kbID, tenantID).
		Take(&lockedKnowledgeBase).Error
}

func (r *knowledgeRepository) CreateKnowledgeUploadSession(ctx context.Context, session *types.KnowledgeUploadSession) error {
	return r.db.WithContext(ctx).Create(session).Error
}

func (r *knowledgeRepository) CreateKnowledgeUploadSessionWithQuota(
	ctx context.Context, session *types.KnowledgeUploadSession, maxUserSessions, maxTenantBytes int64,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" || tx.Dialector.Name() == "mysql" {
			var lockedTenant types.Tenant
			if err := tx.Model(&types.Tenant{}).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id").Where("id = ?", session.TenantID).
				Take(&lockedTenant).Error; err != nil {
				return err
			}
		}
		if err := lockKnowledgeUploadFolderMutation(tx, session.TenantID, session.KnowledgeBaseID); err != nil {
			return err
		}

		var userSessions int64
		if err := tx.Model(&types.KnowledgeUploadSession{}).
			Where("tenant_id = ? AND user_id = ? AND status IN ?", session.TenantID, session.UserID, activeUploadStatuses).
			Count(&userSessions).Error; err != nil {
			return err
		}
		if userSessions >= maxUserSessions {
			return types.ErrUploadUserQuota
		}

		var tenantBytes int64
		if err := tx.Model(&types.KnowledgeUploadSession{}).
			Select("COALESCE(SUM(file_size), 0)").
			Where("tenant_id = ? AND status IN ?", session.TenantID, activeUploadStatuses).
			Scan(&tenantBytes).Error; err != nil {
			return err
		}
		if tenantBytes+session.FileSize > maxTenantBytes {
			return types.ErrUploadTenantQuota
		}
		return tx.Create(session).Error
	})
}

func knowledgeUploadHashLockKey(tenantID uint64, kbID, fileType, fileHash string) int64 {
	canonical := fmt.Sprintf("%d\x00%s\x00%s\x00%s", tenantID, kbID, strings.ToLower(fileType), fileHash)
	sum := sha256.Sum256([]byte(canonical))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// CreateKnowledgeWithFileHashLock 在 PostgreSQL 中按内容哈希串行化最终建库，
// 并在同一事务内重新检查重复文件和原子预留原始文件配额。
func (r *knowledgeRepository) CreateKnowledgeWithFileHashLock(
	ctx context.Context, knowledge *types.Knowledge, sourceFileQuotaBytes int64,
) (existing *types.Knowledge, created bool, err error) {
	if knowledge == nil || knowledge.FileHash == "" {
		return nil, false, fmt.Errorf("knowledge and file hash are required")
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			key := knowledgeUploadHashLockKey(
				knowledge.TenantID, knowledge.KnowledgeBaseID, knowledge.FileType, knowledge.FileHash,
			)
			if lockErr := tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error; lockErr != nil {
				return lockErr
			}
		} else if tx.Dialector.Name() == "mysql" {
			var lockedTenant types.Tenant
			if lockErr := tx.Model(&types.Tenant{}).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id").Where("id = ?", knowledge.TenantID).
				Take(&lockedTenant).Error; lockErr != nil {
				return lockErr
			}
			var lockedKnowledgeBase types.KnowledgeBase
			if lockErr := tx.Model(&types.KnowledgeBase{}).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id").
				Where("id = ? AND tenant_id = ?", knowledge.KnowledgeBaseID, knowledge.TenantID).
				Take(&lockedKnowledgeBase).Error; lockErr != nil {
				return lockErr
			}
		}

		var duplicate types.Knowledge
		duplicateQuery := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND parse_status <> ? AND type = ? AND file_hash = ?",
				knowledge.TenantID, knowledge.KnowledgeBaseID, types.ParseStatusFailed, "file", knowledge.FileHash)
		if knowledge.FileType != "" {
			duplicateQuery = duplicateQuery.Where("LOWER(file_type) = ?", strings.ToLower(knowledge.FileType))
		}
		if findErr := duplicateQuery.First(&duplicate).Error; findErr == nil {
			existing = &duplicate
			return nil
		} else if findErr != gorm.ErrRecordNotFound {
			return findErr
		}

		if sourceFileQuotaBytes > 0 {
			var tenant types.Tenant
			if tenantErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", knowledge.TenantID).First(&tenant).Error; tenantErr != nil {
				return tenantErr
			}
			if tenant.StorageQuota > 0 &&
				(tenant.StorageUsed >= tenant.StorageQuota || sourceFileQuotaBytes > tenant.StorageQuota-tenant.StorageUsed) {
				return types.NewStorageQuotaExceededError()
			}
			if updateErr := tx.Model(&types.Tenant{}).Where("id = ?", tenant.ID).
				Update("storage_used", tenant.StorageUsed+sourceFileQuotaBytes).Error; updateErr != nil {
				return updateErr
			}
		}

		if createErr := tx.Create(knowledge).Error; createErr != nil {
			return createErr
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return existing, created, nil
}

func (r *knowledgeRepository) GetKnowledgeUploadSession(
	ctx context.Context, tenantID uint64, kbID, userID, uploadID string,
) (*types.KnowledgeUploadSession, error) {
	var session types.KnowledgeUploadSession
	if err := r.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", uploadID, tenantID, kbID, userID).
		First(&session).Error; err != nil {
		return nil, err
	}
	var parts []types.KnowledgeUploadPart
	if err := r.db.WithContext(ctx).Where("session_id = ?", uploadID).
		Order("part_number ASC").Find(&parts).Error; err != nil {
		return nil, err
	}
	session.ReceivedPartHashes = make(map[int]string, len(parts))
	for _, part := range parts {
		session.ReceivedParts = append(session.ReceivedParts, part.PartNumber)
		session.ReceivedPartHashes[part.PartNumber] = part.SHA256
	}
	return &session, nil
}

func (r *knowledgeRepository) CountActiveKnowledgeUploadSessions(
	ctx context.Context, tenantID uint64, userID string,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.KnowledgeUploadSession{}).
		Where("tenant_id = ? AND user_id = ? AND status IN ?", tenantID, userID, activeUploadStatuses).
		Count(&count).Error
	return count, err
}

func (r *knowledgeRepository) SumActiveKnowledgeUploadBytes(ctx context.Context, tenantID uint64) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&types.KnowledgeUploadSession{}).
		Select("COALESCE(SUM(file_size), 0)").
		Where("tenant_id = ? AND status IN ?", tenantID, activeUploadStatuses).
		Scan(&total).Error
	return total, err
}

func (r *knowledgeRepository) GetKnowledgeUploadPart(
	ctx context.Context, tenantID uint64, kbID, userID, uploadID string, partNumber int,
) (*types.KnowledgeUploadPart, error) {
	var part types.KnowledgeUploadPart
	if err := r.db.WithContext(ctx).
		Where("session_id IN (?) AND part_number = ?",
			r.db.Model(&types.KnowledgeUploadSession{}).Select("id").
				Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", uploadID, tenantID, kbID, userID),
			partNumber).
		First(&part).Error; err != nil {
		return nil, err
	}
	return &part, nil
}

func (r *knowledgeRepository) ConfirmKnowledgeUploadPart(
	ctx context.Context, tenantID uint64, kbID, userID, uploadID string, part *types.KnowledgeUploadPart,
) (*types.KnowledgeUploadSession, error) {
	var result types.KnowledgeUploadSession
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", uploadID, tenantID, kbID, userID).
			First(&result).Error; err != nil {
			return err
		}
		if result.Status != types.KnowledgeUploadCreated && result.Status != types.KnowledgeUploadUploading && result.Status != types.KnowledgeUploadFailed {
			return types.ErrUploadSessionState
		}
		var existing types.KnowledgeUploadPart
		err := tx.Where("session_id = ? AND part_number = ?", uploadID, part.PartNumber).First(&existing).Error
		if err == nil {
			if existing.PartOffset != part.PartOffset || existing.PartSize != part.PartSize || existing.SHA256 != part.SHA256 {
				return types.ErrUploadPartConflict
			}
			return nil
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if part.PartOffset != result.ReceivedBytes {
			return types.ErrUploadPartOutOfOrder
		}
		if err := tx.Create(part).Error; err != nil {
			return err
		}
		result.ReceivedBytes += part.PartSize
		result.Status = types.KnowledgeUploadUploading
		result.ErrorMessage = ""
		result.UpdatedAt = time.Now()
		return tx.Model(&types.KnowledgeUploadSession{}).Where("id = ?", uploadID).
			Updates(map[string]any{
				"received_bytes": result.ReceivedBytes,
				"status":         result.Status,
				"error_message":  "",
				"updated_at":     result.UpdatedAt,
			}).Error
	})
	return &result, err
}

func (r *knowledgeRepository) ClaimKnowledgeUploadForCompletion(
	ctx context.Context, tenantID uint64, kbID, userID, uploadID string,
) (*types.KnowledgeUploadSession, error) {
	var session types.KnowledgeUploadSession
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", uploadID, tenantID, kbID, userID).
			First(&session).Error; err != nil {
			return err
		}
		if session.Status == types.KnowledgeUploadCompleted {
			return nil
		}
		if session.Status != types.KnowledgeUploadCreated && session.Status != types.KnowledgeUploadUploading && session.Status != types.KnowledgeUploadFailed {
			return types.ErrUploadSessionState
		}
		if session.ReceivedBytes != session.FileSize {
			return types.ErrUploadIncomplete
		}
		if time.Now().After(session.ExpiresAt) {
			return types.ErrUploadSessionState
		}
		session.Status = types.KnowledgeUploadCompleting
		session.UpdatedAt = time.Now()
		return tx.Model(&types.KnowledgeUploadSession{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ? AND status IN ?",
				uploadID, tenantID, kbID, userID, []types.KnowledgeUploadStatus{
					types.KnowledgeUploadCreated, types.KnowledgeUploadUploading, types.KnowledgeUploadFailed,
				}).
			Updates(map[string]any{"status": session.Status, "updated_at": session.UpdatedAt}).Error
	})
	return &session, err
}

func (r *knowledgeRepository) CancelKnowledgeUploadSession(
	ctx context.Context, tenantID uint64, kbID, userID, uploadID string,
) (*types.KnowledgeUploadSession, error) {
	var session types.KnowledgeUploadSession
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", uploadID, tenantID, kbID, userID).
			First(&session).Error; err != nil {
			return err
		}
		if session.Status != types.KnowledgeUploadCreated && session.Status != types.KnowledgeUploadUploading && session.Status != types.KnowledgeUploadFailed {
			if session.Status == types.KnowledgeUploadCancelled || session.Status == types.KnowledgeUploadCancelledCleanupPending {
				return nil
			}
			return types.ErrUploadSessionState
		}
		session.Status = types.KnowledgeUploadCancelledCleanupPending
		session.ErrorMessage = ""
		session.UpdatedAt = time.Now()
		return tx.Model(&types.KnowledgeUploadSession{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ? AND status IN ?",
				uploadID, tenantID, kbID, userID, []types.KnowledgeUploadStatus{
					types.KnowledgeUploadCreated, types.KnowledgeUploadUploading, types.KnowledgeUploadFailed,
				}).
			Updates(map[string]any{"status": session.Status, "error_message": "", "updated_at": session.UpdatedAt}).Error
	})
	return &session, err
}

func (r *knowledgeRepository) ExpireKnowledgeUploadSession(ctx context.Context, session *types.KnowledgeUploadSession) (bool, error) {
	result := r.db.WithContext(ctx).Model(&types.KnowledgeUploadSession{}).
		Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ? AND expires_at < ? AND status IN ?",
			session.ID, session.TenantID, session.KnowledgeBaseID, session.UserID, time.Now(), []types.KnowledgeUploadStatus{
				types.KnowledgeUploadCreated, types.KnowledgeUploadUploading,
				types.KnowledgeUploadCompleting, types.KnowledgeUploadFailed,
			}).
		Updates(map[string]any{"status": types.KnowledgeUploadExpiredCleanupPending, "updated_at": time.Now()})
	return result.RowsAffected == 1, result.Error
}

func (r *knowledgeRepository) UpdateKnowledgeUploadSession(ctx context.Context, session *types.KnowledgeUploadSession) error {
	return r.db.WithContext(ctx).Model(&types.KnowledgeUploadSession{}).
		Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", session.ID, session.TenantID, session.KnowledgeBaseID, session.UserID).
		Updates(map[string]any{
			"status": session.Status, "received_bytes": session.ReceivedBytes,
			"knowledge_id": session.KnowledgeID, "error_message": session.ErrorMessage,
			"final_file_path": session.FinalFilePath, "finalize_stage": session.FinalizeStage,
			"expires_at": session.ExpiresAt, "updated_at": time.Now(),
		}).Error
}

func (r *knowledgeRepository) ListExpiredKnowledgeUploadSessions(
	ctx context.Context, before time.Time, limit int,
) ([]*types.KnowledgeUploadSession, error) {
	var sessions []*types.KnowledgeUploadSession
	err := r.db.WithContext(ctx).
		Where("status = ? OR (expires_at < ? AND status IN ?) OR status IN ?",
			types.KnowledgeUploadCompleting, before, expirableUploadStatuses, cleanupPendingUploadStatuses).
		Order("expires_at ASC").Limit(limit).Find(&sessions).Error
	return sessions, err
}

func (r *knowledgeRepository) DeleteKnowledgeUploadParts(
	ctx context.Context, tenantID uint64, kbID, userID, uploadID string,
) error {
	return r.db.WithContext(ctx).Where("session_id IN (?)",
		r.db.Model(&types.KnowledgeUploadSession{}).Select("id").
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", uploadID, tenantID, kbID, userID),
	).Delete(&types.KnowledgeUploadPart{}).Error
}
