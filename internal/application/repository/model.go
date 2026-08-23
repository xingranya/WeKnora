package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// modelRepository implements the model repository interface
type modelRepository struct {
	db *gorm.DB
}

// NewModelRepository creates a new model repository
func NewModelRepository(db *gorm.DB) interfaces.ModelRepository {
	return &modelRepository{db: db}
}

// Create creates a new model
func (r *modelRepository) Create(ctx context.Context, m *types.Model) error {
	return r.db.WithContext(ctx).Create(m).Error
}

// GetByID retrieves a model by ID
func (r *modelRepository) GetByID(ctx context.Context, tenantID uint64, id string) (*types.Model, error) {
	var m types.Model
	if err := r.db.WithContext(ctx).Where("id = ?", id).Where(
		"(tenant_id = ? OR is_builtin = true)", tenantID,
	).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// List lists models with optional filtering
func (r *modelRepository) List(
	ctx context.Context, tenantID uint64, modelType types.ModelType, source types.ModelSource,
) ([]*types.Model, error) {
	var models []*types.Model
	query := r.db.WithContext(ctx).Where(
		"(tenant_id = ? OR is_builtin = true)", tenantID,
	)

	if modelType != "" {
		query = query.Where("type = ?", modelType)
	}

	if source != "" {
		query = query.Where("source = ?", source)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}

	return models, nil
}

// Update 保留完整模型兼容更新；新增生产路径应使用按意图拆分的方法。
func (r *modelRepository) Update(ctx context.Context, m *types.Model) error {
	// 只更新允许变更的列；map 保留 false、0、空字符串和空对象等显式零值。
	// 主键、租户、创建时间和软删除标记不得由模型编辑路径覆盖。
	updates := map[string]interface{}{
		"name":         m.Name,
		"display_name": m.DisplayName,
		"type":         m.Type,
		"source":       m.Source,
		"description":  m.Description,
		"parameters":   m.Parameters,
		"is_default":   m.IsDefault,
		"is_builtin":   m.IsBuiltin,
		"managed_by":   m.ManagedBy,
		"status":       m.Status,
	}
	return r.db.WithContext(ctx).Model(&types.Model{}).Where(
		"id = ? AND tenant_id = ?", m.ID, m.TenantID,
	).Updates(updates).Error
}

// UpdateConfiguration 只写入普通模型配置。
// 事务内锁定当前行后从数据库回填凭据，避免并发凭据更新被旧快照覆盖。
func (r *modelRepository) UpdateConfiguration(ctx context.Context, m *types.Model) error {
	if m == nil {
		return errors.New("model is nil")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.Model
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ?", m.ID, m.TenantID).
			First(&current).Error; err != nil {
			return err
		}

		parameters := m.Parameters
		parameters.APIKey = current.Parameters.APIKey
		parameters.AppSecret = current.Parameters.AppSecret
		return tx.Model(&types.Model{}).
			Where("id = ? AND tenant_id = ?", m.ID, m.TenantID).
			Updates(map[string]interface{}{
				"name":         m.Name,
				"display_name": m.DisplayName,
				"type":         m.Type,
				"source":       m.Source,
				"description":  m.Description,
				"parameters":   parameters,
				"managed_by":   m.ManagedBy,
			}).Error
	})
}

// UpdateCredentials 只写入明确传入的凭据字段，并保留并发更新后的普通配置。
func (r *modelRepository) UpdateCredentials(
	ctx context.Context,
	tenantID uint64,
	id string,
	apiKey *string,
	appSecret *string,
	managedBy *string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.Model
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ?", id, tenantID).
			First(&current).Error; err != nil {
			return err
		}

		if apiKey != nil {
			current.Parameters.APIKey = *apiKey
		}
		if appSecret != nil {
			current.Parameters.AppSecret = *appSecret
		}
		updates := map[string]interface{}{"parameters": current.Parameters}
		if managedBy != nil {
			updates["managed_by"] = *managedBy
		}
		return tx.Model(&types.Model{}).
			Where("id = ? AND tenant_id = ?", id, tenantID).
			Updates(updates).Error
	})
}

// UpdateStatus 只写入后台模型下载流程维护的状态。
func (r *modelRepository) UpdateStatus(
	ctx context.Context,
	tenantID uint64,
	id string,
	status types.ModelStatus,
) error {
	return r.db.WithContext(ctx).Model(&types.Model{}).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		Update("status", status).Error
}

// Delete deletes a model
func (r *modelRepository) Delete(ctx context.Context, tenantID uint64, id string) error {
	return r.db.WithContext(ctx).Where(
		"id = ? AND tenant_id = ?", id, tenantID,
	).Delete(&types.Model{}).Error
}

// ClearDefaultByType clears the default flag for all models of a specific type
// This is a batch operation that updates all matching records in one query
func (r *modelRepository) ClearDefaultByType(
	ctx context.Context,
	tenantID uint,
	modelType types.ModelType,
	excludeID string,
) error {
	query := r.db.WithContext(ctx).Model(&types.Model{}).Where(
		"tenant_id = ? AND type = ? AND is_default = ?", tenantID, modelType, true,
	)

	// If excludeID is provided, exclude that model from the update
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}

	// Batch update: set is_default to false for all matching records
	return query.Update("is_default", false).Error
}
