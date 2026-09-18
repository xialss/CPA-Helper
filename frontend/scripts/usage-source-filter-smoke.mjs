import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, nextTick } from 'vue'

const sourceA = 'a'.repeat(64)
const sourceB = 'b'.repeat(64)
const missingSource = 'c'.repeat(64)
const safeLabel = '同名渠道 <reference> · sk-abcd…wxyz'
const frontendRoot = fileURLToPath(new URL('..', import.meta.url))
const bridge = { current: null }
globalThis.__usageSourceFilterSmoke = bridge
globalThis.localStorage = { getItem: () => 'zh', setItem: () => {} }
globalThis.document = { documentElement: { lang: 'zh-CN', clientWidth: 1280 } }
globalThis.getComputedStyle = () => ({ getPropertyValue: () => '' })
globalThis.window = {
  setInterval(callback, delay) {
    const id = bridge.current.nextTimer++
    bridge.current.timers.set(id, { callback, delay })
    return id
  },
  clearInterval(id) { assert.ok(bridge.current.timers.delete(id), 'Unmount removes its timer') },
  matchMedia() {
    const listeners = new Set()
    return {
      matches: true,
      addEventListener(_event, callback) { listeners.add(callback) },
      removeEventListener(_event, callback) { assert.ok(listeners.delete(callback)) },
    }
  },
}
globalThis.fetch = (url) => bridge.current.fetch(url)

const passiveUI = ['NButton', 'NDataTable', 'NDatePicker', 'NDrawerContent', 'NIcon', 'NPagination', 'NRadioButton', 'NRadioGroup', 'NSpin', 'NTag', 'NTooltip']
const iconNames = ['CircleDollarSign', 'ClipboardList', 'Gauge', 'Info', 'Layers3', 'ShieldCheck', 'Timer', 'Zap']
const stubComponent = "{ inheritAttrs: false, render: () => null }"

// Compile the actual SFC script and template. Only UI, router and HTTP boundaries
// are replaced; filter handlers, timers, API serialization and retention stay real.
const server = await createServer({
  root: frontendRoot,
  logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'vue-router', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'usage-source-filter-smoke',
    enforce: 'pre',
    resolveId(id) {
      if (id.startsWith('/__usage-source-smoke__/')) return `\0${id}`
      if (id === 'naive-ui' || id === 'vue-router' || id === 'lucide-vue-next') return `\0smoke:${id}`
      if (id.replaceAll('\\', '/').endsWith('/shared/components/ChartPanel.vue')) return '\0smoke:chart'
    },
    async load(id) {
      if (id.startsWith('\0/__usage-source-smoke__/')) {
        const name = id.split('/').at(-1).replace('.ts', '.vue')
        const filename = `${frontendRoot}/src/features/usage/views/${name}`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: name, genDefaultAs: '__smokeView' })
        const template = compileTemplate({
          id: name,
          filename,
          source: descriptor.template.content,
          compilerOptions: { bindingMetadata: script.bindings },
        })
        assert.deepEqual(template.errors, [])
        const compiled = `${script.content}\n${template.code}\n__smokeView.render = render; export default __smokeView`
        return (await transformWithEsbuild(compiled, filename, { loader: 'ts' })).code
      }
      if (id === '\0smoke:vue-router') {
        return 'export const useRoute = () => globalThis.__usageSourceFilterSmoke.current.route; export const useRouter = () => globalThis.__usageSourceFilterSmoke.current.router'
      }
      if (id === '\0smoke:naive-ui') {
        return `import { onBeforeUnmount } from 'vue';
          ${passiveUI.map(name => `export const ${name} = ${stubComponent};`).join('\n')}
          export const useMessage = () => globalThis.__usageSourceFilterSmoke.current.message;
          export const NDrawer = {
            inheritAttrs: false,
            props: { show: Boolean, autoFocus: { type: Boolean, default: true } },
            emits: ['update:show'],
            setup(props) {
              const drawers = globalThis.__usageSourceFilterSmoke.current.drawers;
              drawers.add(props);
              onBeforeUnmount(() => drawers.delete(props));
              return () => null;
            }
          };
          export const NSelect = {
            inheritAttrs: false,
            props: { value: [String, Number], options: Array, placeholder: String, clearable: Boolean, filterable: Boolean, renderLabel: Function, fallbackOption: Function },
            emits: ['update:value'],
            setup(props, { emit }) {
              const selects = globalThis.__usageSourceFilterSmoke.current.selects;
              const select = { props, emit };
              selects.add(select);
              onBeforeUnmount(() => selects.delete(select));
              return () => null;
            }
          };`
      }
      if (id === '\0smoke:lucide-vue-next') return iconNames.map(name => `export const ${name} = ${stubComponent};`).join('\n')
      if (id === '\0smoke:chart') return `export default ${stubComponent}`
    },
  }],
})

const renderer = createRenderer({
  insert(node, parent, anchor) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    node.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
  },
  remove(node) { node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  createElement: (tag) => ({ tag, children: [], style: {} }),
  createText: (text) => ({ text }),
  createComment: (text) => ({ text }),
  setText(node, text) { node.text = text },
  setElementText(node, text) { node.text = text },
  parentNode: (node) => node.parent,
  nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  patchProp(node, key, _previous, value) { node[key] = value },
})

function summaryFor(params) {
  const count = params.source_key === sourceA ? 11 : params.source_key === sourceB ? 22 : params.source_key === missingSource ? 0 : 33
  return {
    start: params.start ?? '2026-09-13T00:00:00+08:00',
    end: params.end ?? '2026-09-14T00:00:00+08:00',
    total_records: count, failed_records: count ? 1 : 0, success_records: Math.max(0, count - 1),
    input_tokens: count, output_tokens: count, cached_tokens: 0, normal_input_tokens: count,
    cache_read_tokens: 0, cache_creation_tokens: 0, reasoning_tokens: 0, total_tokens: count * 2,
    average_ttft_ms: null, estimated_cost_usd: count / 100, unpriced_records: 0,
  }
}

function makeEnvironment(query = {}) {
  const env = {
    route: { query }, requests: [], replacements: [], pushes: [], errors: [],
    selects: new Set(), drawers: new Set(), timers: new Map(), nextTimer: 1, gates: [],
    sourceOptions: [{ key: sourceA, label: safeLabel }, { key: sourceB, label: safeLabel }],
  }
  env.message = { error: (error) => env.errors.push(error) }
  env.router = {
    replace: async (target) => { env.replacements.push(target); env.route.query = target.query },
    push: async (target) => { env.pushes.push(target) },
  }
  env.fetch = async (url) => {
    assert.ok(url.startsWith('/api/'), 'Only mocked application requests are allowed')
    const parsed = new URL(url, 'http://usage-smoke.invalid')
    const entry = { path: parsed.pathname, params: Object.fromEntries(parsed.searchParams) }
    env.requests.push(entry)
    const summary = summaryFor(entry.params)
    let payload
    if (entry.path === '/api/usage/summary') payload = summary
    else if (entry.path === '/api/usage/overview') payload = {
      summary, trends: [{ bucket: summary.start, records: summary.total_records, failed_records: summary.failed_records, total_tokens: summary.total_tokens, estimated_cost_usd: summary.estimated_cost_usd }],
      user_ranking: { group_by: 'user', items: [] }, model_ranking: { group_by: 'model', items: [] },
      distributions: { providers: [], models: [], endpoints: [], channel_costs: [] },
    }
    else if (entry.path === '/api/usage/options') payload = {
      users: [], api_key_descriptions: [], providers: ['provider-a'], models: ['model-a'], endpoints: ['/v1/messages'],
      sources: entry.params.scope === 'account' ? [] : structuredClone(env.sourceOptions),
    }
    else if (entry.path === '/api/usage/records') payload = {
      start: summary.start, end: summary.end, total: summary.total_records,
      items: summary.total_records ? [{ id: summary.total_records, user_id: null, api_key_description: null, provider: 'provider-a', model: 'model-a', endpoint: '/v1/messages' }] : [],
    }
    else if (/^\/api\/usage\/records\/\d+$/.test(entry.path)) payload = {
      id: Number(entry.path.split('/').at(-1)), source: 'x'.repeat(64), source_label: '测试渠道 · sk-abcd…wxyz', raw_json: {},
    }
    else if (entry.path === '/api/account/quota') payload = null
    else assert.fail(`Unexpected API call ${entry.path}`)
    const gateIndex = env.gates.findIndex(gate => gate.matches(entry))
    if (gateIndex >= 0) {
      const [gate] = env.gates.splice(gateIndex, 1)
      gate.entry = entry
      gate.payload = payload
      payload = await gate.promise
    }
    return { ok: true, status: 200, json: async () => payload }
  }
  env.holdNext = (matches) => {
    let resolve
    let reject
    const promise = new Promise((accept, fail) => { resolve = accept; reject = fail })
    const gate = {
      matches, promise,
      release: () => { assert.ok(gate.entry, 'The held request was reached'); resolve(gate.payload) },
      reject: (error) => { assert.ok(gate.entry, 'The held request was reached'); reject(error) },
    }
    env.gates.push(gate)
    return gate
  }
  env.fireTimer = (delay) => {
    const timer = [...env.timers.values()].find(item => item.delay === delay)
    assert.ok(timer, `Missing ${delay}ms refresh timer`)
    timer.callback()
  }
  return env
}

async function settle() {
  await nextTick()
  await nextTurn()
  await nextTick()
}

async function mountView(name, scope, env) {
  bridge.current = env
  const { default: component } = await server.ssrLoadModule(`/__usage-source-smoke__/${name}.ts`)
  const app = renderer.createApp({
    ...component,
    setup(props, context) {
      env.state = component.setup(props, context)
      return env.state
    },
  }, { scope })
  app.mount({ children: [] })
  env.unmount = () => {
    app.unmount()
    assert.equal(env.timers.size, 0)
    assert.equal(env.selects.size, 0)
    assert.equal(env.drawers.size, 0)
  }
  await settle()
  return env
}

function sourceSelect(env) {
  return [...env.selects].find(select => ['来源', 'Source'].includes(select.props.placeholder))
}

function assertSourceRequests(env, source, start = 0) {
  const requests = env.requests.slice(start).filter(entry => entry.path.startsWith('/api/usage/'))
  const dataRequests = requests.filter(entry => entry.path !== '/api/usage/options')
  assert.ok(dataRequests.length, 'The view made real serialized usage requests')
  for (const entry of dataRequests) assert.equal(entry.params.source_key, source, `${entry.path} preserves the selected source`)
  for (const entry of requests.filter(entry => entry.path === '/api/usage/options')) {
    assert.ok(Object.keys(entry.params).every(key => ['scope', 'start', 'end'].includes(key)), 'Source options remain scoped only by scope and date')
  }
  return dataRequests
}

function assertSourceControl(env) {
  const select = sourceSelect(env)
  assert.ok(select, 'An admin view renders a Source select')
  assert.equal(select.props.clearable, true)
  assert.equal(select.props.filterable, true)
  assert.deepEqual(select.props.options.map(option => option.value), [sourceA, sourceB], 'Equal labels do not merge distinct source values')
  assert.ok(select.props.options.every(option => option.label === safeLabel))
  for (const selected of [false, true]) {
    const label = select.props.renderLabel(select.props.options[0], selected)
    assert.equal(label.type, 'span')
    assert.equal(label.children, safeLabel)
    assert.equal(label.props.title, safeLabel, 'Hover retains the complete safe label as text')
  }
  assert.deepEqual(select.props.fallbackOption(missingSource), { value: missingSource, label: '已选来源' })
  return select
}

let scenarios = 0
let requestCount = 0
async function withView(name, scope, env, check) {
  await mountView(name, scope, env)
  try {
    await check(env)
    scenarios += 1
    requestCount += env.requests.length
  } finally {
    env.unmount()
  }
}

const isMainOverview = entry => entry.path === '/api/usage/overview' && entry.params.primary_ranking_sort !== undefined
const isMinuteSummary = entry => entry.path === '/api/usage/summary' && new Date(entry.params.end).getTime() - new Date(entry.params.start).getTime() === 60000

try {
  await withView('UsageHistoryView', 'admin', makeEnvironment({
    source_key: sourceA, quick_range: 'today', provider: 'provider-a', model: 'model-a',
    user_id: '7', api_key_description: 'Key 描述', endpoint: '/v1/messages', failed: 'false',
  }), async (env) => {
    assert.equal(env.requests[0].params.source_key, sourceA, 'History restores the original source key into the real overview request')
    assert.equal(env.state.filterForm.source_key, sourceA)
    const select = assertSourceControl(env)
    const initial = assertSourceRequests(env, sourceA)
    assert.equal(initial.filter(entry => entry.path === '/api/usage/overview').length, 3, 'Main, today and failed overview requests all execute')
    assert.equal(initial.filter(isMinuteSummary).length, 2, 'Both minute summaries use the same source')
    assert.equal(initial.filter(entry => entry.params.failed === 'true').length, 1)
    assert.equal(env.replacements.at(-1).query.source_key, sourceA)

    let start = env.requests.length
    env.fireTimer(5000)
    await settle()
    assert.equal(assertSourceRequests(env, sourceA, start).length, 3, 'The five-second refresh includes the main and both minute summaries')
    start = env.requests.length
    env.state.primaryRankingSort.value = 'cost'
    env.state.modelRankingSort.value = 'records'
    env.fireTimer(60000)
    await settle()
    assert.equal(assertSourceRequests(env, sourceA, start).length, 5, 'The sixty-second refresh keeps all auxiliary requests')
    assert.equal(env.requests[start].params.primary_ranking_sort, 'cost')
    assert.equal(env.requests[start].params.model_ranking_sort, 'records')
    assert.equal(env.requests.slice(start).some(entry => entry.path === '/api/usage/options'), false)

    start = env.requests.length
    select.emit('update:value', sourceB)
    await settle()
    assertSourceRequests(env, sourceB, start)
    assert.equal(env.state.summary.value.total_records, 22)
    assert.equal(env.replacements.at(-1).query.source_key, sourceB)
    env.state.handleProviderChange('provider-b')
    await settle()
    env.state.handleModelChange('model-b')
    await settle()
    assert.equal(env.state.filterForm.source_key, sourceB, 'Provider and model changes keep the independent source condition')
    assert.equal(env.requests.findLast(isMainOverview).params.source_key, sourceB)
    assert.equal(env.requests.findLast(isMainOverview).params.provider, 'provider-b')

    start = env.requests.length
    select.emit('update:value', null)
    await settle()
    assertSourceRequests(env, undefined, start)
    assert.equal(env.state.filterForm.source_key, null)
    assert.equal('source_key' in env.replacements.at(-1).query, false)
    assert.equal(env.replacements.at(-1).query.provider, 'provider-b')
    assert.equal(env.replacements.at(-1).query.model, 'model-b')
    assert.equal(env.replacements.at(-1).query.api_key_description, 'Key 描述')
    assert.equal(env.replacements.at(-1).query.user_id, '7')
    assert.equal(env.replacements.at(-1).query.failed, 'false')
    assert.deepEqual(env.errors, [])
  })

  await withView('UsageRecordsView', 'admin', makeEnvironment({ source_key: sourceA, quick_range: 'today', provider: 'provider-a', model: 'model-a' }), async (env) => {
    assert.equal([...env.drawers][0].autoFocus, false, 'The request-detail drawer does not autofocus the Source tooltip trigger')
    const select = assertSourceControl(env)
    assertSourceRequests(env, sourceA)
    assert.equal(env.replacements.at(-1).query.source_key, sourceA)
    let start = env.requests.length
    env.fireTimer(5000)
    await settle()
    assert.equal(assertSourceRequests(env, sourceA, start).length, 1)
    env.sourceOptions = env.sourceOptions.map(option => ({ ...option, label: `改名渠道 · sk-abcd…wxyz` }))
    select.emit('update:value', sourceB)
    await settle()
    assert.equal(select.props.value, sourceB)
    assert.equal(select.props.options[1].label, '改名渠道 · sk-abcd…wxyz')
    assert.equal(env.requests.findLast(entry => entry.path === '/api/usage/records').params.source_key, sourceB)
    start = env.requests.length
    select.emit('update:value', null)
    await settle()
    assertSourceRequests(env, undefined, start)
    assert.equal('source_key' in env.replacements.at(-1).query, false)
    assert.equal(env.replacements.at(-1).query.provider, 'provider-a')
    env.state.hoveredSourceTooltipKey.value = 'detail'
    env.state.focusedSourceTooltipKey.value = 'detail'
    await env.state.openRecord({ id: 22 })
    assert.equal(env.state.drawerOpen.value, true)
    assert.equal(env.state.hoveredSourceTooltipKey.value, null)
    assert.equal(env.state.focusedSourceTooltipKey.value, null, 'Opening details clears any stale Source tooltip state')
    assert.deepEqual(env.errors, [])
  })

  for (const name of ['UsageHistoryView', 'UsageRecordsView']) {
    await withView(name, 'account', makeEnvironment({ source_key: sourceA, user_id: '7', quick_range: 'today' }), async (env) => {
      assert.equal(sourceSelect(env), undefined, 'Account templates hide the Source selector')
      assertSourceRequests(env, undefined)
      assert.ok(env.requests.every(entry => entry.params.user_id === undefined))
      assert.equal('source_key' in env.replacements.at(-1).query, false, 'Account URL updates remove the inaccessible filter')
      const start = env.requests.length
      env.fireTimer(5000)
      await settle()
      assertSourceRequests(env, undefined, start)
      if (name === 'UsageHistoryView') {
        env.fireTimer(60000)
        await settle()
        env.state.goRecords()
        assert.equal(env.pushes.at(-1).name, 'account-records')
        assert.equal('source_key' in env.pushes.at(-1).query, false)
      }
      assertSourceRequests(env, undefined)
      assert.deepEqual(env.errors, [])
    })

    await withView(name, 'admin', makeEnvironment({ source_key: missingSource, quick_range: 'last7d' }), async (env) => {
      const select = sourceSelect(env)
      assert.equal(select.props.value, missingSource)
      assert.equal(select.props.options.some(option => option.value === missingSource), false)
      assert.deepEqual(select.props.fallbackOption(missingSource), { value: missingSource, label: '已选来源' })
      env.state.currentLanguage.value = 'en'
      await settle()
      assert.deepEqual(select.props.fallbackOption(missingSource), { value: missingSource, label: 'Selected source' })
      env.state.currentLanguage.value = 'zh'
      env.sourceOptions = []
      await env.state.refresh()
      await settle()
      assert.deepEqual(select.props.options, [])
      assert.equal(select.props.value, missingSource, 'Missing options never clear the real filter value')
      assert.equal(name === 'UsageHistoryView' ? env.state.summary.value.total_records : env.state.total.value, 0, 'An empty filtered result does not broaden the source query')
      assertSourceRequests(env, missingSource)
      assert.equal(env.replacements.at(-1).query.source_key, missingSource)
      assert.deepEqual(env.errors, [])
    })
  }

  for (const name of ['UsageHistoryView', 'UsageRecordsView']) {
    for (const failOld of [false, true]) {
      const env = makeEnvironment({ source_key: sourceA, quick_range: 'today' })
      const old = env.holdNext(entry => name === 'UsageHistoryView' ? isMainOverview(entry) : entry.path === '/api/usage/records')
      await withView(name, 'admin', env, async () => {
        sourceSelect(env).emit('update:value', sourceB)
        await settle()
        assert.equal(env.state.filterForm.source_key, sourceB)
        if (failOld) old.reject(new Error('stale source A failure'))
        else old.release()
        await settle()
        assert.equal(name === 'UsageHistoryView' ? env.state.summary.value.total_records : env.state.total.value, 22)
        assert.ok(env.replacements.every(target => target.query.source_key === sourceB), 'Stale source A cannot replace the new URL')
        assert.deepEqual(env.errors, [], 'A stale failure does not become a new-selection error')
        assertSourceRequests(env, sourceB, env.requests.findIndex(entry => entry.params.source_key === sourceB))
      })
    }
  }

  for (const phase of ['today', 'options']) {
    const env = makeEnvironment({ source_key: sourceA, quick_range: 'today' })
    const old = env.holdNext(entry => phase === 'options'
      ? entry.path === '/api/usage/options'
      : entry.path === '/api/usage/overview' && !isMainOverview(entry) && entry.params.failed !== 'true')
    await withView('UsageHistoryView', 'admin', env, async () => {
      assert.ok(old.entry)
      const next = env.holdNext(isMainOverview)
      env.sourceOptions = [{ key: sourceB, label: '渠道 B · sk-abcd…wxyz' }]
      sourceSelect(env).emit('update:value', sourceB)
      old.release()
      await settle()
      assert.ok(next.entry)
      if (phase === 'options') assert.deepEqual(env.state.options.value.sources, [], 'Stale options cannot publish while source B loads')
      else assert.deepEqual(env.state.todayTrends.value, [], 'Stale auxiliary success cannot publish while source B loads')
      next.release()
      await settle()
      assert.equal(env.state.summary.value.total_records, 22)
      assert.deepEqual(env.state.options.value.sources, env.sourceOptions)
      assert.deepEqual(env.errors, [])
    })
  }

  const slow = makeEnvironment({ source_key: sourceA, quick_range: 'all', provider: 'provider-a', model: 'model-a' })
  const firstOverview = slow.holdNext(isMainOverview)
  await withView('UsageHistoryView', 'admin', slow, async (env) => {
    const queuedOverview = env.holdNext(isMainOverview)
    env.fireTimer(5000)
    env.fireTimer(60000)
    assert.equal(env.requests.length, 1, 'Timers do not start competing requests during a slow full refresh')
    firstOverview.release()
    await settle()
    assert.ok(queuedOverview.entry)
    assert.equal(env.state.summary.value.total_records, 11, 'A silent timer does not invalidate the slow all-history result')
    assert.equal(env.replacements.at(-1).query.start, '0001-01-01T00:00:00+08:00')
    assert.equal(env.replacements.at(-1).query.end, '9999-12-31T23:59:59+08:00')
    queuedOverview.release()
    await settle()
    assertSourceRequests(env, sourceA)
    assert.equal(env.requests.some(isMinuteSummary), false, 'All-history queries do not enable today-only minute summaries')
    for (const extra of [{}, { failed: true }, env.state.rankingFilters({ user_id: 17 }), env.state.modelFilters({ key: 'provider-b::model-b' })]) {
      env.state.goRecords(extra)
      const target = env.pushes.at(-1)
      assert.equal(target.name, 'admin-records')
      assert.equal(target.query.source_key, sourceA, 'Every drilldown retains source through the retention conversion')
      assert.equal(target.query.quick_range, 'last7d')
      assert.notEqual(target.query.start, env.state.buildFilters().start)
      assert.notEqual(target.query.end, env.state.buildFilters().end)
    }
    assert.equal(env.pushes[1].query.failed, 'true')
    assert.equal(env.pushes[2].query.user_id, '17')
    assert.equal(env.pushes[3].query.model, 'model-b')
    assert.deepEqual(env.errors, [])
  })

  const partial = makeEnvironment({ source_key: sourceA, quick_range: 'today' })
  const previousMinute = partial.holdNext(entry => isMinuteSummary(entry) && partial.requests.filter(isMinuteSummary).length === 2)
  await withView('UsageHistoryView', 'admin', partial, async (env) => {
    previousMinute.reject(new Error('previous minute unavailable'))
    await settle()
    assert.equal(env.state.realtimeSummary.value.total_records, 11)
    assert.equal(env.state.previousRealtimeSummary.value, null)
    assert.equal(env.state.previousRealtimeSummaryFailed.value, true)
    const currentMinute = env.holdNext(isMinuteSummary)
    env.fireTimer(5000)
    await settle()
    currentMinute.reject(new Error('current minute unavailable'))
    await settle()
    assert.equal(env.state.realtimeSummary.value, null)
    assert.equal(env.state.realtimeSummaryFailed.value, true)
    assert.equal(env.state.previousRealtimeSummary.value.total_records, 11)
    assertSourceRequests(env, sourceA)
    assert.deepEqual(env.errors, [])
  })
  globalThis.console.log(`Usage source filter smoke passed: ${scenarios} actual SFC scenarios, ${requestCount} serialized API requests; source controls, permissions, timers, stale responses, partial failures and retention navigation`)
} finally {
  await server.close()
}
