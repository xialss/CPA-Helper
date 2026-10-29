import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, h, nextTick, reactive } from 'vue'

const frontendRoot = fileURLToPath(new URL('..', import.meta.url))
const featureRoot = `${frontendRoot}/src/features/ai-providers`
const bridge = { current: null }
globalThis.__aiProviderConfigSmoke = bridge
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN' } }
globalThis.window = { setTimeout, clearTimeout }
globalThis.fetch = (...args) => bridge.current.fetch(...args)

const renderer = createRenderer({
  insert(node, parent, anchor) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    node.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
  },
  remove(node) { node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  createElement: tag => ({ tag, children: [], props: {} }),
  createText: text => ({ text }), createComment: text => ({ text }),
  setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text },
  parentNode: node => node.parent,
  nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  patchProp(node, key, _previous, value) { node.props[key] = value },
})

const passiveUI = ['NAlert', 'NButton', 'NCheckbox', 'NDataTable', 'NDrawerContent', 'NForm', 'NFormItem', 'NIcon', 'NInput', 'NInputNumber', 'NModal', 'NSelect', 'NSpace', 'NSwitch', 'NTabPane', 'NTabs', 'NTag', 'NTooltip']
const icons = ['Bot', 'CheckCircle2', 'ChevronDown', 'ChevronUp', 'Edit3', 'FlaskConical', 'Plus', 'Pencil', 'RefreshCw', 'RotateCcw', 'Search', 'Settings', 'Trash2', 'Wifi', 'XCircle']
const server = await createServer({
  root: frontendRoot, logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'vue-router', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'ai-provider-config-smoke', enforce: 'pre',
    resolveId(id) {
      if (id.startsWith('/__ai-provider-smoke__/')) return `\0${id}`
      if (['naive-ui', 'vue-router', 'lucide-vue-next'].includes(id)) return `\0smoke:${id}`
    },
    async load(id) {
      if (id.startsWith('\0/__ai-provider-smoke__/')) {
        const relative = id.slice('\0/__ai-provider-smoke__/'.length).replace(/\.ts$/, '.vue')
        const filename = `${featureRoot}/${relative}`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: relative, genDefaultAs: '__smokeComponent' })
        const template = compileTemplate({ id: relative, filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        const code = `${script.content}\n${template.code}\n__smokeComponent.render = render; export default __smokeComponent`
          .replace(/from (['"])(\.\.?\/[^'"]+)\1/g, (_match, quote, request) => {
            const resolved = path.resolve(path.dirname(filename), request).replaceAll('\\', '/')
            const target = resolved.endsWith('.vue') ? `/__ai-provider-smoke__/${path.relative(featureRoot, resolved).replaceAll('\\', '/').replace(/\.vue$/, '.ts')}` : resolved
            return `from ${quote}${target}${quote}`
          })
        return (await transformWithEsbuild(code, filename, { loader: 'ts' })).code
      }
      if (id === '\0smoke:naive-ui') return `import { h } from 'vue';
        const stub = name => ({ inheritAttrs:false, setup(_props,{attrs,slots}) { return () => name === 'NDrawer' && !attrs.show ? null : h('div', {'data-control':name,...attrs}, slots.default?.()) } });
        ${passiveUI.map(name => `export const ${name} = stub('${name}');`).join('\n')}
        export const NDrawer = stub('NDrawer');
        export const useMessage = () => globalThis.__aiProviderConfigSmoke.current.message;
        export const useDialog = () => globalThis.__aiProviderConfigSmoke.current.dialog;`
      if (id === '\0smoke:vue-router') return 'export const useRouter = () => ({push:async()=>{}})'
      if (id === '\0smoke:lucide-vue-next') return icons.map(name => `export const ${name} = {render:()=>null}`).join('\n')
    },
  }],
})

function environment() {
  const env = { errors: [], successes: [], requests: [], held: [] }
  env.message = { error: value => env.errors.push(value), success: value => env.successes.push(value), warning() {} }
  env.dialog = { warning() {} }
  env.fetch = async (url, init = {}) => {
    const entry = { url, method: init.method ?? 'GET', body: init.body ? JSON.parse(init.body) : null }
    env.requests.push(entry)
    let payload
    if (url === '/api/ai-providers') payload = { providers: [], summary: { total: 0 }, usage: [] }
    else if (url === '/api/ai-providers/discover-models') {
      const gate = env.held.shift()
      if (gate) return gate.promise
      payload = { ok: true, status: 'ok', status_code: 200, duration_ms: 1, models: [{ name: 'new-model' }] }
    } else assert.fail(`Unexpected request: ${url}`)
    return { ok: true, status: 200, json: async () => payload }
  }
  env.hold = () => {
    let accept, fail
    const promise = new Promise((resolve, reject) => { accept = resolve; fail = reject })
    const gate = {
      promise,
      resolve: models => accept({ ok: true, status: 200, json: async () => ({ ok: true, status: 'ok', status_code: 200, duration_ms: 1, models }) }),
      reject: error => fail(error),
    }
    env.held.push(gate)
    return gate
  }
  return env
}
async function settle() { await nextTick(); await nextTurn(); await nextTick() }
async function mount(relative, input = {}, env = environment()) {
  bridge.current = env
  const { default: component } = await server.ssrLoadModule(`/__ai-provider-smoke__/${relative}.ts`)
  const props = reactive(input)
  const wrapped = { ...component, setup(componentProps, context) { env.state = component.setup(componentProps, context); return env.state } }
  const tree = { children: [] }
  const app = renderer.createApp({ setup: () => () => h(wrapped, {
    ...props,
    'onUpdate:modelValue': value => { props.modelValue = value },
    'onUpdate:legacySigning': value => { props.legacySigning = value },
  }) })
  app.mount(tree)
  await settle()
  return { env, props, tree, unmount: () => app.unmount() }
}
function findTag(node, tag) { return node.tag === tag ? node : node.children?.map(child => findTag(child, tag)).find(Boolean) }
function treeText(node) { return `${node.text ?? ''}${(node.children ?? []).map(treeText).join('')}` }
function controls(node, name) { return [...(node.props?.['data-control'] === name ? [node] : []), ...(node.children ?? []).flatMap(child => controls(child, name))] }

try {
  const advanced = await server.ssrLoadModule('/src/features/ai-providers/utils/providerAdvancedConfig.ts')
  const exclusions = await server.ssrLoadModule('/src/features/ai-providers/utils/excludedModelRules.ts')
  const original = advanced.providerAdvancedDraft({ disable_cooling: null, request_retry: 0, request_scoped_errors: [], cloak: { mode: 'always', cache_user_id: null, sensitive_words: [' keep '] }, fingerprint_profile: 'oauth-cli' })
  const current = advanced.cloneAdvancedDraft(original)
  assert.deepEqual(advanced.advancedConfigPayload(current, original, 'claude', 'edit'), {}, 'An untouched edit sends no new overrides or Cloak object')
  current.request_retry = null
  current.disable_cooling = false
  current.cloak.cache_user_id = false
  assert.deepEqual(advanced.advancedConfigPayload(current, original, 'claude', 'edit'), { disable_cooling: false, request_retry: null, cloak: { cache_user_id: false } })
  current.cloak = null
  assert.equal(advanced.advancedConfigPayload(current, original, 'claude', 'edit').cloak, null)
  const configuredCch = advanced.providerAdvancedDraft({ experimental_cch_signing: true })
  const restoredCch = advanced.cloneAdvancedDraft(configuredCch)
  restoredCch.experimental_cch_signing = null
  assert.deepEqual(advanced.advancedConfigPayload(restoredCch, configuredCch, 'claude', 'edit'), { experimental_cch_signing: null }, 'Restoring legacy CCH to its default is an explicit clear')
  restoredCch.experimental_cch_signing = false
  assert.deepEqual(advanced.advancedConfigPayload(restoredCch, configuredCch, 'claude', 'edit'), { experimental_cch_signing: false }, 'Explicit false remains distinct from inherit')
  assert.deepEqual(advanced.advancedConfigPayload(configuredCch, configuredCch, 'claude', 'edit'), {}, 'An untouched CCH setting is omitted')
  const fresh = advanced.providerAdvancedDraft()
  assert.deepEqual(advanced.advancedConfigPayload(fresh, fresh, 'claude', 'create'), {}, 'A new untouched form does not create Cloak')
  fresh.request_scoped_errors = []
  assert.deepEqual(advanced.advancedConfigPayload(fresh, advanced.providerAdvancedDraft(), 'codex', 'create'), { request_scoped_errors: [] }, 'Explicit empty rules are not inheritance')
  assert.deepEqual(advanced.advancedConfigPayload(fresh, advanced.providerAdvancedDraft(), 'vertex', 'create'), {}, 'Vertex omits unsupported rules')
  assert.ok(exclusions.matchesExcludedRule('Claude-SONNET-thinking', 'claude-*-thinking'))
  assert.ok(!exclusions.matchesExcludedRule('aXb', 'a.b'))
  assert.ok(exclusions.matchesExcludedRule('a.b', 'a.b'))
  assert.ok(!exclusions.matchesExcludedRule('aaab', 'a+b'))
  assert.deepEqual(exclusions.excludedModelCatalog([{ name: 'Model-A', alias: 'configured' }], [{ name: 'model-a' }, { name: 'Model-B' }]), [{ name: 'Model-A', alias: 'configured' }, { name: 'Model-B' }])

  const cloak = await mount('components/ClaudeCloakEditor', { modelValue: null, legacySigning: null })
  assert.equal(findTag(cloak.tree, 'details').props.open, undefined, 'Cloak starts collapsed')
  assert.equal(cloak.props.modelValue, null, 'Rendering or expanding does not create configuration')
  cloak.env.state.setCustom(true)
  await settle()
  assert.deepEqual(cloak.props.modelValue, {})
  cloak.env.state.update({ mode: 'uncloak' })
  await settle()
  assert.equal(cloak.props.modelValue.mode, 'uncloak')
  cloak.env.state.setLegacy('inherit')
  await settle()
  assert.equal(cloak.props.legacySigning, null, 'The actual Cloak editor emits inherit as an explicit null')
  cloak.unmount()

  const policy = await mount('components/ProviderRuntimePolicyEditor', { modelValue: advanced.providerAdvancedDraft(), supportsErrors: true })
  policy.env.state.setRuleMode('override')
  await settle()
  policy.env.state.updateRule(0, { action: 'stop' })
  policy.env.state.update({ request_scoped_errors: [{ action: 'continue', match: ['  significant whitespace  '], match_regex: ['(?i)busy'] }] })
  await settle()
  policy.env.state.setRuleMode('inherit')
  await settle()
  assert.equal(policy.props.modelValue.request_scoped_errors, null)
  policy.env.state.setRuleMode('override')
  await settle()
  assert.equal(policy.props.modelValue.request_scoped_errors[0].match[0], '  significant whitespace  ', 'Switching modes retains the unsaved override draft')
  const statusRule = controls(policy.tree, 'NFormItem').find(node => String(node.props.label).includes('HTTP 状态码'))
  assert.ok(statusRule)
  assert.match(statusRule.props.label, /必填/)
  assert.match(treeText(statusRule), /正整数/)
  policy.unmount()

  const picker = await mount('components/ExcludedModelsPicker', { modelValue: ['legacy-absent', 'claude-*', 'claude-a'], configured: [{ name: 'claude-a' }], discovered: [], loading: false, loadError: null, loaded: false })
  picker.env.state.toggle('claude-a', false)
  await settle()
  assert.deepEqual(picker.props.modelValue, ['legacy-absent', 'claude-*'])
  assert.deepEqual(picker.env.state.wildcardRules('claude-a'), ['claude-*'], 'Unchecking exact model does not erase wildcard exclusion')
  picker.env.state.newRule.value = '*'
  picker.env.state.addRule()
  assert.ok(picker.env.state.ruleError.value)
  assert.deepEqual(picker.props.modelValue, ['legacy-absent', 'claude-*'])
  picker.env.state.editRule('claude-*')
  picker.env.state.newRule.value = 'claude-*-thinking'
  picker.env.state.addRule()
  await settle()
  assert.deepEqual(picker.props.modelValue, ['legacy-absent', 'claude-*-thinking'], 'Wildcard rules can be edited without losing unknown exact exclusions')
  picker.unmount()

  const view = await mount('views/AIProvidersView')
  const state = view.env.state
  const inheritedModel = state.modelToDraft({ name: 'gpt-5' })
  const inheritedModelProvider = state.defaultDraft('codex')
  inheritedModelProvider.models = [inheritedModel]
  inheritedModelProvider.advanced.request_retry = 2
  const inheritedModelPayload = state.draftToPayload(inheritedModelProvider, 'edit')
  assert.deepEqual(inheritedModelPayload.models, [{ name: 'gpt-5' }], 'An unchanged inherited model remains omitted when another policy field changes')
  assert.equal(inheritedModelPayload.request_retry, 2)
  state.updateModelForceMapping(inheritedModel, true)
  state.updateModelForceMapping(inheritedModel, false)
  assert.deepEqual(state.draftToPayload(inheritedModelProvider, 'edit').models, [{ name: 'gpt-5', force_mapping: false }], 'Explicit false is retained after toggling back from true')
  const nullModel = state.modelToDraft({ name: 'gpt-5', force_mapping: null })
  inheritedModelProvider.models = [nullModel]
  assert.deepEqual(state.draftToPayload(inheritedModelProvider, 'edit').models, [{ name: 'gpt-5', force_mapping: null }])
  const sourceDraft = state.defaultDraft('codex')
  sourceDraft.api_key = 'fixture-only'
  sourceDraft.base_url = 'https://fixture.invalid/v1'
  sourceDraft.source_hash = 'safe-source-snapshot'
  const sourcePayload = state.draftToPayload(sourceDraft, 'create')
  assert.equal(sourcePayload.source_hash, 'safe-source-snapshot', 'Source snapshot travels through action/save payload')
  assert.equal(state.providerOrderItem({ ...sourcePayload, index: 0 }).source_hash, 'safe-source-snapshot', 'Reordering preserves source identity')
  state.openCreateDialog()
  state.handleBrandChange('codex')
  state.form.value.api_key = 'fixture-only'
  state.form.value.base_url = 'https://fixture.invalid/v1'
  await settle()
  const staleSuccess = view.env.hold()
  const first = state.runDiscovery()
  await settle()
  state.drawerOpen.value = false
  state.openCreateDialog()
  state.form.value.api_key = 'fixture-only'
  const currentDiscovery = view.env.hold()
  const second = state.runDiscovery()
  await settle()
  staleSuccess.resolve([{ name: 'stale-model' }])
  await first
  assert.equal(state.isDiscovering.value, true, 'Old finally does not clear the new loading state')
  assert.deepEqual(state.discoveredModels.value, [], 'Old success does not enter reopened form')
  currentDiscovery.resolve([{ name: 'current-model' }])
  await second
  assert.equal(state.discoveredModels.value[0].name, 'current-model')
  const staleFailure = view.env.hold()
  const third = state.runDiscovery()
  await settle()
  state.form.value.base_url = 'https://changed.invalid/v1'
  const errorCount = view.env.errors.length
  staleFailure.reject(new Error('stale failure'))
  await third
  assert.equal(view.env.errors.length, errorCount, 'Old errors are ignored after source-context replacement')
  assert.deepEqual(state.discoveredModels.value, [])
  view.unmount()
  console.log('AI provider configuration smoke passed: presence, actual components, exclusions and stale discovery callbacks.')
} finally {
  await server.close()
  delete globalThis.__aiProviderConfigSmoke
}
