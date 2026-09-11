package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// stubFillSecretsModelService 只实现读取模型，用于验证保存配置的凭据边界。
type stubFillSecretsModelService struct {
	interfaces.ModelService
	getModelByID func(ctx context.Context, id string) (*types.Model, error)
}

func (s *stubFillSecretsModelService) GetModelByID(ctx context.Context, id string) (*types.Model, error) {
	return s.getModelByID(ctx, id)
}

func TestResolveStoredModelConfig_FillsExtraConfig(t *testing.T) {
	stored := &types.Model{
		Parameters: types.ModelParameters{
			APIKey:    "sk-stored",
			AppSecret: "app-secret-stored",
			ExtraConfig: map[string]string{
				"thinking_control": "none",
				"api_version":      "2024-10-21",
			},
		},
	}
	h := &InitializationHandler{
		modelService: &stubFillSecretsModelService{
			getModelByID: func(context.Context, string) (*types.Model, error) { return stored, nil },
		},
	}
	req := &ModelTestRequest{ModelID: "m-1"}

	require.NoError(t, h.resolveStoredModelTestRequest(context.Background(), req))

	if req.APIKey != "sk-stored" || req.AppSecret != "app-secret-stored" {
		t.Fatalf("secrets not filled from stored model: apiKey=%q appSecret=%q", req.APIKey, req.AppSecret)
	}
	if req.ExtraConfig == nil {
		t.Fatalf("ExtraConfig not filled from stored model: got nil, want thinking_control/api_version")
	}
	if req.ExtraConfig["thinking_control"] != "none" || req.ExtraConfig["api_version"] != "2024-10-21" {
		t.Fatalf("ExtraConfig contents wrong: %v", req.ExtraConfig)
	}
}

// 保存模型测试使用可信配置；编辑中的请求不能改写保存凭据的目标或参数。
func TestResolveStoredModelConfig_StoredConfigWins(t *testing.T) {
	stored := &types.Model{
		Parameters: types.ModelParameters{
			ExtraConfig: map[string]string{"thinking_control": "none"},
		},
	}
	h := &InitializationHandler{
		modelService: &stubFillSecretsModelService{
			getModelByID: func(context.Context, string) (*types.Model, error) { return stored, nil },
		},
	}
	req := &ModelTestRequest{
		ModelID:     "m-1",
		APIKey:      "sk-typed",
		AppSecret:   "app-typed",
		ExtraConfig: map[string]string{"thinking_control": "enabled"},
	}

	require.NoError(t, h.resolveStoredModelTestRequest(context.Background(), req))

	require.Equal(t, "none", req.ExtraConfig["thinking_control"])
	require.Equal(t, stored.Parameters.APIKey, req.APIKey)
	require.Equal(t, stored.Parameters.AppSecret, req.AppSecret)
}

// TestResolveStoredModelConfig_SecretsStillFilledWithoutExtraConfig:
// regression guard for the early-return — a request that already carries
// both secrets but no extraConfig must still pick up the stored
// extraConfig (this is the exact #3057 scenario: the frontend "test"
// button only sends modelId).
func TestResolveStoredModelConfig_SecretsStillFilledWithoutExtraConfig(t *testing.T) {
	stored := &types.Model{
		Parameters: types.ModelParameters{
			APIKey:      "sk-stored",
			AppSecret:   "app-secret-stored",
			ExtraConfig: map[string]string{"remote_model_name": "gpt-x"},
		},
	}
	h := &InitializationHandler{
		modelService: &stubFillSecretsModelService{
			getModelByID: func(context.Context, string) (*types.Model, error) { return stored, nil },
		},
	}
	req := &ModelTestRequest{ModelID: "m-1", APIKey: "sk-typed", AppSecret: "app-typed"}

	require.NoError(t, h.resolveStoredModelTestRequest(context.Background(), req))

	if req.ExtraConfig == nil || req.ExtraConfig["remote_model_name"] != "gpt-x" {
		t.Fatalf("ExtraConfig not filled when secrets were provided: %v", req.ExtraConfig)
	}
}

func TestResolveStoredModelConfig_NoModelIDNoop(t *testing.T) {
	h := &InitializationHandler{
		modelService: &stubFillSecretsModelService{
			getModelByID: func(context.Context, string) (*types.Model, error) {
				t.Fatal("GetModelByID must not be called without a model id")
				return nil, nil
			},
		},
	}
	req := &ModelTestRequest{}

	require.NoError(t, h.resolveStoredModelTestRequest(context.Background(), req))

	if req.ExtraConfig != nil {
		t.Fatalf("ExtraConfig changed without model id: %v", req.ExtraConfig)
	}
}

func TestResolveStoredModelConfig_ModelNotFoundRejected(t *testing.T) {
	h := &InitializationHandler{
		modelService: &stubFillSecretsModelService{
			getModelByID: func(context.Context, string) (*types.Model, error) {
				return nil, errors.New("not found")
			},
		},
	}
	req := &ModelTestRequest{ModelID: "missing"}

	require.Error(t, h.resolveStoredModelTestRequest(context.Background(), req))

	if req.ExtraConfig != nil {
		t.Fatalf("ExtraConfig changed on model lookup failure: %v", req.ExtraConfig)
	}
}
