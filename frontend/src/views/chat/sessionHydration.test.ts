import assert from 'node:assert/strict'
import test from 'node:test'

import {
  applyRestoredSessionIdentity,
  createSessionGenerationGuard,
  settleRestoredSessionIdentity,
  type SessionHydrationSettings,
} from './sessionHydration.ts'

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

const createSettings = (): SessionHydrationSettings => ({
  selectedAgentSourceTenantId: 'stale-tenant',
  conversationModels: {
    summaryModelId: '',
    selectedChatModelId: 'global-model',
  },
})

test('A-B-A 快速切换时旧 A 请求不能重新成为当前代际', () => {
  const guard = createSessionGenerationGuard()
  const firstA = guard.begin('session-a')
  const sessionB = guard.begin('session-b')
  const secondA = guard.begin('session-a')

  assert.equal(guard.isCurrent(firstA), false)
  assert.equal(guard.isCurrent(sessionB), false)
  assert.equal(guard.isCurrent(secondA), true)
})

test('恢复共享智能体来源空间，并在字段缺失时清理旧值', () => {
  const settings = createSettings()
  applyRestoredSessionIdentity(settings, {
    model_id: 'session-model',
    agent_source_tenant_id: 10000,
  })

  assert.equal(settings.conversationModels.selectedChatModelId, 'session-model')
  assert.equal(settings.selectedAgentSourceTenantId, '10000')

  applyRestoredSessionIdentity(settings, { model_id: 'own-agent-model' })
  assert.equal(settings.selectedAgentSourceTenantId, null)
})

test('agent 资源异步 watcher 覆盖后重新应用会话模型', async () => {
  const guard = createSessionGenerationGuard()
  const token = guard.begin('session-a')
  const settings = createSettings()
  const resources = deferred()

  const settling = settleRestoredSessionIdentity({
    token,
    isCurrent: guard.isCurrent,
    state: { model_id: 'session-model', agent_source_tenant_id: 'tenant-a' },
    settings,
    ensureAgentResources: async () => {
      await resources.promise
      settings.conversationModels.selectedChatModelId = 'agent-default-model'
    },
    flushWatchers: async () => undefined,
  })

  resources.resolve()
  assert.equal(await settling, true)
  assert.equal(settings.conversationModels.selectedChatModelId, 'session-model')
  assert.equal(settings.selectedAgentSourceTenantId, 'tenant-a')
})

test('旧会话的延迟资源结果不能覆盖新会话', async () => {
  const guard = createSessionGenerationGuard()
  const token = guard.begin('session-a')
  const settings = createSettings()
  const resources = deferred()

  const settling = settleRestoredSessionIdentity({
    token,
    isCurrent: guard.isCurrent,
    state: { model_id: 'old-session-model', agent_source_tenant_id: 'tenant-a' },
    settings,
    ensureAgentResources: () => resources.promise,
    flushWatchers: async () => undefined,
  })

  guard.begin('session-b')
  settings.conversationModels.selectedChatModelId = 'new-session-model'
  settings.selectedAgentSourceTenantId = null
  resources.resolve()

  assert.equal(await settling, false)
  assert.equal(settings.conversationModels.selectedChatModelId, 'new-session-model')
  assert.equal(settings.selectedAgentSourceTenantId, null)
})
