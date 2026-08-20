import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'

const extensionDir = new URL('..', import.meta.url)
const networkSource = await readFile(new URL('network.js', extensionDir), 'utf8')
const popupAuthSource = await readFile(new URL('popup-auth.js', extensionDir), 'utf8')

const context = vm.createContext({
  console,
  setTimeout,
  clearTimeout,
  AbortController,
  Error,
  Promise
})
context.globalThis = context
vm.runInContext(networkSource, context, { filename: 'network.js' })
vm.runInContext(popupAuthSource, context, { filename: 'popup-auth.js' })

const network = context.JiwaiNetwork
const popupAuth = context.JiwaiPopupAuth

assert.equal(network.COMPANY_API_BASE, 'https://know.seeway.co/api/v1')
assert.equal(network.REQUEST_TIMEOUT_MS, 12000)
assert.equal(network.MESSAGE_TIMEOUT_MS, 15000)

const successResponse = { ok: true, status: 200 }
assert.equal(
  await network.requestWithTimeout(async () => successResponse, 'https://example.com', {}, 50),
  successResponse
)

await assert.rejects(
  network.requestWithTimeout((_url, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => {
      const error = new Error('aborted')
      error.name = 'AbortError'
      reject(error)
    }, { once: true })
  }), 'https://example.com/pending', {}, 10),
  (error) => error && error.code === 'REQUEST_TIMEOUT'
)

await assert.rejects(
  network.requestTextWithTimeout((_url, options) => Promise.resolve({
    text: () => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('body aborted')
        error.name = 'AbortError'
        reject(error)
      }, { once: true })
    })
  }), 'https://example.com/pending-body', {}, 10),
  (error) => error && error.code === 'REQUEST_TIMEOUT'
)

assert.equal(network.httpFailure(401, 'raw').errorCode, 'AUTH_INVALID')
assert.equal(network.httpFailure(403, 'raw').errorCode, 'AUTH_FORBIDDEN')
assert.equal(network.httpFailure(500, 'raw').errorCode, 'HTTP_ERROR')
assert.equal(network.fetchFailure(new TypeError('failed')).errorCode, 'NETWORK_UNREACHABLE')
assert.equal(network.fetchFailure({ code: 'REQUEST_TIMEOUT' }).errorCode, 'REQUEST_TIMEOUT')
assert.equal(network.invalidResponseFailure().errorCode, 'INVALID_RESPONSE')

const runtimeSuccess = {
  runtime: {
    lastError: null,
    sendMessage(_data, callback) { callback({ success: true }) }
  }
}
assert.equal((await network.sendRuntimeMessage(runtimeSuccess, { type: 'PING' }, 30)).success, true)

const runtimePending = {
  runtime: {
    lastError: null,
    sendMessage() {}
  }
}
assert.equal(
  (await network.sendRuntimeMessage(runtimePending, { type: 'PING' }, 10)).errorCode,
  'BACKGROUND_TIMEOUT'
)

const runtimeUnavailable = {
  runtime: {
    lastError: { message: 'service worker stopped' },
    sendMessage(_data, callback) { callback() }
  }
}
assert.equal(
  (await network.sendRuntimeMessage(runtimeUnavailable, { type: 'PING' }, 30)).errorCode,
  'BACKGROUND_UNAVAILABLE'
)

function controls() {
  return {
    button: { disabled: false, textContent: '连接知识库' },
    statusElement: { textContent: '', className: '' }
  }
}

async function runValidation(result) {
  const ui = controls()
  let call = 0
  const response = await popupAuth.validateCompanyConnection({
    apiKey: 'test-key',
    button: ui.button,
    statusElement: ui.statusElement,
    sendMessage: async () => {
      call++
      if (call === 1) return { success: true }
      if (call === 2) return result
      return { success: true }
    }
  })
  assert.equal(ui.button.disabled, false, '验证结束后必须恢复按钮')
  assert.equal(ui.button.textContent, '连接知识库')
  return { response, ...ui }
}

const authInvalid = await runValidation({ success: false, status: 401, errorCode: 'AUTH_INVALID' })
assert.match(authInvalid.statusElement.textContent, /API Key 无效/)

const authForbidden = await runValidation({ success: false, status: 403, errorCode: 'AUTH_FORBIDDEN' })
assert.match(authForbidden.statusElement.textContent, /权限不足/)

const serverError = await runValidation({ success: false, status: 500, errorCode: 'HTTP_ERROR' })
assert.match(serverError.statusElement.textContent, /HTTP 500/)

const requestTimeout = await runValidation({ success: false, errorCode: 'REQUEST_TIMEOUT' })
assert.match(requestTimeout.statusElement.textContent, /验证超时/)

const networkFailure = await runValidation({ success: false, errorCode: 'NETWORK_UNREACHABLE' })
assert.match(networkFailure.statusElement.textContent, /网络不可达/)

const backgroundTimeout = await runValidation({ success: false, errorCode: 'BACKGROUND_TIMEOUT' })
assert.match(backgroundTimeout.statusElement.textContent, /后台无响应/)

const backgroundUnavailable = await runValidation({ success: false, errorCode: 'BACKGROUND_UNAVAILABLE' })
assert.match(backgroundUnavailable.statusElement.textContent, /后台失联/)

const authPersistFailure = controls()
let authPersistCall = 0
const authPersistResult = await popupAuth.validateCompanyConnection({
  apiKey: 'test-key',
  button: authPersistFailure.button,
  statusElement: authPersistFailure.statusElement,
  sendMessage: async () => {
    authPersistCall++
    if (authPersistCall < 3) return { success: true }
    return { success: false, errorCode: 'BACKGROUND_UNAVAILABLE' }
  }
})
assert.equal(authPersistResult.success, false)
assert.match(authPersistFailure.statusElement.textContent, /后台失联/)
assert.equal(authPersistFailure.button.disabled, false)

const ui = controls()
let releaseValidation
let validationCall = 0
const validationPending = popupAuth.validateCompanyConnection({
  apiKey: 'test-key',
  button: ui.button,
  statusElement: ui.statusElement,
  sendMessage: async () => {
    validationCall++
    if (validationCall === 1) return { success: true }
    if (validationCall === 2) return new Promise((resolve) => { releaseValidation = resolve })
    return { success: true }
  }
})
await new Promise((resolve) => setTimeout(resolve, 0))
assert.equal(ui.button.disabled, true, '验证进行期间必须禁用按钮')
releaseValidation({ success: true })
assert.equal((await validationPending).success, true)
assert.equal(ui.button.disabled, false)
assert.match(ui.statusElement.textContent, /验证通过/)

console.log('EXTENSION_NETWORK_TEST_OK')
