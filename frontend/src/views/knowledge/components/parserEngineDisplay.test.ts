import assert from 'node:assert/strict'
import test from 'node:test'
import { parserEngineDisplayName } from './parserEngineDisplay'

test('文件超限提示使用本地化解析器展示名而不是内部 ID', () => {
  const translate = (key: string) => key === 'kbSettings.parser.engines.builtin.name' ? '内置' : key

  assert.equal(parserEngineDisplayName('builtin', translate), '内置')
})

test('未登记的第三方解析器仍显示服务端提供的名称', () => {
  assert.equal(parserEngineDisplayName('company-parser', key => key), 'company-parser')
})
