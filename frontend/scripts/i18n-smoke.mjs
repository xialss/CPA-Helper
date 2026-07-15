import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'

import { createServer } from 'vite'

const root = fileURLToPath(new URL('..', import.meta.url))
const languageStorageKey = 'cpa-helper-language'

const server = await createServer({
  root,
  logLevel: 'error',
  server: { middlewareMode: true },
})

let moduleCase = 0

function installBrowserStubs({ storedLanguage = null, browserLanguages = ['en-US'] } = {}) {
  const store = new Map()
  if (storedLanguage !== null) {
    store.set(languageStorageKey, storedLanguage)
  }

  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key) => store.get(key) ?? null,
      setItem: (key, value) => store.set(key, String(value)),
    },
  })
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: {
      language: browserLanguages[0],
      languages: browserLanguages,
    },
  })
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      documentElement: {
        lang: '',
      },
    },
  })

  return store
}

async function loadI18nWithBrowserStubs(options) {
  const store = installBrowserStubs(options)
  server.moduleGraph.invalidateAll()
  const i18n = await server.ssrLoadModule(`/src/shared/i18n/index.ts?case=${moduleCase++}`)
  return { i18n, store }
}

try {
  installBrowserStubs({ browserLanguages: ['en-US'] })

  let {
    localizedApiErrorMessage,
    localizedKeeperStatusDetail,
    localizedServerMessage,
    localizedUsageChannelFallbackLabel,
    setLanguage,
  } = await server.ssrLoadModule('/src/shared/i18n/index.ts')

  setLanguage('en')
  assert.equal(localizedApiErrorMessage('validation_error', null), 'Invalid request parameters')
  assert.equal(localizedApiErrorMessage(null, null), 'Request failed')
  assert.equal(localizedServerMessage('巡检完成'), 'Inspection complete')
  assert.equal(
    localizedServerMessage('巡检完成：健康 1，坏凭证禁用 2，恢复启用 3，优先级降级 4，网络错误 5，缓存跳过 6'),
    'Inspection complete: 1 healthy, 2 bad credentials disabled, 3 restored, 4 priorities lowered, 5 network errors, 6 skipped by cache',
  )
  assert.equal(
    localizedServerMessage('Codex Keeper 已开始按计划自动巡检'),
    'Codex Keeper scheduled automatic inspection started',
  )
  assert.equal(
    localizedServerMessage('Codex Keeper 已停止自动巡检'),
    'Codex Keeper automatic inspection stopped',
  )
  assert.equal(
    localizedServerMessage(
      'CLIProxyAPI 管理请求失败：HTTP 502',
      '渠道配置暂时不可用',
      'Channel configuration is unavailable',
    ),
    'CLIProxyAPI management request failed: HTTP 502',
  )
  assert.equal(
    localizedServerMessage('codex@example.com-plus.json: 降为低优先级：额度使用率达到阈值 100%'),
    'codex@example.com-plus.json: Lowered priority: quota usage reached the 100% threshold',
  )
  assert.equal(
    localizedServerMessage('codex@example.com-plus.json: 已启用 WebSocket 传输'),
    'codex@example.com-plus.json: enabled WebSocket transport',
  )
  assert.equal(localizedKeeperStatusDetail('守护运行中'), 'Automatic inspection running')
  assert.equal(
    localizedUsageChannelFallbackLabel('codex', 'apikey'),
    'Codex API Key (label unavailable)',
  )
  assert.equal(
    localizedUsageChannelFallbackLabel('openai_compatibility', 'apikey'),
    'OpenAI-compatible channel (label unavailable)',
  )

  setLanguage('zh')
  assert.equal(localizedApiErrorMessage('validation_error', null), '请求参数无效')
  assert.equal(localizedApiErrorMessage(null, null), '请求失败')
  assert.equal(localizedKeeperStatusDetail('守护运行中'), '自动巡检运行中')
  assert.equal(localizedUsageChannelFallbackLabel('codex', 'apikey'), 'Codex API Key（标签不可用）')
  assert.equal(
    localizedUsageChannelFallbackLabel('openai_compatibility', 'apikey'),
    'OpenAI 兼容渠道（标签不可用）',
  )
  const { formatCompact, formatMultiplier } = await server.ssrLoadModule('/src/shared/utils/format.ts')
  assert.equal(formatCompact(12_300), '12.3K')
  assert.equal(formatCompact(52_646_000), '52.6M')
  assert.equal(formatCompact(3_560_000_000), '3.6B')
  assert.equal(formatMultiplier(1.00001), '1.00001')
  assert.equal(formatMultiplier(0.00001), '0.00001')

  let browserCase = await loadI18nWithBrowserStubs({
    browserLanguages: ['fr-FR', 'zh-CN', 'en-US'],
  })
  assert.equal(browserCase.i18n.currentLanguage.value, 'zh')
  assert.equal(globalThis.document.documentElement.lang, 'zh-CN')
  assert.equal(browserCase.store.get(languageStorageKey), 'zh')

  browserCase = await loadI18nWithBrowserStubs({
    storedLanguage: 'en',
    browserLanguages: ['zh-CN', 'en-US'],
  })
  assert.equal(browserCase.i18n.currentLanguage.value, 'en')
  assert.equal(globalThis.document.documentElement.lang, 'en')
  assert.equal(browserCase.store.get(languageStorageKey), 'en')
  browserCase.i18n.toggleLanguage()
  await Promise.resolve()
  assert.equal(browserCase.i18n.currentLanguage.value, 'zh')
  assert.equal(globalThis.document.documentElement.lang, 'zh-CN')
  assert.equal(browserCase.store.get(languageStorageKey), 'zh')

  browserCase = await loadI18nWithBrowserStubs({
    storedLanguage: 'de',
    browserLanguages: ['es-ES', 'en-US', 'zh-CN'],
  })
  assert.equal(browserCase.i18n.currentLanguage.value, 'en')

  ;({ localizedApiErrorMessage, localizedKeeperStatusDetail, localizedServerMessage, setLanguage } =
    browserCase.i18n)
  setLanguage('en')
  assert.equal(
    localizedApiErrorMessage('validation_error', 'API KEY 描述不能为空'),
    'API key description is required',
  )
  assert.equal(
    localizedServerMessage('请求体不是有效 JSON'),
    'Request body is not valid JSON',
  )
  assert.equal(localizedKeeperStatusDetail(null), 'Not running')

  const { apiClient } = await server.ssrLoadModule(`/src/shared/api/apiClient.ts?case=${moduleCase++}`)
  globalThis.fetch = async () => ({
    ok: false,
    status: 418,
    statusText: 'I am a teapot',
    json: async () => {
      throw new Error('not json')
    },
  })
  await assert.rejects(
    () => apiClient.get('/broken'),
    (error) => error instanceof Error && error.message === 'Request failed',
  )
} finally {
  await server.close()
  delete globalThis.fetch
  delete globalThis.localStorage
  delete globalThis.navigator
  delete globalThis.document
}
