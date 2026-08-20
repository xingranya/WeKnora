/** Mirrors backend internal/models/provider.IsQwenThinkingModel */
export function isQwenThinkingModel(modelName: string): boolean {
  const lower = modelName.trim().toLowerCase()
  return (
    lower.startsWith('qwen3')
    || lower.startsWith('qwen-plus')
    || lower.startsWith('qwen-max')
    || lower.startsWith('qwen-turbo')
  )
}

/** Mirrors backend internal/models/provider.IsLKEAPDeepSeekR1Model */
export function isLkeapDeepSeekR1Model(modelName: string): boolean {
  return modelName.toLowerCase().includes('deepseek-r1')
}

/** 与后端 provider.IsSiliconFlowDeepSeekV4Model 保持一致。 */
export function isSiliconFlowDeepSeekV4Model(modelName: string): boolean {
  return modelName.trim().toLowerCase() === 'deepseek-ai/deepseek-v4-flash'
}

/** 与后端 internal/models/provider.IsOpenAIReasoningOrGPT5Model 保持一致。 */
export function isOpenAIReasoningOrGPT5Model(modelName: string): boolean {
  const lower = modelName.trim().toLowerCase()
  if (!lower) return false
  if (lower.startsWith('gpt-5')) return true
  return ['o1', 'o3', 'o4'].some(prefix => lower === prefix || lower.startsWith(`${prefix}-`))
}

export type ThinkingControlValue =
  | 'none'
  | 'chat_template_kwargs'
  | 'enable_thinking'
  | 'thinking_type'
  | 'reasoning_effort'

export function shouldAutoUpdateThinkingControl(
  hasManualValue: boolean,
  providerChanged: boolean,
): boolean {
  return providerChanged || !hasManualValue
}

export function buildThinkingControlExtraConfig(control?: ThinkingControlValue): {
  extraConfig?: { thinking_control: ThinkingControlValue }
} {
  return control ? { extraConfig: { thinking_control: control } } : {}
}

const THINKING_CONTROL_VALUES: ThinkingControlValue[] = [
  'none',
  'chat_template_kwargs',
  'enable_thinking',
  'thinking_type',
  'reasoning_effort',
]

/**
 * Default thinking_control for provider+model.
 * Must stay aligned with chat.resolveProvider(...).Thinking() in provider.go.
 */
export function defaultThinkingControl(
  provider: string,
  modelName = '',
): ThinkingControlValue {
  const p = provider.trim().toLowerCase()
  const model = modelName.trim()

  switch (p) {
    case 'aliyun':
      return isQwenThinkingModel(model) ? 'enable_thinking' : 'none'
    case 'lkeap':
      // R1 系列后端不发 thinking 参数；其余（含未填模型名）按 LKEAP 的 thinking.type 格式预选
      if (model && isLkeapDeepSeekR1Model(model)) return 'none'
      return 'thinking_type'
    case 'siliconflow':
      return isSiliconFlowDeepSeekV4Model(model) ? 'enable_thinking' : 'none'
    case 'generic':
      return isOpenAIReasoningOrGPT5Model(model)
        ? 'reasoning_effort'
        : 'chat_template_kwargs'
    case 'nvidia':
      return 'chat_template_kwargs'
    case 'volcengine':
      return 'thinking_type'
    default:
      // openai, azure_openai, anthropic, zhipu, deepseek, gemini,
      // hunyuan, moonshot, openrouter, weknoracloud, … → baseProvider / noThinking
      return 'none'
  }
}

/** Resolve stored extra_config or fall back to the provider default. */
export function resolveThinkingControl(
  saved: string | undefined,
  provider: string,
  modelName = '',
): ThinkingControlValue {
  const v = saved?.trim().toLowerCase()
  if (THINKING_CONTROL_VALUES.includes(v as ThinkingControlValue)) {
    return v as ThinkingControlValue
  }
  return defaultThinkingControl(provider, modelName)
}

/** Whether the debug drawer should expose Think on/off for this saved model. */
export function modelSupportsThinking(model: {
  type: string
  source: string
  name: string
  parameters: {
    provider?: string
    extra_config?: { thinking_control?: string }
  }
}): boolean {
  if (model.type !== 'KnowledgeQA' || model.source !== 'remote') return false
  return resolveThinkingControl(
    model.parameters.extra_config?.thinking_control,
    model.parameters.provider || '',
    model.name || '',
  ) !== 'none'
}
