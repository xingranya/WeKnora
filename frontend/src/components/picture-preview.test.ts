import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const component = readFileSync(new URL('./picture-preview.vue', import.meta.url), 'utf8')
const wikiBrowser = readFileSync(new URL('../views/knowledge/wiki/WikiBrowser.vue', import.meta.url), 'utf8')

test('keeps the viewer mounted for keyboard focus without rendering an empty trigger image', () => {
  assert.doesNotMatch(component, /<t-image-viewer\s+v-if=/)
  assert.match(component, /:images="reviewUrl \? \[reviewUrl\] : \[\]"/)
  assert.match(component, /<template #trigger \/>/)
})

test('keeps the wiki image preview mounted before its first keyboard interaction', () => {
  assert.match(wikiBrowser, /<picturePreview :reviewImg="imagePreviewVisible"/)
  assert.doesNotMatch(wikiBrowser, /<picturePreview v-if="imagePreviewVisible"/)
})
