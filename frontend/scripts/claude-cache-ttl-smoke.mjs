import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, h, nextTick, reactive } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const env = { errors: [], models: [], renderTable: true }
globalThis.__claudeCacheTTLSmoke = env
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN', clientWidth: 1280 } }
globalThis.window = {
  localStorage: globalThis.localStorage,
  matchMedia: () => ({ matches: true, addEventListener() {}, removeEventListener() {} }),
}
globalThis.getComputedStyle = () => ({ getPropertyValue: () => '' })
globalThis.fetch = async (url) => {
  const path = new URL(url, 'http://claude-ttl-smoke.invalid').pathname
  let result
  if (path === '/api/account/models') result = {
    has_api_keys: true, api_key_count: 1, queryable_api_key_count: 1,
    model_list_filter_status: 'enabled', models: env.models, errors: [],
  }
  else if (path === '/api/model-prices/catalog') result = { models: [], channels: [], errors: [] }
  else if (['/api/model-prices', '/api/model-prices/library-conflicts', '/api/model-prices/channel-aliases'].includes(path)) result = []
  else assert.fail(`Unexpected API request ${path}`)
  return Response.json(result)
}

const views = {
  models: 'src/features/models/views/AvailableModelsView.vue',
  prices: 'src/features/pricing/views/ModelPricesView.vue',
  editor: 'src/features/pricing/components/PriceTimeRuleEditor.vue',
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
    name: 'claude-cache-ttl-smoke', enforce: 'pre',
    async resolveId(id, importer) {
      for (const name of Object.keys(views)) if (id === `/__claude-ttl-${name}.ts`) return `\0claude-ttl:${name}`
      if (['naive-ui', 'vue-router', 'lucide-vue-next'].includes(id)) return `\0claude-ttl:${id}`
      if (['DeepSeekTemplateModal.vue', 'PriceRatesMatrix.vue', 'ModelListNotice.vue'].some(name => id.endsWith(`/${name}`))) return '\0claude-ttl:passive'
      const source = views[importer?.replace('\0claude-ttl:', '')]
      if (source && id.startsWith('.')) return this.resolve(resolve(dirname(`${root}/${source}`), id), undefined, { skipSelf: true })
    },
    async load(id) {
      const name = id.replace('\0claude-ttl:', '')
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
          setup(props) { return () => globalThis.__claudeCacheTTLSmoke.renderTable
            ? h('table', props.data.map(row => h('tr', { 'data-model': row.id }, props.columns.map(column => h('td', { 'data-column': column.key }, column.render ? column.render(row) : row[column.key])))))
            : null; }
        };
        export const useMessage = () => ({ error: value => globalThis.__claudeCacheTTLSmoke.errors.push(value) });
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
  const { default: component } = await server.ssrLoadModule(`/__claude-ttl-${name}.ts`)
  let state
  const host = { children: [] }
  app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } })
  app.mount(host)
  await settle()
  return { state, host }
}
function availableModel(id, provider, oneHour = 6) {
  return {
    id, name: id, owner: provider, metadata: {}, sources: [],
    price: {
      provider, model: id, billing_unit: 'token', request_usd: null, long_context: null,
      input_usd_per_million: 3, output_usd_per_million: 15, cache_read_usd_per_million: 0.3,
      cache_creation_usd_per_million: 3.75, cache_creation_1h_usd_per_million: oneHour,
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
    availableModel('claude-pair', 'anthropic'), availableModel('claude-unconfigured', 'Claude', null),
    availableModel('claude-free', 'claude', 0), availableModel('claude-looking-name', 'openai', 99),
    { ...availableModel('request-price', 'claude'), price: { ...availableModel('request-price', 'claude').price, billing_unit: 'request', request_usd: 0.04 } },
    { ...availableModel('no-price', 'claude'), price: null },
  ]
  const models = await mount('models')
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    await settle()
    const pair = quoteCell(models.host, 'claude-pair')
    assert.equal(textContent(pair), '5m3.751h6', 'Claude quotes contain only the two TTL-labelled write rates')
    assert.equal(descendants(pair).filter(node => node.type === 'small').length, 2)
    assert.equal(textContent(quoteCell(models.host, 'claude-unconfigured')), `5m3.751h${language === 'zh' ? '未配置' : 'Not configured'}`)
    assert.equal(textContent(quoteCell(models.host, 'claude-free')), '5m3.751h0', 'Explicit zero remains a configured quote')
    assert.equal(textContent(quoteCell(models.host, 'claude-looking-name')), '3.75', 'Provider identity, not the model name or stray 1h data, controls Claude rates')
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
    cache_creation_usd_per_million: 3.75, cache_creation_1h_usd_per_million: 6,
    long_context: null, request_usd: null, time_pricing: null,
    preserved_long_context: {
      threshold_input_tokens: null, input_usd_per_million: null, output_usd_per_million: 30,
      cache_read_usd_per_million: null, cache_creation_usd_per_million: 7.5,
      cache_creation_1h_usd_per_million: 12,
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
    assert.ok(textContent(alert).includes(`${prefix} 5m 7.5 · ${prefix} 1h 12`))
    assert.ok(!textContent(alert).includes(`${prefix} 7.5`), 'Claude has no third generic write label')
    alert = await edit({ preserved_long_context: { ...price.preserved_long_context, cache_creation_1h_usd_per_million: null } })
    assert.ok(textContent(alert).includes(`${prefix} 1h ${language === 'zh' ? '未设置' : 'Not set'}`))
    alert = await edit({ preserved_long_context: { ...price.preserved_long_context, cache_creation_1h_usd_per_million: 0 } })
    assert.ok(textContent(alert).includes(`${prefix} 1h 0`))
    alert = await edit({ provider: 'openai' })
    assert.ok(textContent(alert).includes(`${prefix} 7.5`))
    assert.ok(!textContent(alert).includes(`${prefix} 1h`), 'Non-Claude summaries keep one generic write rate')
  }
  app.unmount()
  const { default: editor } = await server.ssrLoadModule('/__claude-ttl-editor.ts')
  const editorProps = reactive({
    claude: true,
    modelValue: {
      timezone: 'Asia/Shanghai', peak_windows: [{ weekdays: [1], start: '09:00', end: '12:00' }],
      offpeak_mode: 'explicit', offpeak_multiplier: 0.5, long_context_multiplier: 1,
      offpeak_rates: { input_usd_per_million: 1, output_usd_per_million: 2, cache_read_usd_per_million: 0.1, cache_creation_usd_per_million: 1.25, cache_creation_1h_usd_per_million: 2 },
    },
  })
  const host = { children: [] }
  app = renderer.createApp({ setup: () => () => h(editor, { ...editorProps, 'onUpdate:modelValue': value => { editorProps.modelValue = value } }) })
  app.mount(host)
  await settle()
  const oneHourInput = () => {
    const field = ui(host, 'NFormItem').find(node => node.props.label.startsWith('Off-peak cache write 1h'))
    assert.ok(field)
    return ui(field, 'NInputNumber')[0]
  }
  assert.equal(oneHourInput().props.value, 2)
  updateValue(oneHourInput(), null)
  await settle()
  assert.equal(editorProps.modelValue.offpeak_rates.cache_creation_1h_usd_per_million, null)
  assert.equal(oneHourInput().props.value, null, 'Clearing the real input emits and renders null, never an implicit free rate')
  assert.equal(JSON.parse(JSON.stringify(editorProps.modelValue)).offpeak_rates.cache_creation_1h_usd_per_million, null)
  updateValue(oneHourInput(), 0)
  await settle()
  assert.equal(editorProps.modelValue.offpeak_rates.cache_creation_1h_usd_per_million, 0)
  assert.equal(oneHourInput().props.value, 0)
  assert.equal(editorProps.modelValue.offpeak_rates.cache_creation_usd_per_million, 1.25)
  editorProps.claude = false
  await settle()
  assert.equal(ui(host, 'NFormItem').filter(node => node.props.label.startsWith('Off-peak cache write')).length, 1)
  assert.equal(env.errors.length, 0)
  globalThis.console.log('Claude cache TTL SFC smoke tests passed')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.__claudeCacheTTLSmoke
}
