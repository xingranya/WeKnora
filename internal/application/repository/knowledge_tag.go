package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrKnowledgeTagMutationConflict 表示知识的租户、知识库或生命周期状态
// 已与调用方验证时的预期不一致，调用方应返回冲突而不是继续写入旧标签。
var ErrKnowledgeTagMutationConflict = errors.New("knowledge tag mutation conflicts with knowledge lifecycle")

func uniqueKnowledgeTagIDs(tagIDs []string) []string {
	seen := make(map[string]struct{}, len(tagIDs))
	unique := make([]string, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		if tagID == "" {
			continue
		}
		if _, exists := seen[tagID]; exists {
			continue
		}
		seen[tagID] = struct{}{}
		unique = append(unique, tagID)
	}
	return unique
}

func knowledgeTagBlockedStatuses(rejectFailed bool) []string {
	statuses := []string{
		types.ParseStatusMoving,
		types.ParseStatusDeleting,
		types.ParseStatusCancelled,
	}
	if rejectFailed {
		statuses = append(statuses, types.ParseStatusFailed)
	}
	return statuses
}

func knowledgeTagMutationConflict(knowledgeID, status string) error {
	if status == types.ParseStatusMoving {
		return fmt.Errorf(
			"%w: %w: knowledge %s",
			ErrKnowledgeTagMutationConflict,
			types.ErrKnowledgeMoveInProgress,
			knowledgeID,
		)
	}
	if status == "" {
		return fmt.Errorf("%w: knowledge %s is outside the expected scope", ErrKnowledgeTagMutationConflict, knowledgeID)
	}
	return fmt.Errorf(
		"%w: knowledge %s is not mutable in status %s",
		ErrKnowledgeTagMutationConflict,
		knowledgeID,
		status,
	)
}

func classifyKnowledgeTagMutationConflict(
	tx *gorm.DB,
	tenantID uint64,
	kbID, knowledgeID string,
	rejectFailed bool,
) error {
	var current types.Knowledge
	if err := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return knowledgeTagMutationConflict(knowledgeID, "")
		}
		return err
	}
	if current.KnowledgeBaseID != kbID {
		return knowledgeTagMutationConflict(knowledgeID, "")
	}
	for _, blocked := range knowledgeTagBlockedStatuses(rejectFailed) {
		if current.ParseStatus == blocked {
			return knowledgeTagMutationConflict(knowledgeID, current.ParseStatus)
		}
	}
	return knowledgeTagMutationConflict(knowledgeID, "")
}

func knowledgeTagMutationRowQuery(tx *gorm.DB, tenantID uint64, knowledgeID string) *gorm.DB {
	query := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID)
	if tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return query
}

// lockKnowledgeForTagMutation 在写标签前锁定并验证知识行。PostgreSQL/MySQL
// 使用行锁；SQLite 先做受状态约束的无值变化 CAS，以取得单写者锁并覆盖
// “验证后被移动”窗口。
func lockKnowledgeForTagMutation(
	tx *gorm.DB,
	tenantID uint64,
	kbID, knowledgeID string,
	rejectFailed bool,
) (*types.Knowledge, error) {
	if tenantID == 0 || kbID == "" || knowledgeID == "" {
		return nil, knowledgeTagMutationConflict(knowledgeID, "")
	}

	blockedStatuses := knowledgeTagBlockedStatuses(rejectFailed)
	if tx.Dialector.Name() == "sqlite" {
		result := tx.Model(&types.Knowledge{}).
			Where("tenant_id = ? AND id = ? AND knowledge_base_id = ?", tenantID, knowledgeID, kbID).
			Where("parse_status IS NULL OR parse_status NOT IN ?", blockedStatuses).
			UpdateColumn("updated_at", gorm.Expr("updated_at"))
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, classifyKnowledgeTagMutationConflict(tx, tenantID, kbID, knowledgeID, rejectFailed)
		}
	}

	var knowledge types.Knowledge
	query := knowledgeTagMutationRowQuery(tx, tenantID, knowledgeID)
	if err := query.First(&knowledge).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, knowledgeTagMutationConflict(knowledgeID, "")
		}
		return nil, err
	}
	if knowledge.KnowledgeBaseID != kbID {
		return nil, knowledgeTagMutationConflict(knowledgeID, "")
	}
	for _, blocked := range blockedStatuses {
		if knowledge.ParseStatus == blocked {
			return nil, knowledgeTagMutationConflict(knowledgeID, knowledge.ParseStatus)
		}
	}
	return &knowledge, nil
}

func validateKnowledgeTagScope(
	tx *gorm.DB,
	tenantID uint64,
	kbID string,
	tagIDs []string,
) error {
	if len(tagIDs) == 0 {
		return nil
	}
	var tagCount int64
	if err := tx.Model(&types.KnowledgeTag{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id IN ?", tenantID, kbID, tagIDs).
		Count(&tagCount).Error; err != nil {
		return err
	}
	if tagCount != int64(len(tagIDs)) {
		return fmt.Errorf("one or more tags do not belong to tenant %d and knowledge base %s", tenantID, kbID)
	}
	return nil
}

func buildKnowledgeTagRelations(knowledgeID string, tagIDs []string) []types.KnowledgeTagRelation {
	now := time.Now()
	relations := make([]types.KnowledgeTagRelation, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		relations = append(relations, types.KnowledgeTagRelation{
			KnowledgeID: knowledgeID,
			TagID:       tagID,
			CreatedAt:   now,
		})
	}
	return relations
}

// SetKnowledgeTags 在同一事务中验证作用域、锁定知识行并替换全部标签。
func (r *knowledgeRepository) SetKnowledgeTags(
	ctx context.Context,
	tenantID uint64,
	kbID, knowledgeID string,
	tagIDs []string,
) error {
	unique := uniqueKnowledgeTagIDs(tagIDs)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockKnowledgeForTagMutation(tx, tenantID, kbID, knowledgeID, false); err != nil {
			return err
		}
		if err := validateKnowledgeTagScope(tx, tenantID, kbID, unique); err != nil {
			return err
		}
		if err := tx.Where("knowledge_id = ?", knowledgeID).
			Delete(&types.KnowledgeTagRelation{}).Error; err != nil {
			return err
		}
		if len(unique) == 0 {
			return nil
		}
		return tx.Create(buildKnowledgeTagRelations(knowledgeID, unique)).Error
	})
}

// AddKnowledgeTagRelations 在锁定知识行后增量添加标签，不删除人工标签；
// 复合主键和 DO NOTHING 共同保证重复投递幂等。
func (r *knowledgeRepository) AddKnowledgeTagRelations(
	ctx context.Context,
	tenantID uint64,
	kbID, knowledgeID string,
	tagIDs []string,
) error {
	unique := uniqueKnowledgeTagIDs(tagIDs)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockKnowledgeForTagMutation(tx, tenantID, kbID, knowledgeID, true); err != nil {
			return err
		}
		if len(unique) == 0 {
			return nil
		}
		if err := validateKnowledgeTagScope(tx, tenantID, kbID, unique); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(buildKnowledgeTagRelations(knowledgeID, unique)).Error
	})
}

// GetKnowledgeTags returns tags for multiple knowledge IDs.
// The result is a map from knowledge ID to its tag list.
func (r *knowledgeRepository) GetKnowledgeTags(
	ctx context.Context,
	knowledgeIDs []string,
) (map[string][]*types.KnowledgeTag, error) {
	result := make(map[string][]*types.KnowledgeTag)
	if len(knowledgeIDs) == 0 {
		return result, nil
	}

	// Query relations and join with knowledge_tags to get full tag info
	type relationWithTag struct {
		KnowledgeID string `gorm:"column:knowledge_id"`
		types.KnowledgeTag
	}
	var rows []relationWithTag
	if err := r.db.WithContext(ctx).
		Table("knowledge_tag_relations AS ktr").
		Select("ktr.knowledge_id, kt.id, kt.seq_id, kt.tenant_id, kt.knowledge_base_id, kt.name, kt.color, kt.sort_order, kt.created_at, kt.updated_at").
		Joins("JOIN knowledge_tags AS kt ON ktr.tag_id = kt.id").
		Where("ktr.knowledge_id IN (?)", knowledgeIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	for _, row := range rows {
		tag := row.KnowledgeTag
		result[row.KnowledgeID] = append(result[row.KnowledgeID], &tag)
	}
	return result, nil
}

// DeleteKnowledgeTagRelations deletes all tag relations for a knowledge entry.
func (r *knowledgeRepository) DeleteKnowledgeTagRelations(
	ctx context.Context,
	knowledgeID string,
) error {
	return r.db.WithContext(ctx).
		Where("knowledge_id = ?", knowledgeID).
		Delete(&types.KnowledgeTagRelation{}).Error
}
