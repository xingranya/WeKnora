import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const parserSettings = readFileSync(new URL('./ParserEngineSettings.vue', import.meta.url), 'utf8')
const modelSettings = readFileSync(new URL('./ModelSettings.vue', import.meta.url), 'utf8')
const modelSelector = readFileSync(new URL('../../components/ModelSelector.vue', import.meta.url), 'utf8')

test('普通员工解析页只加载公开引擎列表', () => {
  assert.match(parserSettings, /const canManageCompanyPreset = computed\(\(\) => authStore\.isSystemAdmin\)/)
  assert.match(parserSettings, /if \(canManageCompanyPreset\.value\)[\s\S]*loadConfig\(\)/)
  assert.match(parserSettings, /else \{\s*await loadEngines\(\)/)
  assert.doesNotMatch(parserSettings, /getWeKnoraCloudStatus/)
  assert.doesNotMatch(parserSettings, /getParserEngineConfig,/)
  assert.match(parserSettings, /canManageCompanyPreset && currentEngine\.Name === 'mineru'/)
  assert.match(parserSettings, /:hide-footer="!canManageCompanyPreset"/)
})

test('公司预置模型不能被普通员工测试', () => {
  assert.match(modelSettings, /allModels\.value\.filter\(model => !model\.is_builtin\)/)
  assert.match(modelSettings, /:models="debugModels"/)
  assert.match(modelSelector, /model\.is_builtin/)
  assert.match(modelSelector, /model\.builtinTag/)
})
