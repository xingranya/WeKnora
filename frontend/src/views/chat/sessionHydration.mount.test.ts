import assert from 'node:assert/strict'
import test from 'node:test'
import { Window } from 'happy-dom'

import {
  createSessionGenerationGuard,
  settleRestoredSessionIdentity,
  type SessionHydrationSettings,
} from './sessionHydration'

const browserWindow = new Window({ url: 'http://localhost/' })
const browserGlobals = {
  window: browserWindow,
  document: browserWindow.document,
  navigator: browserWindow.navigator,
  Node: browserWindow.Node,
  Element: browserWindow.Element,
  HTMLElement: browserWindow.HTMLElement,
  SVGElement: browserWindow.SVGElement,
  Event: browserWindow.Event,
  MutationObserver: browserWindow.MutationObserver,
}
for (const [name, value] of Object.entries(browserGlobals)) {
  Object.defineProperty(globalThis, name, { configurable: true, writable: true, value })
}

const { flushPromises, mount } = await import('@vue/test-utils')
const { defineComponent, h, onMounted, onUnmounted, ref } = await import('vue')

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

test('真实 Vue 挂载后等待资源加载，再重放会话模型', async () => {
    const resources = deferred()
    const settings: SessionHydrationSettings = {
      selectedAgentSourceTenantId: null,
      conversationModels: { selectedChatModelId: 'global-model' },
    }
    const guard = createSessionGenerationGuard()

    const Harness = defineComponent({
      setup() {
        const state = ref('pending')
        const token = guard.begin('session-a')

        onMounted(async () => {
          const applied = await settleRestoredSessionIdentity({
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
          state.value = applied ? 'applied' : 'stale'
        })
        onUnmounted(guard.invalidate)

        return () => h('output', { 'data-state': state.value }, settings.conversationModels.selectedChatModelId)
      },
    })

    const wrapper = mount(Harness, { attachTo: document.body })
    assert.equal(wrapper.get('output').attributes('data-state'), 'pending')

    resources.resolve()
    await flushPromises()

    assert.equal(wrapper.get('output').attributes('data-state'), 'applied')
    assert.equal(wrapper.get('output').text(), 'session-model')
    assert.equal(settings.selectedAgentSourceTenantId, 'tenant-a')
    wrapper.unmount()
})

test('Vue 组件卸载使延迟 hydration 失效', async () => {
    const resources = deferred()
    const settings: SessionHydrationSettings = {
      selectedAgentSourceTenantId: null,
      conversationModels: { selectedChatModelId: 'global-model' },
    }
    const guard = createSessionGenerationGuard()

    const Harness = defineComponent({
      setup() {
        const token = guard.begin('session-a')
        onMounted(() => settleRestoredSessionIdentity({
          token,
          isCurrent: guard.isCurrent,
          state: { model_id: 'stale-model' },
          settings,
          ensureAgentResources: () => resources.promise,
          flushWatchers: async () => undefined,
        }))
        onUnmounted(guard.invalidate)
        return () => h('output', settings.conversationModels.selectedChatModelId)
      },
    })

    const wrapper = mount(Harness)
    wrapper.unmount()
    resources.resolve()
    await flushPromises()

    assert.equal(settings.conversationModels.selectedChatModelId, 'global-model')
})

test('用户先发送后释放旧恢复，迟到状态不能覆盖新请求模型', async () => {
    const resources = deferred()
    const settings: SessionHydrationSettings = {
      selectedAgentSourceTenantId: null,
      conversationModels: { selectedChatModelId: 'initial-model' },
    }
    const guard = createSessionGenerationGuard()
    const sentQueries: string[] = []

    const Harness = defineComponent({
      setup() {
        const token = guard.begin('session-a')
        onMounted(() => settleRestoredSessionIdentity({
          token,
          isCurrent: guard.isCurrent,
          state: { model_id: 'stale-restored-model' },
          settings,
          ensureAgentResources: () => resources.promise,
          flushWatchers: async () => undefined,
        }))
        const send = (query: string) => {
          guard.begin('session-a')
          settings.conversationModels.selectedChatModelId = 'user-selected-model'
          sentQueries.push(query)
        }
        return () => h('button', { onClick: () => send('new question') }, 'send')
      },
    })

    const wrapper = mount(Harness)
    await wrapper.get('button').trigger('click')
    resources.resolve()
    await flushPromises()

    assert.deepEqual(sentQueries, ['new question'])
    assert.equal(settings.conversationModels.selectedChatModelId, 'user-selected-model')
    wrapper.unmount()
})
