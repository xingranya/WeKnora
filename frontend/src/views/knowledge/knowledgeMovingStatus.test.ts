import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { parseStatusToTimelineStatus } from '../../utils/knowledgeProcessingStatus'
import { isKnowledgeParseInFlight } from './wikiStatusRefresh'

const actionMenu = readFileSync(new URL('./components/DocumentActionMenu.vue', import.meta.url), 'utf8')
const cardView = readFileSync(new URL('./components/DocumentCardView.vue', import.meta.url), 'utf8')
const listView = readFileSync(new URL('./components/DocumentListView.vue', import.meta.url), 'utf8')

test('moving remains pollable and uses the running timeline vocabulary', () => {
  assert.equal(isKnowledgeParseInFlight('moving'), true)
  assert.equal(parseStatusToTimelineStatus('moving'), 'running')
})

test('moving documents expose status but suppress conflicting mutation actions', () => {
  assert.match(actionMenu, /const isMoving = computed\(\(\) => props\.item\.parse_status === 'moving'\)/)
  assert.match(actionMenu, /v-if="canMutateKnowledge && !isMoving"/)
  assert.match(actionMenu, /v-if="!isMoving" theme="warning"/)
  assert.match(cardView, /knowledgeBase\.statusMoving/)
  assert.match(listView, /knowledgeBase\.statusMoving/)
})
