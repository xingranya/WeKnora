package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// PlatformParserEngineConfigRepository 定义平台解析引擎配置的持久化接口。
type PlatformParserEngineConfigRepository interface {
	Get(ctx context.Context) (*types.PlatformParserEngineConfig, error)
	Upsert(ctx context.Context, config *types.PlatformParserEngineConfig) error
}

// PlatformParserEngineConfigService 提供平台解析配置的管理和运行时解析能力。
type PlatformParserEngineConfigService interface {
	GetConfig(ctx context.Context) (*types.ParserEngineConfig, error)
	UpdateConfig(ctx context.Context, config *types.ParserEngineConfig) (*types.ParserEngineConfig, error)
	ResolveConfig(ctx context.Context) *types.ParserEngineConfig
}
