import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, nextTick } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const env = { errors: [], successes: [], models: [], policyWrites: [], descriptionWrites: [], writes: [], policyReads: [], statusReads: [], keyReads: [], revision: 1, failPolicy: false, failDescription: false, failPolicyLoad: false, deferPolicyReads: false, deferStatusReads: false, deferKeyReads: false }
globalThis.__apiKeyPolicySmoke = env
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN' } }
const row = { api_key_hash: 'member-hash', description: 'Member', api_key: 'sk-member-secret-key', models: [], last_model: null }
const keyPolicy = { id: 'member-scope', label: 'Member', enabled: true, group_ids: [], max_concurrency: 0, rule: {} }
env.key = structuredClone(keyPolicy)
const policyResponse = () => ({ policy_revision: env.revision, groups: [], key: env.key })
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
    return Response.json(row)
  }
  if (path === '/api/usage/overview') return Response.json({ summary: {} })
  if (path === '/api/account/quota') return Response.json(null)
  if (path === '/api/account/model-request') return Response.json({ openai_base_url: 'http://example.invalid/v1' })
  if (path === '/api/cpa-helper-plugin/status') {
    if (env.deferStatusReads) return new Promise((resolve, reject) => env.statusReads.push({ resolve, reject }))
    return Response.json({ loaded: true, capabilities: {}, health: {} })
  }
  if (path === '/api/cpa-helper-plugin/keys-summary') return Response.json({ keys: [{ api_key_hash: row.api_key_hash, enabled: env.key.enabled, group_names: [], current_concurrency: 0, max_concurrency: env.key.max_concurrency }] })
  if (path === '/api/cpa-helper-plugin/directory') {
    assert.equal(new URL(url, 'http://api-key-smoke.invalid').searchParams.get('api_key_hash'), row.api_key_hash, 'Policy editor requests the selected key model catalog')
    return Response.json({ models: [], credentials: [], credential_providers: [] })
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
const passiveUI = ['NAlert', 'NButton', 'NDataTable', 'NForm', 'NFormItem', 'NIcon', 'NInput', 'NInputNumber', 'NModal', 'NRadioButton', 'NRadioGroup', 'NSelect', 'NSpace', 'NSwitch', 'NTag']
const icons = ['Activity', 'CircleDollarSign', 'Copy', 'Eye', 'EyeOff', 'KeyRound', 'Layers3', 'Pencil', 'Power', 'Send', 'Trash2']
const server = await createServer({
  root, logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'api-key-policy-smoke', enforce: 'pre',
    resolveId(id) {
      if (id === '/__api-key-policy-smoke.ts') return '\0api-key-policy-view'
      if (id === 'naive-ui' || id === 'lucide-vue-next') return `\0api-key-policy:${id}`
    },
    async load(id) {
      if (id === '\0api-key-policy-view') {
        const filename = `${root}/src/features/api-keys/views/ApiKeysView.vue`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: 'api-key-policy', genDefaultAs: '__view' })
        const template = compileTemplate({ id: 'api-key-policy', filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        return (await transformWithEsbuild(`${script.content}\n${template.code}\n__view.render = render; export default __view`, filename, { loader: 'ts' })).code
      }
      if (id === '\0api-key-policy:naive-ui') return `
        ${passiveUI.map(name => `export const ${name} = { render: () => null };`).join('\n')}
        export const useMessage = () => ({ error: message => globalThis.__apiKeyPolicySmoke.errors.push(message), success: message => globalThis.__apiKeyPolicySmoke.successes.push(message), info() {} });
        export const useDialog = () => ({ warning() {} });`
      if (id === '\0api-key-policy:lucide-vue-next') return icons.map(name => `export const ${name} = { render: () => null };`).join('\n')
    },
  }],
})
const renderer = createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  insert() {}, remove() {}, setText() {}, setElementText() {}, patchProp() {}, parentNode: () => null, nextSibling: () => null,
})
const settle = async () => { await nextTick(); await nextTurn(); await nextTick() }
const catalog = (id) => ({
  has_api_keys: true, api_key_count: 1, queryable_api_key_count: 1, model_list_filter_status: 'enabled', errors: [],
  models: [{ id, name: id, sources: [{ api_key_hash: row.api_key_hash, api_key_preview: 'sk-mem...-key', description: 'Member' }] }],
})
let app
try {
  const { default: component } = await server.ssrLoadModule('/__api-key-policy-smoke.ts')
  let state
  app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } })
  app.mount({})
  await settle()
  assert.deepEqual(env.errors, [], 'Initial page loading succeeds')

  env.deferStatusReads = true
  const oldStatusFailure = state.refresh()
  await settle()
  const rejectedStatus = env.statusReads.at(-1)
  const replacementStatus = state.refresh()
  await settle()
  env.statusReads.at(-1).resolve(Response.json({ loaded: true, capabilities: {}, health: { policy_revision: 2 } }))
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
  staleStatus.resolve(Response.json({ loaded: true, capabilities: {}, health: { policy_revision: 3 } }))
  await oldStatusSuccess
  assert.equal(state.pluginStatus.value, null, 'A late status success cannot overwrite the current failure')
  assert.equal(state.pluginStatusError.value, currentStatusError)

  const recoveredStatus = state.refresh()
  await settle()
  env.statusReads.at(-1).resolve(Response.json({ loaded: true, capabilities: {}, health: { policy_revision: 4 } }))
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
  assert.match(state.apiKeyEditSaveError.value, /描述已保存.*插件策略未保存/, 'Partial success is explicit')
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
  app.mount({})
  await settle()
  env.deferStatusReads = true
  const lateStatusSuccess = detachedState.refresh()
  await settle()
  const detachedStatus = env.statusReads.at(-1)
  const detachedStatusSnapshot = JSON.stringify(detachedState.pluginStatus.value)
  app.unmount()
  app = null
  detachedStatus.resolve(Response.json({ loaded: true, capabilities: {}, health: { policy_revision: 99 } }))
  await lateStatusSuccess
  assert.equal(JSON.stringify(detachedState.pluginStatus.value), detachedStatusSnapshot, 'Unmount suppresses a current late status success')
  assert.equal(detachedState.pluginStatusError.value, null)
  console.log('API key policy smoke passed: combined/partial/description-only saves, unavailable policy, fresh quick toggles, duplicate/failure guards, stale refresh/status/editor/model queries and unmount')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.__apiKeyPolicySmoke
  delete globalThis.fetch
  delete globalThis.localStorage
  delete globalThis.document
}
