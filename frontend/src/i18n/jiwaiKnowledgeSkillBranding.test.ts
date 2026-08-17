import assert from 'node:assert/strict'
import test from 'node:test'
import enUS from './locales/en-US.ts'
import koKR from './locales/ko-KR.ts'
import ruRU from './locales/ru-RU.ts'
import zhCN from './locales/zh-CN.ts'
import {
  INTEGRATION_TAB_MIN_ROLE,
  JIWAI_KNOWLEDGE_SKILL_DOWNLOAD_URL,
} from '../config/integrations.ts'

const localeBundles = {
  'en-US': enUS,
  'ko-KR': koKR,
  'ru-RU': ruRU,
  'zh-CN': zhCN,
}

test('见外知识库展示名在所有主界面语言中保持一致', () => {
  for (const [localeName, locale] of Object.entries(localeBundles)) {
    assert.equal(locale.integrations.claw.title, '见外知识库', `${localeName} 页面标题不一致`)
    assert.equal(locale.integrations.tabs.claw, '见外知识库', `${localeName} 设置菜单不一致`)
    assert.equal(locale.common.clawhubSkill, '见外知识库', `${localeName} 辅助标签不一致`)
    assert.match(locale.integrations.claw.setupPrompt, /\{baseUrl\}/, `${localeName} 配置提示缺少 API 地址占位符`)
    assert.match(locale.integrations.claw.setupPrompt, /\{apiKey\}/, `${localeName} 配置提示缺少 API Key 占位符`)
    assert.match(locale.integrations.claw.setupPrompt, /\{archiveName\}/, `${localeName} 配置提示缺少安装包占位符`)
    assert.equal(locale.integrations.claw.compatibility.generic.length > 0, true, `${localeName} 缺少通用 Agent 文案`)
  }
})

test('见外知识库安装包使用稳定的静态下载地址', () => {
  assert.equal(JIWAI_KNOWLEDGE_SKILL_DOWNLOAD_URL, '/downloads/jiwai-knowledge-skill.zip')
})

test('见外知识库安装页仅向可读取完整 API Key 的空间所有者开放', () => {
  assert.equal(INTEGRATION_TAB_MIN_ROLE.claw, 'owner')
})
