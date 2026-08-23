package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/models/asr"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/models/vlm"
	"github.com/Tencent/WeKnora/internal/types"
)

// ModelService defines the model service interface
type ModelService interface {
	// CreateModel creates a model
	CreateModel(ctx context.Context, model *types.Model) error
	// GetModelByID gets a model by ID
	GetModelByID(ctx context.Context, id string) (*types.Model, error)
	// ListModels lists all models
	ListModels(ctx context.Context) ([]*types.Model, error)
	// UpdateModel updates a model
	UpdateModel(ctx context.Context, model *types.Model) error
	// DeleteModel deletes a model
	DeleteModel(ctx context.Context, id string) error

	// UpdateModelCredentials writes one or more credential fields on the
	// model's Parameters. Nil pointer means "do not touch this field";
	// empty string is treated as no-op (use ClearModelCredential to remove).
	// Returns the updated model.
	UpdateModelCredentials(ctx context.Context, id string, apiKey, appSecret *string) (*types.Model, error)
	// ClearModelCredential removes a single credential field. field must be
	// "api_key" or "app_secret". Clearing an already-empty field is a no-op.
	ClearModelCredential(ctx context.Context, id, field string) error
	// GetEmbeddingModel gets an embedding model
	GetEmbeddingModel(ctx context.Context, modelId string) (embedding.Embedder, error)
	// GetEmbeddingModelForTenant gets an embedding model for a specific tenant (for cross-tenant sharing)
	GetEmbeddingModelForTenant(ctx context.Context, modelId string, tenantID uint64) (embedding.Embedder, error)
	// GetRerankModel gets a rerank model
	GetRerankModel(ctx context.Context, modelId string) (rerank.Reranker, error)
	// GetChatModel gets a chat model
	GetChatModel(ctx context.Context, modelId string) (chat.Chat, error)
	// GetVLMModel gets a vision language model
	GetVLMModel(ctx context.Context, modelId string) (vlm.VLM, error)
	// GetASRModel gets an automatic speech recognition model
	GetASRModel(ctx context.Context, modelId string) (asr.ASR, error)
}

// ModelRepository defines the model repository interface
type ModelRepository interface {
	// Create creates a model
	Create(ctx context.Context, model *types.Model) error
	// GetByID gets a model by ID
	GetByID(ctx context.Context, tenantID uint64, id string) (*types.Model, error)
	// List lists all models
	List(
		ctx context.Context,
		tenantID uint64,
		modelType types.ModelType,
		source types.ModelSource,
	) ([]*types.Model, error)
	// Update 保留完整模型兼容更新；新增生产路径必须使用下方的按意图更新方法。
	Update(ctx context.Context, model *types.Model) error
	// UpdateConfiguration 仅更新普通模型配置，并保留数据库中的凭据和运行状态。
	UpdateConfiguration(ctx context.Context, model *types.Model) error
	// UpdateCredentials 仅更新明确传入的凭据字段；nil 表示保持原值。
	// managedBy 非 nil 时同步更新模型的生命周期归属。
	UpdateCredentials(
		ctx context.Context,
		tenantID uint64,
		id string,
		apiKey *string,
		appSecret *string,
		managedBy *string,
	) error
	// UpdateStatus 仅更新后台下载流程维护的运行状态。
	UpdateStatus(ctx context.Context, tenantID uint64, id string, status types.ModelStatus) error
	// Delete deletes a model
	Delete(ctx context.Context, tenantID uint64, id string) error
	// ClearDefaultByType clears the default flag for all models of a specific type
	// optionally excluding a specific model ID.
	ClearDefaultByType(ctx context.Context, tenantID uint, modelType types.ModelType, excludeID string) error
}
