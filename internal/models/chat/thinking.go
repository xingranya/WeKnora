package chat

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/models/provider"
	"github.com/sashabaranov/go-openai"
)

// ExtraConfigThinkingControl is the model parameters.extra_config key for
// selecting how ChatOptions.Thinking is translated to provider HTTP fields.
// The accepted values mirror the strings the frontend writes (see
// ModelEditorDialog.vue): "none", "enable_thinking", "thinking_type",
// "chat_template_kwargs", "reasoning_effort".
const ExtraConfigThinkingControl = "thinking_control"

// Wire-format request bodies used by providers that express extended-thinking
// through a non-standard top-level field. They embed the standard OpenAI
// request so all other fields are marshalled unchanged.

// EnableThinkingChatCompletionRequest 增加兼容接口使用的顶层思考参数。
// ReasoningEffort 仅在供应商明确支持且思考开启时发送。
type EnableThinkingChatCompletionRequest struct {
	openai.ChatCompletionRequest
	EnableThinking  *bool  `json:"enable_thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// ThinkingConfig is the `{ "type": "enabled"|"disabled" }` block used by
// LKEAP / Volcengine style providers.
type ThinkingConfig struct {
	Type string `json:"type"`
}

// ThinkingChatCompletionRequest adds the `thinking` object for providers that
// use the `{ "thinking": { "type": ... } }` wire format.
type ThinkingChatCompletionRequest struct {
	openai.ChatCompletionRequest
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
}

// ReasoningEffortChatCompletionRequest 使用 OpenAI Chat Completions 的顶层
// reasoning_effort 字段，Sub2API 会将其转换为 Responses API 的 reasoning.effort。
type ReasoningEffortChatCompletionRequest struct {
	openai.ChatCompletionRequest
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// ThinkingStrategy encodes how ChatOptions.Thinking is mapped onto a provider's
// HTTP request. Apply returns (customBody, useRawHTTP):
//   - (nil, false) means "send the standard OpenAI request unchanged" (the
//     caller keeps using the SDK path).
//   - a non-nil customBody must be sent verbatim over raw HTTP because it
//     carries fields the OpenAI SDK would strip.
//
// When opts.Thinking is nil most strategies emit nothing, deferring to the
// model's own default; the exception is enableThinking{alwaysSend: true}
// (Aliyun Qwen), which must always pin the field.
type ThinkingStrategy interface {
	Apply(req *openai.ChatCompletionRequest, opts *ChatOptions, isStream bool) (customBody any, useRawHTTP bool)
}

// noThinking sends no thinking-related fields at all.
type noThinking struct{}

func (noThinking) Apply(*openai.ChatCompletionRequest, *ChatOptions, bool) (any, bool) {
	return nil, false
}

// enableThinking encodes thinking via Qwen's `enable_thinking` boolean.
//
//   - alwaysSend: pin the field even when opts.Thinking is nil（阿里云 Qwen 与
//     硅基流动 DeepSeek V4 均需要确定性地发送，默认值为 false）。
//   - disableOnNonStream: force enable_thinking=false for non-stream requests
//     (Qwen3 rejects thinking in non-stream mode).
type enableThinking struct {
	alwaysSend         bool
	disableOnNonStream bool
	reasoningEffort    string
}

func (s enableThinking) Apply(req *openai.ChatCompletionRequest, opts *ChatOptions, isStream bool) (any, bool) {
	thinking := false
	switch {
	case opts != nil && opts.Thinking != nil:
		thinking = *opts.Thinking
	case !s.alwaysSend:
		return nil, false
	}
	if s.disableOnNonStream && !isStream {
		thinking = false
	}
	outbound := EnableThinkingChatCompletionRequest{ChatCompletionRequest: *req}
	outbound.EnableThinking = &thinking
	if thinking {
		outbound.ReasoningEffort = s.reasoningEffort
	}
	return outbound, true
}

// thinkingTypeField encodes thinking via the `{ "thinking": { "type": ... } }`
// object (LKEAP / Volcengine). Emits nothing when opts.Thinking is unset.
type thinkingTypeField struct{}

func (thinkingTypeField) Apply(req *openai.ChatCompletionRequest, opts *ChatOptions, _ bool) (any, bool) {
	if opts == nil || opts.Thinking == nil {
		return nil, false
	}
	r := ThinkingChatCompletionRequest{ChatCompletionRequest: *req}
	thinkingType := "disabled"
	if *opts.Thinking {
		thinkingType = "enabled"
	}
	r.Thinking = &ThinkingConfig{Type: thinkingType}
	return r, true
}

// chatTemplateKwargs encodes thinking via the standard request's
// `chat_template_kwargs.enable_thinking` (vLLM / NVIDIA / generic local
// deployments). Emits nothing when opts.Thinking is unset.
type chatTemplateKwargs struct{}

func (chatTemplateKwargs) Apply(req *openai.ChatCompletionRequest, opts *ChatOptions, _ bool) (any, bool) {
	if opts == nil || opts.Thinking == nil {
		return nil, false
	}
	req.ChatTemplateKwargs = map[string]interface{}{
		"enable_thinking": *opts.Thinking,
	}
	return req, true
}

// reasoningEffort 使用 Chat Completions 标准的 reasoning_effort 字段。
// 当前智能体只有开关，因此开启时使用 high；关闭或未设置时省略字段，交给
// 网关/模型的默认策略处理，避免发送不被接口识别的布尔值。
type reasoningEffort struct{}

func (reasoningEffort) Apply(req *openai.ChatCompletionRequest, opts *ChatOptions, _ bool) (any, bool) {
	if opts == nil || opts.Thinking == nil || !*opts.Thinking {
		return nil, false
	}
	r := ReasoningEffortChatCompletionRequest{ChatCompletionRequest: *req, ReasoningEffort: "high"}
	return r, true
}

// parseThinkingOverride reads extra_config.thinking_control and returns the
// strategy it selects, or nil when unset (the provider adapter's default
// strategy then applies). An unrecognized non-empty value falls back to
// chat_template_kwargs, preserving the legacy default-mode behavior.
func parseThinkingOverride(extraConfig map[string]string) ThinkingStrategy {
	if extraConfig == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(extraConfig[ExtraConfigThinkingControl])) {
	case "":
		return nil
	case "none":
		return noThinking{}
	case "enable_thinking":
		return enableThinking{}
	case "thinking_type":
		return thinkingTypeField{}
	case "reasoning_effort":
		return reasoningEffort{}
	default:
		// "chat_template_kwargs" and any unknown non-empty value.
		return chatTemplateKwargs{}
	}
}

// resolveThinkingOverride 仅在用户选择了不同的请求格式时覆盖供应商默认策略。
// 格式相同时保留供应商策略携带的附加约束，例如 Qwen 非流式强制关闭，或
// SiliconFlow DeepSeek V4 开启时固定 reasoning_effort=high。
func resolveThinkingOverride(adapter providerAdapter, extraConfig map[string]string) ThinkingStrategy {
	override := parseThinkingOverride(extraConfig)
	if override == nil || adapter == nil {
		return override
	}
	if thinkingStrategyName(override) == thinkingStrategyName(adapter.Thinking()) {
		return nil
	}
	return override
}

// EffectiveThinkingControl reports the provider field that will carry
// ChatOptions.Thinking. It intentionally shares the same adapter/override
// resolution as the real request path so diagnostics do not guess from the
// frontend selection.
func EffectiveThinkingControl(config *ChatConfig) string {
	if config == nil {
		return "none"
	}
	if override := parseThinkingOverride(config.ExtraConfig); override != nil {
		return thinkingStrategyName(override)
	}
	providerName := provider.ProviderName(config.Provider)
	if providerName == "" {
		providerName = provider.DetectProvider(config.BaseURL)
	}
	return thinkingStrategyName(resolveProvider(providerName, config.ModelName).Thinking())
}

func thinkingStrategyName(strategy ThinkingStrategy) string {
	switch strategy.(type) {
	case enableThinking:
		return "enable_thinking"
	case thinkingTypeField:
		return "thinking_type"
	case chatTemplateKwargs:
		return "chat_template_kwargs"
	case reasoningEffort:
		return "reasoning_effort"
	default:
		return "none"
	}
}
