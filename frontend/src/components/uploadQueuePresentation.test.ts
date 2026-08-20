import assert from 'node:assert/strict'
import test from 'node:test'
import {
  isKnowledgeBaseUploadRoute,
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

test('上传队列入口只在知识库文档页显示', () => {
  assert.equal(isKnowledgeBaseUploadRoute('knowledgeBaseDetail'), true)
  assert.equal(isKnowledgeBaseUploadRoute('home'), true)
  assert.equal(isKnowledgeBaseUploadRoute('knowledgeBaseList'), false)
  assert.equal(isKnowledgeBaseUploadRoute('globalCreatChat'), false)
  assert.equal(isKnowledgeBaseUploadRoute('kbCreatChat'), false)
  assert.equal(isKnowledgeBaseUploadRoute('chat'), false)
  assert.equal(isKnowledgeBaseUploadRoute('settings'), false)
})
