import assert from 'node:assert/strict'
import test, { mock } from 'node:test'
import { createPinia, setActivePinia } from 'pinia'
import type {
  UploadQueueDependencies,
  UploadQueueTask,
} from './uploadQueue'

class MemoryStorage {
  private values = new Map<string, string>()

  getItem(key: string) {
    return this.values.get(key) ?? null
  }

  setItem(key: string, value: string) {
    this.values.set(key, String(value))
  }

  removeItem(key: string) {
    this.values.delete(key)
  }

  clear() {
    this.values.clear()
  }
}

const storage = new MemoryStorage()
Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: storage })
Object.defineProperty(globalThis, 'window', {
  configurable: true,
  value: {
    __RUNTIME_CONFIG__: {},
    dispatchEvent: () => true,
  },
})

await mock.module(new URL('../i18n/index.ts', import.meta.url).href, {
  defaultExport: {
    global: {
      t: (key: string) => key,
    },
  },
})
await mock.module(new URL('../api/knowledge-base/index.ts', import.meta.url).href, {
  namedExports: {
    cancelKnowledgeUpload: async () => undefined,
    cancelKnowledgeParse: async () => undefined,
    completeKnowledgeUpload: async () => undefined,
    getKnowledgeDetails: async () => undefined,
    getKnowledgeUpload: async () => undefined,
    initializeKnowledgeUpload: async () => undefined,
    reparseKnowledge: async () => undefined,
    uploadKnowledgePart: async () => undefined,
  },
})

const { createUploadQueueStore } = await import('./uploadQueue')

const baseTask = (overrides: Partial<UploadQueueTask> = {}): UploadQueueTask => ({
  id: 'task-1',
  batchId: 'batch-1',
  kbId: 'kb-1',
  kbName: '测试知识库',
  fileName: 'report.txt',
  fileSize: 3,
  mimeType: 'text/plain',
  lastModified: 1,
  targetFolder: '',
  status: 'waiting_parse',
  confirmedBytes: 3,
  displayBytes: 3,
  speedBps: 0,
  etaSeconds: null,
  uploadId: 'upload-1',
  knowledgeId: 'knowledge-1',
  createdAt: 1,
  ...overrides,
})

const createDependencies = (
  overrides: Partial<UploadQueueDependencies> = {},
): UploadQueueDependencies => ({
  cancelKnowledgeUpload: async () => ({ success: true }),
  cancelKnowledgeParse: async () => ({ success: true }),
  completeKnowledgeUpload: async () => ({ data: { id: 'knowledge-1' } }),
  getKnowledgeDetails: async () => ({ data: { parse_status: 'completed' } }),
  getKnowledgeUpload: async () => ({
    data: {
      id: 'upload-1',
      knowledge_base_id: 'kb-1',
      file_name: 'report.txt',
      file_size: 3,
      mime_type: 'text/plain',
      last_modified: 1,
      folder_path: '',
      chunk_size: 3,
      received_bytes: 3,
      received_parts: [0],
      received_part_hashes: { 0: 'hash' },
      status: 'completed',
      knowledge_id: 'knowledge-1',
      expires_at: '2099-01-01T00:00:00Z',
    },
  }),
  initializeKnowledgeUpload: async () => ({
    data: {
      id: 'upload-1',
      knowledge_base_id: 'kb-1',
      file_name: 'report.txt',
      file_size: 3,
      mime_type: 'text/plain',
      last_modified: 1,
      folder_path: '',
      chunk_size: 3,
      received_bytes: 0,
      received_parts: [],
      status: 'created',
      expires_at: '2099-01-01T00:00:00Z',
    },
  }),
  reparseKnowledge: async () => ({ success: true }),
  uploadKnowledgePart: async () => ({ success: true }),
  hashBlob: async () => 'hash',
  sleep: async () => undefined,
  now: () => 0,
  parseStatusUnknownAfterMs: 30 * 60 * 1000,
  normalPollIntervalMs: 2000,
  unknownPollIntervalMs: 10000,
  dispatchKnowledgeFileUploaded: () => undefined,
  ...overrides,
})

let storeSequence = 0
const createStore = (dependencies: UploadQueueDependencies) => {
  setActivePinia(createPinia())
  const useStore = createUploadQueueStore(`uploadQueue-test-${storeSequence++}`, dependencies)
  return useStore()
}

const eventually = async (condition: () => boolean, timeoutMs = 300) => {
  const deadline = Date.now() + timeoutMs
  while (!condition()) {
    if (Date.now() >= deadline) throw new Error('等待队列状态超时')
    await new Promise(resolve => setTimeout(resolve, 0))
  }
}

test('批量清空只移除成功记录并持久化，保留失败、取消和状态未知任务', () => {
  storage.clear()
  storage.setItem('weknora_user', JSON.stringify({ id: 'bulk-user' }))
  storage.setItem('weknora_selected_tenant_id', '10000')
  const otherOwner = 'weknora.uploadQueue.v1:another-user:10000'
  storage.setItem(otherOwner, '[{"id":"other-record"}]')
  let cancelCalls = 0
  const dependencies = createDependencies({
    cancelKnowledgeUpload: async () => { cancelCalls++; return { success: true } },
    cancelKnowledgeParse: async () => { cancelCalls++; return { success: true } },
  })
  const store = createStore(dependencies)
  store.hydrate()
  store.tasks = [
    baseTask({ id: 'success-1', status: 'completed' }),
    baseTask({ id: 'success-2', status: 'completed' }),
    baseTask({ id: 'failed', status: 'failed' }),
    baseTask({ id: 'cancelled', status: 'cancelled' }),
    baseTask({ id: 'unknown', status: 'status_unknown' }),
  ]

  assert.equal(store.clearCompleted(), 2)
  assert.equal(store.completedCount, 0)
  assert.equal(store.failedCount, 1)
  assert.deepEqual(store.tasks.map(task => task.id), ['failed', 'cancelled', 'unknown'])
  const saved = JSON.parse(storage.getItem('weknora.uploadQueue.v1:bulk-user:10000')!)
  assert.deepEqual(saved.map((task: UploadQueueTask) => task.id), ['failed', 'cancelled', 'unknown'])
  assert.equal(storage.getItem(otherOwner), '[{"id":"other-record"}]')
  assert.equal(cancelCalls, 0)
  assert.equal(store.clearCompleted(), 0)
  storage.clear()
})

test('一键重试只处理失败任务，缺失原文件时可继续选择，重复点击不重复提交', async () => {
  let resolveDetails!: (value: any) => void
  const details = new Promise(resolve => { resolveDetails = resolve })
  let parseChecks = 0
  const dependencies = createDependencies({
    getKnowledgeDetails: async () => { parseChecks++; return details },
  })
  const store = createStore(dependencies)
  store.activeTaskId = 'running'
  store.tasks = [
    baseTask({ id: 'failed-parse', status: 'failed' }),
    baseTask({ id: 'failed-upload', status: 'failed', knowledgeId: undefined, file: new File(['abc'], 'report.txt') }),
    baseTask({ id: 'missing-file', status: 'failed', knowledgeId: undefined }),
    baseTask({ id: 'paused', status: 'paused' }),
    baseTask({ id: 'completed', status: 'completed' }),
    baseTask({ id: 'unknown', status: 'status_unknown' }),
  ]
  const result = store.retryFailed()
  assert.equal(result.retried, 2)
  assert.equal(result.needsFile, 1)
  assert.equal(store.tasks.find(task => task.id === 'failed-parse')?.status, 'waiting_parse')
  assert.equal(store.tasks.find(task => task.id === 'failed-upload')?.status, 'queued')
  assert.equal(store.tasks.find(task => task.id === 'missing-file')?.status, 'needs_file')
  assert.equal(store.tasks.find(task => task.id === 'paused')?.status, 'paused')
  assert.equal(store.tasks.find(task => task.id === 'unknown')?.status, 'status_unknown')
  assert.equal(store.tasks.find(task => task.id === 'completed')?.status, 'completed')
  assert.equal(store.retryFailed().retried, 0)
  assert.equal(parseChecks, 1)
  resolveDetails({ data: { parse_status: 'completed' } })
  await eventually(() => store.tasks.find(task => task.id === 'failed-parse')?.status === 'completed')
})

test('刷新后保留失败分类，重试时缺失原文件不会卡在无人执行的排队状态', () => {
  storage.clear()
  storage.setItem('weknora_user', JSON.stringify({ id: 'restore-user' }))
  storage.setItem('weknora_selected_tenant_id', '10000')
  storage.setItem('weknora.uploadQueue.v1:restore-user:10000', JSON.stringify([
    baseTask({ status: 'failed', knowledgeId: undefined, uploadId: undefined, error: '网络断开' }),
  ]))
  const store = createStore(createDependencies())
  store.hydrate()
  assert.equal(store.failedCount, 1)
  assert.equal(store.tasks[0].status, 'failed')
  assert.equal(store.retryFailed().needsFile, 1)
  assert.equal(store.tasks[0].status, 'needs_file')
  storage.clear()
})

for (const status of ['expired', 'expired_cleanup_pending']) {
  test(`过期会话 ${status} 重试时新建会话，从头上传并保留目录与标签`, async () => {
    let initialized: any
    const uploads: Array<{ session: string; offset: number }> = []
    const dependencies = createDependencies({
      getKnowledgeUpload: async () => ({ data: {
        ...(await createDependencies().getKnowledgeUpload('kb-1', 'expired-upload')).data,
        status, knowledge_id: undefined,
      } }),
      initializeKnowledgeUpload: async (_kbId, payload) => {
        initialized = payload
        return { data: {
          ...(await createDependencies().initializeKnowledgeUpload('kb-1', payload)).data,
          id: 'new-upload', chunk_size: 2,
        } }
      },
      uploadKnowledgePart: async (_kbId, session, _part, _blob, offset) => {
        uploads.push({ session, offset })
        return { success: true }
      },
    })
    const store = createStore(dependencies)
    store.tasks = [baseTask({
      status: 'queued', knowledgeId: undefined, uploadId: 'expired-upload',
      file: new File(['abc'], 'report.txt', { type: 'text/plain', lastModified: 1 }),
      targetFolder: '资料/开发', tagIds: ['tag-1'],
    })]
    await store.runTask('task-1')
    await eventually(() => store.tasks[0].status === 'completed')
    assert.equal(initialized.folder_path, '资料/开发')
    assert.deepEqual(initialized.tag_ids, ['tag-1'])
    assert.deepEqual(uploads, [{ session: 'new-upload', offset: 0 }, { session: 'new-upload', offset: 2 }])
    assert.equal(store.tasks[0].uploadId, 'new-upload')
  })
}

test('过期会话查询返回前已取消时不创建新的上传会话', async () => {
  let resolveSession!: (value: any) => void
  const response = new Promise(resolve => { resolveSession = resolve })
  let initializeCalls = 0
  const dependencies = createDependencies({
    getKnowledgeUpload: async () => response,
    initializeKnowledgeUpload: async (_kbId, payload) => {
      initializeCalls++
      return createDependencies().initializeKnowledgeUpload('kb-1', payload)
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({ status: 'queued', knowledgeId: undefined, file: new File(['abc'], 'report.txt') })]
  const running = store.runTask('task-1')
  await store.cancel('task-1')
  resolveSession({ data: { id: 'expired-upload', status: 'expired' } })
  await running
  assert.equal(store.tasks[0].status, 'cancelled')
  assert.equal(initializeCalls, 0)
})

test('解析状态查询短暂失败后保持状态未知并继续跟踪服务端终态', async () => {
  let calls = 0
  const dependencies = createDependencies({
    getKnowledgeDetails: async () => {
      calls++
      if (calls === 1) throw new Error('network offline')
      return { data: { parse_status: 'completed' } }
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask()]
  const observed: string[] = []
  store.$subscribe((_mutation, state) => observed.push(state.tasks[0]?.status || ''), { flush: 'sync' })

  await store.pollParsing('task-1', 'knowledge-1')

  assert.equal(calls, 2)
  assert.ok(observed.includes('status_unknown'))
  assert.equal(store.tasks[0].status, 'completed')
})

test('解析超过客户端观察窗口后转为状态未知但继续等待完成', async () => {
  let now = 0
  let calls = 0
  const dependencies = createDependencies({
    now: () => now,
    parseStatusUnknownAfterMs: 100,
    getKnowledgeDetails: async () => {
      calls++
      if (calls === 1) {
        now = 101
        return { data: { parse_status: 'processing' } }
      }
      return { data: { parse_status: 'completed' } }
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({ status: 'parsing' })]
  const observed: string[] = []
  store.$subscribe((_mutation, state) => observed.push(state.tasks[0]?.status || ''), { flush: 'sync' })

  await store.pollParsing('task-1', 'knowledge-1')

  assert.ok(observed.includes('status_unknown'))
  assert.equal(store.tasks[0].status, 'completed')
})

test('刷新恢复的状态未知任务在服务端确认前不会退回普通解析状态', async () => {
  let calls = 0
  const dependencies = createDependencies({
    getKnowledgeDetails: async () => {
      calls++
      return calls === 1
        ? { data: { parse_status: 'processing' } }
        : { data: { parse_status: 'completed' } }
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({ status: 'status_unknown' })]
  const observed: string[] = []
  store.$subscribe((_mutation, state) => observed.push(state.tasks[0]?.status || ''), { flush: 'sync' })

  await store.pollParsing('task-1', 'knowledge-1')

  assert.equal(observed.includes('parsing'), false)
  assert.equal(store.tasks[0].status, 'completed')
})

test('重试前先读取服务端，原解析仍在运行时不重复提交 reparse', async () => {
  let detailCalls = 0
  let reparseCalls = 0
  const dependencies = createDependencies({
    getKnowledgeDetails: async () => {
      detailCalls++
      return detailCalls === 1
        ? { data: { parse_status: 'processing' } }
        : { data: { parse_status: 'completed' } }
    },
    reparseKnowledge: async () => {
      reparseCalls++
      return { success: true }
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({ status: 'failed', error: '旧的客户端错误' })]

  await store.retryParsing('task-1', 'knowledge-1')

  assert.equal(reparseCalls, 0)
  assert.equal(store.tasks[0].status, 'completed')
})

test('重试查询返回前取消成功时不会再次提交解析', async () => {
  let resolveDetails!: (value: any) => void
  let reparseCalls = 0
  const details = new Promise(resolve => { resolveDetails = resolve })
  const dependencies = createDependencies({
    getKnowledgeDetails: async () => details,
    reparseKnowledge: async () => {
      reparseCalls++
      return { success: true }
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({ status: 'failed' })]

  const retry = store.retryParsing('task-1', 'knowledge-1')
  await eventually(() => store.tasks[0].status === 'waiting_parse')
  await store.cancel('task-1')
  resolveDetails({ data: { parse_status: 'failed' } })
  await retry

  assert.equal(reparseCalls, 0)
  assert.equal(store.tasks[0].status, 'cancelled')
})

test('取消请求的迟到错误不会覆盖并发形成的完成终态', async () => {
  let store: ReturnType<ReturnType<typeof createUploadQueueStore>>
  const dependencies = createDependencies({
    cancelKnowledgeUpload: async () => {
      store.patch('task-1', { status: 'completed' })
      throw new Error('upload already completed')
    },
    getKnowledgeUpload: async () => {
      throw new Error('temporary network error')
    },
  })
  store = createStore(dependencies)
  store.tasks = [baseTask({ knowledgeId: undefined, status: 'completing' })]

  await store.cancel('task-1')

  assert.equal(store.tasks[0].status, 'completed')
})

test('取消结果暂时未知时自动复核服务端终态而不是标记失败', async () => {
  let cancelCalls = 0
  let sessionCalls = 0
  const dependencies = createDependencies({
    cancelKnowledgeUpload: async () => {
      cancelCalls++
      throw new Error('upload already completed')
    },
    getKnowledgeUpload: async () => {
      sessionCalls++
      if (sessionCalls === 1) throw new Error('temporary network error')
      return createDependencies().getKnowledgeUpload('kb-1', 'upload-1')
    },
    cancelKnowledgeParse: async () => {
      throw new Error('parse already completed')
    },
    getKnowledgeDetails: async () => ({ data: { parse_status: 'completed' } }),
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({ knowledgeId: undefined, status: 'completing' })]

  await store.cancel('task-1')
  await eventually(() => store.tasks[0].status === 'completed')

  assert.ok(cancelCalls >= 1)
  assert.equal(store.tasks[0].status, 'completed')
})

test('文档进入解析后立即释放上传槽，后续文件无需等待解析完成', async () => {
  let sessionSequence = 0
  let uploadedParts = 0
  const dependencies = createDependencies({
    initializeKnowledgeUpload: async (_kbId, payload) => {
      const sequence = ++sessionSequence
      return {
        data: {
          id: `upload-${sequence}`,
          knowledge_base_id: 'kb-1',
          file_name: payload.file_name,
          file_size: payload.file_size,
          mime_type: payload.mime_type || 'text/plain',
          last_modified: payload.last_modified || 1,
          folder_path: '',
          chunk_size: payload.file_size,
          received_bytes: 0,
          received_parts: [],
          status: 'created',
          expires_at: '2099-01-01T00:00:00Z',
        },
      }
    },
    uploadKnowledgePart: async () => {
      uploadedParts++
      return { success: true }
    },
    completeKnowledgeUpload: async (_kbId, uploadId) => ({
      data: { id: `knowledge-${uploadId}` },
    }),
    getKnowledgeDetails: async () => new Promise(() => undefined),
  })
  const store = createStore(dependencies)
  const files = [
    new File(['one'], 'one.txt', { type: 'text/plain', lastModified: 1 }),
    new File(['two'], 'two.txt', { type: 'text/plain', lastModified: 1 }),
  ]

  store.enqueueFiles({
    kbId: 'kb-1',
    kbName: '测试知识库',
    files,
    targetFolder: '',
    fileNames: files.map(file => file.name),
  })

  await eventually(() => uploadedParts === 2)
  assert.equal(uploadedParts, 2)
})

test('暂停后即使浏览器把中止包装成网络错误也保持暂停状态', async () => {
  let rejectUpload!: (error: Error) => void
  const uploadPending = new Promise((_resolve, reject) => { rejectUpload = reject })
  const dependencies = createDependencies({
    getKnowledgeUpload: async () => ({
      data: {
        ...(await createDependencies().getKnowledgeUpload('kb-1', 'upload-1')).data,
        status: 'uploading',
        received_bytes: 0,
        received_parts: [],
        received_part_hashes: {},
        knowledge_id: undefined,
      },
    }),
    uploadKnowledgePart: async () => uploadPending,
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({
    status: 'queued',
    confirmedBytes: 0,
    displayBytes: 0,
    knowledgeId: undefined,
    file: new File(['abc'], 'report.txt', { type: 'text/plain', lastModified: 1 }),
  })]

  const running = store.runTask('task-1')
  await eventually(() => store.tasks[0].status === 'uploading')
  store.pause('task-1')
  rejectUpload(new Error('网络错误，请检查您的网络连接'))
  await running

  assert.equal(store.tasks[0].status, 'paused')
  assert.equal(store.tasks[0].error, undefined)
})

test('完成清理中的会话幂等调用完成接口且不重传分片', async () => {
  let completeCalls = 0
  let uploadCalls = 0
  const dependencies = createDependencies({
    getKnowledgeUpload: async () => ({
      data: {
        ...(await createDependencies().getKnowledgeUpload('kb-1', 'upload-1')).data,
        status: 'completed_cleanup_pending',
      },
    }),
    completeKnowledgeUpload: async () => {
      completeCalls++
      return { data: { id: 'knowledge-1' } }
    },
    uploadKnowledgePart: async () => {
      uploadCalls++
      return { success: true }
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({
    status: 'queued',
    knowledgeId: undefined,
    file: new File(['abc'], 'report.txt', { type: 'text/plain', lastModified: 1 }),
  })]

  await store.runTask('task-1')
  await eventually(() => store.tasks[0].status === 'completed')

  assert.equal(completeCalls, 1)
  assert.equal(uploadCalls, 0)
})

test('取消清理中的会话恢复时继续取消接口完成服务端清理', async () => {
  let cancelCalls = 0
  const dependencies = createDependencies({
    getKnowledgeUpload: async () => ({
      data: {
        ...(await createDependencies().getKnowledgeUpload('kb-1', 'upload-1')).data,
        status: 'cancelled_cleanup_pending',
        knowledge_id: undefined,
      },
    }),
    cancelKnowledgeUpload: async () => {
      cancelCalls++
      return { success: true }
    },
  })
  const store = createStore(dependencies)
  store.tasks = [baseTask({ status: 'needs_file', knowledgeId: undefined })]

  await store.recoverPersistedSessions()

  assert.equal(cancelCalls, 1)
  assert.equal(store.tasks[0].status, 'cancelled')
})
