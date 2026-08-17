package session

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
)

type attachmentParserConfigServiceStub struct {
	config *types.ParserEngineConfig
}

func (s *attachmentParserConfigServiceStub) GetConfig(context.Context) (*types.ParserEngineConfig, error) {
	return s.config, nil
}

func (s *attachmentParserConfigServiceStub) UpdateConfig(
	context.Context,
	*types.ParserEngineConfig,
) (*types.ParserEngineConfig, error) {
	return s.config, nil
}

func (s *attachmentParserConfigServiceStub) ResolveConfig(context.Context) *types.ParserEngineConfig {
	return s.config
}

func TestAttachmentParserOverridesUsePlatformConfig(t *testing.T) {
	processor := &AttachmentProcessor{parserConfigSvc: &attachmentParserConfigServiceStub{
		config: &types.ParserEngineConfig{MinerUEndpoint: "https://platform.example.com"},
	}}
	ctx := context.WithValue(context.Background(), types.TenantInfoContextKey, &types.Tenant{
		ParserEngineConfig: &types.ParserEngineConfig{MinerUEndpoint: "https://tenant.example.com"},
	})

	overrides := processor.getParserEngineOverrides(ctx)
	assert.Equal(t, "https://platform.example.com", overrides["mineru_endpoint"])
}
