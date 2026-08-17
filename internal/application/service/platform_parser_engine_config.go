package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type platformParserEngineConfigService struct {
	repo interfaces.PlatformParserEngineConfigRepository
}

// NewPlatformParserEngineConfigService 创建平台解析配置服务。
func NewPlatformParserEngineConfigService(
	repo interfaces.PlatformParserEngineConfigRepository,
) interfaces.PlatformParserEngineConfigService {
	return &platformParserEngineConfigService{repo: repo}
}

func (s *platformParserEngineConfigService) GetConfig(ctx context.Context) (*types.ParserEngineConfig, error) {
	row, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Config == nil {
		return &types.ParserEngineConfig{}, nil
	}
	config := *row.Config
	return &config, nil
}

func (s *platformParserEngineConfigService) UpdateConfig(
	ctx context.Context,
	config *types.ParserEngineConfig,
) (*types.ParserEngineConfig, error) {
	if config == nil {
		config = &types.ParserEngineConfig{}
	}
	stored := *config
	modifiedBy, _ := types.UserIDFromContext(ctx)
	row := &types.PlatformParserEngineConfig{
		Config:         &stored,
		LastModifiedBy: modifiedBy,
	}
	if err := s.repo.Upsert(ctx, row); err != nil {
		return nil, err
	}
	return &stored, nil
}

func (s *platformParserEngineConfigService) ResolveConfig(ctx context.Context) *types.ParserEngineConfig {
	config, err := s.GetConfig(ctx)
	if err != nil {
		logger.Errorf(ctx, "读取平台解析引擎配置失败: %v", err)
		return &types.ParserEngineConfig{}
	}
	return config
}
