package service

import (
	"context"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const modelDeleteAutoTagKnowledgeBaseDDL = `
CREATE TABLE knowledge_bases (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    embedding_model_id VARCHAR(64) NOT NULL,
    summary_model_id VARCHAR(64) NOT NULL,
    image_processing_config TEXT NOT NULL DEFAULT '{}',
    vlm_config TEXT NOT NULL DEFAULT '{}',
    asr_config TEXT,
    wiki_config TEXT,
    auto_tag_config TEXT,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);`

func TestDeleteModelRejectsAutoTagOnlyReferenceUntilReleased(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.Model{}, &types.Knowledge{}))
	require.NoError(t, db.Exec(modelDeleteAutoTagKnowledgeBaseDDL).Error)

	const modelID = "auto-tag-only-model"
	require.NoError(t, db.Create(&types.Model{
		ID:       modelID,
		TenantID: 1,
		Name:     "auto-tag-only",
		Type:     types.ModelTypeKnowledgeQA,
		Source:   types.ModelSourceRemote,
		Status:   types.ModelStatusActive,
	}).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_bases (
			id, tenant_id, embedding_model_id, summary_model_id,
			image_processing_config, vlm_config, asr_config, wiki_config, auto_tag_config
		) VALUES (?, ?, ?, ?, '{}', '{}', '{}', '{}', ?)
	`, "auto-tag-kb", 1, "other-embedding", "other-summary",
		`{"enabled":true,"model_id":"auto-tag-only-model"}`).Error)

	modelService := NewModelService(
		apprepo.NewModelRepository(db),
		apprepo.NewKnowledgeBaseRepository(db),
		&stubAgentRepoForModelDelete{},
		nil,
		nil,
		nil,
	)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))

	err = modelService.DeleteModel(ctx, modelID)
	require.Error(t, err)
	appErr, ok := apperrors.IsAppError(err)
	require.True(t, ok)
	assert.Equal(t, apperrors.ErrBadRequest, appErr.Code)
	assert.Contains(t, appErr.Message, "knowledge base")

	var activeCount int64
	require.NoError(t, db.Model(&types.Model{}).Where("id = ?", modelID).Count(&activeCount).Error)
	assert.Equal(t, int64(1), activeCount)

	require.NoError(t, db.Model(&types.KnowledgeBase{}).
		Where("id = ?", "auto-tag-kb").
		Update("auto_tag_config", types.AutoTagConfig{Enabled: false}).Error)
	require.NoError(t, modelService.DeleteModel(ctx, modelID))

	var deleted types.Model
	require.NoError(t, db.Unscoped().First(&deleted, "id = ?", modelID).Error)
	assert.True(t, deleted.DeletedAt.Valid)
}
