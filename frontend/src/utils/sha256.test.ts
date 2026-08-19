import assert from 'node:assert/strict'
import test from 'node:test'
import { sha256BlobHex } from './sha256'

test('Web Crypto 不可用时仍可计算标准 SHA-256', async () => {
  const digest = await sha256BlobHex(new Blob(['abc']), undefined)

  assert.equal(
    digest,
    'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad',
  )
})
