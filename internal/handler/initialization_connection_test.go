package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
)

type initializationModelServiceStub struct {
	interfaces.ModelService
	model *types.Model
	err   error
}

func (s *initializationModelServiceStub) GetModelByID(context.Context, string) (*types.Model, error) {
	return s.model, s.err
}

func testConnectionChatModel(baseURL string) *types.Model {
	return &types.Model{
		ID:     "model-1",
		Name:   "test-model",
		Type:   types.ModelTypeKnowledgeQA,
		Source: types.ModelSourceRemote,
		Parameters: types.ModelParameters{
			BaseURL:  baseURL,
			APIKey:   "test-key",
			Provider: "generic",
		},
	}
}

func allowLocalModelTestServer(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
}

func TestCheckChatModelConnectionRejectsHTTP400(t *testing.T) {
	allowLocalModelTestServer(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid max_tokens","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	available, message := (&InitializationHandler{}).checkChatModelConnection(
		context.Background(), testConnectionChatModel(server.URL), "", "",
	)

	require.False(t, available)
	require.Contains(t, message, "400")
}

func TestCheckChatModelConnectionAcceptsValidResponse(t *testing.T) {
	allowLocalModelTestServer(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":1,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	available, message := (&InitializationHandler{}).checkChatModelConnection(
		context.Background(), testConnectionChatModel(server.URL), "", "",
	)

	require.True(t, available, message)
	require.Equal(t, "连接正常，模型可用", message)
}

func TestResolveStoredModelTestRequestPinsEntireStoredConfiguration(t *testing.T) {
	stored := &types.Model{
		ID:        "company-model",
		Name:      "stored-model",
		Source:    types.ModelSourceSiliconFlow,
		IsBuiltin: true,
		Parameters: types.ModelParameters{
			BaseURL:       "https://trusted.example/v1",
			APIKey:        "stored-api-key",
			AppSecret:     "stored-app-secret",
			Provider:      "siliconflow",
			InterfaceType: "chat-completions",
			CustomHeaders: map[string]string{"X-Tenant": "company"},
			ExtraConfig:   map[string]string{"region": "cn"},
			EmbeddingParameters: types.EmbeddingParameters{
				Dimension:                 2048,
				SupportsDimensionOverride: true,
			},
		},
	}
	handler := &InitializationHandler{
		modelService: &initializationModelServiceStub{model: stored},
	}
	req := &ModelTestRequest{
		ModelID:       stored.ID,
		ModelName:     "attacker-model",
		BaseURL:       "https://attacker.example/v1",
		APIKey:        "attacker-key",
		AppSecret:     "attacker-secret",
		Provider:      "generic",
		CustomHeaders: map[string]string{"Authorization": "attacker"},
		ExtraConfig:   map[string]string{"proxy": "attacker"},
	}

	require.NoError(t, handler.resolveStoredModelTestRequest(context.Background(), req))
	require.Equal(t, string(stored.Source), req.Source)
	require.Equal(t, stored.Name, req.ModelName)
	require.Equal(t, stored.Parameters.BaseURL, req.BaseURL)
	require.Equal(t, stored.Parameters.APIKey, req.APIKey)
	require.Equal(t, stored.Parameters.AppSecret, req.AppSecret)
	require.Equal(t, stored.Parameters.Provider, req.Provider)
	require.Equal(t, stored.Parameters.InterfaceType, req.InterfaceType)
	require.Equal(t, stored.Parameters.EmbeddingParameters.Dimension, req.Dimension)
	require.Equal(t, stored.Parameters.CustomHeaders, req.CustomHeaders)
	require.Equal(t, stored.Parameters.ExtraConfig, req.ExtraConfig)

	req.CustomHeaders["X-Tenant"] = "changed"
	req.ExtraConfig["region"] = "changed"
	require.Equal(t, "company", stored.Parameters.CustomHeaders["X-Tenant"])
	require.Equal(t, "cn", stored.Parameters.ExtraConfig["region"])
}

func TestResolveStoredModelTestRequestFailsClosedWhenModelUnavailable(t *testing.T) {
	handler := &InitializationHandler{
		modelService: &initializationModelServiceStub{err: errors.New("not found")},
	}
	req := &ModelTestRequest{ModelID: "missing-model", BaseURL: "https://attacker.example/v1"}

	err := handler.resolveStoredModelTestRequest(context.Background(), req)
	require.Error(t, err)
	require.Equal(t, "https://attacker.example/v1", req.BaseURL)
}
