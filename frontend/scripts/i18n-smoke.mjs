import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
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
  const {
    modelMonitorSampleTimeFormatOptions,
    modelMonitorTimestampFormatOptions,
  } = await server.ssrLoadModule('/src/features/model-monitor/utils/timeFormat.ts')
  assert.deepEqual(modelMonitorSampleTimeFormatOptions('day'), {
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  })
  assert.deepEqual(modelMonitorSampleTimeFormatOptions('minute'), {
    hour: '2-digit',
    minute: '2-digit',
  })
  assert.deepEqual(modelMonitorTimestampFormatOptions(), {
    dateStyle: 'medium',
    timeStyle: 'short',
  })

  const { minuteHistoryStripItems } = await server.ssrLoadModule(
    '/src/features/model-monitor/utils/historyStrip.ts',
  )
  const minuteSamples = [
    { timestamp: '2026-08-31T12:00:00Z' },
    { timestamp: '2026-08-31T12:01:00Z' },
    { timestamp: '2026-08-31T12:01:00Z' },
  ]
  const paddedMinuteItems = minuteHistoryStripItems(minuteSamples, 'minute')
  assert.equal(paddedMinuteItems.length, 60)
  assert.ok(paddedMinuteItems.slice(0, 57).every((item) => item.sample === null))
  assert.deepEqual(
    paddedMinuteItems.slice(-3).map((item) => item.sample),
    minuteSamples,
  )
  assert.equal(new Set(paddedMinuteItems.map((item) => item.key)).size, 60)
  assert.notEqual(paddedMinuteItems[58].key, paddedMinuteItems[59].key)

  const fullMinuteSamples = Array.from(
    { length: 60 },
    (_, index) => ({ timestamp: new Date(Date.UTC(2026, 7, 31, 12, index)).toISOString() }),
  )
  const fullMinuteItems = minuteHistoryStripItems(fullMinuteSamples, 'minute')
  assert.equal(fullMinuteItems.length, 60)
  assert.ok(fullMinuteItems.every((item) => item.sample !== null))
  assert.deepEqual(
    fullMinuteItems.map((item) => item.sample),
    fullMinuteSamples,
  )

  const overflowMinuteSamples = [
    ...fullMinuteSamples,
    { timestamp: '2026-08-31T13:00:00.000Z' },
  ]
  const overflowMinuteItems = minuteHistoryStripItems(overflowMinuteSamples, 'minute')
  assert.equal(overflowMinuteItems.length, overflowMinuteSamples.length)
  assert.deepEqual(
    overflowMinuteItems.map((item) => item.sample),
    overflowMinuteSamples,
  )

  const dayItems = minuteHistoryStripItems(minuteSamples, 'day')
  assert.equal(dayItems.length, minuteSamples.length)
  assert.deepEqual(
    dayItems.map((item) => item.sample),
    minuteSamples,
  )

  const { usageRecordsLatestRange, usageRecordsRetentionStart } = await server.ssrLoadModule(
    '/src/features/usage/recordsRetention.ts',
  )
  const retentionNow = Date.UTC(2026, 7, 4, 12, 0, 0, 999)
  const retentionStart = usageRecordsRetentionStart(retentionNow)
  const retentionWindowMs = 7 * 24 * 60 * 60 * 1000
  assert.equal(retentionStart % 1000, 0)
  assert.deepEqual(usageRecordsLatestRange(retentionNow), [retentionStart, retentionNow])
  const cutoffAfterThreeSeconds =
    Math.floor((retentionNow + 3 * 1000) / 1000) * 1000 - retentionWindowMs
  assert.ok(retentionStart >= cutoffAfterThreeSeconds)

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
  assert.equal(
    localizedApiErrorMessage('usage_record_expired', '使用明细已超过 7 天保留期'),
    'Usage record is outside the 7-day retention period',
  )
  assert.equal(
    localizedApiErrorMessage('validation_error', '请求明细仅保留最近 7 天，不能使用 range=all'),
    'Request records are retained for only the last 7 days; range=all is unavailable',
  )
  assert.equal(localizedApiErrorMessage(null, null), 'Request failed')
  assert.equal(localizedServerMessage('巡检完成'), 'Inspection complete')
  const modelMonitorErrors = [
    ['模型监控上游返回 HTTP 502', 'Model monitoring upstream returned HTTP 502'],
    ['模型监控上游返回了不支持的内容类型 "text/html"', 'Model monitoring upstream returned unsupported content type "text/html"'],
    ['读取模型监控上游响应失败: unexpected EOF', 'Failed to read model monitoring upstream response: unexpected EOF'],
    ['模型监控上游响应超过 8 MiB 限制', 'Model monitoring upstream response exceeds the 8 MiB limit'],
    ['OpenAI 页面分组结构无效', 'OpenAI page group structure is invalid'],
    ['OpenAI summary components 字段无效', 'OpenAI summary components field is invalid'],
    ['Anthropic 当前组件缺少必要字段', 'Anthropic current component is missing required fields'],
    ['Anthropic 组件 "claude-api" 的历史结构无效', 'Anthropic component "claude-api" history structure is invalid'],
    ['Anthropic 页面 uptimeData 无效: JSON 对象未闭合', 'Anthropic page uptimeData is invalid: JSON object is not closed'],
    ['OpenAI uptime 引用了未知 group "group-api"', 'OpenAI uptime references unknown group "group-api"'],
    ['OpenAI uptime 引用了未知组件 "api"', 'OpenAI uptime references unknown component "api"'],
    ['OpenAI impact 引用了未知组件 "api"', 'OpenAI impact references unknown component "api"'],
    ['OpenAI impact 引用了无法映射的 incident "incident-live"', 'OpenAI impact references unmapped incident "incident-live"'],
    ['OpenAI 可见结构引用的 component "api" 缺少当前状态', 'OpenAI component referenced by the visible structure "api" is missing current status'],
    ['OpenAI 页面 incident link 缺少 ID 或标题', 'OpenAI page incident link is missing ID or title'],
    ['OpenAI 页面缺少incident links', 'OpenAI page is missing incident links'],
    ['OpenAI 页面缺少 incident links', 'OpenAI page is missing incident links'],
    ['OpenAI group "group-api" 缺少aggregated uptime', 'OpenAI group "group-api" is missing aggregated uptime'],
    ['OpenAI group "group-api" 缺少 aggregated uptime', 'OpenAI group "group-api" is missing aggregated uptime'],
    ['Anthropic 当前组件 "claude-api" 缺少历史', 'Anthropic current component "claude-api" is missing history'],
    ['Anthropic 当前组件 "claude-api" 缺少 历史', 'Anthropic current component "claude-api" is missing history'],
    ['Anthropic 组件 "claude-api" 的 related event 缺少名称或 code', 'Anthropic component "claude-api" related event is missing name or code'],
    ['DeepSeek 当前组件 "api" 缺少必要字段', 'DeepSeek current component "api" is missing required fields'],
    ['DeepSeek 页面 uptimeData 无效: JSON 对象未闭合', 'DeepSeek page uptimeData is invalid: JSON object is not closed'],
    ['AI.INPUT.IM service 结构无效', 'AI.INPUT.IM service structure is invalid'],
    ['AI.INPUT.IM 响应缺少必要字段', 'AI.INPUT.IM response is missing required fields'],
  ]
  for (const [input, expected] of modelMonitorErrors) {
    const translated = localizedServerMessage(input)
    assert.equal(translated, expected)
    assert.doesNotMatch(translated, /\p{Script=Han}/u)
  }
  assert.equal(
    localizedServerMessage('模型监控请求失败: 状态页重定向必须使用 HTTPS'),
    'Model monitoring request failed: Status page redirects must use HTTPS',
  )
  assert.equal(
    localizedServerMessage('模型监控请求失败: 状态页重定向次数过多'),
    'Model monitoring request failed: Too many status page redirects',
  )
  assert.equal(
    localizedServerMessage('模型监控请求失败: Get "https://status.example.com": 状态页重定向必须使用 HTTPS'),
    'Model monitoring request failed: Get "https://status.example.com": Status page redirects must use HTTPS',
  )
  assert.equal(
    localizedServerMessage('模型监控请求失败: Get "https://status.example.com": 状态页重定向次数过多'),
    'Model monitoring request failed: Get "https://status.example.com": Too many status page redirects',
  )
  assert.equal(
    localizedServerMessage('模型监控请求失败: dial tcp: 上游自定义详情'),
    'Model monitoring request failed: dial tcp: 上游自定义详情',
  )
  assert.equal(
    localizedServerMessage('模型监控来源设置缺少 enabled_source_ids 字段'),
    'Model monitoring source settings are missing enabled_source_ids',
  )
  assert.equal(
    localizedServerMessage('enabled_source_ids 必须是 JSON 字符串数组'),
    'enabled_source_ids must be a JSON string array',
  )
  assert.equal(
    localizedServerMessage('模型监控来源 ID "deepseek" 不受支持'),
    'Model monitoring source ID "deepseek" is not supported',
  )
  assert.equal(
    localizedServerMessage('模型监控来源配置无效: 模型监控来源 ID "unknown" 重复'),
    'Model monitoring source settings are invalid: model monitoring source ID "unknown" is duplicated',
  )
  assert.equal(
    localizedServerMessage('模型监控来源不存在'),
    'Model monitoring source does not exist',
  )
  assert.equal(
    localizedServerMessage('审计页面组件无效'),
    'Request failed: 审计页面组件 is invalid',
  )
  assert.equal(
    localizedServerMessage('审计标题无效'),
    'Request failed: 审计标题 is invalid',
  )
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
  assert.equal(
    localizedUsageChannelFallbackLabel('xai', 'apikey'),
    'xAI API Key (label unavailable)',
  )

  setLanguage('zh')
  assert.equal(localizedApiErrorMessage('validation_error', null), '请求参数无效')
  assert.equal(
    localizedApiErrorMessage('usage_record_expired', null),
    '使用明细已超过 7 天保留期',
  )
  assert.equal(localizedApiErrorMessage(null, null), '请求失败')
  assert.equal(localizedKeeperStatusDetail('守护运行中'), '自动巡检运行中')
  assert.equal(localizedUsageChannelFallbackLabel('codex', 'apikey'), 'Codex API Key（标签不可用）')
  assert.equal(
    localizedUsageChannelFallbackLabel('openai_compatibility', 'apikey'),
    'OpenAI 兼容渠道（标签不可用）',
  )
  assert.equal(localizedUsageChannelFallbackLabel('xai', 'apikey'), 'xAI API Key（标签不可用）')
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

  const { apiClient, isApiRequestError } = await server.ssrLoadModule(
    `/src/shared/api/apiClient.ts?case=${moduleCase++}`,
  )
  globalThis.fetch = async () => ({
    ok: false,
    status: 410,
    statusText: 'Gone',
    json: async () => ({
      detail: {
        code: 'usage_record_expired',
        message: '使用明细已超过 7 天保留期',
      },
    }),
  })
  await assert.rejects(
    () => apiClient.get('/usage/records/42'),
    (error) =>
      isApiRequestError(error) &&
      error.status === 410 &&
      error.code === 'usage_record_expired' &&
      error.message === 'Usage record is outside the 7-day retention period',
  )

  const { getUsageRecords, isUsageRecordsLegacyRangeValidationError } =
    await server.ssrLoadModule(`/src/features/usage/api/usageApi.ts?case=${moduleCase++}`)
  globalThis.fetch = async () => ({
    ok: false,
    status: 422,
    statusText: 'Unprocessable Entity',
    json: async () => ({
      detail: {
        code: 'validation_error',
        message: '请求明细仅保留最近 7 天，不能使用 range=all',
      },
    }),
  })
  await assert.rejects(
    () => getUsageRecords({}, 1, 50, { range: 'all' }),
    (error) => isUsageRecordsLegacyRangeValidationError(error),
  )
  globalThis.fetch = async () => ({
    ok: false,
    status: 500,
    statusText: 'Internal Server Error',
    json: async () => ({
      detail: {
        code: 'validation_error',
        message: '请求参数无效',
      },
    }),
  })
  await assert.rejects(
    () => getUsageRecords({}, 1, 50, { range: 'all' }),
    (error) => !isUsageRecordsLegacyRangeValidationError(error),
  )

  const usageRecordsView = await readFile(
    new URL('../src/features/usage/views/UsageRecordsView.vue', import.meta.url),
    'utf8',
  )
  const usageHistoryView = await readFile(
    new URL('../src/features/usage/views/UsageHistoryView.vue', import.meta.url),
    'utf8',
  )
  assert.match(
    usageHistoryView,
    /function quickRangeFromQuery\(\): QuickRangeKey \| null \{\s*const value = route\.query\.quick_range\s*if \(isQuickRangeKey\(value\)\) \{\s*return value\s*\}\s*return route\.query\.range === 'all' \? 'all' : null/,
  )
  assert.match(
    usageHistoryView,
    /const \[retentionStart, end\] = usageRecordsLatestRange\(\)/,
  )
  assert.match(
    usageRecordsView,
    /function clearLegacyAllRangeRequest\(\) \{\s*if \(\s*legacyAllRangeRequested\.value &&\s*\(dateRange\.value === null \|\| !isRecordsRangeWithinRetention\(dateRange\.value\)\)\s*\) \{\s*activeQuickRange\.value = 'last7d'\s*dateRange\.value = latestRecordsRange\(\)\s*\}\s*legacyAllRangeRequested\.value = false\s*[\s\S]*legacyAllRangeTerminal\.value = false/,
  )
  assert.match(
    usageRecordsView,
    /function refreshAfterFilterChange\(\) \{\s*clearLegacyAllRangeRequest\(\)\s*void refresh\(\{ resetPage: true, recoverLegacyRange: true \}\)/,
  )
  assert.match(
    usageRecordsView,
    /requestedRange === 'all' &&\s*legacyAllRangeRequested\.value &&\s*isUsageRecordsLegacyRangeValidationError\(error\)/,
  )
  assert.match(
    usageRecordsView,
    /recoverLegacyRange: Boolean\(queuedRefresh\?\.recoverLegacyRange \|\| options\.recoverLegacyRange\)/,
  )
  assert.match(
    usageRecordsView,
    /const requestGeneration = silent \? refreshGeneration : \+\+refreshGeneration/,
  )
  assert.match(
    usageRecordsView,
    /if \(requestGeneration !== refreshGeneration\) \{\s*return\s*\}/,
  )
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
