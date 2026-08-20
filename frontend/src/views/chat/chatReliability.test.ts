import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const menuSource = readFileSync(new URL('../../components/menu.vue', import.meta.url), 'utf8')
const platformSource = readFileSync(new URL('../platform/index.vue', import.meta.url), 'utf8')

test('无首问时在父组件挂载后等待同一 hydration 并重放状态', () => {
  assert.match(source, /initialSessionHydrationPromise\s*=\s*loadSessionAndHydrate\(session_id\.value\)/)
  assert.match(source, /await initialSessionHydrationPromise;\s*await settlePendingSessionHydration\(\)/)
  assert.match(source, /watch\(\(\) => route\.params\.chatid,/)
  assert.doesNotMatch(source, /watch\(\[\(\) => route\.params\]/)
})

test('首问先同步消费且任何发送都会使迟到 hydration 失效', () => {
  const consumeIndex = source.indexOf("usemenuStore.changeFirstQuery('', [], '', [], [])")
  const sendIndex = source.indexOf('void sendMsg(', consumeIndex)
  const hydrationWaitIndex = source.indexOf('await initialSessionHydrationPromise;', sendIndex)
  assert.ok(consumeIndex > 0, '首问必须先从全局队列移除')
  assert.ok(sendIndex > consumeIndex, '首问清空后必须立即发送')
  assert.ok(hydrationWaitIndex > sendIndex, '首问不得等待可选 hydration')

  const sendFunctionIndex = source.indexOf('const sendMsg = async')
  const supersedeIndex = source.indexOf('supersedePendingSessionHydration();', sendFunctionIndex)
  const stopIndex = source.indexOf('stopStream();', sendFunctionIndex)
  assert.ok(supersedeIndex > sendFunctionIndex && supersedeIndex < stopIndex,
    '用户发送必须在停止旧流前先废弃迟到 hydration')
})

test('移动端聊天页提供可触控的侧栏展开入口并解除桌面最小宽度', () => {
  assert.match(menuSource, /class="menu_item sidebar-toggle-item"/)
  assert.match(menuSource, /@click="uiStore\.toggleSidebar"/)
  assert.match(menuSource, /\.aside_box--collapsed \.sidebar-toggle-item\s*\{\s*display:\s*flex;/)
  assert.match(platformSource, /window\.matchMedia\('\(max-width: 768px\)'\)/)
  assert.match(source, /@media \(max-width: 768px\)[\s\S]*?&:not\(\.is-embedded\)[\s\S]*?min-width:\s*0;/)
})

test('更早历史加载失败后停在显式重试态', () => {
  assert.match(source, /historyLoadMoreError\.value\s*\|\|\s*!hasMoreHistory\.value/)
  assert.match(source, /historyLoadMoreError\.value\s*=\s*err\?\.message\s*\|\|\s*t\('chat\.historyLoadMoreFailed'\)/)
  assert.match(source, /const retryHistoryLoadMore\s*=\s*\(\)\s*=>/)
})
