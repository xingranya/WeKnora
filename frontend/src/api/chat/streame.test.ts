import assert from 'node:assert/strict'
import test, { mock } from 'node:test'

class MemoryStorage {
  private values = new Map<string, string>()

  getItem(key: string) {
    return this.values.get(key) ?? null
  }

  setItem(key: string, value: string) {
    this.values.set(key, String(value))
  }
}

const storage = new MemoryStorage()
storage.setItem('weknora_token', 'test-token')
Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: storage })
Object.defineProperty(globalThis, 'window', {
  configurable: true,
  value: { __RUNTIME_CONFIG__: {} },
})

await mock.module('vue', {
  namedExports: {
    ref: <T>(value: T) => ({ value }),
    onUnmounted: () => undefined,
  },
})
await mock.module(new URL('../../i18n/index.ts', import.meta.url).href, {
  defaultExport: {
    global: {
      locale: { value: 'zh-CN' },
      t: (key: string) => key,
    },
  },
})
await mock.module(new URL('../../utils/index.ts', import.meta.url).href, {
  namedExports: {
    generateRandomString: () => 'request-id',
  },
})
await mock.module(new URL('../../utils/api-base.ts', import.meta.url).href, {
  namedExports: {
    getApiBaseUrl: () => 'http://localhost',
  },
})

const {
  buildStreamHTTPErrorMessage,
  isTerminalStreamEvent,
  readBoundedResponseText,
  useStream,
} = await import('./streame.ts')

test('工具失败事件只在 done=true 时结束整个流', () => {
  assert.equal(isTerminalStreamEvent({ response_type: 'error', done: false }), false)
  assert.equal(isTerminalStreamEvent({ response_type: 'error', done: true }), true)
  assert.equal(isTerminalStreamEvent({ response_type: 'complete', done: false }), true)
  assert.equal(isTerminalStreamEvent({ response_type: 'answer', done: true }), false)
})

test('答案完成后继续接收引用、complete 和会话标题', async () => {
  const chunks: string[] = []
  let signalAbortedAfterComplete = false
  const stream = useStream({
    fetchEventSource: (async (_url: string, options: any) => {
      await options.onopen(new Response(null, { status: 200 }))
      options.onmessage({ data: '{"response_type":"answer","content":"answer","done":true}' })
      options.onmessage({ data: '{"response_type":"references","content":"refs","done":true}' })
      options.onmessage({ data: '{"response_type":"complete","done":true}' })
      signalAbortedAfterComplete = options.signal.aborted
      options.onmessage({ data: '{"response_type":"session_title","content":"new title","done":true}' })
      options.onclose()
    }) as any,
  })
  stream.onChunk((chunk) => chunks.push(`${chunk.response_type}:${chunk.content || ''}`))

  await stream.startStream(baseParams)

  assert.equal(signalAbortedAfterComplete, false)
  assert.deepEqual(chunks, [
    'answer:answer',
    'references:refs',
    'complete:',
    'session_title:new title',
  ])
  assert.equal(stream.error.value, null)
  assert.equal(stream.isStreaming.value, false)
})

const baseParams = {
  session_id: 'session-a',
  query: 'hello',
  method: 'POST',
  url: '/api/v1/knowledge-chat',
}

test('正常 EOF 未收到明确终态时失败并结束流状态', async () => {
  const stream = useStream({
    fetchEventSource: (async (_url: string, options: any) => {
      await options.onopen(new Response(null, { status: 200 }))
      options.onmessage({ data: '{"response_type":"answer","content":"partial","done":false}' })
      options.onclose()
    }) as any,
  })

  await stream.startStream(baseParams)

  assert.equal(stream.error.value, 'error.streamFailed')
  assert.equal(stream.isStreaming.value, false)
  assert.equal(stream.isLoading.value, false)
})

test('主动 Abort 静默结束且不写错误', async () => {
  let started!: () => void
  const ready = new Promise<void>((resolve) => { started = resolve })
  const stream = useStream({
    fetchEventSource: ((_url: string, options: any) => new Promise<void>((_resolve, reject) => {
      started()
      options.signal.addEventListener('abort', () => {
        reject(new DOMException('aborted', 'AbortError'))
      }, { once: true })
    })) as any,
  })

  const pending = stream.startStream(baseParams)
  await ready
  stream.stopStream()
  await pending

  assert.equal(stream.error.value, null)
  assert.equal(stream.isStreaming.value, false)
})

test('旧流回调不能污染新会话流', async () => {
  const calls: Array<{ options: any; resolve: () => void }> = []
  const chunks: string[] = []
  const stream = useStream({
    fetchEventSource: ((_url: string, options: any) => new Promise<void>((resolve) => {
      calls.push({ options, resolve })
    })) as any,
  })
  stream.onChunk((chunk) => chunks.push(`${chunk.response_type}:${chunk.content || ''}`))

  const first = stream.startStream(baseParams)
  await new Promise<void>((resolve) => queueMicrotask(resolve))
  const second = stream.startStream({ ...baseParams, session_id: 'session-b' })
  await new Promise<void>((resolve) => queueMicrotask(resolve))

  calls[0].options.onmessage({ data: '{"response_type":"answer","content":"old","done":false}' })
  calls[1].options.onmessage({ data: '{"response_type":"answer","content":"new","done":false}' })
  calls[1].options.onmessage({ data: '{"response_type":"complete","done":true}' })
  calls[0].resolve()
  calls[1].resolve()
  await Promise.all([first, second])

  assert.deepEqual(chunks, ['answer:new', 'complete:'])
  assert.equal(stream.error.value, null)
  assert.equal(stream.isStreaming.value, false)
})

test('非 2xx 响应优先解析 JSON 错误消息', async () => {
  const stream = useStream({
    fetchEventSource: (async (_url: string, options: any) => {
      await options.onopen(new Response(
        JSON.stringify({ error: { message: 'quota exceeded' } }),
        { status: 429, statusText: 'Too Many Requests' },
      ))
    }) as any,
  })

  await stream.startStream(baseParams)
  assert.equal(stream.error.value, 'HTTP 429: quota exceeded')
})

test('非 2xx 文本错误体按字节上限读取并取消剩余内容', async () => {
  let cancelled = false
  const response = new Response(new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode('x'.repeat(128)))
    },
    cancel() {
      cancelled = true
    },
  }), { status: 500 })

  const body = await readBoundedResponseText(response, 32)
  assert.equal(body, 'x'.repeat(32))
  assert.equal(cancelled, true)

  const message = await buildStreamHTTPErrorMessage(new Response('upstream unavailable', { status: 502 }))
  assert.equal(message, 'HTTP 502: upstream unavailable')
})
