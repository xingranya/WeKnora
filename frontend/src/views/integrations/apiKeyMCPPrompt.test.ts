import assert from 'node:assert/strict'
import test from 'node:test'
import { buildAPIKeyMCPPrompt, mcpKeyAvailability } from './apiKeyMCPPrompt'
import { JIWAI_CLI_RELEASE, resolveCLIServiceRoot } from './cliIntegration'
import type { TenantAPIKey } from '@/api/tenant'
import zhCN from '@/i18n/locales/zh-CN'
import enUS from '@/i18n/locales/en-US'
import jaJP from '@/i18n/locales/ja-JP'
import koKR from '@/i18n/locales/ko-KR'
import ruRU from '@/i18n/locales/ru-RU'

const key: TenantAPIKey = {
  id: 12, name: '检索专用', api_key: 'sk-abcd...wxyz', full_access: false,
  capabilities: ['retrieve'], knowledge_base_ids: ['kb-approved'], created_at: '',
}
const secret = 'sk-complete-secret-value-1234567890'
const translate = (_key: string, values: Record<string, string>) =>
  zhCN.integrations.api.mcpSetup.prompt.replace(/\{(\w+)\}/g, (match, name) => values[name] ?? match)
const options = {
  apiBaseUrl: 'https://kb.example.com/team/api/v1', origin: 'https://ui.example.com',
  tenantId: 7, key, token: secret, translate,
}

test('MCP 使用真实服务根地址并保留反向代理前缀', () => {
  assert.equal(resolveCLIServiceRoot('/proxy/api/v1/', 'https://kb.example.com'), 'https://kb.example.com/proxy')
  const prompt = buildAPIKeyMCPPrompt(options)
  assert.match(prompt, /服务根地址：https:\/\/kb\.example\.com\/team\n/)
  assert.match(prompt, /weknora profile add jiwai-mcp-7-12 --host 'https:\/\/kb\.example\.com\/team'/)
  assert.match(prompt, /weknora --profile jiwai-mcp-7-12 auth login --with-token/)
  assert.doesNotMatch(prompt, /--use|--password|--api-key/)
  assert.equal(prompt.split(secret).length - 1, 1)
  assert.doesNotMatch(prompt, /sk-abcd\.\.\.wxyz/)
  assert.match(prompt, /"args": \[\s*"--profile",\s*"jiwai-mcp-7-12",\s*"mcp",\s*"serve"/)
  assert.match(prompt, /kb-approved/)
  assert.ok(prompt.includes(JIWAI_CLI_RELEASE.version))
  assert.ok(prompt.includes(JIWAI_CLI_RELEASE.upstreamCommit))
  assert.ok(prompt.includes(JIWAI_CLI_RELEASE.patchUrl))
  assert.ok(prompt.includes(JIWAI_CLI_RELEASE.patchSHA256))
  assert.ok(prompt.includes(JIWAI_CLI_RELEASE.buildFlags))
})

test('MCP 子进程清空环境凭据和地址，使用所选 profile 且不复制密钥到配置', () => {
  const configJSON = buildAPIKeyMCPPrompt({ ...options, translate: (_key, values) => values.mcpConfig! })
  const config = JSON.parse(configJSON)
  assert.deepEqual(config.mcpServers['jiwai-mcp-7-12'], {
    command: 'weknora',
    args: ['--profile', 'jiwai-mcp-7-12', 'mcp', 'serve'],
    env: { WEKNORA_TOKEN: '', WEKNORA_API_KEY: '', WEKNORA_HOST: '' },
  })
  assert.doesNotMatch(configJSON, new RegExp(secret))
})

test('连接未解析或带 URL 凭据时禁止生成可复制提示词', () => {
  for (const [apiBaseUrl, origin] of [
    ['', 'https://kb.example.com'], ['/api/v1', 'null'], ['/api/v1', 'wails://wails.localhost'],
    ['https://your-server.com/api/v1', 'https://kb.example.com'],
    ['https://user:secret@kb.example.com/api/v1', 'https://kb.example.com'],
  ]) assert.throws(() => buildAPIKeyMCPPrompt({ ...options, apiBaseUrl: apiBaseUrl!, origin: origin! }))
})

test('掩码、占位值和空 Key 不能进入 MCP 配置提示词', () => {
  for (const token of ['', 'sk-abc********tail', 'sk-abcd...wxyz', '<API_KEY>', 'YOUR_API_KEY',
    'your-api-key-here', 'sk-YOUR_API_KEY', '[API_KEY]', '${API_KEY}', 'placeholder', 'not a token']) {
    assert.throws(() => buildAPIKeyMCPPrompt({ ...options, token }), /invalid_api_key/)
  }
})

test('过期、撤销列表值、平台 Key 与无检索能力均不可选，完整权限保持可用', () => {
  const now = Date.parse('2026-09-11T00:00:00Z')
  assert.equal(mcpKeyAvailability(key, now), 'available')
  assert.equal(mcpKeyAvailability({ ...key, expires_at: '2026-09-10T00:00:00Z' }, now), 'expired')
  assert.equal(mcpKeyAvailability({ ...key, expires_at: 'invalid' }, now), 'expired')
  assert.equal(mcpKeyAvailability({ ...key, api_key: '' }, now), 'unavailable')
  assert.equal(mcpKeyAvailability({ ...key, scope_type: 'platform' }, now), 'unavailable')
  assert.equal(mcpKeyAvailability({ ...key, capabilities: ['chat'] }, now), 'noRetrieval')
  assert.equal(mcpKeyAvailability({ ...key, full_access: true, capabilities: [] }, now), 'available')
  assert.throws(() => buildAPIKeyMCPPrompt({ ...options, key: { ...key, expires_at: '2000-01-01T00:00:00Z' } }), /unavailable_key/)
})

test('所有语言的提示词保留十工具范围、凭据与真实 MCP 验收要求', () => {
  for (const locale of [zhCN, enUS, jaJP, koKR, ruRU]) {
    const prompt = locale.integrations.api.mcpSetup.prompt
    for (const field of ['serviceRoot', 'apiKey', 'profileName', 'profileCommand', 'loginCommand', 'mcpConfig', 'capabilities', 'knowledgeBaseScope', 'cliVersion', 'cliSourceUrl', 'cliUpstreamCommit', 'cliPatchUrl', 'cliPatchSHA256', 'cliBuildFlags']) {
      assert.ok(prompt.includes(`{${field}}`), `missing ${field}`)
    }
    for (const tool of ['kb_list', 'kb_view', 'doc_list', 'doc_view', 'doc_download', 'search_chunks', 'chunk_list', 'agent_list', 'chat', 'session_ask']) {
      assert.ok(prompt.includes(tool), `missing ${tool}`)
    }
    assert.match(prompt, /1 MiB/)
    assert.match(prompt, /initialize/)
    assert.match(prompt, /tools\/list/)
    assert.match(prompt, /MCP_SERVER_AUTH_TOKEN/)
    assert.match(prompt, /weknora profile list --format json/)
    assert.match(prompt, /weknora profile use/)
    assert.match(prompt, /--profile \{profileName\}/)
    for (const envName of ['WEKNORA_TOKEN', 'WEKNORA_API_KEY', 'WEKNORA_HOST']) {
      assert.ok(prompt.includes(envName), `missing ${envName}`)
    }
    assert.match(prompt, /恢复失败不能报告完成|Do not report completion if restoration fails/)
    assert.match(prompt, /不要修改全局环境变量|do not change global environment variables/)
    assert.match(prompt, /Get-FileHash -Algorithm SHA256/)
    assert.match(prompt, /git apply --check/)
    assert.match(prompt, /LOCALAPPDATA\/Programs\/JiwaiCLI/)
    assert.match(prompt, /先备份目标位置已有程序|Back up an existing target binary/)
    assert.match(prompt, /最终二进制的绝对路径|final absolute binary path/)
  }
})
