import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, nextTick, reactive, toRefs } from 'vue'

const require = createRequire(import.meta.url)
const { descriptor } = parse(readFileSync(new URL('./UploadQueueHost.vue', import.meta.url), 'utf8'))
const script = compileScript(descriptor, { id: 'upload-queue-host-test' }).content
  .replace('__expose();', '')
  .replace('return __returned__', '__expose(__returned__); return __returned__')
const compiled = ts.transpileModule(script, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

async function fixture(needsFile = false) {
  const notices: string[] = []
  let clearCalls = 0
  let retryCalls = 0
  const queue = reactive({
    tasks: [
      { id: 'success', status: 'completed', createdAt: 1 },
      { id: 'failed', status: 'failed', createdAt: 2 },
      { id: 'running', status: 'uploading', createdAt: 3 },
    ],
    activeCount: 1, unfinishedCount: 2,
    get completedCount() { return this.tasks.filter((task: { status: string }) => task.status === 'completed').length },
    get failedCount() { return this.tasks.filter((task: { status: string }) => task.status === 'failed').length },
    hydrate() {},
    clearCompleted() {
      clearCalls++
      this.tasks = this.tasks.filter(task => task.status !== 'completed')
      return 1
    },
    retryFailed() {
      retryCalls++
      this.tasks.find(task => task.id === 'failed')!.status = needsFile ? 'needs_file' : 'queued'
      return { retried: needsFile ? 0 : 1, needsFile: needsFile ? 1 : 0 }
    },
  })
  const exports: any = {}
  runInNewContext(compiled, {
    exports,
    require(name: string) {
      if (name === 'vue') return require('vue')
      if (name === 'vue-router') return { useRoute: () => ({ name: 'knowledgeBaseDetail' }) }
      if (name === 'pinia') return { storeToRefs: toRefs }
      if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
      if (name === 'tdesign-vue-next') return {
        MessagePlugin: { success: (value: string) => notices.push(value), warning: (value: string) => notices.push(value) },
      }
      if (name === '@/stores/auth') return { useAuthStore: () => ({ isLoggedIn: true, effectiveTenantId: 10000 }) }
      if (name === '@/stores/uploadQueue') return { useUploadQueueStore: () => queue }
      if (name === './uploadQueuePresentation') return require('./uploadQueuePresentation')
      throw new Error(name)
    },
  })
  exports.default.render = () => null
  const renderer = createRenderer<any, any>({
    createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
    insert() {}, remove() {}, setElementText() {}, setText() {}, patchProp() {},
    parentNode: () => null, nextSibling: () => null,
  })
  const app = renderer.createApp(exports.default)
  const vm: any = app.mount({})
  await nextTick()
  return { vm, queue, notices, clearCalls: () => clearCalls, retryCalls: () => retryCalls, close: () => app.unmount() }
}

test('队列界面筛选成功和失败任务，并保留全部任务的显示顺序与计数', async () => {
  const f = await fixture()
  try {
    assert.deepEqual(Array.from(f.vm.orderedTasks, (task: any) => task.id), ['running', 'failed', 'success'])
    f.vm.statusFilter = 'completed'
    assert.deepEqual(Array.from(f.vm.orderedTasks, (task: any) => task.id), ['success'])
    f.vm.statusFilter = 'failed'
    assert.deepEqual(Array.from(f.vm.orderedTasks, (task: any) => task.id), ['failed'])
    assert.deepEqual(Array.from(f.vm.statusFilters, (filter: any) => filter.count), [3, 1, 1])
    assert.deepEqual(f.queue.tasks.map(task => task.id), ['success', 'failed', 'running'])
  } finally { f.close() }
})

test('清空成功记录后显示对应空状态，失败任务和批量重试入口仍保留', async () => {
  const f = await fixture()
  try {
    f.vm.statusFilter = 'completed'
    f.vm.clearCompleted()
    assert.equal(f.clearCalls(), 1)
    assert.equal(f.vm.orderedTasks.length, 0)
    assert.equal(f.vm.emptyMessage, 'knowledgeBase.uploadQueue.emptyCompleted')
    assert.equal(f.vm.completedCount, 0)
    assert.equal(f.vm.failedCount, 1)
    f.vm.retryFailed()
    assert.equal(f.retryCalls(), 1)
    assert.equal(f.vm.failedCount, 0)
    assert.ok(f.notices.includes('knowledgeBase.uploadQueue.retryStarted'))
  } finally { f.close() }
})

test('失败任务需要原文件时切回全部视图，避免文件选择入口被筛选隐藏', async () => {
  const f = await fixture(true)
  try {
    f.vm.statusFilter = 'failed'
    f.vm.retryFailed()
    assert.equal(f.vm.statusFilter, 'all')
    assert.equal(f.vm.orderedTasks.length, 3)
    assert.equal(f.queue.tasks.find(task => task.id === 'failed')?.status, 'needs_file')
    assert.ok(f.notices.includes('knowledgeBase.uploadQueue.retryNeedsFile'))
  } finally { f.close() }
})
