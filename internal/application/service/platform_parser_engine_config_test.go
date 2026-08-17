package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type platformParserConfigRepoStub struct {
	row *types.PlatformParserEngineConfig
}

func (s *platformParserConfigRepoStub) Get(context.Context) (*types.PlatformParserEngineConfig, error) {
	return s.row, nil
}

func (s *platformParserConfigRepoStub) Upsert(_ context.Context, row *types.PlatformParserEngineConfig) error {
	s.row = row
	return nil
}

func TestPlatformParserEngineConfigServiceStoresSingletonConfig(t *testing.T) {
	repo := &platformParserConfigRepoStub{}
	service := NewPlatformParserEngineConfigService(repo)

	updated, err := service.UpdateConfig(context.Background(), &types.ParserEngineConfig{
		MinerUEndpoint:        "https://parser.example.com",
		PaddleOCRVLCloudToken: "secret-token",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://parser.example.com", updated.MinerUEndpoint)
	require.NotNil(t, repo.row)

	loaded, err := service.GetConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "secret-token", loaded.PaddleOCRVLCloudToken)
}

func TestPlatformParserEngineConfigServiceReturnsEmptyConfigWhenUnset(t *testing.T) {
	service := NewPlatformParserEngineConfigService(&platformParserConfigRepoStub{})
	config, err := service.GetConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Nil(t, config.ToOverridesMap())
}
