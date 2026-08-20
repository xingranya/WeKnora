package provider

import (
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// IsSiliconFlowDeepSeekV4Model 判断是否为硅基流动官方支持思考开关的
// DeepSeek V4 系列。模型 ID 以官方 API 文档列出的名称为准，同时兼容 Pro/ 前缀。
func IsSiliconFlowDeepSeekV4Model(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	normalized = strings.TrimPrefix(normalized, "pro/")
	return normalized == "deepseek-ai/deepseek-v4" ||
		normalized == "deepseek-ai/deepseek-v4-flash" ||
		normalized == "deepseek-ai/deepseek-v4-pro"
}

const (
	SiliconFlowBaseURL = "https://api.siliconflow.cn/v1"
)

// SiliconFlowProvider 实现硅基流动的 Provider 接口
type SiliconFlowProvider struct{}

func init() {
	Register(&SiliconFlowProvider{})
}

// Info 返回硅基流动 provider 的元数据
func (p *SiliconFlowProvider) Info() ProviderInfo {
	return ProviderInfo{
		Name:        ProviderSiliconFlow,
		DisplayName: "硅基流动 SiliconFlow",
		Description: "deepseek-ai/DeepSeek-V3.1, etc.",
		DefaultURLs: map[types.ModelType]string{
			types.ModelTypeKnowledgeQA: SiliconFlowBaseURL,
			types.ModelTypeEmbedding:   SiliconFlowBaseURL,
			types.ModelTypeRerank:      SiliconFlowBaseURL,
			types.ModelTypeVLLM:        SiliconFlowBaseURL,
			types.ModelTypeASR:         SiliconFlowBaseURL,
		},
		ModelTypes: []types.ModelType{
			types.ModelTypeKnowledgeQA,
			types.ModelTypeEmbedding,
			types.ModelTypeRerank,
			types.ModelTypeVLLM,
			types.ModelTypeASR,
		},
		RequiresAuth: true,
	}
}

// ValidateConfig 验证硅基流动 provider 配置
func (p *SiliconFlowProvider) ValidateConfig(config *Config) error {
	if config.APIKey == "" {
		return fmt.Errorf("API key is required for SiliconFlow provider")
	}
	return nil
}
