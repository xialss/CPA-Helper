import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, nextTick } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const env = { errors: [], successes: [], models: [], policyWrites: [], descriptionWrites: [], writes: [], policyReads: [], statusReads: [], keyReads: [], revision: 1, failPolicy: false, failDescription: false, failPolicyLoad: false, failDirectory: false, deferPolicyReads: false, deferStatusReads: false, deferKeyReads: false, groups: [], directory: { models: [], credentials: [], credential_providers: [] }, summary: { group_names: [], has_custom_rule: false } }
globalThis.__apiKeyPolicySmoke = env
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN' } }
const row = { api_key_hash: 'member-hash', description: 'Member', api_key: 'sk-member-secret-key', models: [], last_model: null, expires_at: null, expiration_enforced_at: null, expiration_sync_error: null }
const keyPolicy = { id: 'member-scope', label: 'Member', enabled: true, group_ids: [], max_concurrency: 0, rule: {} }
env.key = structuredClone(keyPolicy)
env.status = { loaded: true, capabilities: {}, health: {}, response_model_verification_supported: false }
const statusResponse = (health = env.status.health) => ({ ...env.status, health })
const policyResponse = () => ({ policy_revision: env.revision, groups: env.groups, key: env.key })
const apiError = (message, status = 503) => Response.json({ detail: { code: 'plugin_unavailable', message } }, { status })
globalThis.fetch = async (url, init) => {
  const path = new URL(url, 'http://api-key-smoke.invalid').pathname
  if (path === '/api/account/models') {
    // Deliberately permit late transport completion after abort. The view's
    // generation must guard late success, rejection and loading cleanup.
    return new Promise((resolve, reject) => env.models.push({ resolve, reject, signal: init.signal }))
  }
  if (path === '/api/api-keys') {
    if (env.deferKeyReads) return new Promise((resolve, reject) => env.keyReads.push({ resolve, reject }))
    return Response.json([row])
  }
  if (path === '/api/api-keys/member-hash' && init.method === 'PUT') {
    const payload = JSON.parse(init.body)
    env.descriptionWrites.push(payload)
    env.writes.push('description')
    if (env.failDescription) return apiError('描述保存失败')
    row.description = payload.description
    if ('expires_at' in payload) row.expires_at = payload.expires_at
    return Response.json(row)
  }
  if (path === '/api/usage/overview') return Response.json({ summary: {} })
  if (path === '/api/account/quota') return Response.json(null)
  if (path === '/api/account/model-request') return Response.json({ openai_base_url: 'http://example.invalid/v1' })
  if (path === '/api/cpa-helper-plugin/status') {
    if (env.deferStatusReads) return new Promise((resolve, reject) => env.statusReads.push({ resolve, reject }))
    if (env.failStatus) return apiError('插件状态不可用')
    return Response.json(statusResponse())
  }
  if (path === '/api/cpa-helper-plugin/keys-summary') return Response.json({ keys: [{ api_key_hash: row.api_key_hash, enabled: env.key.enabled, ...env.summary, current_concurrency: 0, max_concurrency: env.key.max_concurrency }] })
  if (path === '/api/cpa-helper-plugin/directory') {
    assert.equal(new URL(url, 'http://api-key-smoke.invalid').searchParams.get('api_key_hash'), row.api_key_hash, 'Policy editor requests the selected key model catalog')
    return env.failDirectory ? apiError('插件凭据目录不可用') : Response.json(env.directory)
  }
  if (path === '/api/cpa-helper-plugin/policy/key/member-hash' && (init.method ?? 'GET') === 'GET') {
    if (env.deferPolicyReads) return new Promise((resolve, reject) => env.policyReads.push({ resolve, reject }))
    if (env.failPolicyLoad) return apiError('插件策略不可用')
    return Response.json(policyResponse())
  }
  if (path === '/api/cpa-helper-plugin/policy/key/member-hash' && init.method === 'PUT') {
    const payload = JSON.parse(init.body)
    env.policyWrites.push(payload)
    env.writes.push('policy')
    if (env.failPolicy) return apiError('策略保存失败')
    assert.equal(payload.policy_revision, env.revision, 'Writes use the current revision')
    env.revision++
    env.key = { ...payload.key, label: row.description }
    return Response.json(policyResponse())
  }
  assert.fail(`Unexpected API call ${path}`)
}
const passiveUI = ['NAlert', 'NButton', 'NDatePicker', 'NForm', 'NFormItem', 'NIcon', 'NInput', 'NInputNumber', 'NRadioButton', 'NRadioGroup', 'NSelect', 'NSpace', 'NSwitch', 'NTag']
const icons = ['Activity', 'CircleDollarSign', 'Copy', 'Eye', 'EyeOff', 'KeyRound', 'Layers3', 'Pencil', 'Power', 'Send', 'Trash2']
const views = { view: 'src/features/api-keys/views/ApiKeysView.vue', fields: 'src/features/plugin/components/PluginRuleFields.vue', notice: 'src/features/models/components/ModelListNotice.vue' }
const server = await createServer({
  root, logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'api-key-policy-smoke', enforce: 'pre',
    resolveId(id) {
      if (id === '/__api-key-policy-smoke.ts') return '\0api-key-policy-view'
      for (const name of ['fields', 'notice']) if (id.replaceAll('\\', '/').endsWith(views[name])) return `\0api-key-policy-${name}`
      if (id === 'naive-ui' || id === 'lucide-vue-next') return `\0api-key-policy:${id}`
    },
    async load(id) {
      const name = id.replace('\0api-key-policy-', '')
      if (views[name]) {
        const filename = `${root}/${views[name]}`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: `api-key-policy-${name}`, genDefaultAs: '__view' })
        const template = compileTemplate({ id: `api-key-policy-${name}`, filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        return (await transformWithEsbuild(`${script.content}\n${template.code}\n__view.render = render; export default __view`, filename, { loader: 'ts' })).code
      }
      if (id === '\0api-key-policy:naive-ui') return `
        import { h } from 'vue';
        ${passiveUI.map(name => `export const ${name} = { inheritAttrs: false, setup(_, { attrs, slots }) { return () => h('stub-${name}', attrs, slots.default?.()); } };`).join('\n')}
        export const NModal = { inheritAttrs: false, props: { show: Boolean }, setup(props, { attrs, slots }) { return () => props.show ? h('stub-NModal', attrs, slots.default?.()) : null; } };
        export const NDataTable = { inheritAttrs: false, setup(_, { attrs }) { return () => h('stub-NDataTable', attrs, (attrs.data ?? []).flatMap(row => (attrs.columns ?? []).map(column => h('stub-table-cell', { 'data-key': column.key, 'data-hash': row.api_key_hash }, column.render ? column.render(row) : row[column.key])))); } };
        export const useMessage = () => ({ error: message => globalThis.__apiKeyPolicySmoke.errors.push(message), success: message => globalThis.__apiKeyPolicySmoke.successes.push(message), info() {} });
        export const useDialog = () => ({ warning() {} });`
      if (id === '\0api-key-policy:lucide-vue-next') return icons.map(name => `export const ${name} = { render: () => null };`).join('\n')
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
const expirationButtons = (host, title) => {
  const modal = ui(host, 'NModal').find((node) => node.props.title === title)
  assert.ok(modal, `The ${title} modal is rendered`)
  assert.ok(!textContent(modal).includes('留空表示永久不过期。到期后'), 'Expiration hint is removed from the form')
  return descendants(modal).filter((node) => node.type === 'stub-NButton' && /^\d+ 天$/.test(textContent(node)))
}
const select = (host, label) => ui(ui(host, 'NFormItem').find((node) => node.props.label === label), 'NSelect')[0]
const updateValue = (node, value) => {
  const listeners = node.props['onUpdate:value']
  for (const listener of Array.isArray(listeners) ? listeners : [listeners]) listener(value)
}
const click = (node) => {
  const listeners = node.props.onClick
  for (const listener of Array.isArray(listeners) ? listeners : [listeners]) listener()
}
const optionLabel = (node, option) => (node.props['render-label'] ?? node.props.renderLabel)(option)
const catalog = (id) => ({
  has_api_keys: true, api_key_count: 1, queryable_api_key_count: 1, model_list_filter_status: 'enabled', errors: [],
  models: [{ id, name: id, sources: [{ api_key_hash: row.api_key_hash, api_key_preview: 'sk-mem...-key', description: 'Member' }] }],
})
let app
try {
  const { default: component } = await server.ssrLoadModule('/__api-key-policy-smoke.ts')
  const { setLanguage } = await server.ssrLoadModule('/src/shared/i18n/index.ts')
  let state
  const host = { children: [] }
  app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } })
  app.mount(host)
  await settle()
  assert.deepEqual(env.errors, [], 'Initial page loading succeeds')

  state.openCreateDialog()
  await settle()
  const createExpirationButtons = expirationButtons(host, '新建 API 密钥')
  assert.deepEqual(createExpirationButtons.map(textContent), ['1 天', '3 天', '7 天', '30 天', '90 天'], 'The create form offers all quick expiration durations')
  state.isSaving.value = true
  await settle()
  assert.ok(createExpirationButtons.every((button) => button.props.disabled), 'Create expiration buttons disable while creating')
  state.isSaving.value = false
  await settle()
  for (const [index, days] of [1, 3, 7, 30, 90].entries()) {
    const clickedAt = Date.UTC(2026, 9, 19, 12, 0, index)
    const originalNow = Date.now
    Date.now = () => clickedAt
    try {
      assert.equal(typeof createExpirationButtons[index].props.onClick, 'function', 'Each create duration has a click handler')
      click(createExpirationButtons[index])
    } finally {
      Date.now = originalNow
    }
    assert.equal(state.apiKeyExpiresAt.value, clickedAt + days * 24 * 60 * 60 * 1000, `Create ${days}-day expiration starts at click time`)
  }
  state.editorVisible.value = false

  await state.editApiKey(row)
  await settle()
  const editExpirationButtons = expirationButtons(host, '编辑 API Key')
  assert.deepEqual(editExpirationButtons.map(textContent), ['1 天', '3 天', '7 天', '30 天', '90 天'], 'The edit form offers all quick expiration durations')
  state.isPluginPolicySaving.value = true
  await settle()
  assert.ok(editExpirationButtons.every((button) => button.props.disabled), 'Edit expiration buttons disable while saving')
  state.isPluginPolicySaving.value = false
  await settle()
  for (const [index, days] of [1, 3, 7, 30, 90].entries()) {
    const clickedAt = Date.UTC(2026, 9, 19, 12, 1, index)
    const originalNow = Date.now
    Date.now = () => clickedAt
    try {
      assert.equal(typeof editExpirationButtons[index].props.onClick, 'function', 'Each edit duration has a click handler')
      click(editExpirationButtons[index])
    } finally {
      Date.now = originalNow
    }
    assert.equal(state.editedApiKeyExpiresAt.value, clickedAt + days * 24 * 60 * 60 * 1000, `Edit ${days}-day expiration starts at click time`)
  }
  state.editedApiKeyExpiresAt.value = state.savedApiKeyExpiresAt.value
  state.closePluginKeyPolicy()
  await settle()

  env.deferStatusReads = true
  const oldStatusFailure = state.refresh()
  await settle()
  const rejectedStatus = env.statusReads.at(-1)
  const replacementStatus = state.refresh()
  await settle()
  env.statusReads.at(-1).resolve(Response.json(statusResponse({ policy_revision: 2 })))
  await replacementStatus
  rejectedStatus.reject(new Error('old plugin status failed'))
  await oldStatusFailure
  assert.equal(state.pluginStatus.value?.health.policy_revision, 2, 'A late status rejection cannot erase a newer successful refresh')
  assert.equal(state.pluginStatusError.value, null, 'A late status rejection cannot publish an old warning')

  const oldStatusSuccess = state.refresh()
  await settle()
  const staleStatus = env.statusReads.at(-1)
  const currentStatusFailure = state.refresh()
  await settle()
  const currentStatusError = new Error('current plugin status failed')
  env.statusReads.at(-1).reject(currentStatusError)
  await currentStatusFailure
  assert.equal(state.pluginStatusError.value, currentStatusError, 'Current status failures remain visible')
  staleStatus.resolve(Response.json(statusResponse({ policy_revision: 3 })))
  await oldStatusSuccess
  assert.equal(state.pluginStatus.value, null, 'A late status success cannot overwrite the current failure')
  assert.equal(state.pluginStatusError.value, currentStatusError)

  const recoveredStatus = state.refresh()
  await settle()
  env.statusReads.at(-1).resolve(Response.json(statusResponse({ policy_revision: 4 })))
  await recoveredStatus
  assert.equal(state.pluginStatusError.value, null, 'A current success clears the current warning')
  assert.equal(state.pluginStatus.value.health.policy_revision, 4)
  env.deferStatusReads = false

  env.deferKeyReads = true
  const oldRefreshFailure = state.refresh()
  await settle()
  const rejectedKeys = env.keyReads.at(-1)
  const replacementRefresh = state.refresh()
  await settle()
  const replacementKeys = env.keyReads.at(-1)
  rejectedKeys.reject(new Error('old API key refresh failed'))
  await oldRefreshFailure
  assert.deepEqual(env.errors, [], 'Stale refresh errors cannot appear over a replacement refresh')
  assert.equal(state.isLoading.value, true, 'Stale refresh cleanup cannot clear the replacement loading state')
  replacementKeys.resolve(Response.json([row]))
  await replacementRefresh
  assert.equal(state.isLoading.value, false)

  const oldRefreshSuccess = state.refresh()
  await settle()
  const staleKeys = env.keyReads.at(-1)
  const currentRefreshSuccess = state.refresh()
  await settle()
  env.keyReads.at(-1).resolve(Response.json([{ ...row, description: 'Latest refresh' }]))
  await currentRefreshSuccess
  staleKeys.resolve(Response.json([{ ...row, description: 'Old refresh' }]))
  await oldRefreshSuccess
  assert.equal(state.apiKeys.value[0].description, 'Latest refresh', 'Stale refresh data cannot overwrite newer keys')
  env.deferKeyReads = false
  await state.refresh()

  state.openRequestTest(row)
  await settle()
  const stale = env.models.at(-1)
  state.requestTestVisible.value = false
  await settle()
  assert.equal(stale.signal.aborted, true, 'Closing cancels the old query')
  await state.editApiKey(row)
  assert.equal(state.pluginKeyPolicyError.value, null, 'Combined editor policy loading succeeds')
  state.pluginKeyPolicyDraft.value.max_concurrency = 3
  await state.saveEditedApiKey()
  assert.equal(env.policyWrites.length, 1, 'The real feature API serializes the policy write')
  state.openRequestTest(row)
  await settle()
  const current = env.models.at(-1)
  assert.notEqual(current, stale, 'Reopening starts a new query while the old transport is pending')
  stale.resolve(Response.json(catalog('old-rule-model')))
  await settle()
  assert.equal(state.availableModels.value, null, 'A late old result cannot refill the directory')
  assert.equal(state.isAvailableModelsLoading.value, true, 'Old finally cannot clear the new loading state')
  current.resolve(Response.json(catalog('new-rule-model')))
  await settle()
  assert.deepEqual(state.requestTestModelOptions.value.map(option => option.value), ['new-rule-model'])
  assert.equal(state.isAvailableModelsLoading.value, false)

  const oldFailure = state.loadAvailableModelsForTest()
  await settle()
  const failedRequest = env.models.at(-1)
  const nextQuery = state.loadAvailableModelsForTest()
  await settle()
  const nextRequest = env.models.at(-1)
  failedRequest.reject(new Error('old request failed'))
  await oldFailure
  assert.deepEqual(env.errors, [], 'Late stale errors do not appear in the replacement query')
  assert.equal(state.isAvailableModelsLoading.value, true, 'Stale rejection cannot finish the replacement query')
  nextRequest.resolve(Response.json(catalog('latest-model')))
  await nextQuery
  assert.equal(state.requestTestModel.value, 'latest-model')

  const activeFailure = state.loadAvailableModelsForTest()
  await settle()
  env.models.at(-1).reject(new Error('current request failed'))
  await activeFailure
  assert.equal(env.errors.length, 1, 'Current failures remain visible')
  assert.equal(state.isAvailableModelsLoading.value, false)

  const beforeSave = state.loadAvailableModelsForTest()
  await settle()
  const saveInvalidatedRequest = env.models.at(-1)
  await state.editApiKey(row)
  state.pluginKeyPolicyDraft.value.max_concurrency = 4
  await state.saveEditedApiKey()
  assert.equal(saveInvalidatedRequest.signal.aborted, true, 'Saving invalidates a query even if the modal stays open')
  saveInvalidatedRequest.resolve(Response.json(catalog('pre-save-model')))
  await beforeSave
  assert.equal(state.availableModels.value, null, 'A query invalidated by a save cannot publish')

  const bothWriteStart = env.writes.length
  await state.editApiKey(row)
  assert.equal(state.editorVisible.value, false, 'Existing keys use the combined editor, separate from creation')
  state.editedApiKeyDescription.value = 'Combined edit'
  state.pluginKeyPolicyDraft.value.enabled = false
  await state.saveEditedApiKey()
  assert.deepEqual(env.writes.slice(bothWriteStart), ['description', 'policy'], 'Description saves before policy without a refetch between them')
  assert.equal(row.description, 'Combined edit')
  assert.equal(env.key.enabled, false)
  assert.equal(env.key.label, 'Combined edit', 'Policy gets the saved description label')
  assert.equal(state.pluginKeyPolicyVisible.value, false, 'Combined success closes the editor')

  await state.editApiKey(row)
  const writesBeforeDescriptionFailure = env.policyWrites.length
  const successesBeforeDescriptionFailure = env.successes.length
  state.editedApiKeyDescription.value = 'Keep description draft'
  state.pluginKeyPolicyDraft.value.max_concurrency = 7
  env.failDescription = true
  await state.saveEditedApiKey()
  assert.equal(env.policyWrites.length, writesBeforeDescriptionFailure, 'A failed description does not start the second write')
  assert.equal(env.successes.length, successesBeforeDescriptionFailure, 'Failure does not show success')
  assert.equal(state.editedApiKeyDescription.value, 'Keep description draft')
  assert.equal(state.pluginKeyPolicyDraft.value.max_concurrency, 7)
  env.failDescription = false
  env.failPolicy = true
  await state.saveEditedApiKey()
  assert.equal(state.pluginKeyPolicyVisible.value, true, 'Partial failure keeps the editor open')
  assert.equal(state.savedApiKeyDescription.value, 'Keep description draft', 'The committed description is recorded')
  assert.equal(state.pluginKeyPolicyDraft.value.max_concurrency, 7, 'The unsaved policy draft survives')
  assert.equal(state.pluginKeyPolicyDirty.value, true)
  assert.match(state.apiKeyEditSaveError.value, /密钥信息已保存.*插件策略未保存/, 'Partial success is explicit')
  assert.equal(env.successes.length, successesBeforeDescriptionFailure, 'Partial failure does not report complete success')
  const descriptionWritesBeforeRetry = env.descriptionWrites.length
  env.failPolicy = false
  await state.saveEditedApiKey()
  assert.equal(env.descriptionWrites.length, descriptionWritesBeforeRetry, 'Retry submits only the remaining dirty policy')
  assert.equal(env.key.max_concurrency, 7)
  assert.equal(state.pluginKeyPolicyVisible.value, false)

  await state.editApiKey(row)
  const policiesBeforeDescriptionOnly = env.policyWrites.length
  state.editedApiKeyDescription.value = 'Description only'
  await state.saveEditedApiKey()
  assert.equal(env.policyWrites.length, policiesBeforeDescriptionOnly, 'An unchanged policy is not saved')
  env.failPolicyLoad = true
  await state.editApiKey(row)
  assert.equal(state.pluginKeyPolicyDraft.value, null)
  assert.ok(state.pluginKeyPolicyError.value, 'Unavailable policy has an explicit error')
  state.editedApiKeyDescription.value = 'Still editable'
  await state.saveEditedApiKey()
  assert.equal(row.description, 'Still editable', 'Plugin failure does not block the base description edit')
  assert.equal(env.policyWrites.length, policiesBeforeDescriptionOnly)
  env.failPolicyLoad = false

  env.deferPolicyReads = true
  const oldEditor = state.editApiKey(row)
  await settle()
  const oldEditorRequest = env.policyReads.at(-1)
  state.closePluginKeyPolicy()
  const newEditor = state.editApiKey(row)
  await settle()
  const newEditorRequest = env.policyReads.at(-1)
  oldEditorRequest.resolve(Response.json({ ...policyResponse(), key: { ...env.key, max_concurrency: 99 } }))
  await oldEditor
  assert.equal(state.pluginKeyPolicyDraft.value, null, 'An old response cannot populate the reopened editor')
  assert.equal(state.isPluginPolicyLoading.value, true, 'An old finally cannot finish the reopened editor load')
  newEditorRequest.resolve(Response.json(policyResponse()))
  await newEditor
  state.closePluginKeyPolicy()
  env.deferPolicyReads = false

  env.key = {
    ...env.key, enabled: true, group_ids: ['private-group'], max_concurrency: 9, future_key_field: { preserve: true },
    rule: { models: ['allowed-model'], denied_models: ['denied-model'], credential_ids: ['credential-1'], denied_credential_ids: [], credential_providers: [], denied_credential_providers: [], future_rule_field: ['keep'] },
  }
  env.revision = 31
  await state.refreshPluginKeySummaries()
  const freshKey = structuredClone(env.key)
  const writesBeforeToggle = env.policyWrites.length
  env.deferPolicyReads = true
  const disable = state.toggleApiKeyEnabled(row)
  await settle()
  await state.toggleApiKeyEnabled(row)
  assert.equal(env.policyWrites.length, writesBeforeToggle, 'Duplicate toggles are blocked while the fresh read is pending')
  const duringToggle = state.pluginKeySummaryByHash.value.get(row.api_key_hash)
  assert.equal(duringToggle.enabled, true, 'The summary is not optimistically disabled')
  env.policyReads.at(-1).resolve(Response.json(policyResponse()))
  await disable
  env.deferPolicyReads = false
  assert.deepEqual(env.policyWrites.at(-1), { policy_revision: 31, key: { ...freshKey, enabled: false } }, 'Quick disable preserves every other fresh policy field')
  assert.equal(state.pluginKeySummaryByHash.value.get(row.api_key_hash).enabled, false)
  await state.toggleApiKeyEnabled(row)
  assert.equal(state.pluginKeySummaryByHash.value.get(row.api_key_hash).enabled, true, 'A disabled key can be enabled again')
  const successesBeforeToggleFailure = env.successes.length
  env.failPolicy = true
  await state.toggleApiKeyEnabled(row)
  assert.equal(state.pluginKeySummaryByHash.value.get(row.api_key_hash).enabled, true, 'Failed toggles leave the real summary intact')
  assert.equal(env.successes.length, successesBeforeToggleFailure, 'Failed toggles do not show success')
  assert.equal(state.pluginKeyToggleHash.value, null)
  env.failPolicy = false
  state.pluginKeySummaries.value = null
  const writesBeforeUnknown = env.policyWrites.length
  await state.toggleApiKeyEnabled(row)
  assert.equal(env.policyWrites.length, writesBeforeUnknown, 'Unknown enabled state cannot be toggled')

  const ruleCell = () => descendants(host).find((node) => node.type === 'stub-table-cell' && node.props['data-key'] === 'group_names')
  const routeCases = [
    { group_names: [], has_custom_rule: true, enabled: true, max_concurrency: 0 },
    { group_names: ['Global batch'], has_custom_rule: false, enabled: true, max_concurrency: 0 },
    { group_names: ['Global batch'], has_custom_rule: true, enabled: true, max_concurrency: 0 },
    { group_names: [], has_custom_rule: false, enabled: false, max_concurrency: 7 },
  ]
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    for (const summary of routeCases) {
      env.summary = { group_names: summary.group_names, has_custom_rule: summary.has_custom_rule }
      env.key.enabled = summary.enabled
      env.key.max_concurrency = summary.max_concurrency
      await state.refreshPluginKeySummaries()
      await settle()
      const text = textContent(ruleCell())
      assert.equal(text.includes(language === 'zh' ? '自定义规则' : 'Custom rule'), summary.has_custom_rule, 'The rendered table uses the backend custom-rule flag')
      assert.equal(text.includes('Global batch'), summary.group_names.length > 0, 'Linked global rules remain visible alongside a custom rule')
      assert.equal(text.includes(language === 'zh' ? '未配置路由规则' : 'No route rules configured'), !summary.has_custom_rule && summary.group_names.length === 0, 'Only an empty routing state gets the no-rules label')
      assert.ok(text.includes(summary.enabled ? language === 'zh' ? '已启用' : 'Enabled' : language === 'zh' ? '已禁用' : 'Disabled'), 'Enabled state stays independent from routing rules')
    }
  }
  setLanguage('zh')
  for (const invalidCustomState of [undefined, 'false', 1, null]) {
    env.summary = { group_names: [], has_custom_rule: invalidCustomState }
    await state.refreshPluginKeySummaries()
    assert.equal(state.pluginKeySummaries.value, null, 'Missing or non-boolean custom state is not reported as an empty rule')
    assert.match(state.pluginKeySummaryError.value.message, /摘要响应格式无效/)
  }

  const longNote = `${'Nightly routing for the batch team. '.repeat(20)}End of note`
  env.groups = [
    { id: 'with-note', name: 'With note', note: 'Interactive requests', rule: {} },
    { id: 'no-note', name: 'No note', note: '', rule: {} },
    { id: 'long-note', name: 'Long note', note: longNote, rule: {} },
  ]
  const allowedRef = `sha256:${'a'.repeat(64)}`
  const deniedRef = `sha256:${'b'.repeat(64)}`
  const missingAllowedRef = `sha256:${'c'.repeat(64)}`
  const missingDeniedRef = `sha256:${'d'.repeat(64)}`
  env.directory = {
    models: ['route-model', 'TEAM/public-alias'],
    credentials: [
      { ref: allowedRef, label: 'Codex primary · https://codex-a.invalid/v1 · sk-upa...1234', source: 'ai-providers', provider: 'codex', status: 'active', auth_index: '101' },
      { ref: deniedRef, label: 'Codex other · https://codex-b.invalid/v1 · sk-upb...5678', source: 'ai-providers', provider: 'codex', status: 'active', auth_index: '102' },
    ],
    credential_providers: [{ source: 'ai-providers', provider: 'codex', label: 'Codex' }],
  }
  env.key = {
    ...env.key,
    rule: {
      models: ['team/public-alias'],
      denied_models: ['Team/public-alias'],
      credential_ids: [allowedRef],
      denied_credential_ids: [deniedRef],
      credential_providers: [{ source: 'ai-providers', provider: 'codex' }],
      denied_credential_providers: [],
    },
  }
  await state.refreshPluginKeySummaries()
  await state.editApiKey(row)
  await settle()
  const caseAllowedModels = select(host, '允许的模型')
  const caseDeniedModels = select(host, '禁止的模型')
  const equivalentAllowedOptions = caseAllowedModels.props.options.filter((option) => option.value.toLowerCase() === 'team/public-alias')
  const equivalentDeniedOptions = caseDeniedModels.props.options.filter((option) => option.value.toLowerCase() === 'team/public-alias')
  assert.deepEqual(equivalentAllowedOptions, [{ label: 'TEAM/public-alias', value: 'team/public-alias' }], 'A case-insensitive catalog match keeps the allowed rule spelling without a duplicate option')
  assert.deepEqual(equivalentDeniedOptions, [{ label: 'TEAM/public-alias', value: 'Team/public-alias' }], 'A case-insensitive catalog match keeps the denied rule spelling without a duplicate option')
  assert.ok(!ui(host, 'NAlert').some((alert) => alert.props.class === 'plugin-rule-missing'), 'Case-only model variants do not show the missing-directory warning')
  updateValue(caseAllowedModels, ['team/public-alias', 'route-model'])
  updateValue(caseDeniedModels, ['Team/public-alias'])
  await state.saveEditedApiKey()
  assert.deepEqual(env.policyWrites.at(-1).key.rule.models, ['team/public-alias', 'route-model'], 'Saving preserves the allowed model spelling')
  assert.deepEqual(env.policyWrites.at(-1).key.rule.denied_models, ['Team/public-alias'], 'Saving preserves the denied model spelling')

  const references = {
    credential_ids: [allowedRef, missingAllowedRef], denied_credential_ids: [deniedRef, missingDeniedRef],
    credential_providers: [{ source: 'ai-providers', provider: 'codex' }],
    denied_credential_providers: [{ source: 'auth-files', provider: 'retired-file-provider' }],
  }
  env.key = { ...env.key, enabled: true, group_ids: ['with-note', 'long-note'], rule: { ...env.key.rule, models: ['route-model', 'retired-allowed-model'], denied_models: ['retired-denied-model'], ...references } }
  env.summary = { group_names: ['With note', 'Long note'], has_custom_rule: true }
  await state.refreshPluginKeySummaries()
  await state.editApiKey(row)
  await settle()
  const linkedRules = select(host, '关联全局规则')
  assert.ok(linkedRules.props.filterable === true || linkedRules.props.filterable === '', 'Rule names and notes are searchable in the actual select')
  assert.deepEqual(linkedRules.props.options, [
    { label: 'With note · Interactive requests', value: 'with-note' },
    { label: 'No note', value: 'no-note' },
    { label: `Long note · ${longNote}`, value: 'long-note' },
  ], 'The actual select shows notes and keeps rule IDs as values')
  const renderedLongNote = optionLabel(linkedRules, linkedRules.props.options[2])
  assert.equal(renderedLongNote.props.title, `Long note · ${longNote}`, 'The full long note remains available in the selected-label tooltip')
  assert.equal(renderedLongNote.children, `Long note · ${longNote}`, 'The label renderer does not shorten a long note')
  const allowedCredentials = select(host, '允许的凭据')
  const deniedCredentials = select(host, '禁止的凭据')
  const allowedModels = select(host, '允许的模型')
  const deniedModels = select(host, '禁止的模型')
  assert.match(allowedModels.props.options.find((option) => option.value === 'retired-allowed-model').label, /已选模型（当前目录缺失）/)
  assert.match(deniedModels.props.options.find((option) => option.value === 'retired-denied-model').label, /已选模型（当前目录缺失）/)
  for (const credential of env.directory.credentials) {
    const option = allowedCredentials.props.options.find((value) => value.value === credential.ref)
    assert.equal(option.label, credential.label, 'Backend channel labels remain intact without appended auth indexes')
    assert.equal(optionLabel(allowedCredentials, option).props.title, credential.label)
  }
  assert.match(allowedCredentials.props.options.find((option) => option.value === missingAllowedRef).label, /当前目录缺失/)
  assert.match(deniedCredentials.props.options.find((option) => option.value === missingDeniedRef).label, /当前目录缺失/)
  assert.match(select(host, '禁止的凭据类别').props.options.find((option) => option.value === 'auth-files/retired-file-provider').label, /当前目录缺失/)
  assert.match(textContent(host), /部分已选模型、凭据或类别/)
  assert.match(textContent(host), /原引用仍会保留并保存/)
  setLanguage('en')
  await settle()
  assert.match(select(host, 'Allowed models').props.options.find((option) => option.value === 'retired-allowed-model').label, /Selected model \(missing from directory\)/)
  assert.match(select(host, 'Denied models').props.options.find((option) => option.value === 'retired-denied-model').label, /Selected model \(missing from directory\)/)
  assert.match(select(host, 'Allowed credentials').props.options.find((option) => option.value === missingAllowedRef).label, /Selected credential \(missing from directory\)/)
  assert.match(select(host, 'Denied credential categories').props.options.find((option) => option.value === 'auth-files/retired-file-provider').label, /Selected category \(missing from directory\)/)
  assert.match(textContent(host), /Some selected models, credentials or categories are missing/)
  assert.match(textContent(host), /Their references will be kept when saving/)
  setLanguage('zh')
  await settle()
  updateValue(select(host, '关联全局规则'), ['with-note', 'long-note'])
  updateValue(select(host, '允许的凭据'), references.credential_ids)
  updateValue(select(host, '禁止的凭据'), references.denied_credential_ids)
  updateValue(select(host, '允许的模型'), ['edited-route-model'])
  await state.saveEditedApiKey()
  assert.deepEqual(env.policyWrites.at(-1).key.group_ids, ['with-note', 'long-note'], 'Saving note-rich selections sends unchanged group IDs')
  for (const [field, value] of Object.entries(references)) assert.deepEqual(env.policyWrites.at(-1).key.rule[field], value, 'The real Key PUT retains both allowed and denied credential references')

  env.groups[0].note = 'Updated operator note'
  for (const [index, channelName] of ['Codex renamed', 'Codex renamed again', 'Codex other', 'Codex'].entries()) {
    env.directory.credentials[0].label = `${channelName} · https://codex-a.invalid/v1 · sk-upa...1234`
    await state.editApiKey(row)
    await settle()
    const options = select(host, '允许的凭据').props.options
    assert.equal(options.find((option) => option.value === allowedRef).label, env.directory.credentials[0].label, 'A fresh directory updates the label for the same reference')
    assert.equal(options.find((option) => option.value === deniedRef).label, env.directory.credentials[1].label, 'Renaming one channel does not relabel another')
    assert.equal(select(host, '关联全局规则').props.options[0].label, 'With note · Updated operator note')
    updateValue(select(host, '允许的模型'), [`renamed-route-model-${index}`])
    await state.saveEditedApiKey()
    for (const [field, value] of Object.entries(references)) assert.deepEqual(env.policyWrites.at(-1).key.rule[field], value, 'Directory label changes never rebind saved Key rules')
  }

  env.directory = { models: [], credentials: [], credential_providers: [] }
  await state.editApiKey(row)
  await settle()
  assert.deepEqual(select(host, '允许的模型').props.value, env.key.rule.models)
  assert.deepEqual(select(host, '禁止的模型').props.value, env.key.rule.denied_models)
  assert.match(select(host, '允许的模型').props.options.find((option) => option.value === env.key.rule.models[0]).label, /已选模型（当前目录缺失）/)
  assert.match(select(host, '禁止的模型').props.options.find((option) => option.value === 'retired-denied-model').label, /已选模型（当前目录缺失）/)
  assert.deepEqual(select(host, '允许的凭据').props.value, references.credential_ids)
  assert.deepEqual(select(host, '禁止的凭据').props.value, references.denied_credential_ids)
  for (const ref of [...references.credential_ids, ...references.denied_credential_ids]) assert.match(select(host, '允许的凭据').props.options.find((option) => option.value === ref).label, /当前目录缺失/, 'Every selected missing reference remains a labelled option')
  updateValue(select(host, '允许的凭据类别'), ['ai-providers/codex'])
  updateValue(select(host, '禁止的凭据类别'), ['auth-files/retired-file-provider'])
  updateValue(select(host, '允许的模型'), ['keep-missing-references'])
  env.failPolicy = true
  const successesBeforeMissingFailure = env.successes.length
  await state.saveEditedApiKey()
  assert.equal(env.successes.length, successesBeforeMissingFailure)
  assert.equal(state.pluginKeyPolicyVisible.value, true, 'A failed save keeps the editor and reference warning visible')
  assert.deepEqual(state.pluginKeyPolicyDraft.value.rule.models, ['keep-missing-references'])
  assert.deepEqual(state.pluginKeyPolicyDraft.value.rule.denied_models, ['retired-denied-model'])
  for (const [field, value] of Object.entries(references)) assert.deepEqual(state.pluginKeyPolicyDraft.value.rule[field], value, 'A failed save preserves selected missing allowed and denied references')
  env.failPolicy = false
  await state.saveEditedApiKey()
  assert.deepEqual(env.policyWrites.at(-1).key.rule.models, ['keep-missing-references'])
  assert.deepEqual(env.policyWrites.at(-1).key.rule.denied_models, ['retired-denied-model'])
  for (const [field, value] of Object.entries(references)) assert.deepEqual(env.policyWrites.at(-1).key.rule[field], value)
  const writesBeforeDirectoryFailure = env.policyWrites.length
  env.failDirectory = true
  await state.editApiKey(row)
  assert.ok(state.pluginKeyPolicyError.value, 'A failed directory read stays an explicit error')
  assert.equal(env.policyWrites.length, writesBeforeDirectoryFailure, 'A failed directory read never writes a cleared policy')
  for (const [field, value] of Object.entries(references)) assert.deepEqual(env.key.rule[field], value)
  env.failDirectory = false
  await state.editApiKey(row)
  await settle()
  for (const [field, value] of Object.entries(references)) assert.deepEqual(state.pluginKeyPolicyDraft.value.rule[field], value, 'Recovery reloads the original saved references')
  state.closePluginKeyPolicy()

  const { normalizeCPAHelperPluginKey, getCPAHelperPluginStatus } = await server.ssrLoadModule('/src/features/plugin/api/cpaHelperPluginApi.ts')
  assert.equal(Object.hasOwn(normalizeCPAHelperPluginKey(keyPolicy), 'claude_cache_ttl'), false)
  assert.deepEqual(await getCPAHelperPluginStatus(), env.status, 'The status contract works without the retired TTL feature')
  const statusReadsBeforeEdit = env.statusReads.length
  env.deferStatusReads = true
  await state.editApiKey(row)
  await settle()
  assert.equal(env.statusReads.length, statusReadsBeforeEdit, 'Key editing does not wait for a retired feature status lookup')
  assert.equal(state.apiKeyEditDirty.value, false)
  for (const language of ['zh', 'en']) {
    setLanguage(language)
    await settle()
    assert.ok(!ui(host, 'NFormItem').some((node) => /TTL/.test(node.props.label ?? '')), 'No TTL preference remains in either language')
  }
  setLanguage('zh')
  state.pluginKeyPolicyDraft.value.max_concurrency++
  await state.saveEditedApiKey()
  assert.equal(Object.hasOwn(env.policyWrites.at(-1).key, 'claude_cache_ttl'), false, 'Normal policy saves never create a TTL field')
  env.deferStatusReads = false
  const validStatus = env.status
  for (const invalid of [{ loaded: 1 }, { capabilities: null }, { health: [] }, { response_model_verification_supported: 'false' }]) {
    env.status = { ...validStatus, ...invalid }
    await assert.rejects(getCPAHelperPluginStatus(), /插件能力响应格式无效/, 'Other capability validation remains strict')
  }
  env.status = validStatus

  const errorsBeforeUnmount = env.errors.length
  const beforeUnmount = state.loadAvailableModelsForTest()
  await settle()
  const removedRequest = env.models.at(-1)
  env.deferStatusReads = true
  const statusBeforeUnmount = state.refresh()
  await settle()
  const removedStatus = env.statusReads.at(-1)
  const statusSnapshot = JSON.stringify(state.pluginStatus.value)
  app.unmount()
  app = null
  assert.equal(removedRequest.signal.aborted, true)
  removedRequest.reject(new Error('unmounted request failed'))
  removedStatus.reject(new Error('unmounted plugin status failed'))
  await beforeUnmount
  await statusBeforeUnmount
  assert.equal(env.errors.length, errorsBeforeUnmount, 'Unmount suppresses late query errors')
  assert.equal(JSON.stringify(state.pluginStatus.value), statusSnapshot, 'Unmount invalidates the pending status refresh')
  assert.equal(state.pluginStatusError.value, null, 'Unmount suppresses late plugin status errors')

  env.deferStatusReads = false
  let detachedState
  app = renderer.createApp({ ...component, setup(props, context) { detachedState = component.setup(props, context); return detachedState } })
  app.mount({ children: [] })
  await settle()
  env.deferStatusReads = true
  const lateStatusSuccess = detachedState.refresh()
  await settle()
  const detachedStatus = env.statusReads.at(-1)
  const detachedStatusSnapshot = JSON.stringify(detachedState.pluginStatus.value)
  app.unmount()
  app = null
  detachedStatus.resolve(Response.json(statusResponse({ policy_revision: 99 })))
  await lateStatusSuccess
  assert.equal(JSON.stringify(detachedState.pluginStatus.value), detachedStatusSnapshot, 'Unmount suppresses a current late status success')
  assert.equal(detachedState.pluginStatusError.value, null)
  console.log('API key policy smoke passed: four routing states, required custom flag, notes and long labels, lossless allowed/denied references through rename/missing/error recovery, combined saves, quick toggles, stale queries and unmount')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.__apiKeyPolicySmoke
  delete globalThis.fetch
  delete globalThis.localStorage
  delete globalThis.document
}
