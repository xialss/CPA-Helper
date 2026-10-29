import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, nextTick } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const env = { errors: [], saved: [], failSave: false, versionRequests: [], numberUpdates: [] }
globalThis.__ultrafastSmoke = env
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN', clientWidth: 1280 } }
globalThis.window = {
  localStorage: globalThis.localStorage,
  matchMedia: () => ({ matches: true, addEventListener() {}, removeEventListener() {} }),
  setInterval: () => 1, clearInterval() {},
}
globalThis.getComputedStyle = () => ({ getPropertyValue: () => '' })
globalThis.fetch = async (url, options = {}) => {
  const path = new URL(url, 'http://ultrafast-smoke.invalid').pathname
  if (path.endsWith('/versions')) return new Promise(resolve => env.versionRequests.push({ path, resolve }))
  if (path.endsWith('/priority-multiplier')) {
    env.saved.push({ path, payload: JSON.parse(options.body) })
    if (env.failSave) return Response.json({ detail: { message: 'ultrafast 倍率会产生无法安全计价的金额', code: 'validation_error' } }, { status: 422 })
    return Response.json({})
  }
  if (path === '/api/model-prices/catalog') return Response.json({ models: [], channels: [], errors: [] })
  if (['/api/model-prices', '/api/model-prices/library-conflicts', '/api/model-prices/channel-aliases'].includes(path)) return Response.json([])
  if (path === '/api/usage/options') return Response.json({ users: [], api_key_descriptions: [], providers: [], models: [], sources: [], endpoints: [] })
  if (path === '/api/usage/records') return Response.json({ items: [], total: 0, page: 1, page_size: 50 })
  assert.fail(`Unexpected API request ${path}`)
}

const views = {
  prices: 'src/features/pricing/views/ModelPricesView.vue',
  matrix: 'src/features/pricing/components/PriceRatesMatrix.vue',
  usage: 'src/features/usage/views/UsageRecordsView.vue',
}
const passiveUI = ['NAlert', 'NButton', 'NCheckbox', 'NDatePicker', 'NDrawer', 'NDrawerContent', 'NEmpty', 'NForm', 'NFormItem', 'NIcon', 'NInput', 'NPagination', 'NRadioButton', 'NRadioGroup', 'NSelect', 'NSpace', 'NSpin', 'NSwitch', 'NTag', 'NTooltip']
const icons = ['Database', 'Layers3', 'Pencil', 'RefreshCw', 'RotateCcw', 'Search', 'Server', 'Zap']
// Compile and execute real SFC scripts/templates. Only external UI/router/HTTP
// boundaries are replaced; draft handling, payloads and formatting remain real.
const server = await createServer({
  root, logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'vue-router', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'ultrafast-sfc-smoke', enforce: 'pre',
    async resolveId(id, importer) {
      for (const name of Object.keys(views)) if (id === `/__ultrafast-${name}.ts`) return `\0ultrafast:${name}`
      if (['naive-ui', 'vue-router', 'lucide-vue-next'].includes(id)) return `\0ultrafast:${id}`
      if (id.endsWith('/PriceRatesMatrix.vue')) return '\0ultrafast:matrix'
      if (['DeepSeekTemplateModal.vue', 'PriceTimeRuleEditor.vue', 'UsageModelAuditPanel.vue'].some(name => id.endsWith(`/${name}`))) return '\0ultrafast:passive'
      const source = views[importer?.replace('\0ultrafast:', '')]
      if (source && id.startsWith('.')) return this.resolve(resolve(dirname(`${root}/${source}`), id), undefined, { skipSelf: true })
    },
    async load(id) {
      const name = id.replace('\0ultrafast:', '')
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
        export const NInputNumber = {
          inheritAttrs: false, props: { value: { type: Number, default: null }, disabled: Boolean },
          setup(props, { attrs }) {
            return () => h('stub-NInputNumber', { ...attrs, value: props.value }, h('input', {
              value: props.value ?? '', disabled: props.disabled,
              onInput(event) {
                const value = event.target.value === '' ? null : Number(event.target.value);
                // Naive UI 2.44.1 doUpdateValue suppresses unchanged numeric values.
                if (value === props.value) return;
                globalThis.__ultrafastSmoke.numberUpdates.push(value);
                const listeners = attrs['onUpdate:value'];
                for (const listener of Array.isArray(listeners) ? listeners : [listeners]) listener(value);
              }
            }));
          }
        };
        export const NModal = { props: { show: Boolean }, setup(props, { slots }) { return () => props.show ? h('stub-NModal', null, [...(slots.default?.() ?? []), ...(slots.footer?.() ?? [])]) : null; } };
        export const NDataTable = { render: () => null };
        export const useMessage = () => ({ error: value => globalThis.__ultrafastSmoke.errors.push(value), success() {} });
        export const useDialog = () => ({});`
      if (name === 'vue-router') return 'export const useRouter = () => ({ push() {}, replace() {} }); export const useRoute = () => ({ query: {} });'
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
const typeNumber = (node, value) => {
  assert.ok(node, 'Expected a rendered input')
  const input = descendants(node).find(child => child.type === 'input')
  assert.ok(input, 'Expected the number control to render a native input')
  assert.equal(input.props.disabled, false)
  const path = []
  for (let current = input; current; current = current.parent) path.push(current)
  const event = { type: 'input', target: { value: value === null ? '' : String(value) } }
  for (const current of [...path].reverse()) current.props?.onInputCapture?.(event)
  for (const current of path) current.props?.onInput?.(event)
}
const apps = new Set()
const mount = async (name, props = {}) => {
  const { default: component } = await server.ssrLoadModule(`/__ultrafast-${name}.ts`)
  let state
  const host = { children: [] }
  const app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } }, props)
  apps.add(app)
  app.mount(host)
  await settle()
  return { state, host, app }
}
const price = {
  id: 1, provider: 'openai', model: 'gpt-6.1-sol', price_scope: 'library',
  channel_brand: null, channel_key: null, channel_auth_type: null, billing_unit: 'token',
  input_usd_per_million: 2, output_usd_per_million: 4, cache_read_usd_per_million: 0, cache_creation_usd_per_million: 0,
  request_usd: null, long_context: null, preserved_long_context: null,
  time_pricing: null, priority_multiplier: 2, ultrafast_multiplier: 3,
  source: 'manual', auto_synced: false, updated_at: '2026-10-29T00:00:00Z',
}
try {
  const { setLanguage } = await server.ssrLoadModule('/src/shared/i18n/index.ts')
  const prices = await mount('prices')
  const open = async overrides => {
    prices.state.openPriorityMultiplierEditor({ ...price, ...overrides })
    await settle()
    return ui(prices.host, 'NInputNumber')
  }
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    for (const model of ['gpt-6.1-sol', 'gpt-6-astra']) {
      const inputs = await open({ model })
      assert.equal(inputs.length, 2, 'Both target models use two inputs in the existing modal')
      assert.ok(ui(prices.host, 'NFormItem').some(node => node.props.label === (language === 'zh' ? 'ultrafast 倍率' : 'ultrafast multiplier')))
      typeNumber(inputs[1], 1.00001)
      await prices.state.savePriorityMultiplier()
      assert.deepEqual(env.saved.at(-1).payload, { ultrafast_multiplier: 1.00001 }, 'An unchanged Fast value is omitted')
      const fastOnly = await open({ model })
      typeNumber(fastOnly[0], 0.00001)
      await prices.state.savePriorityMultiplier()
      assert.deepEqual(env.saved.at(-1).payload, { priority_multiplier: 0.00001 }, 'An unchanged ultrafast value is omitted')
    }
    const inputs = await open({ priority_multiplier: null, ultrafast_multiplier: null })
    typeNumber(inputs[0], 1.00001)
    typeNumber(inputs[1], 0.00001)
    env.failSave = true
    await prices.state.savePriorityMultiplier()
    await settle()
    assert.equal(prices.state.priorityModalOpen.value, true, 'Save failures keep the modal open')
    assert.equal(prices.state.priorityMultiplier.value, 1.00001)
    assert.equal(prices.state.ultrafastMultiplier.value, 0.00001, 'Failure retains both exact draft values')
    assert.deepEqual(env.saved.at(-1).payload, { priority_multiplier: 1.00001, ultrafast_multiplier: 0.00001 })
    assert.ok(env.errors.at(-1).includes(language === 'zh' ? '无法安全计价' : 'cannot be priced safely'))
    env.failSave = false
    await prices.state.savePriorityMultiplier()
    assert.equal(prices.state.priorityModalOpen.value, false)
    const other = await open({ model: 'gpt-6-sol' })
    assert.equal(other.length, 1, 'Other models keep their original Fast editor')
    const unsupported = await open({ provider: 'anthropic' })
    assert.equal(unsupported.length, 1, 'Provider scope cannot be inferred from a GPT model name')
  }

  await open({ id: 21, price_scope: 'channel', channel_brand: 'openai_compatibility', provider: 'Vendor A' })
  const oldRequest = env.versionRequests.at(-1)
  const inputs = await open({ id: 22, price_scope: 'channel', channel_brand: 'codex', provider: 'Pool' })
  assert.equal(inputs.length, 2, 'Named compatible and Codex channels use the same eligibility as billing')
  const newRequest = env.versionRequests.at(-1)
  typeNumber(inputs[1], 0.00001)
  const latest = { ...price, id: 220, priority_multiplier: 8, ultrafast_multiplier: 9, effective_at: '2099-01-01T00:00:00Z' }
  newRequest.resolve(Response.json([latest]))
  await settle()
  oldRequest.resolve(Response.json([{ ...latest, id: 210, ultrafast_multiplier: 99 }]))
  await settle()
  assert.equal(prices.state.priorityLatestVersion.value.id, 220, 'A delayed response from a previous price cannot replace this version')
  assert.equal(prices.state.ultrafastMultiplier.value, 0.00001, 'Version loading preserves the edited draft')
  prices.state.priorityCorrectLatest.value = true
  await prices.state.savePriorityMultiplier()
  assert.deepEqual(env.saved.at(-1).payload, { ultrafast_multiplier: 0.00001, correct_latest_version: true }, 'Scheduled corrections omit the untouched tier')
  const future = { ...latest, priority_multiplier: 4, ultrafast_multiplier: 5 }
  for (const [index, value, field] of [[0, 2, 'priority_multiplier'], [1, 3, 'ultrafast_multiplier']]) {
    const matchingCurrent = await open({ id: 22, price_scope: 'channel', channel_brand: 'codex', provider: 'Pool' })
    env.versionRequests.at(-1).resolve(Response.json([future]))
    await settle()
    const updatesBefore = env.numberUpdates.length
    typeNumber(matchingCurrent[index], value)
    assert.equal(env.numberUpdates.length, updatesBefore, 'Retyping the same numeric value emits no update:value')
    prices.state.priorityCorrectLatest.value = true
    await prices.state.savePriorityMultiplier()
    assert.deepEqual(env.saved.at(-1).payload, { [field]: value, correct_latest_version: true }, 'Native input intent can correct either future tier to its current value while omitting the untouched tier')
  }
  const ultraOnly = await open({ id: 22, price_scope: 'channel', channel_brand: 'codex', provider: 'Pool' })
  env.versionRequests.at(-1).resolve(Response.json([future]))
  await settle()
  typeNumber(ultraOnly[1], 6)
  prices.state.priorityCorrectLatest.value = true
  await prices.state.savePriorityMultiplier()
  assert.deepEqual(env.saved.at(-1).payload, { ultrafast_multiplier: 6, correct_latest_version: true }, 'A differing current Fast value is still omitted when only ultrafast was edited')
  await open({ id: 23, price_scope: 'channel', channel_brand: 'codex' })
  env.versionRequests.at(-1).resolve(Response.json({ detail: { message: 'version unavailable' } }, { status: 503 }))
  await settle()
  prices.state.priorityCorrectLatest.value = true
  const savedCount = env.saved.length
  await prices.state.savePriorityMultiplier()
  assert.equal(env.saved.length, savedCount, 'Corrections cannot save with an unresolved version load')
  await open({ id: 24, ultrafast_multiplier: null })
  assert.equal(prices.state.ultrafastMultiplier.value, null, 'Reopening resets the previous draft')

  for (const language of ['zh', 'en']) {
    setLanguage(language)
    const matrix = await mount('matrix', { rates: price, showFast: true, showUltrafast: true, priorityMultiplier: 1.00001, ultrafastMultiplier: 0.00001 })
    assert.ok(textContent(matrix.host).includes('Fast: 1.00001x'))
    assert.ok(textContent(matrix.host).includes('ultrafast: 0.00001x'), 'History displays exact small multipliers')
    matrix.app.unmount()
    apps.delete(matrix.app)
    const empty = await mount('matrix', { rates: price, showUltrafast: true })
    assert.ok(textContent(empty.host).includes(`ultrafast: ${language === 'zh' ? '未配置' : 'Unconfigured'}`))
    empty.app.unmount()
    apps.delete(empty.app)
  }
  const usage = await mount('usage', { scope: 'admin' })
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    const record = { provider: 'openai', model: price.model, service_tier: 'ultrafast', failed: false, cost_breakdown: { billing_unit: 'token', unpriced: false, ultrafast_eligible: true, tier_multiplier: 0.00001 } }
    assert.equal(usage.state.serviceTierLabel(record), 'ultrafast')
    assert.equal(usage.state.tierPricingNote(record), 'ultrafast x0.00001')
    assert.equal(usage.state.isFastRecord(record), true)
    delete record.cost_breakdown.tier_multiplier
    record.provider = 'Vendor A'
    assert.equal(usage.state.serviceTierLabel(record), 'ultrafast', 'A named compatible channel keeps the reported ultrafast label')
    assert.equal(usage.state.tierPricingNote(record), language === 'zh' ? 'ultrafast：未配置倍率，按基础价格估算' : 'ultrafast: multiplier unconfigured; base price estimate')
    assert.equal(usage.state.isFastRecord(record), true, 'Resolved eligible named channels retain the mode marker without a configured multiplier')
    for (const model of ['gpt-6-sol', 'gpt-6-astra']) {
      const unsupported = { ...record, provider: 'openai', model, cost_breakdown: { ...record.cost_breakdown, ultrafast_eligible: false } }
      assert.equal(usage.state.tierPricingNote(unsupported), language === 'zh' ? 'ultrafast：未应用倍率' : 'ultrafast: multiplier not applied', 'Unsupported models and target models on ineligible channels do not suggest configuration')
      assert.equal(usage.state.isFastRecord(unsupported), false)
      assert.equal(usage.state.renderModelWithPricingMarkers(unsupported), model, 'Unsupported raw tiers do not acquire the lightning marker')
    }
    const appliedWithoutEligibility = { ...record, cost_breakdown: { billing_unit: 'token', unpriced: false, tier_multiplier: 1.00001 } }
    assert.equal(usage.state.tierPricingNote(appliedWithoutEligibility), 'ultrafast x1.00001', 'Actually applied multipliers remain authoritative when reading a prior DTO')
    assert.equal(usage.state.isFastRecord(appliedWithoutEligibility), true)
    record.cost_breakdown.unpriced = true
    assert.equal(usage.state.tierPricingNote(record), language === 'zh' ? 'ultrafast：未定价' : 'ultrafast: unpriced')
  }
  console.log('ultrafast actual SFC smoke passed')
} finally {
  for (const app of apps) app.unmount()
  await server.close()
  delete globalThis.__ultrafastSmoke
  delete globalThis.fetch
  delete globalThis.localStorage
  delete globalThis.document
  delete globalThis.window
  delete globalThis.getComputedStyle
}
