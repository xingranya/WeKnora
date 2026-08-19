import assert from 'node:assert/strict'
import test from 'node:test'
import {
  uploadTaskCanCancel,
  uploadTaskCanRemove,
  uploadTaskCanRetry,
} from './uploadQueuePresentation'

test('失败任务可重试或独立移除，但不会再调用服务端取消', () => {
  assert.equal(uploadTaskCanRetry('failed'), true)
  assert.equal(uploadTaskCanRemove('failed'), true)
  assert.equal(uploadTaskCanCancel('failed'), false)
})

test('状态待确认的任务仍可取消，不能被误当成已完成记录移除', () => {
  assert.equal(uploadTaskCanCancel('status_unknown'), true)
  assert.equal(uploadTaskCanRemove('status_unknown'), false)
})
