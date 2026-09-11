import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer, type ViteDevServer } from 'vite'
import { createPinia } from 'pinia'

let server: ViteDevServer
let useBrowserConnectionStore: typeof import('./browserConnection').useBrowserConnectionStore
const runtime = globalThis as typeof globalThis & { __browserConnectionTestGet?: () => Promise<unknown> }

before(async () => {
  server = await createServer({
    configFile: false,
    plugins: [{
      name: 'browser-connection-store-test',
      enforce: 'pre',
      resolveId(id) { if (id.endsWith('/utils/request')) return '\0offline-request' },
      load(id) {
        if (id === '\0offline-request') {
          return 'const request = () => globalThis.__browserConnectionTestGet(); export { request as get, request as post, request as put, request as del };'
        }
      },
    }],
    optimizeDeps: { noDiscovery: true, entries: [] },
    resolve: { alias: { '@': fileURLToPath(new URL('../', import.meta.url)) } },
    server: { middlewareMode: true, hmr: false },
    appType: 'custom',
  })
  ;({ useBrowserConnectionStore } = await server.ssrLoadModule('/src/stores/browserConnection.ts'))
})
after(async () => { delete runtime.__browserConnectionTestGet; await server?.close() })

function documentFixture() {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'document')
  const listeners = new Set<() => void>()
  Object.defineProperty(globalThis, 'document', { configurable: true, value: {
    hidden: false,
    addEventListener: (_event: string, listener: () => void) => listeners.add(listener),
    removeEventListener: (_event: string, listener: () => void) => listeners.delete(listener),
  } })
  return {
    visible: () => { for (const listener of listeners) listener() },
    restore: () => {
      if (original) Object.defineProperty(globalThis, 'document', original)
      else Reflect.deleteProperty(globalThis, 'document')
    },
  }
}

const online = { data: { enabled: true, connected: true, extension_available: true } }
const flush = () => new Promise<void>(resolve => setImmediate(resolve))

test('composer keeps the browser source available until account status loads', () => {
  const store = useBrowserConnectionStore(createPinia())
  assert.equal(store.loaded, false)
  assert.equal(store.online, false)
  assert.equal(store.knownOffline, false)
})

test('an offline or unpaired extension cannot stay selected as a live source', () => {
  const store = useBrowserConnectionStore(createPinia())
  store.apply({
    enabled: true,
    connected: false,
    extension_available: true,
    device: { id: 'chrome', label: 'Chrome', last_seen_at: '' },
  })
  assert.equal(store.loaded, true)
  assert.equal(store.online, false)
  assert.equal(store.knownOffline, true)

  store.apply({ enabled: true, connected: false, extension_available: true })
  assert.equal(store.knownOffline, true)

  store.apply({ enabled: false, connected: false, extension_available: false })
  assert.equal(store.knownOffline, true)

  store.apply({
    enabled: true,
    connected: true,
    extension_available: true,
    device: { id: 'chrome', label: 'Chrome', last_seen_at: '' },
  })
  assert.equal(store.online, true)
  assert.equal(store.knownOffline, false)
})

test('撤销后的状态不会被更早的连接检查覆盖', async () => {
  const store = useBrowserConnectionStore(createPinia())
  let resolve!: (value: typeof online) => void
  runtime.__browserConnectionTestGet = () => new Promise(done => { resolve = done })
  const refreshing = store.refresh()
  store.apply({ enabled: true, connected: false, extension_available: true })
  resolve(online)
  await refreshing
  assert.equal(store.knownOffline, true)
})

test('页面反复恢复可见时只保留一个轮询请求和定时器', async (t) => {
  const doc = documentFixture()
  const store = useBrowserConnectionStore(createPinia())
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let calls = 0
  let resolve!: (value: typeof online) => void
  runtime.__browserConnectionTestGet = () => {
    calls += 1
    if (calls === 1) return new Promise(done => { resolve = done })
    return Promise.resolve(online)
  }
  try {
    store.watchStatus()
    doc.visible()
    doc.visible()
    assert.equal(calls, 1)
    resolve(online)
    await flush()
    doc.visible()
    await flush()
    assert.equal(calls, 2)
    t.mock.timers.tick(5000)
    await flush()
    assert.equal(calls, 3)
  } finally {
    store.unwatchStatus()
    doc.restore()
  }
})

test('离开最后一个页面会清除账号状态并忽略迟到响应', async () => {
  const doc = documentFixture()
  const store = useBrowserConnectionStore(createPinia())
  let resolve!: (value: typeof online) => void
  runtime.__browserConnectionTestGet = () => new Promise(done => { resolve = done })
  try {
    store.apply({ ...online.data, device: { id: 'old-browser', label: 'Previous account', last_seen_at: '' } })
    store.watchStatus()
    store.unwatchStatus()
    resolve(online)
    await flush()
    assert.equal(store.loaded, false)
    assert.equal(store.online, false)
    assert.equal(store.device, undefined)
    assert.equal(store.subscribers, 0)
  } finally { doc.restore() }
})
