import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const component = readFileSync(new URL('./picture-preview.vue', import.meta.url), 'utf8')

test('keeps the viewer mounted for keyboard focus without rendering an empty trigger image', () => {
  assert.doesNotMatch(component, /<t-image-viewer\s+v-if=/)
  assert.match(component, /:images="reviewUrl \? \[reviewUrl\] : \[\]"/)
  assert.match(component, /<template #trigger \/>/)
})
