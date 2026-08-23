import assert from 'node:assert/strict'
import test from 'node:test'

import { prepareModelUpdatePayload } from './modelUpdatePayload'

test('普通部分更新不会补写未传字段', () => {
  const patch = { display_name: '新名称' }
  assert.deepEqual(prepareModelUpdatePayload(patch), patch)
})

test('主编辑表单的完整请求会显式清除空请求头和并发上限', () => {
  assert.deepEqual(
    prepareModelUpdatePayload({
      name: 'gpt-4o',
      type: 'KnowledgeQA',
      source: 'remote',
      parameters: {
        base_url: 'https://example.com/v1',
        provider: 'openai',
      },
    }),
    {
      name: 'gpt-4o',
      type: 'KnowledgeQA',
      source: 'remote',
      parameters: {
        base_url: 'https://example.com/v1',
        provider: 'openai',
        custom_headers: {},
        max_concurrency: 0,
      },
    },
  )
})

test('主编辑表单传入的请求头和并发上限保持不变', () => {
  assert.deepEqual(
    prepareModelUpdatePayload({
      name: 'gpt-4o',
      type: 'KnowledgeQA',
      source: 'remote',
      parameters: {
        custom_headers: { 'X-Route': 'primary' },
        max_concurrency: 8,
      },
    }),
    {
      name: 'gpt-4o',
      type: 'KnowledgeQA',
      source: 'remote',
      parameters: {
        custom_headers: { 'X-Route': 'primary' },
        max_concurrency: 8,
      },
    },
  )
})
