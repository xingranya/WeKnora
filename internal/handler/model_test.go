package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modelUpdateServiceStub struct {
	interfaces.ModelService
	model   *types.Model
	updated *types.Model
}

func (s *modelUpdateServiceStub) GetModelByID(context.Context, string) (*types.Model, error) {
	return s.model, nil
}

func (s *modelUpdateServiceStub) UpdateModel(_ context.Context, model *types.Model) error {
	s.updated = model
	return nil
}

func runModelUpdateHandler(t *testing.T, model *types.Model, body string) (*types.Model, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/models/model-1", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "model-1"}}
	service := &modelUpdateServiceStub{model: model}
	NewModelHandler(service).UpdateModel(c)
	return service.updated, c
}

func modelForPatchTest() *types.Model {
	return &types.Model{
		ID:          "model-1",
		TenantID:    42,
		Name:        "gpt-original",
		DisplayName: "原显示名",
		Description: "原描述",
		Source:      types.ModelSourceRemote,
		Type:        types.ModelTypeKnowledgeQA,
		Status:      types.ModelStatusActive,
		Parameters: types.ModelParameters{
			BaseURL:       "https://example.com/v1",
			APIKey:        "stored-api-key",
			AppSecret:     "stored-app-secret",
			AppID:         "stored-app-id",
			InterfaceType: "openai",
			EmbeddingParameters: types.EmbeddingParameters{
				Dimension:                 1536,
				TruncatePromptTokens:      64,
				SupportsDimensionOverride: true,
			},
			ParameterSize:  "7B",
			Provider:       "openai",
			ExtraConfig:    map[string]string{"mode": "strict"},
			CustomHeaders:  map[string]string{"X-Tenant": "demo"},
			SupportsVision: true,
			MaxConcurrency: 5,
		},
	}
}

func TestUpdateModel_PartialRequestPreservesOmittedFields(t *testing.T) {
	updated, c := runModelUpdateHandler(t, modelForPatchTest(), `{"display_name":"新显示名"}`)
	require.Empty(t, c.Errors)
	require.NotNil(t, updated)

	assert.Equal(t, "gpt-original", updated.Name)
	assert.Equal(t, "新显示名", updated.DisplayName)
	assert.Equal(t, "原描述", updated.Description)
	assert.Equal(t, types.ModelSourceRemote, updated.Source)
	assert.Equal(t, types.ModelTypeKnowledgeQA, updated.Type)
	assert.Equal(t, types.ModelParameters{
		BaseURL:       "https://example.com/v1",
		APIKey:        "stored-api-key",
		AppSecret:     "stored-app-secret",
		AppID:         "stored-app-id",
		InterfaceType: "openai",
		EmbeddingParameters: types.EmbeddingParameters{
			Dimension:                 1536,
			TruncatePromptTokens:      64,
			SupportsDimensionOverride: true,
		},
		ParameterSize:  "7B",
		Provider:       "openai",
		ExtraConfig:    map[string]string{"mode": "strict"},
		CustomHeaders:  map[string]string{"X-Tenant": "demo"},
		SupportsVision: true,
		MaxConcurrency: 5,
	}, updated.Parameters)
}

func TestUpdateModel_ExplicitZeroValuesFollowFieldSemantics(t *testing.T) {
	updated, c := runModelUpdateHandler(t, modelForPatchTest(), `{
		"display_name":"",
		"description":"",
		"parameters":{
			"base_url":"",
			"api_key":"malicious-replacement",
			"app_secret":"malicious-replacement",
			"app_id":"",
			"parameter_size":"13B",
			"extra_config":{},
			"custom_headers":{},
			"supports_vision":false,
			"max_concurrency":0
		}
	}`)
	require.Empty(t, c.Errors)
	require.NotNil(t, updated)

	assert.Empty(t, updated.DisplayName)
	assert.Empty(t, updated.Description)
	assert.Empty(t, updated.Parameters.BaseURL)
	assert.NotNil(t, updated.Parameters.ExtraConfig)
	assert.Empty(t, updated.Parameters.ExtraConfig)
	assert.NotNil(t, updated.Parameters.CustomHeaders)
	assert.Empty(t, updated.Parameters.CustomHeaders)
	assert.False(t, updated.Parameters.SupportsVision)
	assert.Zero(t, updated.Parameters.MaxConcurrency)

	assert.Equal(t, "stored-api-key", updated.Parameters.APIKey)
	assert.Equal(t, "stored-app-secret", updated.Parameters.AppSecret)
	assert.Empty(t, updated.Parameters.AppID)
	assert.Equal(t, "7B", updated.Parameters.ParameterSize)
	assert.Equal(t, "openai", updated.Parameters.Provider)
	assert.Equal(t, "openai", updated.Parameters.InterfaceType)
	assert.Equal(t, 1536, updated.Parameters.EmbeddingParameters.Dimension)
	assert.Equal(t, types.ModelSourceRemote, updated.Source)
	assert.Equal(t, types.ModelTypeKnowledgeQA, updated.Type)
}

func TestUpdateModel_RejectsExplicitlyEmptyRequiredFields(t *testing.T) {
	for _, body := range []string{
		`{"name":""}`,
		`{"source":""}`,
		`{"type":""}`,
	} {
		t.Run(body, func(t *testing.T) {
			updated, c := runModelUpdateHandler(t, modelForPatchTest(), body)
			assert.Nil(t, updated)
			require.Len(t, c.Errors, 1)
		})
	}
}

func TestModelUpdateRequestDisplayNamePresence(t *testing.T) {
	var omitted UpdateModelRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"gpt-4o"}`), &omitted))
	assert.Nil(t, omitted.DisplayName)

	var cleared UpdateModelRequest
	require.NoError(t, json.Unmarshal([]byte(`{"display_name":""}`), &cleared))
	require.NotNil(t, cleared.DisplayName)
	assert.Equal(t, "", *cleared.DisplayName)
}

func TestParseModelDebugOptionsPreservesExplicitThinkingFalse(t *testing.T) {
	opts, err := parseModelDebugOptions(`{"thinking":false,"temperature":0,"max_tokens":256}`)
	require.NoError(t, err)
	require.NotNil(t, opts.Thinking)
	assert.False(t, *opts.Thinking)
	require.NotNil(t, opts.Temperature)
	assert.Zero(t, *opts.Temperature)
	require.NotNil(t, opts.MaxTokens)
	assert.Equal(t, 256, *opts.MaxTokens)
}

func TestParseModelDebugOptionsRejectsOutOfRangeValues(t *testing.T) {
	_, err := parseModelDebugOptions(`{"top_p":0}`)
	require.ErrorContains(t, err, "top_p")
}

func TestRedactedDebugConfig(t *testing.T) {
	got := redactedDebugConfig(map[string]string{
		"thinking_control": "enable_thinking",
		"secret_key":       "do-not-leak",
		"access_token":     "do-not-leak-either",
	})
	assert.Equal(t, "enable_thinking", got["thinking_control"])
	assert.Equal(t, "[REDACTED]", got["secret_key"])
	assert.Equal(t, "[REDACTED]", got["access_token"])
}

func TestConsumeModelDebugChatStream(t *testing.T) {
	stream := make(chan types.StreamResponse, 5)
	stream <- types.StreamResponse{ResponseType: types.ResponseTypeThinking, Content: "reason "}
	stream <- types.StreamResponse{ResponseType: types.ResponseTypeThinking, Content: "more", Done: true}
	stream <- types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Content: "answer "}
	stream <- types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Content: "done"}
	stream <- types.StreamResponse{
		ResponseType: types.ResponseTypeAnswer,
		Done:         true,
		FinishReason: "stop",
		Usage:        &types.TokenUsage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7},
	}
	close(stream)

	got, err := consumeModelDebugChatStream(stream)
	require.NoError(t, err)
	assert.Equal(t, "reason more", got.ReasoningContent)
	assert.Equal(t, "answer done", got.Content)
	assert.Equal(t, "stop", got.FinishReason)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 7, got.Usage.TotalTokens)
	assert.Len(t, got.StreamEvents, 5)
}
