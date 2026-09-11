import type { TenantAPIKey } from '@/api/tenant'
import { extractRevealedAPIKeyToken } from './apiKeyReveal'
import { JIWAI_CLI_RELEASE, resolveCLIServiceRoot } from './cliIntegration'

export type MCPKeyAvailability = 'available' | 'expired' | 'unavailable' | 'noRetrieval'

/** 只选当前列表中仍有效、具有知识库检索能力的 Key，不扩大原授权。 */
export function mcpKeyAvailability(key: TenantAPIKey, now = Date.now()): MCPKeyAvailability {
  if (!key.api_key?.trim() || key.scope_type === 'platform') return 'unavailable'
  if (key.expires_at) {
    const expiresAt = Date.parse(key.expires_at)
    if (!Number.isFinite(expiresAt) || expiresAt <= now) return 'expired'
  }
  if (!key.full_access && !key.capabilities?.includes('retrieve')) return 'noRetrieval'
  return 'available'
}

/** 只在即时读取成功后生成含凭据的文本；调用方不得把结果放进页面或持久状态。 */
export function buildAPIKeyMCPPrompt(options: {
  apiBaseUrl: string
  origin: string
  tenantId: number
  key: TenantAPIKey
  token: string
  translate: (key: string, values: Record<string, string>) => string
}): string {
  const { tenantId, key, translate } = options
  const serviceRoot = resolveCLIServiceRoot(options.apiBaseUrl, options.origin)
  const token = extractRevealedAPIKeyToken({ success: true, data: { token: options.token } })
  if (!serviceRoot || new URL(serviceRoot).hostname === 'your-server.com') throw new Error('invalid_service')
  if (!Number.isSafeInteger(tenantId) || tenantId <= 0 || !Number.isSafeInteger(key.id) || key.id <= 0 ||
    mcpKeyAvailability(key) !== 'available') throw new Error('unavailable_key')
  if (!token || /[\s<>{}\[\]…]|\.{3}/.test(token) || /^(?:sk-)?(?:your[_-]?(?:api[_-]?)?key|api[_-]?key|placeholder)(?:[_-].*)?$/i.test(token)) {
    throw new Error('invalid_api_key')
  }
  const profileName = `jiwai-mcp-${tenantId}-${key.id}`
  const quotedHost = `'${serviceRoot.replace(/'/g, `'"'"'`)}'`
  const profileCommand = `weknora profile add ${profileName} --host ${quotedHost}`
  const loginCommand = `weknora --profile ${profileName} auth login --with-token`
  const mcpConfig = JSON.stringify({
    mcpServers: {
      [profileName]: {
        command: 'weknora',
        args: ['--profile', profileName, 'mcp', 'serve'],
        env: { WEKNORA_TOKEN: '', WEKNORA_API_KEY: '', WEKNORA_HOST: '' },
      },
    },
  }, null, 2)
  return translate('integrations.api.mcpSetup.prompt', {
    serviceRoot,
    apiKey: token,
    profileName,
    profileCommand,
    loginCommand,
    mcpConfig,
    cliVersion: JIWAI_CLI_RELEASE.version,
    cliSourceUrl: JIWAI_CLI_RELEASE.sourceUrl,
    cliUpstreamCommit: JIWAI_CLI_RELEASE.upstreamCommit,
    cliPatchUrl: JIWAI_CLI_RELEASE.patchUrl,
    cliPatchSHA256: JIWAI_CLI_RELEASE.patchSHA256,
    cliBuildFlags: JIWAI_CLI_RELEASE.buildFlags,
    capabilities: key.full_access ? 'full_access' : (key.capabilities || []).join(', '),
    knowledgeBaseScope: JSON.stringify(key.knowledge_base_ids || []),
  })
}
