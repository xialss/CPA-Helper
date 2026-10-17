import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createRenderer, nextTick } from 'vue'
import { createServer, transformWithEsbuild } from 'vite'

const root = fileURLToPath(new URL('..', import.meta.url))
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN' } }
let filterStatus = 'not_loaded'
globalThis.fetch = async (url) => {
  assert.equal(new URL(url, 'http://model-list-notice-smoke.invalid').pathname, '/api/account/models')
  return Response.json({
    has_api_keys: true,
    api_key_count: 1,
    queryable_api_key_count: 1,
    model_list_filter_status: filterStatus,
    models: [{ id: 'raw-cpa-model', name: 'Raw CPA model', owner: null, price: null, sources: [] }],
    errors: [],
  })
}

const server = await createServer({
  root,
  logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'lucide-vue-next', 'vue-router'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'model-list-notice-smoke',
    enforce: 'pre',
    resolveId(id) {
      if (id === '/__model-list-notice-view.ts') return '\0model-list-notice:view'
      if (id.replaceAll('\\', '/').endsWith('/features/models/components/ModelListNotice.vue')) return '\0model-list-notice:notice'
      if (id === 'naive-ui') return '\0model-list-notice:naive-ui'
      if (id === 'lucide-vue-next') return '\0model-list-notice:lucide-vue-next'
      if (id === 'vue-router') return '\0model-list-notice:vue-router'
    },
    async load(id) {
      if (id === '\0model-list-notice:view' || id === '\0model-list-notice:notice') {
        const filename = id === '\0model-list-notice:view'
          ? `${root}/src/features/models/views/AvailableModelsView.vue`
          : `${root}/src/features/models/components/ModelListNotice.vue`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: 'available-models', genDefaultAs: '__view' })
        const template = compileTemplate({ id: 'available-models', filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        return (await transformWithEsbuild(`${script.content}\n${template.code}\n__view.render = render; export default __view`, filename, { loader: 'ts' })).code
      }
      if (id === '\0model-list-notice:naive-ui') {
        return `import { h } from 'vue';\n${['NAlert', 'NButton', 'NDataTable', 'NEmpty', 'NIcon', 'NPopover', 'NSpace', 'NSpin', 'NTag'].map((name) => `export const ${name} = { inheritAttrs: false, setup(_, { attrs, slots }) { return () => h('stub-${name}', attrs, slots.default?.()) } }`).join('\n')}`
      }
      if (id === '\0model-list-notice:vue-router') return 'export const useRouter = () => ({ push: async () => {} })'
      if (id === '\0model-list-notice:lucide-vue-next') {
        return ['Cpu', 'KeyRound', 'RefreshCw', 'ShieldCheck'].map((name) => `export const ${name} = { render: () => null }`).join('\n')
      }
    },
  }],
})

const renderer = createRenderer({
  createElement: (type) => ({ type, props: {}, children: [] }),
  createText: (text) => ({ type: 'text', text }),
  createComment: () => ({ type: 'comment' }),
  insert(node, parent, anchor) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    node.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
  },
  remove(node) { node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  setText(node, text) { node.text = text },
  setElementText(node, text) { node.children = [{ type: 'text', text }] },
  patchProp(node, key, _previous, value) { node.props[key] = value },
  parentNode: (node) => node.parent,
  nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
})
const settle = async () => { await nextTick(); await nextTurn(); await nextTick() }
const textContent = (node) => node.text ?? (node.children ?? []).map(textContent).join('')
const nodesByType = (node, type) => [
  ...(node.type === type ? [node] : []),
  ...(node.children ?? []).flatMap((child) => nodesByType(child, type)),
]
let app

try {
  const { setLanguage } = await server.ssrLoadModule('/src/shared/i18n/index.ts')
  const { default: availableModelsView } = await server.ssrLoadModule('/__model-list-notice-view.ts')
  let state
  const host = { children: [] }
  app = renderer.createApp({
    ...availableModelsView,
    setup(props, context) {
      state = availableModelsView.setup(props, context)
      return state
    },
  })
  app.mount(host)
  await settle()
  const alert = () => {
    const alerts = nodesByType(host, 'stub-NAlert')
    assert.equal(alerts.length, 1, 'The filtering state produces one visible catalog notice')
    return alerts[0]
  }
  const notice = () => textContent(alert())
  assert.equal(state.modelCount.value, 1, 'The original catalog remains available')
  assert.equal(state.queryStatus.value, '未按规则过滤')
  assert.match(textContent(host), /未按规则过滤/, 'The query-status card shows the unfiltered state')
  assert.match(notice(), /插件未加载/)
  assert.match(notice(), /未按插件路由规则过滤/)
  assert.equal(alert().props.type, 'warning')
  setLanguage('en')
  await nextTick()
  assert.equal(state.queryStatus.value, 'Unfiltered')
  assert.match(notice(), /plugin is not loaded/i)
  assert.match(notice(), /without plugin routing-rule filtering/i)
  assert.doesNotMatch(notice(), /still apply/i, 'An unloaded plugin cannot guarantee request-time routing')

  filterStatus = 'not_configured'
  await state.refresh()
  await nextTick()
  assert.equal(state.queryStatus.value, 'Filtering unverified')
  assert.match(textContent(host), /Filtering unverified/, 'An unconfigured management key is not reported as normal or definitely unfiltered')
  assert.match(notice(), /catalog filtering cannot be verified/i)
  assert.match(notice(), /actual request behavior is determined by CPA/i)
  assert.doesNotMatch(notice(), /plugin is not loaded|without plugin routing-rule filtering|still apply/i)
  assert.equal(alert().props.type, 'warning')
  setLanguage('zh')
  await nextTick()
  assert.equal(state.queryStatus.value, '过滤状态未确认')
  assert.match(notice(), /无法确认模型目录是否按路由规则过滤/)

  filterStatus = 'enabled'
  await state.refresh()
  await nextTick()
  assert.equal(state.queryStatus.value, '正常')
  assert.equal(alert().props.type, 'info')
  console.log('Model list notice smoke passed: actual catalog alert and status card, bilingual unloaded/unconfigured states, preserved catalog and enabled state')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.fetch
  delete globalThis.localStorage
  delete globalThis.document
}
