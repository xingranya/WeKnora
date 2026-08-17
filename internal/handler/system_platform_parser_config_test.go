package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type platformParserConfigServiceStub struct {
	config *types.ParserEngineConfig
}

func (s *platformParserConfigServiceStub) GetConfig(context.Context) (*types.ParserEngineConfig, error) {
	return s.config, nil
}

func (s *platformParserConfigServiceStub) UpdateConfig(
	_ context.Context,
	config *types.ParserEngineConfig,
) (*types.ParserEngineConfig, error) {
	s.config = config
	return config, nil
}

func (s *platformParserConfigServiceStub) ResolveConfig(context.Context) *types.ParserEngineConfig {
	return s.config
}

func TestCompanyPresetParserEnginesFiltersWeKnoraCloud(t *testing.T) {
	engines := companyPresetParserEngines([]types.ParserEngineInfo{
		{Name: "mineru", Available: true},
		{Name: "weknoracloud", Available: true},
		{Name: "paddleocr_vl_cloud", Available: true},
	})

	require.Len(t, engines, 2)
	assert.Equal(t, "mineru", engines[0].Name)
	assert.True(t, engines[0].CompanyPreset)
	assert.Equal(t, "paddleocr_vl_cloud", engines[1].Name)
	assert.True(t, engines[1].CompanyPreset)
}

func TestGetPlatformParserEngineConfigRedactsSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &SystemHandler{parserConfigSvc: &platformParserConfigServiceStub{config: &types.ParserEngineConfig{
		MinerUEndpoint:        "https://parser.example.com",
		MinerUAPIKey:          "mineru-secret",
		PaddleOCRVLCloudToken: "paddle-secret",
	}}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/system/admin/parser-engine-config", nil)

	handler.GetPlatformParserEngineConfig(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "mineru-secret")
	assert.NotContains(t, recorder.Body.String(), "paddle-secret")
	assert.Contains(t, recorder.Body.String(), types.RedactedSecretPlaceholder)
}

func TestUpdatePlatformParserEngineConfigPreservesRedactedSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &platformParserConfigServiceStub{config: &types.ParserEngineConfig{
		MinerUAPIKey:          "mineru-secret",
		PaddleOCRVLCloudToken: "paddle-secret",
	}}
	handler := &SystemHandler{parserConfigSvc: service}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	body := `{"mineru_api_key":"***","paddleocr_vl_cloud_token":"***","mineru_model":"pipeline"}`
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/system/admin/parser-engine-config", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.UpdatePlatformParserEngineConfig(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotNil(t, service.config)
	assert.Equal(t, "mineru-secret", service.config.MinerUAPIKey)
	assert.Equal(t, "paddle-secret", service.config.PaddleOCRVLCloudToken)
	assert.Equal(t, "pipeline", service.config.MinerUModel)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.NotContains(t, recorder.Body.String(), "mineru-secret")
	assert.NotContains(t, recorder.Body.String(), "paddle-secret")
}
