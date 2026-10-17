import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createRenderer, nextTick } from 'vue'
import { createServer, transformWithEsbuild } from 'vite'

const root = fileURLToPath(new URL('..', import.meta.url))
const responsePath = '/api/cpa-helper-plugin/response-model'
const directoryPath = '/api/cpa-helper-plugin/directory'
const env = {
  errors: [], successes: [], warnings: [], dialogs: [], reads: [], writes: [], pendingReads: [], pendingWrites: [],
  deferredReads: new Set(), deferredWrites: new Set(), failedWrites: new Set(),
  settings: { cliaproxy_url: 'http://cpa.invalid', model_request_url: 'http://cpa.invalid', management_key: '', management_key_set: true, collector_enabled: true, queue_name: 'usage', batch_size: 100, poll_interval_seconds: 2, retry_interval_seconds: 10 },
  responseConfig: { enabled: false, stream_enabled: true, responses_only_stream: true, max_buffer_bytes: 2097152, max_total_buffer_bytes: 8388608, action: 'reject', unknown_action: 'pass', case_sensitive: false, ignore_thinking_suffix: true, ignored_models: ['ignored-model'], accepted_models: { 'request-model': ['response-model'] } },
  policy: { contract_version: 'v1', plugin_version: '0.1.5', policy_revision: 1, generated_at: '', groups: [{ id: 'existing', name: 'Existing', note: '', rule: {} }], keys: [], future_policy_field: { preserve: true } },
}
const originalGlobals = new Map(['fetch', 'localStorage', 'document', 'crypto'].map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
globalThis.__pluginFormsSmoke = env
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN' } }
// LAN HTTP exposes getRandomValues but not the secure-context-only randomUUID.
Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { getRandomValues: webcrypto.getRandomValues.bind(webcrypto) } })
const jsonResponse = (value) => Response.json(structuredClone(value))
const apiError = () => Response.json({ detail: { code: 'plugin_unavailable', message: '测试插件暂时不可用' } }, { status: 503 })
globalThis.fetch = async (url, init) => {
  const path = new URL(url, 'http://plugin-forms-smoke.invalid').pathname
  const method = init.method ?? 'GET'
  let snapshot
  if (method === 'PUT') {
    const payload = JSON.parse(init.body)
    env.writes.push({ path, payload })
    if (env.failedWrites.has(path)) return apiError()
    if (path === '/api/settings') {
      Object.assign(env.settings, payload)
      snapshot = env.settings
    } else if (path === responsePath) {
      env.responseConfig = payload
      snapshot = env.responseConfig
    } else if (path === '/api/cpa-helper-plugin/policy') {
      assert.equal(payload.policy_revision, env.policy.policy_revision, 'Writes carry the latest revision')
      env.policy = { ...payload, policy_revision: payload.policy_revision + 1 }
      snapshot = env.policy
    } else assert.fail(`Unexpected write ${path}`)
  } else {
    env.reads.push(path)
    if (path === '/api/settings') snapshot = env.settings
    else if (path === '/api/collector/status') snapshot = { enabled: env.settings.collector_enabled, running: false, records_collected: 0 }
    else if (path === responsePath) snapshot = env.responseConfig
    else if (path === '/api/cpa-helper-plugin/policy') snapshot = env.policy
    else if (path === directoryPath) snapshot = { models: [], credentials: [], credential_providers: [] }
    else if (path === '/api/cpa-helper-plugin/status') snapshot = { loaded: true, capabilities: {}, health: {} }
    else assert.fail(`Unexpected read ${path}`)
  }
  snapshot = structuredClone(snapshot)
  const pending = method === 'PUT' ? env.pendingWrites : env.pendingReads
  const deferred = method === 'PUT' ? env.deferredWrites : env.deferredReads
  if (deferred.has(path)) return new Promise((resolve, reject) => pending.push({ path, snapshot, resolve, reject }))
  return jsonResponse(snapshot)
}
const passiveUI = ['NAlert', 'NButton', 'NDataTable', 'NDescriptions', 'NDescriptionsItem', 'NForm', 'NFormItem', 'NInput', 'NInputNumber', 'NSelect', 'NSpace', 'NSwitch', 'NTag']
const icons = ['Activity', 'Database', 'Power', 'Server']
const views = { settings: 'src/features/settings/views/SettingsView.vue', rules: 'src/features/plugin/views/PluginRouteRulesView.vue', fields: 'src/features/plugin/components/PluginRuleFields.vue' }
const server = await createServer({
  root, logLevel: 'error', ssr: { noExternal: ['naive-ui', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'plugin-forms-smoke', enforce: 'pre',
    resolveId(id) {
      for (const name of Object.keys(views)) if (id === `/__plugin-forms-${name}.ts`) return `\0plugin-forms:${name}`
      if (id.replaceAll('\\', '/').endsWith('/features/plugin/components/PluginRuleFields.vue')) return '\0plugin-forms:fields'
      if (id === 'naive-ui' || id === 'lucide-vue-next') return `\0plugin-forms:${id}`
    },
    async load(id) {
      const name = id.replace('\0plugin-forms:', '')
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
          props: { show: Boolean, closeOnEsc: { type: Boolean, default: true } }, emits: ['esc', 'update:show'],
          setup(props, { emit, slots }) {
            globalThis.__pluginFormsSmoke.pressEscape = () => { emit('esc'); if (props.show && props.closeOnEsc) emit('update:show', false); };
            return () => props.show ? h('stub-NModal', null, slots.default?.()) : null;
          }
        };
        export const useMessage = () => ({ error: value => globalThis.__pluginFormsSmoke.errors.push(value), success: value => globalThis.__pluginFormsSmoke.successes.push(value), warning: value => globalThis.__pluginFormsSmoke.warnings.push(value) });
        export const useDialog = () => ({ warning(options) { const confirmation = { ...options, loading: false }; globalThis.__pluginFormsSmoke.dialogs.push(confirmation); return confirmation; } });`
      if (name === 'lucide-vue-next') return icons.map(component => `export const ${component} = { render: () => null };`).join('\n')
    },
  }],
})
const renderer = createRenderer({
  createElement: (type) => ({ type, props: {}, children: [] }),
  createText: (text) => ({ type: 'text', text }), createComment: () => ({ type: 'comment' }),
  insert(node, parent, anchor = null) {
    if (node.parent) { const oldIndex = node.parent.children.indexOf(node); if (oldIndex >= 0) node.parent.children.splice(oldIndex, 1) }
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index >= 0 ? index : parent.children.length, 0, node)
    node.parent = parent
  },
  remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text; node.children = [] },
  patchProp(node, key, _, value) { node.props[key] = value },
  parentNode: (node) => node.parent, nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
})
const settle = async () => { await nextTick(); await nextTurn(); await nextTick() }
const descendants = (node) => [node, ...(node.children ?? []).flatMap(descendants)]
const textContent = (node) => (node.text ?? '') + (node.children ?? []).map(textContent).join('')
const ui = (host, type) => descendants(host).filter((node) => node.type === `stub-${type}`)
const button = (host, text) => ui(host, 'NButton').find((node) => textContent(node).trim() === text)
const updateValue = (node, value) => {
  const listeners = node.props['onUpdate:value']
  for (const listener of Array.isArray(listeners) ? listeners : [listeners]) listener(value)
}
const writeCount = (path) => env.writes.filter((write) => write.path === path).length
const readCount = (path) => env.reads.filter((read) => read === path).length
const lastRead = (path) => env.pendingReads.findLast((read) => read.path === path)
const lastWrite = (path) => env.pendingWrites.findLast((write) => write.path === path)
let app
const mount = async (name) => {
  const { default: component } = await server.ssrLoadModule(`/__plugin-forms-${name}.ts`)
  let state
  const host = { children: [] }
  app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } })
  app.mount(host)
  await settle()
  return { state, host }
}
try {
  const { setCurrentUser } = await server.ssrLoadModule('/src/features/auth/state/currentUser.ts')
  setCurrentUser({ is_admin: true, is_super_admin: true })
  const settings = await mount('settings')
  const state = settings.state
  assert.equal(state.responseModelLoaded.value, true)
  assert.equal(state.responseModelDirty.value, false)
  assert.equal(state.responseConfig.responses_only_stream, true, 'The legacy 0.1.4 response field loads into the shared config type')
  const responseReads = readCount(responsePath)
  state.ignoredModelsText.value = 'keep-my-draft'
  state.settingsForm.batch_size = 200
  await state.saveSettings()
  assert.equal(env.settings.batch_size, 200)
  assert.equal(readCount(responsePath), responseReads, 'Collection save never reloads response verification')
  assert.equal(writeCount(responsePath), 0)
  assert.equal(state.ignoredModelsText.value, 'keep-my-draft', 'Independent collection save preserves the response draft')
  assert.equal(state.responseModelDirty.value, true)
  state.refresh()
  await env.dialogs.at(-1).onPositiveClick()
  await settle()
  assert.equal(state.responseModelDirty.value, false)

  const responseForm = ui(settings.host, 'NForm').find((node) => node.props.class === 'response-verification-form')
  const enableSwitch = ui(responseForm, 'NSwitch')[0]
  updateValue(enableSwitch, true)
  await settle()
  assert.equal(state.responseModelDirty.value, true, 'A switch update is dirty without a native input event')
  const beforeDirtyRefresh = readCount(responsePath)
  const beforeDirtyDialog = env.dialogs.length
  state.refresh()
  assert.equal(env.dialogs.length, beforeDirtyDialog + 1)
  assert.equal(readCount(responsePath), beforeDirtyRefresh, 'Dirty refresh waits for discard confirmation')
  updateValue(enableSwitch, false)
  assert.equal(state.responseModelDirty.value, false, 'Reverting the switch restores the saved snapshot')
  const originalRows = JSON.parse(JSON.stringify(state.acceptedModelRows.value))
  button(responseForm, '删除').props.onClick()
  assert.equal(state.responseModelDirty.value, true, 'Deleting a mapping is dirty without native change events')
  state.acceptedModelRows.value = originalRows
  assert.equal(state.responseModelDirty.value, false)
  button(responseForm, '添加映射').props.onClick()
  assert.equal(state.responseModelDirty.value, true, 'Adding an empty mapping still protects the draft')
  state.acceptedModelRows.value.pop()
  for (const [field, value] of Object.entries({ stream_enabled: false, action: 'audit', unknown_action: 'reject', max_buffer_bytes: 4194304, max_total_buffer_bytes: 16777216, case_sensitive: true, ignore_thinking_suffix: false })) {
    const previous = state.responseConfig[field]
    state.responseConfig[field] = value
    assert.equal(state.responseModelDirty.value, true, `${field} participates in the complete draft snapshot`)
    state.responseConfig[field] = previous
    assert.equal(state.responseModelDirty.value, false)
  }
  state.acceptedModelRows.value[0].responses = 'edited-response'
  assert.equal(state.responseModelDirty.value, true)
  state.ignoredModelsText.value = 'failed-save-draft'
  env.failedWrites.add(responsePath)
  const beforeFailureSuccess = env.successes.length
  await state.saveResponseModelConfig()
  assert.equal(env.writes.filter((write) => write.path === responsePath).at(-1).payload.responses_only_stream, true, 'The legacy 0.1.4 field is preserved in a config round trip')
  assert.equal(state.ignoredModelsText.value, 'failed-save-draft')
  assert.equal(state.responseModelDirty.value, true)
  assert.equal(env.successes.length, beforeFailureSuccess, 'Failed verification saves preserve edits and never report success')
  env.failedWrites.delete(responsePath)
  await state.saveResponseModelConfig()
  assert.equal(state.responseModelDirty.value, false)

  const acceptedMappings = JSON.parse('{"request-model":["response-model"],"__proto__":["gpt-upstream"],"constructor":["constructor-upstream"],"toString":["string-upstream"]}')
  const objectPrototypeBeforeMappings = Object.getOwnPropertyDescriptors(Object.prototype)
  env.responseConfig = { ...env.responseConfig, accepted_models: acceptedMappings }
  await state.loadResponseModelConfig()
  assert.deepEqual(state.acceptedModelRows.value.map((row) => row.request), Object.keys(acceptedMappings), 'The actual config loader keeps every own model-name key')
  const configBeforeSwitch = structuredClone(env.responseConfig)
  state.responseConfig.enabled = !configBeforeSwitch.enabled
  await state.saveResponseModelConfig()
  const switchPayload = env.writes.filter((write) => write.path === responsePath).at(-1).payload
  assert.deepEqual(switchPayload, { ...configBeforeSwitch, enabled: !configBeforeSwitch.enabled }, 'A switch-only save preserves all mappings in the actual JSON wire payload')
  for (const request of ['__proto__', 'constructor', 'toString']) {
    assert.equal(Object.hasOwn(switchPayload.accepted_models, request), true, `${request} remains an own JSON data key`)
    assert.deepEqual(switchPayload.accepted_models[request], acceptedMappings[request])
  }
  assert.deepEqual(Object.getOwnPropertyDescriptors(Object.prototype), objectPrototypeBeforeMappings, 'Model-name keys never mutate Object.prototype')
  assert.equal(Object.getPrototypeOf(switchPayload.accepted_models), Object.prototype, 'The serialized mapping remains an ordinary JSON object')
  assert.equal(state.responseModelDirty.value, false)
  const rowsBeforeDuplicates = JSON.parse(JSON.stringify(state.acceptedModelRows.value))
  for (const request of ['request-model', '__proto__', 'constructor', 'toString']) {
    const writesBeforeDuplicate = writeCount(responsePath)
    state.acceptedModelRows.value = [...rowsBeforeDuplicates, { request: ` ${request} `, responses: 'duplicate-target' }]
    await state.saveResponseModelConfig()
    assert.equal(writeCount(responsePath), writesBeforeDuplicate, `Duplicate ${request} mappings cannot be submitted`)
    assert.match(env.errors.at(-1), /请求模型映射不能重复/)
  }
  state.acceptedModelRows.value = rowsBeforeDuplicates
  assert.equal(state.responseModelDirty.value, false)

  env.deferredReads.add(responsePath)
  const oldSettingsLoad = state.loadSettings()
  await settle()
  const oldResponseRead = lastRead(responsePath)
  state.responseConfig.action = 'audit'
  await state.saveResponseModelConfig()
  oldResponseRead.resolve(jsonResponse(oldResponseRead.snapshot))
  await oldSettingsLoad
  assert.equal(state.responseConfig.action, 'audit', 'A delayed pre-save GET cannot overwrite the saved config')
  assert.equal(state.responseModelDirty.value, false)
  const oldFailure = state.loadResponseModelConfig()
  await settle()
  const rejectedRead = lastRead(responsePath)
  const replacement = state.loadResponseModelConfig()
  await settle()
  const replacementRead = lastRead(responsePath)
  rejectedRead.reject(new Error('stale response config read'))
  await oldFailure
  assert.equal(state.responseModelConfigError.value, null, 'Stale config rejection cannot publish an error')
  assert.equal(state.responseModelConfigLoading.value, true, 'Stale finally cannot finish a replacement load')
  replacementRead.resolve(jsonResponse(replacementRead.snapshot))
  await replacement
  env.deferredReads.delete(responsePath)
  env.deferredWrites.add(responsePath)
  state.responseConfig.unknown_action = 'reject'
  const savingResponse = state.saveResponseModelConfig()
  await settle()
  const beforeDuplicateResponse = writeCount(responsePath)
  await state.saveResponseModelConfig()
  state.refresh()
  assert.equal(writeCount(responsePath), beforeDuplicateResponse, 'Verification save has a duplicate submission guard')
  lastWrite(responsePath).resolve(jsonResponse(lastWrite(responsePath).snapshot))
  await savingResponse
  env.deferredWrites.delete(responsePath)
  assert.equal(state.responseModelDirty.value, false)
  env.deferredReads.add(responsePath)
  const unmountedResponse = state.loadResponseModelConfig()
  await settle()
  const unmountedRead = lastRead(responsePath)
  app.unmount()
  app = null
  unmountedRead.reject(new Error('unmounted response config read'))
  await unmountedResponse
  assert.equal(state.responseModelConfigError.value, null)
  env.deferredReads.delete(responsePath)

  const rules = await mount('rules')
  const route = rules.state
  assert.equal(route.policy.value.policy_revision, 1)
  route.openEditor()
  await settle()
  const firstId = route.groupDraft.id
  assert.match(firstId, /^[a-f0-9]{32}$/, 'New rules work with randomUUID absent in LAN HTTP')
  env.pressEscape()
  await settle()
  assert.equal(route.modalVisible.value, false, 'Escape closes a clean editor through its close handler')
  route.openEditor()
  await settle()
  assert.notEqual(route.groupDraft.id, firstId)
  const modal = ui(rules.host, 'NModal')[0]
  updateValue(ui(modal, 'NInput')[0], 'New rule')
  updateValue(ui(modal, 'NInput')[1], 'keep-my-note')
  await settle()
  const beforeEscapeDialog = env.dialogs.length
  env.pressEscape()
  assert.equal(route.modalVisible.value, true, 'Escape cannot discard a dirty editor through v-model')
  assert.equal(route.groupDraft.note, 'keep-my-note')
  assert.equal(env.dialogs.length, beforeEscapeDialog + 1, 'Escape invokes the same discard confirmation as Cancel')
  const discard = env.dialogs.at(-1)
  env.deferredWrites.add('/api/cpa-helper-plugin/policy')
  const saveRule = route.saveGroup()
  await settle()
  const beforeDuplicateRule = writeCount('/api/cpa-helper-plugin/policy')
  env.pressEscape()
  assert.equal(route.modalVisible.value, true, 'Escape cannot close an editor during save')
  assert.equal(discard.onPositiveClick(), false, 'An older discard confirmation cannot close an editor during save')
  await route.saveGroup()
  await route.refresh()
  await route.removeGroup(route.groups.value[0])
  assert.equal(writeCount('/api/cpa-helper-plugin/policy'), beforeDuplicateRule)
  assert.equal(env.dialogs.length, beforeEscapeDialog + 1)
  const savedWrite = lastWrite('/api/cpa-helper-plugin/policy')
  savedWrite.resolve(jsonResponse(savedWrite.snapshot))
  await saveRule
  env.deferredWrites.delete('/api/cpa-helper-plugin/policy')
  assert.equal(route.modalVisible.value, false)
  assert.deepEqual(route.policy.value.future_policy_field, { preserve: true })
  route.openEditor(route.groups.value[1])
  route.groupDraft.note = 'failed-rule-draft'
  route.dirty.value = true
  env.failedWrites.add('/api/cpa-helper-plugin/policy')
  const beforeRuleFailureSuccess = env.successes.length
  await route.saveGroup()
  assert.equal(route.modalVisible.value, true)
  assert.equal(route.groupDraft.note, 'failed-rule-draft')
  assert.equal(route.dirty.value, true)
  assert.equal(env.successes.length, beforeRuleFailureSuccess)
  env.failedWrites.delete('/api/cpa-helper-plugin/policy')
  route.closeEditor()
  env.dialogs.at(-1).onPositiveClick()
  await settle()

  env.deferredReads.add(directoryPath)
  const oldRefresh = route.refresh()
  await settle()
  const oldDirectory = lastRead(directoryPath)
  const beforeLoadingDialogs = env.dialogs.length
  route.openEditor()
  await route.removeGroup(route.groups.value[0])
  assert.equal(route.modalVisible.value, false, 'A loading policy cannot start a new editor')
  assert.equal(env.dialogs.length, beforeLoadingDialogs, 'A loading policy cannot start deletion')
  assert.equal(button(rules.host, '新增规则').props.disabled, true)
  env.deferredReads.delete(directoryPath)
  await route.refresh()
  route.openEditor()
  route.groupDraft.name = 'After replacement refresh'
  await route.saveGroup()
  const savedRevision = route.policy.value.policy_revision
  const savedId = route.policy.value.groups.at(-1).id
  oldDirectory.resolve(jsonResponse(oldDirectory.snapshot))
  await oldRefresh
  assert.equal(route.policy.value.policy_revision, savedRevision, 'A delayed old refresh cannot publish a pre-save revision')
  assert.ok(route.policy.value.groups.some((group) => group.id === savedId), 'Saved rules remain visible after an old refresh finishes')
  env.deferredReads.add(directoryPath)
  const staleRefresh = route.refresh()
  await settle()
  const staleDirectory = lastRead(directoryPath)
  const freshRefresh = route.refresh()
  await settle()
  const freshDirectory = lastRead(directoryPath)
  const beforeStaleError = env.errors.length
  staleDirectory.reject(new Error('stale directory read'))
  await staleRefresh
  assert.equal(env.errors.length, beforeStaleError)
  assert.equal(route.loading.value, true, 'An old finally cannot finish a newer route refresh')
  assert.equal(route.loadError.value, null)
  freshDirectory.resolve(jsonResponse(freshDirectory.snapshot))
  await freshRefresh
  env.deferredReads.delete(directoryPath)

  env.policy = {
    ...route.policy.value,
    groups: [
      { id: 'existing', name: 'Existing', note: '', rule: {} },
      {
        id: 'extension-a', name: 'Extension A', note: 'A note',
        plugin_extension: { owner: 'a' },
        // Helper's own-field preservation contract; the current strict v0.1.7 plugin rejects unknown group fields.
        ...JSON.parse('{"__proto__":{"helper_extension_owner":"a"},"constructor":{"keep":true},"toString":"extension text"}'),
        rule: { models: ['model-a'], future_rule_field: { keep: true } },
      },
      { id: 'plain-b', name: 'Plain B', note: 'B note', rule: { models: ['model-b'] } },
    ],
    keys: [{
      id: 'key-a', label: 'Key A', enabled: true, group_ids: ['unrelated-group'], max_concurrency: 0,
      rule: { denied_models: ['denied-a'] }, key_extension: { preserve: true },
    }],
    future_policy_field: { preserve: true },
  }
  await route.refresh()
  route.openEditor(route.groups.value[1])
  assert.equal(Object.hasOwn(route.groups.value[1], '__proto__'), true, 'The Helper fixture contains an own extension data key')
  assert.deepEqual(route.groupDraft.plugin_extension, { owner: 'a' }, 'Editing A loads its unknown group fields')
  assert.deepEqual(route.groupDraft.rule.future_rule_field, { keep: true }, 'Editing A loads its unknown rule fields')
  assert.deepEqual(route.groupKeyIds.value, [], 'Editing A does not claim an unrelated key association')
  assert.equal(route.dirty.value, false)
  await settle()
  updateValue(ui(rules.host, 'NInput')[0], 'Extension A updated')
  await settle()
  assert.equal(route.groupDraft.name, 'Extension A updated', 'The actual name v-model edits the draft')
  assert.equal(route.dirty.value, true, 'The actual name update marks the draft dirty')
  await route.saveGroup()
  const savedA = env.writes.at(-1).payload
  const serializedAAfterEdit = savedA.groups.find((group) => group.id === 'extension-a')
  assert.equal(Object.hasOwn(serializedAAfterEdit, '__proto__'), true, 'Helper serializes every own extension data key')
  assert.deepEqual(serializedAAfterEdit.__proto__, { helper_extension_owner: 'a' })
  assert.deepEqual(serializedAAfterEdit.constructor, { keep: true })
  assert.equal(serializedAAfterEdit.toString, 'extension text')
  assert.equal(Object.prototype.helper_extension_owner, undefined, 'Editing extension data does not pollute Object.prototype')
  assert.deepEqual(serializedAAfterEdit.plugin_extension, { owner: 'a' }, 'Editing A preserves its unknown group fields')
  assert.deepEqual(serializedAAfterEdit.rule.future_rule_field, { keep: true }, 'Editing A preserves its unknown rule fields')
  assert.deepEqual(savedA.keys[0].key_extension, { preserve: true }, 'Editing A preserves other key fields')
  assert.deepEqual(savedA.keys[0].group_ids, ['unrelated-group'], 'Editing A preserves unrelated key associations')
  route.openEditor()
  assert.equal(Object.hasOwn(route.groupDraft, '__proto__'), false, 'Creating after A clears its own prototype-named field')
  assert.equal(route.groupDraft.helper_extension_owner, undefined, 'Creating after A does not inherit extension fields')
  assert.equal(Object.hasOwn(route.groupDraft, 'constructor'), false)
  assert.equal(Object.hasOwn(route.groupDraft, 'toString'), false)
  assert.equal(route.groupDraft.plugin_extension, undefined, 'Creating after A clears its unknown group fields')
  assert.equal(route.groupDraft.rule.future_rule_field, undefined, 'Creating after A clears its unknown rule fields')
  assert.deepEqual(route.groupDraft.rule, {}, 'Creating after A starts with a clean rule')
  route.closeEditor()
  route.openEditor(route.groups.value[2])
  assert.equal(Object.hasOwn(route.groupDraft, '__proto__'), false, 'Editing B clears A own prototype-named field')
  assert.equal(route.groupDraft.helper_extension_owner, undefined, 'Editing B does not inherit extension fields')
  assert.equal(route.groupDraft.plugin_extension, undefined, 'Editing B clears unknown fields from the previous row')
  assert.equal(route.groupDraft.rule.future_rule_field, undefined, 'Editing B clears unknown nested fields from the previous row')
  assert.deepEqual(route.groupDraft.rule.models, ['model-b'], 'Editing B keeps its own rule fields')
  assert.deepEqual(route.groupKeyIds.value, [], 'Editing B does not inherit A key associations')
  route.groupDraft.name = 'Plain B updated'
  route.dirty.value = true
  await route.saveGroup()
  const serializedPolicy = env.writes.at(-1).payload
  const serializedA = serializedPolicy.groups.find((group) => group.id === 'extension-a')
  const serializedB = serializedPolicy.groups.find((group) => group.id === 'plain-b')
  assert.deepEqual(serializedA.plugin_extension, { owner: 'a' }, 'Saving B preserves A unknown group fields')
  assert.equal(Object.hasOwn(serializedA, '__proto__'), true, 'Saving B preserves A own extension data key')
  assert.deepEqual(serializedA.__proto__, { helper_extension_owner: 'a' })
  assert.deepEqual(serializedA.rule.future_rule_field, { keep: true }, 'Saving B preserves A unknown rule fields')
  assert.deepEqual(serializedPolicy.keys[0].key_extension, { preserve: true }, 'Saving B preserves unrelated key fields')
  assert.deepEqual(serializedPolicy.keys[0].group_ids, ['unrelated-group'], 'Saving B preserves unrelated key associations')
  assert.equal(serializedB.plugin_extension, undefined, 'Saving B does not serialize A unknown group fields')
  assert.equal(Object.hasOwn(serializedB, '__proto__'), false, 'Saving B does not serialize A prototype-named field')
  assert.equal(Object.prototype.helper_extension_owner, undefined)
  assert.equal(serializedB.rule.future_rule_field, undefined, 'Saving B does not serialize A unknown rule fields')

  await route.removeGroup(route.groups.value[0])
  const deletion = env.dialogs.at(-1)
  env.deferredWrites.add('/api/cpa-helper-plugin/policy')
  const deleting = deletion.onPositiveClick()
  await settle()
  assert.equal(deletion.loading, true, 'Deletion confirmation shows loading while the real PUT is pending')
  const beforeDuplicateDelete = writeCount('/api/cpa-helper-plugin/policy')
  assert.equal(await deletion.onPositiveClick(), false)
  const beforeBusyDialogs = env.dialogs.length
  await route.removeGroup(route.groups.value[1])
  route.openEditor()
  await route.refresh()
  assert.equal(writeCount('/api/cpa-helper-plugin/policy'), beforeDuplicateDelete, 'Repeated confirmation sends only one PUT')
  assert.equal(env.dialogs.length, beforeBusyDialogs)
  assert.equal(route.modalVisible.value, false)
  const actions = route.columns.value.find((column) => column.key === 'actions').render(route.groups.value[0]).children.default()
  assert.ok(actions.every((action) => action.props.disabled), 'Every table mutation is disabled while deleting')
  const deletedWrite = lastWrite('/api/cpa-helper-plugin/policy')
  deletedWrite.resolve(jsonResponse(deletedWrite.snapshot))
  await deleting
  env.deferredWrites.delete('/api/cpa-helper-plugin/policy')
  assert.equal(deletion.loading, false)
  assert.equal(route.saving.value, false)
  assert.equal(await deletion.onPositiveClick(), false, 'A completed confirmation cannot re-delete a missing rule')
  assert.ok(!route.groups.value.some((group) => group.id === 'existing'))
  env.deferredReads.add(directoryPath)
  const removedRefresh = route.refresh()
  await settle()
  const removedDirectory = lastRead(directoryPath)
  app.unmount()
  app = null
  const beforeUnmountError = env.errors.length
  removedDirectory.reject(new Error('unmounted directory read'))
  await removedRefresh
  assert.equal(env.errors.length, beforeUnmountError)
  console.log('Plugin forms smoke passed: HTTP rule IDs, independent saves, lossless model-name mappings, complete dirty snapshots, stale success/error/finally, Escape/discard/save guards, duplicate delete and unmount')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.__pluginFormsSmoke
  for (const [key, descriptor] of originalGlobals) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else delete globalThis[key]
  }
}
