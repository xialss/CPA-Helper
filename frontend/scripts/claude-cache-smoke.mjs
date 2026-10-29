import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, h, nextTick, reactive } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const env = { errors: [], models: [], writes: [], renderTable: true }
globalThis.__claudeCacheSmoke = env
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN', clientWidth: 1280 } }
globalThis.window = {
  localStorage: globalThis.localStorage,
  matchMedia: () => ({ matches: true, addEventListener() {}, removeEventListener() {} }),
}
globalThis.getComputedStyle = () => ({ getPropertyValue: () => '' })
globalThis.fetch = async (url, init = {}) => {
  const path = new URL(url, 'http://claude-cache-smoke.invalid').pathname
  let result
  if (path === '/api/account/models') result = {
    has_api_keys: true, api_key_count: 1, queryable_api_key_count: 1,
    model_list_filter_status: 'enabled', models: env.models, errors: [],
  }
  else if (path === '/api/model-prices/1' && init.method === 'PUT') {
    const payload = JSON.parse(init.body)
    env.writes.push(payload)
    result = { id: 1, ...payload }
  }
  else if (path === '/api/model-prices/1/versions') result = []
  else if (path === '/api/model-prices/catalog') result = { models: [], channels: [], errors: [] }
  else if (['/api/model-prices', '/api/model-prices/library-conflicts', '/api/model-prices/channel-aliases'].includes(path)) result = []
  else assert.fail(`Unexpected API request ${path}`)
  return Response.json(result)
}

const views = {
  models: 'src/features/models/views/AvailableModelsView.vue',
  prices: 'src/features/pricing/views/ModelPricesView.vue',
  editor: 'src/features/pricing/components/PriceTimeRuleEditor.vue',
  matrix: 'src/features/pricing/components/PriceRatesMatrix.vue',
}
const passiveUI = ['NAlert', 'NButton', 'NCheckbox', 'NEmpty', 'NForm', 'NFormItem', 'NIcon', 'NInput', 'NInputNumber', 'NPagination', 'NPopover', 'NRadioButton', 'NRadioGroup', 'NSelect', 'NSpace', 'NSpin', 'NSwitch', 'NTag', 'NTooltip']
const icons = ['Cpu', 'KeyRound', 'RefreshCw', 'ShieldCheck', 'Database', 'Layers3', 'Pencil', 'RotateCcw', 'Search', 'Server', 'Zap']
// Execute the real SFC setup and template, replacing only UI, router and HTTP
// boundaries. Quotes, price identity, editor handlers and summaries stay real.
const server = await createServer({
  root, logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'vue-router', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'claude-cache-smoke', enforce: 'pre',
    async resolveId(id, importer) {
      for (const name of Object.keys(views)) if (id === `/__claude-cache-${name}.ts`) return `\0claude-cache:${name}`
      if (['naive-ui', 'vue-router', 'lucide-vue-next'].includes(id)) return `\0claude-cache:${id}`
      if (id.endsWith('/PriceRatesMatrix.vue')) return '\0claude-cache:matrix'
      if (id.endsWith('/PriceTimeRuleEditor.vue')) return '\0claude-cache:editor'
      if (['DeepSeekTemplateModal.vue', 'ModelListNotice.vue'].some(name => id.endsWith(`/${name}`))) return '\0claude-cache:passive'
      const source = views[importer?.replace('\0claude-cache:', '')]
      if (source && id.startsWith('.')) return this.resolve(resolve(dirname(`${root}/${source}`), id), undefined, { skipSelf: true })
    },
    async load(id) {
      const name = id.replace('\0claude-cache:', '')
      if (views[name]) {
        const filename = `${root}/${views[name]}`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: name, genDefaultAs: '__view' })
        const template = compileTemplate({ id: name, filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        return (await transformWithEsbuild(`${script.content}\n${template.code}\n__view.render = render; export default __view`, filename, { loader: 'ts' })).code
      }
      if (name === 'naive-ui') return `
        import { h } from 'vue';
        ${passiveUI.map(component => `export const ${component} = { inheritAttrs: false, setup(_, { attrs, slots }) { return () => h('stub-${component}', attrs, slots.default?.()); } };`).join('\n')}
        export const NModal = {
          props: { show: Boolean },
          setup(props, { slots }) { return () => props.show ? h('stub-NModal', null, slots.default?.()) : null; }
        };
        export const NDataTable = {
          props: { columns: Array, data: Array },
          setup(props) { return () => globalThis.__claudeCacheSmoke.renderTable
            ? h('table', props.data.map(row => h('tr', { 'data-model': row.id }, props.columns.map(column => h('td', { 'data-column': column.key }, column.render ? column.render(row) : row[column.key])))))
            : null; }
        };
        export const useMessage = () => ({ error: value => globalThis.__claudeCacheSmoke.errors.push(value), success() {} });
        export const useDialog = () => ({});`
      if (name === 'vue-router') return 'export const useRouter = () => ({ push() {} })'
      if (name === 'lucide-vue-next') return icons.map(component => `export const ${component} = { render: () => null };`).join('\n')
      if (name === 'passive') return 'export default { render: () => null }'
    },
  }],
})

const renderer = createRenderer({
  createElement: type => ({ type, props: {}, children: [] }),
  createText: text => ({ type: 'text', text }), createComment: () => ({ type: 'comment' }),
  insert(node, parent, anchor = null) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index >= 0 ? index : parent.children.length, 0, node)
    node.parent = parent
  },
  remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text; node.children = [] },
  patchProp(node, key, _, value) { node.props[key] = value },
  parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
})
const settle = async () => { await nextTick(); await nextTurn(); await nextTick() }
const descendants = node => [node, ...(node.children ?? []).flatMap(descendants)]
const textContent = node => [node.text ?? '', ...(node.children ?? []).map(textContent)].join('')
const ui = (host, type) => descendants(host).filter(node => node.type === `stub-${type}`)
const updateValue = (node, value) => {
  assert.ok(node, 'Expected a rendered input')
  const listeners = node.props['onUpdate:value']
  for (const listener of Array.isArray(listeners) ? listeners : [listeners]) listener(value)
}
let app
const mount = async (name) => {
  const { default: component } = await server.ssrLoadModule(`/__claude-cache-${name}.ts`)
  let state
  const host = { children: [] }
  app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } })
  app.mount(host)
  await settle()
  return { state, host }
}
function availableModel(id, provider, cacheWrite = 3.75) {
  return {
    id, name: id, owner: provider, metadata: {}, sources: [],
    price: {
      provider, model: id, billing_unit: 'token', request_usd: null, long_context: null,
      input_usd_per_million: 3, output_usd_per_million: 15, cache_read_usd_per_million: 0.3,
      cache_creation_usd_per_million: cacheWrite,
    },
  }
}
function quoteCell(host, id) {
  const row = descendants(host).find(node => node.type === 'tr' && node.props['data-model'] === id)
  assert.ok(row, `Expected model row ${id}`)
  return row.children.find(node => node.props['data-column'] === 'cache_creation_usd_per_million')
}
try {
  const { setLanguage } = await server.ssrLoadModule('/src/shared/i18n/index.ts')
  env.models = [
    availableModel('claude-priced', 'anthropic'), availableModel('claude-provider', 'Claude'),
    availableModel('claude-free', 'claude', 0), availableModel('claude-looking-name', 'openai'),
    { ...availableModel('request-price', 'claude'), price: { ...availableModel('request-price', 'claude').price, billing_unit: 'request', request_usd: 0.04 } },
    { ...availableModel('no-price', 'claude'), price: null },
  ]
  const models = await mount('models')
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    await settle()
    const quote = quoteCell(models.host, 'claude-priced')
    assert.equal(textContent(quote), '3.75', 'Claude displays one normal cache write rate')
    assert.equal(descendants(quote).filter(node => node.type === 'small').length, 0)
    assert.equal(textContent(quoteCell(models.host, 'claude-provider')), '3.75')
    assert.equal(textContent(quoteCell(models.host, 'claude-free')), '0', 'Explicit zero remains a configured quote')
    assert.equal(textContent(quoteCell(models.host, 'claude-looking-name')), '3.75')
    assert.equal(textContent(quoteCell(models.host, 'request-price')), '-')
    assert.equal(textContent(quoteCell(models.host, 'no-price')), '-')
  }
  app.unmount()
  env.renderTable = false
  const prices = await mount('prices')
  const price = {
    id: 1, provider: 'anthropic', model: 'opaque-model', price_scope: 'library',
    channel_brand: null, channel_key: null, channel_auth_type: null, billing_unit: 'token',
    input_usd_per_million: 3, output_usd_per_million: 15, cache_read_usd_per_million: 0.3,
    cache_creation_usd_per_million: 3.75,
    long_context: null, request_usd: null, time_pricing: null,
    preserved_long_context: {
      threshold_input_tokens: null, input_usd_per_million: null, output_usd_per_million: 30,
      cache_read_usd_per_million: null, cache_creation_usd_per_million: 7.5,
    },
  }
  const edit = async (overrides = {}) => {
    prices.state.openEdit({ price: { ...price, ...overrides }, provider: 'Anthropic', templatePrice: null, channelIdentityHash: null })
    await settle()
    return ui(prices.host, 'NAlert').find(node => textContent(node).includes('partial long-context') || textContent(node).includes('历史部分长上下文'))
  }
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    const prefix = language === 'zh' ? '缓存写' : 'Cache write'
    let alert = await edit()
    assert.ok(alert, 'The actual editing modal shows preserved long-context fields')
    assert.ok(textContent(alert).includes(`${prefix} 7.5`))
    assert.ok(!/\b(?:5m|1h|TTL)\b/.test(textContent(prices.host)), 'Claude price forms have no TTL labels')
    assert.equal(ui(prices.host, 'NFormItem').filter(node => node.props.label === prefix).length, 1)
    alert = await edit({ provider: 'openai' })
    assert.ok(textContent(alert).includes(`${prefix} 7.5`))
    assert.ok(!textContent(alert).includes(`${prefix} 1h`), 'Non-Claude summaries also keep one generic write rate')
  }
  const longContext = {
    threshold_input_tokens: 200000, input_usd_per_million: 6, output_usd_per_million: 30,
    cache_read_usd_per_million: 0.6, cache_creation_usd_per_million: 7.5,
  }
  const rule = {
    timezone: 'Asia/Shanghai', peak_windows: [{ weekdays: [1], start: '09:00', end: '12:00' }],
    offpeak_mode: 'explicit', offpeak_multiplier: 0.5, long_context_multiplier: 0.5,
    offpeak_rates: { input_usd_per_million: 1, output_usd_per_million: 2, cache_read_usd_per_million: 0.1, cache_creation_usd_per_million: 1.25 },
  }
  await edit({ price_scope: 'channel', channel_brand: 'claude', channel_auth_type: 'apikey', channel_key: 'claude-channel', preserved_long_context: null, long_context: longContext, time_pricing: rule })
  await prices.state.savePrice()
  assert.equal(env.writes.length, 1)
  assert.equal(env.writes[0].cache_creation_usd_per_million, 3.75)
  assert.deepEqual(env.writes[0].long_context, longContext)
  assert.deepEqual(env.writes[0].time_pricing, rule)
  assert.ok(!/cache_creation_(?:1h|5m|ttl)/.test(JSON.stringify(env.writes[0])), 'The real price editor saves only the single cache write rate at every level')
  app.unmount()
  const { default: matrix } = await server.ssrLoadModule('/__claude-cache-matrix.ts')
  const matrixHost = { children: [] }
  app = renderer.createApp({ setup: () => () => h(matrix, { rates: price, longContext, rule, preservedLongContext: price.preserved_long_context }) })
  app.mount(matrixHost)
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    await settle()
    const header = descendants(matrixHost).find(node => node.type === 'thead')
    assert.deepEqual(descendants(header).filter(node => node.type === 'th').map(textContent), language === 'zh'
      ? ['USD / 1M', '输入', '输出', '缓存读', '缓存写']
      : ['USD / 1M', 'Input', 'Output', 'Cache read', 'Cache write'])
    const body = descendants(matrixHost).find(node => node.type === 'tbody')
    assert.deepEqual(descendants(body).filter(node => node.type === 'tr').map(row => descendants(row).filter(node => node.type === 'td').map(textContent)), [
      ['3', '15', '0.3', '3.75'], ['1', '2', '0.1', '1.25'], ['6', '30', '0.6', '7.5'], ['3', '15', '0.3', '3.75'],
    ])
    assert.ok(!/\b(?:5m|1h|TTL)\b/.test(textContent(matrixHost)), 'Saved/version previews use four normal rate columns')
  }
  app.unmount()
  const { default: editor } = await server.ssrLoadModule('/__claude-cache-editor.ts')
  const editorProps = reactive({
    modelValue: structuredClone(rule),
  })
  const host = { children: [] }
  app = renderer.createApp({ setup: () => () => h(editor, { ...editorProps, 'onUpdate:modelValue': value => { editorProps.modelValue = value } }) })
  app.mount(host)
  await settle()
  const cacheWriteInput = () => {
    const field = ui(host, 'NFormItem').find(node => node.props.label.startsWith('Off-peak cache write'))
    assert.ok(field)
    return ui(field, 'NInputNumber')[0]
  }
  assert.equal(ui(host, 'NFormItem').filter(node => node.props.label.startsWith('Off-peak cache write')).length, 1)
  assert.equal(cacheWriteInput().props.value, 1.25)
  updateValue(cacheWriteInput(), 2)
  await settle()
  assert.equal(editorProps.modelValue.offpeak_rates.cache_creation_usd_per_million, 2)
  assert.equal(cacheWriteInput().props.value, 2)
  updateValue(cacheWriteInput(), 0)
  await settle()
  assert.equal(editorProps.modelValue.offpeak_rates.cache_creation_usd_per_million, 0)
  assert.equal(cacheWriteInput().props.value, 0)
  assert.ok(!/cache_creation_(?:1h|5m|ttl)/.test(JSON.stringify(editorProps.modelValue)))
  assert.equal(env.errors.length, 0)
  globalThis.console.log('Claude cache SFC smoke tests passed: single-rate quotes, forms, payloads, preserved tiers and matrix previews')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.__claudeCacheSmoke
}
