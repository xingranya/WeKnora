import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'

const extensionDir = new URL('..', import.meta.url)
const collectionSource = await readFile(new URL('collection.js', extensionDir), 'utf8')
const backgroundSource = await readFile(new URL('background.js', extensionDir), 'utf8')

function createEvent() {
  return {
    listeners: [],
    addListener(listener) { this.listeners.push(listener) }
  }
}

const storageData = {}
const tabs = new Map()
let nextTabId = 100
let lastRequestBody = null

function selectStorage(keys) {
  if (keys == null) return { ...storageData }
  const list = Array.isArray(keys) ? keys : [keys]
  return list.reduce((result, key) => {
    if (Object.prototype.hasOwnProperty.call(storageData, key)) result[key] = storageData[key]
    return result
  }, {})
}

const chrome = {
  runtime: {
    lastError: null,
    onMessage: createEvent(),
    onInstalled: createEvent(),
    sendMessage() { return Promise.resolve() }
  },
  storage: {
    local: {
      get(keys, callback) {
        const result = selectStorage(keys)
        if (callback) callback(result)
        else return Promise.resolve(result)
      },
      set(value, callback) {
        Object.assign(storageData, value)
        if (callback) callback()
        else return Promise.resolve()
      },
      remove(keys, callback) {
        ;(Array.isArray(keys) ? keys : [keys]).forEach((key) => delete storageData[key])
        if (callback) callback()
        else return Promise.resolve()
      }
    },
    session: {
      setAccessLevel() { return Promise.resolve() }
    }
  },
  alarms: {
    onAlarm: createEvent(),
    create() { return Promise.resolve() },
    clear() { return Promise.resolve(true) }
  },
  tabs: {
    onRemoved: createEvent(),
    async create(options) {
      const tab = { id: nextTabId++, url: options.url, windowId: 1 }
      tabs.set(tab.id, tab)
      return tab
    },
    async update(tabId, options) {
      const tab = tabs.get(tabId)
      if (!tab) throw new Error('标签页不存在')
      Object.assign(tab, options)
      return tab
    },
    async get(tabId) {
      const tab = tabs.get(tabId)
      if (!tab) throw new Error('标签页不存在')
      return tab
    },
    async remove(tabId) { tabs.delete(tabId) },
    async reload() {},
    query(_options, callback) { callback([]) },
    sendMessage() { return Promise.resolve() },
    captureVisibleTab() { return Promise.resolve('data:image/jpeg;base64,AA==') }
  },
  scripting: {
    executeScript() { return Promise.resolve([]) }
  },
  action: {
    setBadgeText() { return Promise.resolve() },
    setBadgeBackgroundColor() { return Promise.resolve() }
  },
  contextMenus: {
    onClicked: createEvent(),
    removeAll(callback) { callback() },
    create(_options, callback) { if (callback) callback() },
    update(_id, _options, callback) { if (callback) callback() }
  },
  sidePanel: {
    setPanelBehavior() { return Promise.resolve() },
    open() { return Promise.resolve() }
  },
  commands: { onCommand: createEvent() }
}

const context = vm.createContext({
  chrome,
  console,
  fetch: async (_url, options) => {
    if (options && options.body) lastRequestBody = JSON.parse(options.body)
    return { ok: true, json: async () => ({ success: true, data: { id: 'knowledge-screenshot' } }) }
  },
  setTimeout,
  clearTimeout,
  TextDecoder,
  URL,
  Blob,
  FileReader: class {},
  importScripts() {}
})
context.globalThis = context

vm.runInContext(collectionSource, context, { filename: 'collection.js' })
vm.runInContext(backgroundSource, context, { filename: 'background.js' })
await new Promise((resolve) => setTimeout(resolve, 0))

assert.ok(
  context.scoreFrameExtractionCandidate({ adapterId: 'feishu', matchedSite: true, markdownLength: 10000 }) >
  context.scoreFrameExtractionCandidate({ adapterId: 'oceanengine-shell', matchedSite: false, markdownLength: 500, incomplete: true }),
  '完整飞书 frame 必须优先于巨量外壳'
)

let frameExtractionPass = 0
chrome.scripting.executeScript = async (options) => {
  if (options.files) return []
  const source = options.func ? options.func.toString() : ''
  if (source.includes('tt-docs-component')) {
    return [{ frameId: 0, result: { hasEmbeddedDocument: true, frameReady: true } }]
  }
  frameExtractionPass++
  const shell = {
    frameId: 0,
    result: {
      adapterId: 'oceanengine-shell',
      incomplete: true,
      matchedSite: false,
      markdownLength: 149
    }
  }
  if (frameExtractionPass === 1) return [shell]
  return [
    shell,
    {
      frameId: 7,
      result: {
        adapterId: 'feishu',
        incomplete: false,
        matchedSite: true,
        markdownLength: 7182,
        blockCount: 88,
        imageCount: 1
      }
    }
  ]
}
const frameResult = await context.extractAllFrames(88)
assert.equal(frameResult.success, true)
assert.equal(frameResult.data[0].frameId, 7, '第二轮就绪的飞书 frame 应替代首轮巨量外壳')
assert.equal(frameResult.data[0].adapterId, 'feishu')

vm.runInContext(`
  collectionDelay = async function () {};
  var __nestedDiscoveryPending = true;
  discoverCollectionPagesInTab = async function () {
    if (!__nestedDiscoveryPending) return { pages: [] };
    __nestedDiscoveryPending = false;
    return { pages: [{ url: 'https://docs.example.com/docs/three', title: '第三篇' }] };
  };
  readCollectionExtraction = async function (_tabId, pageUrl) {
    return {
      title: pageUrl.split('/').pop(),
      content: '# 正文\\n\\n' + pageUrl,
      url: pageUrl,
      adapterId: 'generic',
      imageCount: 0,
      blockCount: 1
    };
  };
  saveCollectionPage = async function (_task, page) {
    return 'knowledge-' + page.url.split('/').pop();
  };
`, context)

const startResult = await context.startDocumentCollection({
  title: '测试文档集',
  scope: 'https://docs.example.com',
  kbId: 'kb-1',
  kbName: '测试知识库',
  sourceTabId: 9,
  pages: [
    { url: 'https://docs.example.com/docs/one', title: '第一篇' },
    { url: 'https://docs.example.com/docs/two', title: '第二篇' },
    { url: 'https://docs.example.com/docs/one#duplicate', title: '重复项' }
  ]
}, {})

assert.equal(startResult.success, true)
assert.equal(startResult.data.total, 2, '启动时应按规范化 URL 去重')

const paused = await context.pauseDocumentCollection()
assert.equal(paused.data.status, 'paused')
const resumed = await context.resumeDocumentCollection()
assert.equal(resumed.data.status, 'running')

const workerTabId = resumed.data.workerTabId
await context.processDocumentCollectionPage({ tab: { id: workerTabId } }, { url: 'https://docs.example.com/docs/one' })
let task = await context.getDocumentCollectionTask()
assert.equal(task.total, 3, '处理分类页时应递归追加新文档')
assert.equal(task.completed, 1)
assert.equal(task.currentIndex, 1)

await context.processDocumentCollectionPage({ tab: { id: workerTabId } }, { url: 'https://docs.example.com/docs/two' })
await context.processDocumentCollectionPage({ tab: { id: workerTabId } }, { url: 'https://docs.example.com/docs/three' })
task = await context.getDocumentCollectionTask()

assert.equal(task.status, 'completed')
assert.equal(task.completed, 3)
assert.equal(task.failed, 0)
assert.equal(task.currentIndex, 3)
assert.equal(tabs.has(workerTabId), false, '完成后应关闭专用采集标签页')

storageData.clipKbId = 'kb-1'
storageData.clipKbName = '测试知识库'
storageData.ka_clips = [{ id: 'clip-screenshot' }]
const screenshotResult = await context.syncClipToKb({
  id: 'clip-screenshot',
  type: 'select-clip',
  title: '长截图',
  content: '选区正文',
  screenshot: 'data:image/jpeg;base64,AA==',
  meta: { url: 'https://docs.example.com/long' }
})
assert.equal(screenshotResult.synced, true)
assert.match(lastRequestBody.content, /!\[网页截取\]\(data:image\/jpeg;base64,AA==\)/, '截图应随 Markdown 写入知识库')

console.log('EXTENSION_BACKGROUND_TEST_OK')
