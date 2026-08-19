import assert from 'node:assert/strict'
import test from 'node:test'
import { generateUUID } from './randomUUID'

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

test('generateUUID 在 randomUUID 不可用时使用 getRandomValues', () => {
  const value = generateUUID({
    getRandomValues(bytes) {
      bytes.forEach((_, index) => { bytes[index] = index })
      return bytes
    },
  })
  assert.match(value, UUID_V4)
  assert.equal(value, '00010203-0405-4607-8809-0a0b0c0d0e0f')
})

test('generateUUID 在 Web Crypto 完全不可用时仍返回 UUID v4', () => {
  assert.match(generateUUID({}), UUID_V4)
})
