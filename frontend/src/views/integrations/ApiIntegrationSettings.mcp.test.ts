import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer, type ViteDevServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp, nextTick } from 'vue'
import { renderToString } from 'vue/server-renderer'

let server: ViteDevServer
let component: any
let state: any
let translate: (key: string, values?: Record<string, string>) => string
const descriptors = new Map(['window', 'localStorage'].map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]))
const secret = 'sk-complete-secret-for-mcp-1234567890'
const validKey = { id: 12, name: '检索专用', api_key: 'sk-abcd...wxyz', full_access: false, capabilities: ['retrieve'], knowledge_base_ids: ['kb-approved'], created_at: '' }

before(async () => {
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { origin: 'https://kb.example.com' } } })
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => null } })
  const mocks: Record<string, string> = {
    '@/test-mcp-state': `import { reactive } from 'vue'; export const state = reactive({ auth: { effectiveTenantId: '7' }, keys: [], copied: [], revealCalls: [], listResponse: null, reveal: async () => ({ success: false }) });`,
    'vue-i18n': `import locale from '/src/i18n/locales/zh-CN.ts'; export const t = (key, values = {}) => { let value = key.split('.').reduce((o, k) => o?.[k], locale); return typeof value === 'string' ? value.replace(/\\{(\\w+)\\}/g, (m, k) => values[k] ?? m) : key }; export const useI18n = () => ({t});`,
    'tdesign-vue-next': `export const MessagePlugin = { error() {}, success() {}, warning() {} }; export const DialogPlugin = { confirm() { return { destroy() {} } } };`,
    '@/stores/auth': `import { state } from '@/test-mcp-state'; export const useAuthStore = () => state.auth;`,
    '@/utils/clipboard': `import { state } from '@/test-mcp-state'; export async function copyWithToast(text) { state.copied.push(text); return true; }`,
    '@/utils/api-base': `export const getApiBaseUrl = () => '/proxy';`,
    '@/api/auth': `import { state } from '@/test-mcp-state'; export const getCurrentUser = async () => ({ data: { tenant: { id: Number(state.auth.effectiveTenantId) } } });`,
    '@/api/agent': `export const listAgents = async () => ({ data: [] }); export const BUILTIN_SMART_REASONING_ID = 'smart';`,
    '@/api/knowledge-base': `export const listKnowledgeBases = async () => ({ data: [] });`,
    '@/components/settings/SettingDrawer.vue': `export default { render: () => null };`,
    '@/api/tenant': `import { state } from '@/test-mcp-state';
      export const listTenantAPIKeys = async () => state.listResponse || ({ success: true, data: state.keys });
      export const revealTenantAPIKey = async (tenant, key) => { state.revealCalls.push([tenant, key]); return state.reveal(tenant, key); };
      export const getAPIPrincipalConfig = async () => ({ success: true, data: { mode: 'tenant' } });
      export const createTenantAPIKey = async () => ({}); export const deleteTenantAPIKey = async () => ({});
      export const createAPIPrincipalTestToken = async () => ({}); export const updateTenantAPIKey = async () => ({}); export const updateAPIPrincipalConfig = async () => ({});`,
  }
  server = await createServer({
    configFile: false,
    plugins: [{ name: 'mcp-prompt-api-mocks', enforce: 'pre',
      resolveId(id) { if (id.startsWith('\0mock:')) return id },
      load(id) { if (id.startsWith('\0mock:')) return mocks[id.slice(6)] },
    }, vue()],
    optimizeDeps: { noDiscovery: true, entries: [] },
    resolve: { alias: [
      ...Object.keys(mocks).map(find => ({ find, replacement: '\0mock:' + find })),
      { find: '@', replacement: fileURLToPath(new URL('../../', import.meta.url)) },
    ] },
    server: { middlewareMode: true, hmr: false }, appType: 'custom',
  })
  ;({ state } = await server.ssrLoadModule('\0mock:@/test-mcp-state'))
  ;({ t: translate } = await server.ssrLoadModule('\0mock:vue-i18n'))
  component = (await server.ssrLoadModule('/src/views/integrations/ApiIntegrationSettings.vue')).default
})

after(async () => {
  await server?.close()
  for (const [name, descriptor] of descriptors) {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor)
    else Reflect.deleteProperty(globalThis, name)
  }
})

async function page() {
  state.auth.effectiveTenantId = '7'
  state.keys = [{ ...validKey }]
  state.listResponse = null
  state.copied = []
  state.revealCalls = []
  state.reveal = async () => ({ success: true, data: { token: secret } })
  let bindings: any
  const app = createSSRApp({ ...component, setup(props: any, context: any) {
    bindings = component.setup(props, context)
    return bindings
  } })
  app.config.globalProperties.$t = translate as typeof app.config.globalProperties.$t
  app.config.warnHandler = () => undefined
  await renderToString(app)
  await bindings.load()
  bindings.selectedMCPKeyId.value = validKey.id
  await nextTick()
  return bindings
}

test('API Key 页面仅在点击后即时读取密钥并复制 MCP 提示词，明文不写页面状态', async () => {
  const bindings = await page()
  assert.equal(state.revealCalls.length, 0)
  await bindings.copyMCPSetupPrompt()
  assert.deepEqual([...state.revealCalls[0]], [7, 12])
  assert.equal(state.copied.length, 1)
  assert.ok(state.copied[0].includes(secret))
  assert.ok(state.copied[0].includes('https://kb.example.com/proxy'))
  assert.equal(bindings.apiKey.value, '')
  assert.equal(bindings.apiKeys.value[0].api_key, validKey.api_key)
  assert.ok(!JSON.stringify(bindings.mcpKeyOptions.value).includes(secret))
})

test('reveal 返回拒绝、掩码或占位值时不写剪贴板', async () => {
  for (const response of [{ success: false }, { success: true, data: { token: 'sk-abcd...wxyz' } }, { success: true, data: { token: '<API_KEY>' } }]) {
    const bindings = await page()
    state.reveal = async () => response
    await bindings.copyMCPSetupPrompt()
    assert.equal(state.copied.length, 0)
    assert.ok(bindings.mcpSetupError.value)
  }
})

test('过期或列表已撤销的 Key 不触发明文读取', async () => {
  for (const keys of [[], [{ ...validKey, expires_at: '2000-01-01T00:00:00Z' }]]) {
    const bindings = await page()
    state.keys = keys
    await bindings.loadAPIKeys()
    await bindings.copyMCPSetupPrompt()
    assert.equal(state.revealCalls.length, 0)
    assert.equal(state.copied.length, 0)
  }
})

test('密钥读取期间切换空间或刷新列表时不复制迟到凭据', async () => {
  for (const change of ['workspace', 'list']) {
    const bindings = await page()
    let resolve!: (value: any) => void
    state.reveal = () => new Promise(done => { resolve = done })
    const pending = bindings.copyMCPSetupPrompt()
    await nextTick()
    if (change === 'workspace') state.auth.effectiveTenantId = '8'
    else await bindings.loadAPIKeys()
    await nextTick()
    resolve({ success: true, data: { token: secret } })
    await pending
    assert.equal(state.copied.length, 0)
  }
})

test('刷新列表失败时清除旧选择并禁止读取旧 Key', async () => {
  const bindings = await page()
  state.listResponse = { success: false, message: 'network failed' }
  await bindings.loadAPIKeys()
  await bindings.copyMCPSetupPrompt()
  assert.equal(bindings.apiKeys.value.length, 0)
  assert.ok(bindings.apiKeysError.value)
  assert.equal(state.revealCalls.length, 0)
  assert.equal(state.copied.length, 0)
})
