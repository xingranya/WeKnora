import assert from 'node:assert/strict'
import test from 'node:test'

import {
  buildThinkingControlExtraConfig,
  defaultThinkingControl,
  resolveThinkingControl,
  shouldAutoUpdateThinkingControl,
} from './thinkingControl.ts'

// Cases mirror internal/models/chat/provider_test.go TestResolveProvider thinking defaults.
test('defaultThinkingControl matches backend provider adapters', () => {
  const cases: Array<[string, string, ReturnType<typeof defaultThinkingControl>]> = [
    ['generic', 'anything', 'chat_template_kwargs'],
    ['generic', 'gpt-5.6-terra', 'reasoning_effort'],
    ['generic', 'o3-mini', 'reasoning_effort'],
    ['generic', 'gpt-4o', 'chat_template_kwargs'],
    ['nvidia', 'anything', 'chat_template_kwargs'],
    ['volcengine', 'doubao', 'thinking_type'],
    ['aliyun', 'qwen3-32b', 'enable_thinking'],
    ['aliyun', 'qwen-plus', 'enable_thinking'],
    ['aliyun', 'gpt-4', 'none'],
    ['lkeap', '', 'thinking_type'],
    ['lkeap', 'deepseek-v3.1', 'thinking_type'],
    ['lkeap', 'deepseek-r1', 'none'],
    ['openai', 'gpt-4o', 'none'],
    ['openai', 'gpt-5', 'none'],
    ['azure_openai', 'gpt-4', 'none'],
    ['deepseek', 'deepseek-chat', 'none'],
    ['zhipu', 'glm-4', 'none'],
    ['gemini', 'gemini-2.0', 'none'],
    ['siliconflow', 'qwen3-8b', 'none'],
    ['siliconflow', 'deepseek-ai/DeepSeek-V4-Flash', 'enable_thinking'],
    ['siliconflow', 'deepseek-ai/DeepSeek-V4-Pro', 'none'],
    ['siliconflow', 'deepseek-ai/DeepSeek-V4', 'none'],
    ['siliconflow', 'Pro/deepseek-ai/DeepSeek-V4-Flash', 'none'],
    ['siliconflow', 'vendor/deepseek-v4-flash-copy', 'none'],
    ['hunyuan', 'hunyuan-turbo', 'none'],
    ['moonshot', 'moonshot-v1-8k', 'none'],
    ['weknoracloud', 'anything', 'none'],
  ]
  for (const [provider, model, want] of cases) {
    assert.equal(
      defaultThinkingControl(provider, model),
      want,
      `${provider}/${model}`,
    )
  }
})

test('resolveThinkingControl preserves valid saved values and repairs missing values', () => {
  assert.equal(resolveThinkingControl('thinking_type', 'siliconflow', 'deepseek-ai/DeepSeek-V4-Flash'), 'thinking_type')
  assert.equal(resolveThinkingControl(undefined, 'siliconflow', 'deepseek-ai/DeepSeek-V4-Pro'), 'none')
  assert.equal(resolveThinkingControl('invalid', 'siliconflow', 'Qwen/Qwen3.5'), 'none')
})

test('thinking control follows model changes only until the user chooses a value', () => {
  assert.equal(shouldAutoUpdateThinkingControl(false, false), true)
  assert.equal(shouldAutoUpdateThinkingControl(true, false), false)
  assert.equal(shouldAutoUpdateThinkingControl(true, true), true)
})

test('thinking control test payload uses the backend extraConfig contract', () => {
  assert.deepEqual(buildThinkingControlExtraConfig('enable_thinking'), {
    extraConfig: { thinking_control: 'enable_thinking' },
  })
  assert.deepEqual(buildThinkingControlExtraConfig(undefined), {})
})
