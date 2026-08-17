package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
)

type runtimeParserConfigServiceStub struct {
	config *types.ParserEngineConfig
}

func (s *runtimeParserConfigServiceStub) GetConfig(context.Context) (*types.ParserEngineConfig, error) {
	return s.config, nil
}

func (s *runtimeParserConfigServiceStub) UpdateConfig(
	context.Context,
	*types.ParserEngineConfig,
) (*types.ParserEngineConfig, error) {
	return s.config, nil
}

func (s *runtimeParserConfigServiceStub) ResolveConfig(context.Context) *types.ParserEngineConfig {
	return s.config
}

func TestKnowledgeParserOverridesUsePlatformConfig(t *testing.T) {
	service := &knowledgeService{parserConfigSvc: &runtimeParserConfigServiceStub{
		config: &types.ParserEngineConfig{MinerUAPIKey: "platform-secret"},
	}}
	ctx := context.WithValue(context.Background(), types.TenantInfoContextKey, &types.Tenant{
		ParserEngineConfig: &types.ParserEngineConfig{MinerUAPIKey: "tenant-secret"},
	})

	overrides := service.getParserEngineOverridesFromContext(ctx)
	assert.Equal(t, "platform-secret", overrides["mineru_api_key"])
}

func TestTemporaryDocumentParserConfigUsesPlatformConfig(t *testing.T) {
	service := &temporaryDocumentService{parserConfigSvc: &runtimeParserConfigServiceStub{
		config: &types.ParserEngineConfig{PaddleOCRVLCloudToken: "platform-token"},
	}}
	ctx := context.WithValue(context.Background(), types.TenantInfoContextKey, &types.Tenant{
		ParserEngineConfig: &types.ParserEngineConfig{PaddleOCRVLCloudToken: "tenant-token"},
	})

	config := service.resolveParserConfig(ctx)
	assert.Equal(t, "platform-token", config.PaddleOCRVLCloudToken)
}
