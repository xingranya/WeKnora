import assert from 'node:assert/strict'
import test from 'node:test'
import { extractRevealedAPIKeyToken } from './apiKeyReveal.ts'

test('即时读取接口只接受完整 API Key', () => {
  const token = 'sk-complete-secret-value-1234567890'
  assert.equal(extractRevealedAPIKeyToken({ success: true, data: { token } }), token)
})

test('即时读取接口拒绝星号和省略号脱敏值', () => {
  assert.equal(extractRevealedAPIKeyToken({ success: true, data: { token: 'sk-abc********tail' } }), '')
  assert.equal(extractRevealedAPIKeyToken({ success: true, data: { token: 'sk-abcd...wxyz' } }), '')
  assert.equal(extractRevealedAPIKeyToken({ success: false, data: { token: 'sk-complete' } }), '')
})
