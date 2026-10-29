import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

import { createServer } from 'vite'

const root = fileURLToPath(new URL('..', import.meta.url))
const languageStorageKey = 'cpa-helper-language'

const server = await createServer({
  root,
  logLevel: 'error',
  // SSR-only assertions do not need client dependency scanning or listeners.
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
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

  const { modelGroupLibraryPriceState } = await server.ssrLoadModule(
    '/src/features/pricing/utils/modelPriceGroupSummary.ts',
  )
  assert.equal(modelGroupLibraryPriceState(null, true), 'empty')
  assert.equal(modelGroupLibraryPriceState(null, false), 'unconfigured')
  assert.equal(modelGroupLibraryPriceState({ id: 1 }, true), 'library')

  const modelPricesView = await readFile(
    new URL('../src/features/pricing/views/ModelPricesView.vue', import.meta.url),
    'utf8',
  )
  assert.match(
    modelPricesView,
    /const channelAliasMap = computed\(\(\) => \{[\s\S]*?const key = channelIdentityKey\(alias\.auth_type, alias\.channel_brand, alias\.channel_key\)[\s\S]*?if \(key !== null\) \{[\s\S]*?aliases\.set\(key, alias\.label\)/,
  )
  assert.match(
    modelPricesView,
    /const identity = channelIdentityKey\(authType, brand, key\)\s*return identity === null \? '' : channelAliasMap\.value\.get\(identity\) \?\? ''/,
  )
  assert.match(
    modelPricesView,
    /function providerGroupUnpricedCount\(row: PriceGroupRow\): number \{\s*\/\/ Keep the provider subtitle aligned with the operational status column\.[\s\S]*?return row\.unpricedCount\s*\}/,
  )

  const pricingApi = await readFile(
    new URL('../src/features/pricing/api/pricingApi.ts', import.meta.url),
    'utf8',
  )
  assert.match(pricingApi, /apiClient\.get<PriceTimeRule>\('\/model-prices\/deepseek-template'\)/)
  assert.match(pricingApi, /apiClient\.put<PriceTimeRule>\('\/model-prices\/deepseek-template', rule\)/)
  assert.match(pricingApi, /apiClient\.post<TimePricingBatchResult>\(`\/model-prices\/deepseek-template\/\$\{apply \? 'apply' : 'preview'\}`, payload\)/)

  const deepSeekTemplateModal = await readFile(
    new URL('../src/features/pricing/components/DeepSeekTemplateModal.vue', import.meta.url),
    'utf8',
  )
  assert.match(deepSeekTemplateModal, /:disabled="busy \|\| !selected\.length \|\| \(enabled && !rule\)" @click="review"/)

  installBrowserStubs({ browserLanguages: ['en-US'] })

  let {
    errorText,
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
  assert.equal(localizedServerMessage('渠道身份不完整'), 'Channel identity is incomplete')
  assert.equal(localizedServerMessage('ultrafast 倍率必须是大于 0 的有限数值'), 'The ultrafast multiplier must be a finite number greater than 0')
  assert.equal(localizedServerMessage('ultrafast 倍率会产生无法安全计价的金额'), 'The ultrafast multiplier produces an amount that cannot be priced safely')
  assert.equal(localizedServerMessage('ultrafast 倍率仅支持 OpenAI 兼容或 Codex 渠道的 gpt-6.1-sol 和 gpt-6-astra'), 'ultrafast multipliers only support gpt-6.1-sol and gpt-6-astra on OpenAI-compatible or Codex channels')
  assert.equal(localizedApiErrorMessage('not_found', '峰谷时段模板不存在'), 'Time pricing template does not exist')
  const claudePricingErrors = [
    ['峰谷定价仅支持 DeepSeek 或 Claude 渠道 Token 价格', 'Time pricing only supports DeepSeek or Claude channel token prices'],
    ['1 小时缓存价格仅支持 Claude 价格', '1-hour cache prices are only supported for Claude prices'],
    ['请选择 DeepSeek 或 Claude 渠道 Token 价格', 'Select DeepSeek or Claude channel token prices'],
  ]
  for (const [input, expected] of claudePricingErrors) {
    const translated = localizedApiErrorMessage('validation_error', input)
    assert.equal(translated, expected)
    assert.equal(errorText(new Error(translated), '保存价格失败', 'Failed to save price'), expected)
    assert.doesNotMatch(translated, /\p{Script=Han}/u)
  }
  assert.equal(
    localizedServerMessage('渠道名称不能包含控制字符或换行'),
    'Channel name cannot contain control characters or line breaks',
  )
  assert.equal(localizedServerMessage('渠道名称不能超过 200 个字符'), 'Channel name must not exceed 200 characters')
  assert.equal(
    localizedServerMessage('单次最多批量应用 200 个渠道模型价格'),
    'A maximum of 200 channel model prices can be applied at once',
  )
  assert.equal(localizedServerMessage('渠道配置已变化，请刷新后重试'), 'Channel configuration changed. Refresh and try again.')
  assert.equal(
    localizedApiErrorMessage('plugin_sync_failed', '插件策略保存后未生效'),
    'Plugin policy was saved but did not take effect',
  )
  const pluginBoundaryErrors = [
    ['validation_error', 'Claude 缓存 TTL 只能配置在 API 密钥上', 'Claude cache TTL can only be configured on an API key'],
    ['validation_error', 'Claude 缓存 TTL 必须为继承 CPA、5m 或 1h', 'Claude cache TTL must be Inherit CPA, 5m or 1h'],
    ['plugin_claude_ttl_unavailable', 'Claude 缓存 TTL 尚未完成插件配置校验', 'Claude cache TTL plugin configuration has not been verified'],
    ['plugin_sync_failed', '插件 API 密钥缓存 TTL 保存后未生效', 'The plugin API key cache TTL was saved but did not take effect'],
    ['plugin_feature_unsupported', '当前 CPA Helper Plugin 不支持响应模型核验', 'The current CPA Helper Plugin does not support response model verification'],
    ['model_catalog_out_of_sync', '同步期间 CPA 凭据模型注册已变化，请刷新后重试', 'CPA credential model registration changed during synchronization. Refresh and try again.'],
    ['model_catalog_sync_failed', 'CPA 注册凭据与元数据不一致，请重新同步', 'CPA registered credentials do not match the metadata. Sync again.'],
    ['model_catalog_sync_failed', 'CPA 凭据元数据未被宿主注册目录完整覆盖，请重新同步', 'The host registered catalog does not fully cover the CPA credential metadata. Sync again.'],
    ['plugin_sync_failed', '插件策略保存后未生效', 'Plugin policy was saved but did not take effect'],
    ['plugin_sync_failed', '响应核验配置已提交，但插件未确认生效', 'Response verification config was submitted, but the plugin did not confirm activation'],
    ['plugin_sync_failed', '插件策略已提交，但未确认目标修订号', 'Plugin policy was submitted, but the target revision was not confirmed'],
    ['plugin_unavailable', 'CPA Helper Plugin 不可达', 'CPA Helper Plugin is unreachable'],
    ['plugin_auth_failed', 'CPA Helper Plugin 管理端认证失败，请检查管理密钥', 'CPA Helper Plugin management authentication failed. Check the management key.'],
    ['conflict', 'API KEY 已过期，请先修改密钥有效期', 'API key has expired; update its expiration first'],
    ['conflict', 'CPA Helper Plugin 配置已被其他管理端修改，请刷新后重试', 'CPA Helper Plugin configuration was changed by another administrator. Refresh and try again.'],
    ['precondition_required', 'CPA Helper Plugin 写入缺少版本前置条件', 'CPA Helper Plugin write is missing the version precondition'],
    ['plugin_validation_failed', 'CPA Helper Plugin 配置校验失败', 'CPA Helper Plugin configuration validation failed'],
    ['plugin_not_loaded', 'CPA Helper Plugin 未加载或不支持当前管理契约', 'CPA Helper Plugin is not loaded or does not support the current management contract'],
    ['validation_error', 'CPA Helper Plugin 返回的 JSON 不是对象', 'CPA Helper Plugin returned JSON that is not an object'],
    ['plugin_incompatible', 'CPA Helper Plugin 管理契约不兼容', 'CPA Helper Plugin management contract is incompatible'],
    ['model_catalog_unavailable', 'CPA Helper Plugin 模型目录不可用，请同步目录', 'CPA Helper Plugin model catalog is unavailable. Sync the catalog.'],
    ['model_catalog_out_of_sync', 'CPA Helper Plugin 模型目录已失效，请重新同步', 'CPA Helper Plugin model catalog is out of sync. Sync it again.'],
    ['model_catalog_unavailable', 'CPA Helper Plugin 模型目录需要管理员同步，请检查管理配置', 'The CPA Helper Plugin model catalog requires an administrator to sync it. Check the management settings.'],
    ['conflict', 'CPA Helper Plugin 模型目录已被其他管理端修改，请刷新后重试', 'The CPA Helper Plugin model catalog was changed by another administrator. Refresh and try again.'],
    ['model_catalog_sync_failed', 'CPA Helper Plugin 模型目录已提交，但未确认目标修订号', 'The CPA Helper Plugin model catalog was submitted, but the target revision was not confirmed.'],
    ['model_catalog_sync_failed', 'CPA Helper Plugin 模型目录已提交，但未确认生效', 'The CPA Helper Plugin model catalog was submitted, but activation was not confirmed.'],
    ['model_catalog_out_of_sync', '同步期间 CPA 配置或凭据已变化，请刷新后重试', 'CPA configuration or credentials changed during synchronization. Refresh and try again.'],
    ['model_catalog_sync_failed', 'CPA 凭据模型目录格式错误', 'CPA credential model catalog format is invalid'],
    ['validation_error', 'CPA Helper Plugin 策略快照格式无效（groups/keys 必须为对象数组）', 'CPA Helper Plugin policy snapshot is invalid (groups/keys must be arrays of objects)'],
    ['validation_error', 'CPA Helper Plugin 策略字段格式无效', 'CPA Helper Plugin policy field format is invalid'],
    ['validation_error', 'CPA Helper Plugin API 密钥 group_ids 字段格式无效', 'CPA Helper Plugin API key group_ids field format is invalid'],
    ['validation_error', 'CPA Helper Plugin 规则字段格式无效', 'CPA Helper Plugin rule field format is invalid'],
    ['plugin_incompatible', 'CPA Helper Plugin 版本不兼容', 'CPA Helper Plugin version is incompatible'],
    ['plugin_incompatible', 'CPA Helper Plugin 当前并发数据缺失或无效', 'CPA Helper Plugin current concurrency data is missing or invalid'],
    ['plugin_incompatible', 'CPA Helper Plugin 当前并发数据超出可表示范围', 'CPA Helper Plugin current concurrency data exceeds the supported numeric range'],
    ['validation_error', 'scope 参数无效', 'Invalid scope parameter'],
    ['forbidden', '需要管理员权限', 'Admin access is required'],
    ['forbidden', '需要超级管理员权限', 'Super admin access is required'],
    ['conflict', '当前 API 密钥缺少完整密钥，无法映射插件策略', 'The full API key is missing; its plugin policy cannot be mapped'],
    ['plugin_incompatible', 'CPA Helper Plugin API 密钥启用状态无效', 'CPA Helper Plugin API key enabled state is invalid'],
    ['plugin_incompatible', 'CPA Helper Plugin 路由规则关联无效', 'CPA Helper Plugin route rule association is invalid'],
    ['plugin_incompatible', 'CPA Helper Plugin 并发上限数据无效', 'CPA Helper Plugin concurrency limit data is invalid'],
    ['not_found', 'API KEY 不存在', 'API key does not exist'],
    ['validation_error', '插件 API 密钥策略缺少 id', 'Plugin API key policy is missing id'],
    ['validation_error', 'CPA Helper Plugin capabilities 缺少响应核验配置', 'CPA Helper Plugin capabilities are missing the response verification config'],
    ['validation_error', '插件目录响应格式无效', 'Plugin directory response format is invalid'],
    ['validation_error', '插件目录响应缺少 credentials', 'Plugin directory response is missing credentials'],
    ['validation_error', 'CLIProxyAPI 配置响应不是有效 JSON object', 'CLIProxyAPI configuration response is not a valid JSON object'],
    ['validation_error', 'CPA 认证文件目录格式无效', 'CPA auth file directory format is invalid'],
    ['validation_error', 'CPA 凭据缺少宿主 ID', 'CPA credential is missing its host ID'],
    ['validation_error', 'CPA 凭据目录存在重复标识', 'CPA credential directory contains duplicate identifiers'],
    ['validation_error', 'CPA 配置与运行时凭据索引不一致', 'CPA configuration does not match the runtime credential index'],
    ['validation_error', 'CLIProxyAPI v8 配置缺少 access', 'CLIProxyAPI v8 configuration is missing access'],
    ['validation_error', '读取 CPA API KEY 失败：v8 配置格式无效', 'Failed to read CPA API keys: invalid v8 configuration format'],
    ['validation_error', 'CLIProxyAPI v8 配置缺少 api-keys', 'CLIProxyAPI v8 configuration is missing api-keys'],
    ['validation_error', 'CPA 凭据索引格式错误', 'CPA credential index format is invalid'],
    ['validation_error', 'CPA 上游 Key 格式错误', 'CPA upstream key format is invalid'],
    ['validation_error', 'CPA 上游配置格式错误', 'CPA upstream configuration format is invalid'],
    ['validation_error', '插件 API 密钥策略必须包含 policy_revision 和 key', 'Plugin API key policy must include policy_revision and key'],
    ['forbidden', '插件 API 密钥身份映射不匹配', 'Plugin API key identity mapping does not match'],
    ['conflict', '插件配置已被其他管理端修改，请刷新后重试', 'Plugin configuration was changed by another administrator. Refresh and try again.'],
    ['plugin_not_configured', 'CPA 配置未完成：请先到「系统设置」填写 CLIProxyAPI 地址和管理密钥，再返回 API 密钥页操作。', 'CPA settings are incomplete. Fill in the CLIProxyAPI URL and management key in System Settings, then return to API Keys.'],
    ['plugin_unavailable', 'CPA Helper Plugin 暂不可用（上游 HTTP 502）', 'CPA Helper Plugin is temporarily unavailable (upstream HTTP 502)'],
    ['plugin_request_failed', 'CPA Helper Plugin 请求失败（上游 HTTP 502）', 'CPA Helper Plugin request failed (upstream HTTP 502)'],
    ['validation_error', 'CPA 上游配置格式错误: gemini-api-key', 'Invalid CPA upstream configuration format: gemini-api-key'],
    ['validation_error', 'CPA 上游配置格式错误: openai-compatibility.api-key-entries', 'Invalid CPA upstream configuration format: openai-compatibility.api-key-entries'],
    ['validation_error', 'CPA 上游配置条目格式错误: openai-compatibility.api-key-entries', 'Invalid CPA upstream configuration entry format: openai-compatibility.api-key-entries'],
    ['validation_error', 'CPA 上游分组格式错误: gemini', 'Invalid CPA upstream group format: gemini'],
    ['validation_error', 'CPA 上游凭据格式错误: codex', 'Invalid CPA upstream credential format: codex'],
    ['validation_error', 'CPA 上游类型不受支持: custom-provider', 'Unsupported CPA upstream provider type: custom-provider'],
    ['validation_error', 'CLIProxyAPI 管理地址无效：unsupported scheme', 'Invalid CLIProxyAPI management URL: unsupported scheme'],
    ['validation_error', 'CLIProxyAPI 管理地址无效：CLIProxyAPI 地址不能为空', 'Invalid CLIProxyAPI management URL: CLIProxyAPI URL is required'],
    ['validation_error', 'CLIProxyAPI 管理地址无效：CLIProxyAPI 地址无效', 'Invalid CLIProxyAPI management URL: CLIProxyAPI URL is invalid'],
    ['validation_error', 'CLIProxyAPI 管理地址无效：CLIProxyAPI 地址缺少主机名', 'Invalid CLIProxyAPI management URL: CLIProxyAPI URL is missing a hostname'],
    ['validation_error', 'CLIProxyAPI 管理地址无效：CLIProxyAPI 地址必须使用 http、https、tcp、redis 或 resp 协议', 'Invalid CLIProxyAPI management URL: CLIProxyAPI URL must use http, https, tcp, redis, or resp'],
    ['validation_error', '查询 CPA 模型目录失败：upstream HTTP 502', 'Failed to query CPA model catalog: upstream HTTP 502'],
    ['validation_error', '查询 CPA 模型目录失败：CPA 模型列表请求失败：HTTP 502', 'Failed to query CPA model catalog: CPA model list request failed: HTTP 502'],
    ['validation_error', '查询 CPA 模型目录失败：CPA 模型列表请求失败：unexpected EOF', 'Failed to query CPA model catalog: CPA model list request failed: unexpected EOF'],
    ['validation_error', '查询 CPA 模型目录失败：CPA 模型列表响应不是有效 JSON', 'Failed to query CPA model catalog: CPA model list response is not valid JSON'],
    ['validation_error', '查询 CPA 模型目录失败：CPA 模型列表响应格式不支持', 'Failed to query CPA model catalog: CPA model list response format is unsupported'],
    ['validation_error', '查询 CPA 模型目录失败：CPA 模型列表响应字段 data 不是列表', 'Failed to query CPA model catalog: CPA model list response field data is not a list'],
    ['validation_error', '查询 CPA 模型目录失败：CPA 模型列表响应缺少模型列表', 'Failed to query CPA model catalog: CPA model list response is missing the model list'],
    ['validation_error', '查询 CPA 模型目录失败：CPA Helper Plugin 模型列表策略不可用', 'Failed to query CPA model catalog: CPA Helper Plugin model list policy is unavailable'],
    ['validation_error', '查询 CPA 模型目录失败：CPA Helper Plugin 模型目录无效', 'Failed to query CPA model catalog: CPA Helper Plugin model catalog is invalid'],
    ['validation_error', '查询 CPA 模型目录失败：CPA Helper Plugin 模型列表查询失败', 'Failed to query CPA model catalog: CPA Helper Plugin model list query failed'],
    ['validation_error', '查询 CPA 模型目录失败：CPA Helper Plugin 无法识别当前 API 密钥，模型列表未按路由规则过滤', 'Failed to query CPA model catalog: CPA Helper Plugin cannot identify this API key; the model list was not filtered by routing rules'],
  ]
  const pluginBackend = (await Promise.all([
    'cpa_helper_plugin.go', 'cpa_helper_plugin_catalog.go', 'cpa_helper_plugin_credentials.go',
  ].map((file) => readFile(new URL(`../../backend/internal/app/${file}`, import.meta.url), 'utf8')))).join('\n')
  const concatenatedPluginErrorPrefixes = [
    ...pluginBackend.matchAll(/\b(?:validationError|conflictError|notFoundError|forbiddenError)\("([^"]+)"\s*\+/g),
    ...pluginBackend.matchAll(/\bappError\("[^"]+",\s*[^,\n]+,\s*"([^"]+)"\s*\+/g),
  ].map((match) => match[1]).filter((input) => /\p{Script=Han}/u.test(input))
  for (const prefix of new Set(concatenatedPluginErrorPrefixes)) {
    assert.ok(pluginBoundaryErrors.some(([, covered]) => covered.startsWith(prefix)), `Production concatenated plugin error needs a bilingual fixture: ${prefix}`)
  }
  const fixedPluginErrors = [
    ...pluginBackend.matchAll(/\b(?:validationError|conflictError|notFoundError|forbiddenError|pluginCatalogSyncFailed)\("([^"]+)"\)/g),
    ...pluginBackend.matchAll(/\bappError\([^,\n]+,\s*[^,\n]+,\s*"([^"]+)"\)/g),
  ].map((match) => match[1]).filter((input) => /\p{Script=Han}/u.test(input))
  for (const input of new Set(fixedPluginErrors)) {
    assert.ok(pluginBoundaryErrors.some(([, covered]) => covered === input), `Production plugin error needs a bilingual fixture: ${input}`)
  }
  for (const [code, input, expected] of pluginBoundaryErrors) {
    const translated = localizedApiErrorMessage(code, input)
    assert.equal(translated, expected)
    assert.doesNotMatch(translated, /\p{Script=Han}/u)
  }
  // These diagnostic identifiers are already public in the backend error. Translate only the Helper prefix.
  const pluginOpaqueErrors = [
    ['validation_error', 'CPA 上游配置格式错误: 自定义字段', 'Invalid CPA upstream configuration format: 自定义字段'],
    ['validation_error', 'CPA 上游配置条目格式错误: 自定义条目', 'Invalid CPA upstream configuration entry format: 自定义条目'],
    ['validation_error', 'CPA 上游分组格式错误: 私有渠道（A）', 'Invalid CPA upstream group format: 私有渠道（A）'],
    ['validation_error', 'CPA 上游凭据格式错误: 私有凭据（B）', 'Invalid CPA upstream credential format: 私有凭据（B）'],
    ['validation_error', 'CPA 上游类型不受支持: 私有类型（C）', 'Unsupported CPA upstream provider type: 私有类型（C）'],
  ]
  for (const [code, input, expected] of pluginOpaqueErrors) assert.equal(localizedApiErrorMessage(code, input), expected)
  assert.equal(
    localizedApiErrorMessage('plugin_auth_failed', 'CPA Helper Plugin 管理端认证失败，请检查管理密钥'),
    'CPA Helper Plugin management authentication failed. Check the management key.',
  )
  assert.equal(
    localizedApiErrorMessage('plugin_not_loaded', 'CPA Helper Plugin 未加载或不支持当前管理契约'),
    'CPA Helper Plugin is not loaded or does not support the current management contract',
  )
  assert.equal(
    localizedApiErrorMessage('validation_error', 'CPA Helper Plugin 策略快照格式无效（groups/keys 必须为对象数组）'),
    'CPA Helper Plugin policy snapshot is invalid (groups/keys must be arrays of objects)',
  )
  assert.equal(
    localizedApiErrorMessage('plugin_incompatible', 'CPA Helper Plugin 管理契约不兼容'),
    'CPA Helper Plugin management contract is incompatible',
  )
  assert.equal(
    localizedApiErrorMessage('plugin_unavailable', 'CPA Helper Plugin 暂不可用（上游 HTTP 502）'),
    'CPA Helper Plugin is temporarily unavailable (upstream HTTP 502)',
  )
  assert.equal(
    localizedApiErrorMessage('plugin_request_failed', 'CPA Helper Plugin 请求失败（上游 HTTP 502）'),
    'CPA Helper Plugin request failed (upstream HTTP 502)',
  )
  assert.equal(
    localizedApiErrorMessage('plugin_validation_failed', 'CPA Helper Plugin 配置校验失败'),
    'CPA Helper Plugin configuration validation failed',
  )
  assert.equal(
    localizedApiErrorMessage('precondition_required', 'CPA Helper Plugin 写入缺少版本前置条件'),
    'CPA Helper Plugin write is missing the version precondition',
  )
  assert.equal(
    localizedApiErrorMessage('validation_error', 'CPA 上游配置格式错误: gemini-api-key'),
    'Invalid CPA upstream configuration format: gemini-api-key',
  )
  assert.equal(
    localizedApiErrorMessage('validation_error', 'CPA 上游配置条目格式错误: openai-compatibility.api-key-entries'),
    'Invalid CPA upstream configuration entry format: openai-compatibility.api-key-entries',
  )
  assert.equal(
    localizedApiErrorMessage('conflict', '插件配置已被其他管理端修改，请刷新后重试'),
    'Plugin configuration was changed by another administrator. Refresh and try again.',
  )
  assert.equal(
    localizedApiErrorMessage('conflict', 'CPA Helper Plugin 配置已被其他管理端修改，请刷新后重试'),
    'CPA Helper Plugin configuration was changed by another administrator. Refresh and try again.',
  )
  assert.equal(localizedServerMessage('请至少选择一个 LiteLLM 模型'), 'Select at least one LiteLLM model')
  assert.equal(localizedServerMessage('所选 LiteLLM 模型包含重复定价身份，请只选择一个大小写或空格变体'), 'Selected LiteLLM models share a price identity. Select only one case or whitespace variant.')
  assert.equal(
    localizedServerMessage('所选 LiteLLM 模型不存在或价格无效，请刷新清单后重试'),
    'A selected LiteLLM model is missing or has invalid pricing. Refresh the list and try again.',
  )
  assert.equal(localizedServerMessage('巡检完成'), 'Inspection complete')
  assert.equal(
    localizedServerMessage('用户状态已变化，请刷新后重试'),
    'User state changed. Refresh and try again.',
  )
  assert.equal(
    localizedServerMessage('禁用用户时用户状态已变化'),
    'User state changed while disabling the user.',
  )
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
  const aiRadarErrors = [
    ['AI 雷达上游返回 HTTP 503', 'AI radar upstream returned HTTP 503'],
    ['AI 雷达上游返回了不支持的内容类型 "text/html"', 'AI radar upstream returned unsupported content type "text/html"'],
    ['读取 AI 雷达上游响应失败: unexpected EOF', 'Failed to read the AI radar upstream response: unexpected EOF'],
    ['AI 雷达上游响应超过 8 MiB 限制', 'AI radar upstream response exceeds the 8 MiB limit'],
    ['AI 雷达上游返回了无效 JSON: invalid character', 'AI radar upstream returned invalid JSON: invalid character'],
    ['AI 雷达上游 schema 不受支持: 4', 'AI radar upstream schema 4 is not supported'],
    ['AI 雷达上游 schema 不受支持: -1', 'AI radar upstream schema -1 is not supported'],
    ['AI 雷达上游缺少 points 数组', 'AI radar upstream is missing the points array'],
    ['AI 雷达上游 points 不是数组: json: cannot unmarshal object', 'AI radar upstream points is not an array: json: cannot unmarshal object'],
    ['AI 雷达上游缺少有效 source_updated_at', 'AI radar upstream is missing a valid source_updated_at'],
    ['AI 雷达时间范围无效: 3d', 'AI radar time range is invalid: 3d'],
    ['AI 雷达历史快照不是有效 JSON: unexpected EOF', 'AI radar history snapshot is not valid JSON: unexpected EOF'],
    ['AI 雷达历史快照没有可导入的 GPT 观测点', 'AI radar history snapshot has no importable GPT observations'],
  ]
  for (const [input, expected] of aiRadarErrors) {
    const translated = localizedServerMessage(input)
    assert.equal(translated, expected)
    assert.doesNotMatch(translated, /\p{Script=Han}/u)
  }
  assert.equal(
    localizedServerMessage('AI 雷达请求失败: AI 雷达重定向必须使用 HTTPS'),
    'AI radar request failed: AI radar redirects must use HTTPS',
  )
  assert.equal(
    localizedServerMessage('AI 雷达请求失败: AI 雷达重定向次数过多'),
    'AI radar request failed: Too many AI radar redirects',
  )
  assert.equal(
    localizedServerMessage('AI 雷达请求失败: Get "https://api.codexradar.com/": AI 雷达重定向次数过多'),
    'AI radar request failed: Get "https://api.codexradar.com/": Too many AI radar redirects',
  )
  assert.equal(
    localizedServerMessage('AI 雷达请求失败: dial tcp: 上游自定义详情'),
    'AI radar request failed: dial tcp: 上游自定义详情',
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
  for (const [input] of claudePricingErrors) {
    assert.equal(errorText(new Error(localizedApiErrorMessage('validation_error', input))), input)
  }
  for (const [code, input] of [...pluginBoundaryErrors, ...pluginOpaqueErrors]) assert.equal(localizedApiErrorMessage(code, input), input)
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
  const { formatCompact, formatMultiplier, formatPreservedLongContextPrice } = await server.ssrLoadModule('/src/shared/utils/format.ts')
  assert.equal(formatCompact(12_300), '12.3K')
  assert.equal(formatCompact(52_646_000), '52.6M')
  assert.equal(formatCompact(3_560_000_000), '3.6B')
  assert.equal(formatMultiplier(1.00001), '1.00001')
  assert.equal(formatMultiplier(0.00001), '0.00001')

  const historicalTier = {
    threshold_input_tokens: 1000,
    input_usd_per_million: null,
    output_usd_per_million: null,
    cache_read_usd_per_million: 0,
    cache_creation_usd_per_million: 1.00001,
    non_finite_fields: { input_usd_per_million: '+Inf' },
  }
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    for (const nonFinite of ['NaN', '+Inf', '-Inf']) {
      historicalTier.non_finite_fields.input_usd_per_million = nonFinite
      assert.equal(
        formatPreservedLongContextPrice(historicalTier, 'input_usd_per_million'),
        `${language === 'zh' ? '无效值' : 'Invalid value'} (${nonFinite})`,
      )
    }
    assert.equal(formatPreservedLongContextPrice(historicalTier, 'output_usd_per_million'), language === 'zh' ? '未设置' : 'Not set')
    assert.equal(formatPreservedLongContextPrice(historicalTier, 'cache_read_usd_per_million'), '0')
    assert.equal(formatPreservedLongContextPrice(historicalTier, 'cache_creation_usd_per_million'), '1.00001')
  }
  setLanguage('zh')

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
  for (const language of ['en', 'zh']) {
    setLanguage(language)
    for (const [code, input, expected] of [...pluginBoundaryErrors, ...pluginOpaqueErrors]) {
      globalThis.fetch = async () => Response.json({ detail: { code, message: input } }, { status: 503 })
      await assert.rejects(
        () => apiClient.put('/cpa-helper-plugin/response-model', {}),
        (error) => {
          assert.equal(isApiRequestError(error), true)
          assert.equal(error.code, code)
          const rendered = browserCase.i18n.errorText(error, '保存插件配置失败', 'Failed to save plugin config')
          assert.equal(rendered, language === 'zh' ? input : expected, 'The API error and actual UI localizer render the complete localized message')
          if (language === 'en' && pluginBoundaryErrors.some(([, fixed]) => fixed === input)) assert.doesNotMatch(rendered, /\p{Script=Han}/u)
          return true
        },
      )
    }
  }
  setLanguage('en')

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
  const userManagementView = await readFile(
    new URL('../src/features/users/views/UserManagementView.vue', import.meta.url),
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
  assert.match(
    userManagementView,
    /function canManageUser\(row: UserSummary\): boolean \{\s*return canEditSuperAdminRole\.value \|\| \(!row\.is_admin && !row\.is_super_admin\)\s*\}/,
  )
  assert.match(userManagementView, /if \(!canManageUser\(row\)\) \{\s*return null\s*\}/)
  assert.match(
    userManagementView,
    /const isDemotingSelf = computed\(\s*\(\) =>\s*currentUser\.value\?\.id === editingUserId\.value &&\s*currentUser\.value\?\.is_super_admin === true &&\s*!isUserSuperAdmin\.value,\s*\)/,
  )
  assert.match(
    userManagementView,
    /const canEditDraftQuota = computed\(\(\) => canEditQuota\.value && !isDemotingSelf\.value\)/,
  )
  assert.match(userManagementView, /const shouldUpdateSavedUserQuota = canEditDraftQuota\.value/)
  assert.match(
    userManagementView,
    /if \(shouldUpdateSavedUserQuota\) \{\s*await updateUserQuota\(saved\.id,/,
  )
  assert.match(userManagementView, /:disabled="!canEditDraftQuota"/)
  assert.match(userManagementView, /余额设置将不可用，本次输入的余额修改不会保存。/)
  assert.match(
    userManagementView,
    /After removing your own super-admin role, balance settings are unavailable and balance changes entered here will not be saved\./,
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
