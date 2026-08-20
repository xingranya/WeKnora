export interface SessionGenerationToken {
  sessionId: string
  generation: number
}

export interface SessionLastRequestState {
  agent_id?: string
  agent_source_tenant_id?: string | number | null
  model_id?: string
  [key: string]: unknown
}

export interface SessionHydrationSettings {
  selectedAgentSourceTenantId: string | null
  conversationModels: {
    selectedChatModelId: string
    [key: string]: unknown
  }
}

export function createSessionGenerationGuard(initialSessionId = '') {
  let activeSessionId = initialSessionId
  let generation = 0

  const begin = (sessionId: string): SessionGenerationToken => {
    activeSessionId = sessionId
    generation += 1
    return { sessionId, generation }
  }

  const invalidate = () => {
    generation += 1
    activeSessionId = ''
  }

  const isCurrent = (token: SessionGenerationToken): boolean =>
    token.generation === generation && token.sessionId === activeSessionId

  return { begin, invalidate, isCurrent }
}

function normalizeSourceTenantId(value: unknown): string | null {
  if (typeof value !== 'string' && typeof value !== 'number') return null
  const normalized = String(value).trim()
  return normalized && normalized !== '0' ? normalized : null
}

/**
 * 重放会话身份相关字段。来源空间必须显式清空，避免自有智能体沿用上一会话的共享空间。
 */
export function applyRestoredSessionIdentity(
  settings: SessionHydrationSettings,
  state: SessionLastRequestState,
): void {
  settings.selectedAgentSourceTenantId = normalizeSourceTenantId(state.agent_source_tenant_id)
  if (state.model_id !== undefined) {
    settings.conversationModels = {
      ...settings.conversationModels,
      selectedChatModelId: state.model_id || '',
    }
  }
}

interface SettleSessionHydrationOptions {
  token: SessionGenerationToken
  isCurrent: (token: SessionGenerationToken) => boolean
  state: SessionLastRequestState
  settings: SessionHydrationSettings
  ensureAgentResources: () => Promise<void>
  flushWatchers: () => Promise<unknown>
}

/**
 * 等 agent 资源 watcher 消化完异步结果后，再重放服务端记录的会话模型。
 */
export async function settleRestoredSessionIdentity({
  token,
  isCurrent,
  state,
  settings,
  ensureAgentResources,
  flushWatchers,
}: SettleSessionHydrationOptions): Promise<boolean> {
  await ensureAgentResources()
  await flushWatchers()
  if (!isCurrent(token)) return false
  applyRestoredSessionIdentity(settings, state)
  return true
}
