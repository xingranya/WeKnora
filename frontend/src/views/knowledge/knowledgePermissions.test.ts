import assert from 'node:assert/strict'
import test from 'node:test'
import { knowledgeDocumentActionVisibility, knowledgeMutationAllowed } from './knowledgePermissions'

test('共享 Editor 可以上传文档，但不会获得文件夹管理入口', () => {
  assert.deepEqual(knowledgeDocumentActionVisibility(true, false), {
    showDocumentActions: true,
    showFolderManagement: false,
  })
})

test('共享 Editor 仍受当前空间 Contributor 角色下限约束', () => {
  assert.equal(knowledgeMutationAllowed(true, true, false, false, false), false)
  assert.equal(knowledgeMutationAllowed(true, true, false, false, true), true)
})

test('知识库管理者同时拥有上传和文件夹管理入口', () => {
  assert.deepEqual(knowledgeDocumentActionVisibility(true, true), {
    showDocumentActions: true,
    showFolderManagement: true,
  })
})

test('共享知识库管理员也不显示仅源空间所有者可用的目录管理入口', () => {
  assert.deepEqual(knowledgeDocumentActionVisibility(true, true, true), {
    showDocumentActions: true,
    showFolderManagement: false,
  })
})
