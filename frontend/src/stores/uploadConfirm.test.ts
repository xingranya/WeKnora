import assert from 'node:assert/strict'
import test from 'node:test'
import { createPinia, setActivePinia } from 'pinia'
import { useUploadConfirmStore, type UploadConfirmResult } from './uploadConfirm'

const processConfig = {
  chunking_config: {
    chunk_size: 512,
    chunk_overlap: 50,
    separators: ['\n\n'],
    enable_parent_child: false,
    parent_chunk_size: 1024,
    child_chunk_size: 256,
  },
}

test('resolves one confirmation and clears all pending state', async () => {
  setActivePinia(createPinia())
  const store = useUploadConfirmStore()
  const file = new File(['a'], 'a.txt', { type: 'text/plain' })
  const pending = store.open({
    mode: 'file',
    kbInfo: { id: 'kb-a' },
    files: [file],
    targetFolder: 'reports',
  })
  const result: UploadConfirmResult = {
    processConfig,
    mode: 'file',
    files: [file],
    targetFolder: 'reports',
  }

  store.resolveConfirm(result)

  assert.deepEqual(await pending, result)
  assert.equal(store.visible, false)
  assert.equal(store.pendingResolve, null)
  assert.equal(store.pendingReject, null)
  assert.deepEqual(store.files, [])
  assert.equal(store.requestRevision, 1)
})

test('a newer confirmation rejects the superseded caller before replacing its payload', async () => {
  setActivePinia(createPinia())
  const store = useUploadConfirmStore()
  const first = store.open({
    mode: 'reparse',
    kbInfo: { id: 'kb-a' },
    reparse: { knowledgeId: 'knowledge-a' },
  })
  const firstOutcome = first.then(
    () => 'resolved',
    error => error === undefined ? 'cancelled' : 'failed',
  )

  const second = store.open({
    mode: 'reparse',
    kbInfo: { id: 'kb-b' },
    reparse: { knowledgeId: 'knowledge-b' },
  })

  assert.equal(await firstOutcome, 'cancelled')
  assert.equal(store.visible, true)
  assert.equal(store.requestRevision, 2)
  assert.equal(store.kbInfo.id, 'kb-b')
  assert.equal(store.reparse?.knowledgeId, 'knowledge-b')

  const secondResult: UploadConfirmResult = {
    processConfig,
    mode: 'reparse',
    reparse: { knowledgeId: 'knowledge-b' },
  }
  store.resolveConfirm(secondResult)

  assert.deepEqual(await second, secondResult)
  assert.equal(store.visible, false)
  assert.equal(store.pendingResolve, null)
  assert.equal(store.pendingReject, null)
})
