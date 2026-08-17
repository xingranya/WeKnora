package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const platformParserEngineConfigID uint64 = 1

type platformParserEngineConfigRepository struct {
	db *gorm.DB
}

// NewPlatformParserEngineConfigRepository 创建平台解析配置仓库。
func NewPlatformParserEngineConfigRepository(db *gorm.DB) interfaces.PlatformParserEngineConfigRepository {
	return &platformParserEngineConfigRepository{db: db}
}

func (r *platformParserEngineConfigRepository) Get(ctx context.Context) (*types.PlatformParserEngineConfig, error) {
	var row types.PlatformParserEngineConfig
	err := r.db.WithContext(ctx).Where("id = ?", platformParserEngineConfigID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *platformParserEngineConfigRepository) Upsert(
	ctx context.Context,
	config *types.PlatformParserEngineConfig,
) error {
	config.ID = platformParserEngineConfigID
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"config",
				"last_modified_by",
				"updated_at",
			}),
		}).
		Create(config).Error
}
