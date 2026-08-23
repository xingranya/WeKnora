import assert from 'node:assert/strict'
import test from 'node:test'

import { classifyTenantDeleteError } from './deleteTenantError'

test('空间删除冲突会被识别为需要先清理知识库', () => {
  assert.deepEqual(
    classifyTenantDeleteError({
      status: 409,
      error: { code: 1005, message: 'server message' },
    }),
    {
      blockedByResources: true,
      code: 1005,
      status: 409,
      message: 'server message',
    },
  )
})

test('普通删除失败保留服务端消息且不会误判为资源冲突', () => {
  assert.deepEqual(
    classifyTenantDeleteError({ status: 500, message: 'temporary failure' }),
    {
      blockedByResources: false,
      code: undefined,
      status: 500,
      message: 'temporary failure',
    },
  )
})
