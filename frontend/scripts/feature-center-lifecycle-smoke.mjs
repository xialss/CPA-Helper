import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import 'fake-indexeddb/auto'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, nextTick } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const globals = new Map(['window', 'document', 'EventSource', 'fetch', 'localStorage'].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
const windowEvents = new EventTarget()
const documentEvents = new EventTarget()
const timers = new Map()
let timerId = 0
globalThis.window = Object.assign(windowEvents, { setTimeout: callback => { const id = ++timerId; timers.set(id, callback); return id }, clearTimeout: id => timers.delete(id) })
globalThis.document = Object.assign(documentEvents, { visibilityState: 'visible', documentElement: { lang: 'zh-CN' } })
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
const sources = []
class ControlledEventSource {
  constructor(url, options) { assert.equal(url, '/api/features/events'); assert.equal(options.withCredentials, true); this.listeners = new Map(); this.closed = false; sources.push(this) }
  addEventListener(name, callback) { this.listeners.set(name, callback) }
  close() { this.closed = true }
  snapshot(value) { this.listeners.get('snapshot')?.({ data: JSON.stringify(value), lastEventId: String(value.revision) }) }
}
globalThis.EventSource = ControlledEventSource
const enabled = { revision: 1, image_studio_enabled: true, ai_radar_enabled: true }
let actual = { ...enabled }
const requests = []
const held = []
const response = value => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
globalThis.fetch = async (url, init = {}) => {
  requests.push({ url, init })
  const index = held.findIndex(item => item.url === url)
  if (index !== -1) return held.splice(index, 1)[0].gate.promise
  if (url === '/api/features' || url === '/api/settings/features') return response(actual)
  if (url === '/api/ai-radar') return response({ available: false, points: [], source_updated_at: null, message: null })
  if (url.startsWith('/api/ai-radar/history')) return response({ available: false, series: [], message: null })
  assert.fail(`Unexpected request: ${url}`)
}
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
function hold(url) { const gate = deferred(); held.push({ url, gate }); return gate }
async function flush() { await nextTick(); await nextTurn(); await nextTick() }
async function until(predicate, label) { for (let attempt = 0; attempt < 80; attempt++) { if (predicate()) return; await flush() } assert.fail(`Timed out: ${label}`) }
const bridge = { errors: [], successes: [] }
const imageModulesLoaded = new Set()
globalThis.__featureLifecycleSmoke = bridge
const server = await createServer({
  root, logLevel: 'error', ssr: { noExternal: ['naive-ui', 'vue-router', 'lucide-vue-next'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'feature-lifecycle-smoke', enforce: 'pre',
    transform(_code, id) { if (id.replaceAll('\\', '/').includes('/features/image-studio/')) imageModulesLoaded.add(id) },
    resolveId(id) {
      if (id.startsWith('/__feature-smoke__/')) return `\0${id}`
      if (['naive-ui', 'vue-router', 'lucide-vue-next'].includes(id)) return `\0feature-boundary:${id}`
      if (/\/ai-radar\/components\/[^/]+\.vue$/.test(id)) return '\0feature-boundary:chart'
    },
    async load(id) {
      if (id.startsWith('\0/__feature-smoke__/')) {
        const relative = id.slice('\0/__feature-smoke__/'.length).replace(/\.ts$/, '.vue')
        const filename = path.join(root, relative)
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: relative, genDefaultAs: '__featureComponent' })
        const template = compileTemplate({ id: relative, filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        const code = `${script.content}\n${template.code}\n__featureComponent.render = render; export default __featureComponent`.replace(/from (['"])(\.\.?\/[^'"]+)\1/g, (_match, quote, request) => {
          const target = path.resolve(path.dirname(filename), request).replaceAll('\\', '/')
          return `from ${quote}${target.endsWith('.vue') ? `/__feature-smoke__/${path.relative(root, target).replaceAll('\\', '/').replace(/\.vue$/, '.ts')}` : target}${quote}`
        })
        return (await transformWithEsbuild(code, filename, { loader: 'ts' })).code
      }
      if (id === '\0feature-boundary:chart') return 'export default {render:()=>null}'
      if (id === '\0feature-boundary:vue-router') return 'export const useRoute = () => ({meta:{feature:"image_studio"}})'
      if (id === '\0feature-boundary:lucide-vue-next') return 'export const ExternalLink = {render:()=>null}; export const RefreshCw = {render:()=>null}'
      if (id === '\0feature-boundary:naive-ui') return `import {h} from 'vue';
        const control = name => ({inheritAttrs:false, setup(_props,{attrs,slots}){return()=>h('div',{'data-control':name,...attrs},slots.default?.())}});
        ${['NAlert', 'NButton', 'NSwitch', 'NTag', 'NEmpty', 'NIcon', 'NSpin', 'NTooltip'].map(name => `export const ${name}=control('${name}');`).join('\n')}
        export const useMessage=()=>({error:text=>globalThis.__featureLifecycleSmoke.errors.push(text),success:text=>globalThis.__featureLifecycleSmoke.successes.push(text)});`
    },
  }],
})
const renderer = createRenderer({
  insert(node, parent, anchor) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, node) },
  remove(node) { node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  createElement: tag => ({ tag, children: [], props: {} }), createText: text => ({ text }), createComment: text => ({ text }),
  setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text },
  parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  patchProp(node, key, _old, value) { node.props[key] = value },
})
function text(node) { return `${node.text ?? ''}${(node.children ?? []).map(text).join('')}` }
function controls(node, name) { return [...(node.props?.['data-control'] === name ? [node] : []), ...(node.children ?? []).flatMap(child => controls(child, name))] }
async function mount(relative) {
  const { default: component } = await server.ssrLoadModule(`/__feature-smoke__/src/features/${relative}.ts`)
  let state
  const app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } })
  const element = { children: [] }
  app.mount(element)
  await flush()
  return { app, element, state }
}

try {
  const { FeatureController, featureAvailability } = await server.ssrLoadModule('/src/features/feature-center/state/featureController.ts')
  const streams = [], reads = []
  const control = new FeatureController({
    read(signal) { const gate = deferred(); reads.push({ gate, signal }); return gate.promise },
    connect(snapshot, failure) { const stream = { snapshot, failure, closed: false, close() { this.closed = true } }; streams.push(stream); return stream },
  })
  control.setSession(1)
  assert.equal(featureAvailability(control.state, 'image_studio'), 'unknown')
  streams[0].snapshot(enabled)
  const verification = control.verify('image_studio')
  await flush()
  streams[0].snapshot({ ...enabled, revision: 2, image_studio_enabled: false })
  reads[0].gate.resolve(enabled)
  await assert.rejects(verification, /关闭/)
  assert.equal(featureAvailability(control.state, 'ai_radar'), 'enabled')
  streams[0].failure(new Error('connection lost'))
  assert.equal(streams[0].closed, true)
  assert.equal(featureAvailability(control.state, 'ai_radar'), 'unknown')
  const reconnected = control.reconnect()
  streams[1].snapshot({ ...enabled, revision: 2, image_studio_enabled: false })
  await reconnected
  assert.equal(featureAvailability(control.state, 'ai_radar'), 'enabled', 'same revision restores trusted availability')
  streams[0].snapshot({ ...enabled, revision: 99 })
  streams[0].failure(new Error('late old error'))
  assert.equal(control.state.snapshot.revision, 2)
  const oldRead = control.verify()
  await flush()
  control.setSession(2)
  assert.equal(reads[1].signal.aborted, true)
  streams[2].snapshot({ ...enabled, revision: 1, ai_radar_enabled: false })
  reads[1].gate.resolve({ ...enabled, revision: 500 })
  await assert.rejects(oldRead)
  assert.equal(control.state.snapshot.revision, 1)
  assert.equal(featureAvailability(control.state, 'ai_radar'), 'disabled')
  for (const image of [false, true]) for (const radar of [false, true]) {
    streams[2].snapshot({ revision: ++actual.revision, image_studio_enabled: image, ai_radar_enabled: radar })
    assert.equal(featureAvailability(control.state, 'image_studio'), image ? 'enabled' : 'disabled')
    assert.equal(featureAvailability(control.state, 'ai_radar'), radar ? 'enabled' : 'disabled')
  }
  control.setSession(null)
  assert.equal(streams[2].closed, true)
  assert.equal(control.state.snapshot, null)

  const { setCurrentUser } = await server.ssrLoadModule('/src/features/auth/state/currentUser.ts')
  const features = await server.ssrLoadModule('/src/features/feature-center/state/useFeatureState.ts')
  const user = { id: 501, username: 'fixture', is_admin: true, is_super_admin: true, must_change_password: false }
  actual = { ...enabled }
  setCurrentUser(user)
  sources.at(-1).snapshot({ ...actual, image_studio_enabled: false })
  const gate = await mount('feature-center/views/FeatureRouteView')
  assert.match(text(gate.element), /管理员关闭/)
  assert.equal(imageModulesLoaded.size, 0, 'disabled direct URL never imports the image business module or storage')
  gate.app.unmount()

  sources.at(-1).snapshot(actual = { ...enabled, revision: 2 })
  const studioModule = await server.ssrLoadModule('/src/features/image-studio/state/useImageStudio.ts')
  const studio = studioModule.useImageStudio()
  for (let pass = 0; pass < 12 && studio.localLoading.value; pass++) await flush()
  studio.connection.value = { baseUrl: 'https://image.invalid/v1', apiKey: 'fixture-key' }
  const models = hold('https://image.invalid/v1/models')
  const modelLoad = studio.loadModels()
  await flush()
  const oldModelRequest = requests.findLast(request => request.url === 'https://image.invalid/v1/models')
  assert.ok(oldModelRequest)
  sources.at(-1).snapshot(actual = { ...actual, revision: 3, image_studio_enabled: false })
  assert.equal(oldModelRequest.init.signal.aborted, true)
  models.resolve(response({ data: [{ id: 'nai-late' }] }))
  await modelLoad
  assert.deepEqual(studio.models.value, [])
  assert.equal(studio.connection.value.apiKey, 'fixture-key')
  await assert.rejects(studio.submit(), /暂停/)
  sources.at(-1).snapshot(actual = { ...actual, revision: 4, image_studio_enabled: true })
  assert.equal(studio.activeCount.value, 0, 'reenabling never replays a generation')

  studio.draft.value.positivePrompt = 'forest'
  const imageRequest = hold('https://image.invalid/v1/chat/completions')
  const batch = await studio.submit()
  const pendingTask = studio.tasks.value.find(task => task.batchId === batch)
  const generateRequest = requests.findLast(request => request.url === 'https://image.invalid/v1/chat/completions')
  assert.equal(studio.activeCount.value, 1, 'the singleton runs without a mounted studio page')
  sources.at(-1).snapshot(actual = { ...actual, revision: 5, image_studio_enabled: false })
  assert.equal(studio.activeCount.value, 0)
  assert.equal(generateRequest.init.signal.aborted, true)
  imageRequest.resolve(response({ choices: [{ message: { content: '![image](https://resource.invalid/late.png)' } }] }))
  await flush()
  assert.equal(requests.some(request => request.url === 'https://resource.invalid/late.png'), false, 'late generation never starts a follow-up resource request')
  sources.at(-1).snapshot(actual = { ...actual, revision: 6, image_studio_enabled: true })
  assert.equal(studio.tasks.value.find(task => task.id === pendingTask.id).status, 'interrupted')
  assert.equal(requests.filter(request => request.url === 'https://image.invalid/v1/chat/completions').length, 1)

  const { GenerationRunner } = await server.ssrLoadModule('/src/features/image-studio/services/generationRunner.ts')
  const { createDefaultDraft } = await server.ssrLoadModule('/src/features/image-studio/utils/generationParameters.ts')
  const jobs = [], writes = []
  const runner = new GenerationRunner({
    saveTask(owner, task, image, signal) { writes.push({ owner, task, image, signal }); return Promise.resolve() },
    getImage: async () => new Blob(['saved']), deleteTasks: async () => {},
  }, (_request, _key, signal) => { const job = deferred(); jobs.push({ ...job, signal }); return job.promise })
  runner.setOwner(1)
  const draft = { ...createDefaultDraft(), positivePrompt: 'forest', count: 2 }
  runner.submit(draft, { baseUrl: 'https://image.invalid', apiKey: 'fixture' })
  await flush()
  runner.deactivate()
  assert.ok(jobs.every(job => job.signal.aborted))
  assert.ok(runner.tasks.every(task => task.status === 'interrupted'))
  assert.throws(() => runner.submit(draft, { baseUrl: 'https://image.invalid', apiKey: 'fixture' }), /暂停/)
  const result = { blob: new Blob(['original']), metadata: { mimeType: 'image/png', width: 1, height: 1, returnedSeed: null, parameters: {}, warnings: [] } }
  jobs[0].resolve(result); jobs[1].reject(new Error('late upstream failure'))
  await flush()
  assert.ok(runner.tasks.every(task => task.status === 'interrupted'))
  assert.ok(!writes.some(write => write.task.status === 'succeeded'))
  runner.activate()
  assert.equal(jobs.length, 2)
  runner.submit({ ...draft, count: 1 }, { baseUrl: 'https://image.invalid', apiKey: 'fixture' })
  jobs[2].resolve(result)
  await flush()
  const completed = runner.tasks.find(task => task.status === 'succeeded')
  runner.deactivate(); runner.activate()
  assert.equal(runner.tasks.find(task => task.id === completed.id).status, 'succeeded')
  assert.equal(await (await runner.getImage(completed.id)).text(), 'saved')

  const pendingWrites = [], completedJobs = []
  const unsavedRunner = new GenerationRunner({
    saveTask(_owner, task, _image, signal) {
      if (task.status !== 'succeeded') return Promise.resolve()
      const write = deferred(); pendingWrites.push({ ...write, signal }); return write.promise
    },
    getImage: async () => { throw new Error('The unsaved original must remain in memory') }, deleteTasks: async () => {},
  }, () => { const job = deferred(); completedJobs.push(job); return job.promise })
  unsavedRunner.setOwner(2)
  unsavedRunner.submit({ ...draft, count: 1 }, { baseUrl: 'https://image.invalid', apiKey: 'fixture' })
  await flush()
  completedJobs[0].resolve(result)
  await flush()
  const unsaved = unsavedRunner.tasks[0]
  unsavedRunner.deactivate()
  assert.equal(pendingWrites[0].signal.aborted, true)
  unsavedRunner.activate()
  // Simulate persistence ignoring cancellation: it still cannot publish saved
  // state or drop the only in-memory original after the feature epoch changed.
  pendingWrites[0].resolve()
  await flush()
  assert.equal(unsavedRunner.tasks[0].save.status, 'failed')
  assert.equal(await (await unsavedRunner.getImage(unsaved.id)).text(), 'original')

  const { ImageStudioStorage } = await server.ssrLoadModule('/src/features/image-studio/storage/imageStudioStorage.ts')
  const database = new ImageStudioStorage()
  const abortedWrite = new AbortController()
  abortedWrite.abort()
  await assert.rejects(database.saveTask(2, unsaved, result.blob, abortedWrite.signal))
  assert.equal((await database.listHistory(2)).tasks.length, 0)

  const liveGate = hold('/api/ai-radar')
  const radar = await mount('ai-radar/views/AiRadarView')
  const liveRequest = requests.findLast(request => request.url === '/api/ai-radar')
  assert.ok(liveRequest)
  assert.equal(timers.size, 1)
  sources.at(-1).snapshot(actual = { ...actual, revision: 7, ai_radar_enabled: false })
  assert.equal(liveRequest.init.signal.aborted, true)
  assert.equal(timers.size, 0)
  liveGate.resolve(response({ available: true, points: [{ model: 'late' }], source_updated_at: 'late' }))
  await flush()
  assert.equal(radar.state.live.value, null)
  sources.at(-1).snapshot(actual = { ...actual, revision: 8, ai_radar_enabled: true })
  await flush()
  assert.equal(timers.size, 1, 'reenabling arms exactly one timer')
  radar.app.unmount()
  assert.equal(timers.size, 0)

  sources.at(-1).snapshot(actual = { ...actual, revision: 9, image_studio_enabled: false, ai_radar_enabled: false })
  const cleanPanel = await mount('feature-center/components/FeatureSettingsPanel')
  const dirtyPanel = await mount('feature-center/components/FeatureSettingsPanel')
  assert.deepEqual(controls(cleanPanel.element, 'NSwitch').map(node => node.props.value), [false, false])
  controls(dirtyPanel.element, 'NSwitch')[0].props['onUpdate:value'](true)
  await flush()
  sources.at(-1).snapshot(actual = { ...actual, revision: 10, image_studio_enabled: true, ai_radar_enabled: true })
  await flush()
  assert.deepEqual(controls(cleanPanel.element, 'NSwitch').map(node => node.props.value), [true, true], 'an untouched settings panel follows another session through the real SSE binding')
  assert.equal(cleanPanel.state.saved.value.revision, 10)
  assert.equal(cleanPanel.state.dirty.value, false)
  assert.equal(cleanPanel.state.conflict.value, false)
  assert.equal(controls(cleanPanel.element, 'NButton').find(node => text(node) === '保存功能配置').props.disabled, true)
  assert.doesNotMatch(text(cleanPanel.element), /有未保存的草稿/)
  assert.deepEqual(controls(dirtyPanel.element, 'NSwitch').map(node => node.props.value), [true, false], 'an external snapshot retains genuine local edits')
  assert.equal(dirtyPanel.state.saved.value.revision, 10)
  assert.equal(dirtyPanel.state.conflict.value, true)
  assert.match(text(dirtyPanel.element), /您的草稿已保留/)
  assert.match(text(dirtyPanel.element), /有未保存的草稿/)
  assert.equal(controls(dirtyPanel.element, 'NButton').find(node => text(node) === '保存功能配置').props.disabled, true)
  const oldSettingsRead = hold('/api/settings/features')
  const reloading = cleanPanel.state.load()
  await flush()
  sources.at(-1).snapshot(actual = { ...actual, revision: 11, ai_radar_enabled: false })
  oldSettingsRead.resolve(response({ ...actual, revision: 10, ai_radar_enabled: true }))
  await reloading
  await flush()
  assert.equal(cleanPanel.state.saved.value.revision, 11, 'an older settings GET cannot roll back a newer streamed revision')
  assert.deepEqual(controls(cleanPanel.element, 'NSwitch').map(node => node.props.value), [true, false])
  controls(cleanPanel.element, 'NSwitch')[1].props['onUpdate:value'](true)
  const ownSaveReply = hold('/api/settings/features')
  const ownSave = cleanPanel.state.save()
  await flush()
  sources.at(-1).snapshot(actual = { ...actual, revision: 12, ai_radar_enabled: true })
  ownSaveReply.resolve(response(actual))
  await ownSave
  await flush()
  assert.equal(cleanPanel.state.conflict.value, false, 'the SSE notification of the current save is not an external draft conflict')
  assert.equal(cleanPanel.state.dirty.value, false)
  controls(cleanPanel.element, 'NSwitch')[0].props['onUpdate:value'](false)
  const overtakenSaveReply = hold('/api/settings/features')
  const overtakenSave = cleanPanel.state.save()
  await flush()
  const committed = { ...actual, revision: 13, image_studio_enabled: false }
  sources.at(-1).snapshot(committed)
  sources.at(-1).snapshot(actual = { ...committed, revision: 14, image_studio_enabled: true })
  overtakenSaveReply.resolve(response(committed))
  await overtakenSave
  await flush()
  assert.equal(cleanPanel.state.saved.value.revision, 14, 'a delayed PUT response cannot replace a newer authoritative update')
  assert.deepEqual(controls(cleanPanel.element, 'NSwitch').map(node => node.props.value), [true, true])
  assert.equal(cleanPanel.state.dirty.value, false)
  assert.match(cleanPanel.state.feedback.value, /之后又被其他会话更新/)
  cleanPanel.app.unmount()
  dirtyPanel.app.unmount()

  const panel = await mount('feature-center/components/FeatureSettingsPanel')
  panel.state.draft.image_studio_enabled = false
  const putGate = hold('/api/settings/features')
  const saving = panel.state.save()
  await flush()
  // This fixture simulates a committed PUT whose response was lost.
  actual = { ...actual, revision: 15, image_studio_enabled: false }
  putGate.reject(new TypeError('connection lost after commit'))
  await saving
  assert.equal(panel.state.saved.value.revision, 15)
  assert.match(panel.state.feedback.value, /重新读取确认/)
  panel.state.draft.ai_radar_enabled = false
  const conflictGate = hold('/api/settings/features')
  const conflictSave = panel.state.save()
  await flush()
  conflictGate.resolve(new Response(JSON.stringify({ detail: { code: 'conflict', message: '功能配置已被其他管理员修改，请刷新后重试' } }), { status: 409 }))
  await conflictSave
  assert.equal(panel.state.conflict.value, true)
  assert.equal(panel.state.draft.ai_radar_enabled, false, 'conflict retains the administrator draft')
  panel.app.unmount()

  const deleteSave = deferred(), deleteCommit = deferred()
  let deleteCalls = 0
  const deleteRunner = new GenerationRunner({
    saveTask(_owner, task) { return task.status === 'succeeded' ? deleteSave.promise : Promise.resolve() },
    getImage: async () => result.blob,
    deleteTasks() { deleteCalls++; return deleteCommit.promise },
  }, async () => result)
  deleteRunner.setOwner(3)
  deleteRunner.submit({ ...draft, count: 1 }, { baseUrl: 'https://image.invalid', apiKey: 'fixture' })
  await flush()
  const deletingId = deleteRunner.tasks[0].id
  const oldDelete = deleteRunner.deleteTasks([deletingId]).then(() => 'success', cause => cause.problem?.kind)
  deleteRunner.deactivate()
  deleteRunner.activate()
  const newDelete = deleteRunner.deleteTasks([deletingId])
  deleteSave.resolve()
  await flush()
  assert.equal(await oldDelete, 'cancelled')
  assert.equal(deleteCalls, 1, 'pausing releases obsolete deletion ownership so the resumed deletion reaches storage')
  assert.equal(await deleteRunner.retrySave(deletingId), false, 'the old finally cannot release the newer deletion token')
  await assert.rejects(deleteRunner.deleteTasks([deletingId]), cause => cause.problem?.kind === 'cancelled', 'a duplicate pending deletion must not report success')
  deleteCommit.resolve()
  await newDelete
  assert.equal(deleteRunner.tasks.length, 0)

  const originalFavoritesWrite = ImageStudioStorage.prototype.saveTagFavorites
  const originalPresetWrite = ImageStudioStorage.prototype.savePreset
  const originalPresetDelete = ImageStudioStorage.prototype.deletePreset
  const favorite = id => ({ id: `favorite-custom:${id}`, labelZh: id, prompt: id, custom: true })
  const favoriteIds = () => studio.tagFavorites.value.map(tag => tag.id)
  const setImageEnabled = value => sources.at(-1).snapshot(actual = { ...actual, revision: actual.revision + 1, image_studio_enabled: value })
  const a = favorite('A'), b = favorite('B'), c = favorite('C'), d = favorite('D')
  try {
    assert.equal(await studio.saveTagFavorites([a]), true)
    studio.draft.value.positivePrompt = 'keep this unsaved prompt'
    const dirtyConnection = { ...studio.connection.value, apiKey: 'keep-unsaved-key' }
    studio.connection.value = dirtyConnection
    const committedFavorite = deferred(), favoriteReply = deferred()
    ImageStudioStorage.prototype.saveTagFavorites = async function (owner, value) {
      await originalFavoritesWrite.call(this, owner, value)
      committedFavorite.resolve()
      await favoriteReply.promise
    }
    const favoriteSave = studio.saveTagFavorites([a, b])
    await committedFavorite.promise
    setImageEnabled(false)
    favoriteReply.resolve()
    assert.equal(await favoriteSave, false, 'an old feature epoch cannot report a successful UI action')
    assert.deepEqual(favoriteIds(), [a.id], 'paused business state does not publish a late write')
    setImageEnabled(true)
    await until(() => !studio.tagFavoritesSaving.value, 'committed favorites synchronize after reenable')
    assert.deepEqual(favoriteIds(), [a.id, b.id], 'reenabling reconciles a collection whose transaction committed before pause')
    ImageStudioStorage.prototype.saveTagFavorites = originalFavoritesWrite
    assert.equal(await studio.saveTagFavorites([...studio.tagFavorites.value, c]), true)
    assert.deepEqual((await database.getTagFavorites(user.id)).tags.map(tag => tag.id), [a.id, b.id, c.id], 'the next full-list write retains the favorite committed across pause')
    assert.equal(studio.draft.value.positivePrompt, 'keep this unsaved prompt')
    assert.deepEqual(studio.connection.value, dirtyConnection)

    const presetCommitted = deferred(), presetReply = deferred()
    ImageStudioStorage.prototype.savePreset = async function (owner, value) {
      await originalPresetWrite.call(this, owner, value)
      presetCommitted.resolve(value)
      await presetReply.promise
    }
    const presetSave = studio.savePreset('negative', 'across pause', 'original preset text', null)
    const committedPreset = await presetCommitted.promise
    setImageEnabled(false)
    setImageEnabled(true)
    assert.equal(studio.presetsSaving.value, true, 'reenabling before commit completion retains the collection write fence')
    assert.equal(await studio.deletePreset(committedPreset.id), false, 'a new preset write cannot overtake the pending transaction')
    presetReply.resolve()
    assert.equal(await presetSave, null)
    await until(() => !studio.presetsSaving.value, 'preset reconciliation')
    assert.ok(studio.presets.value.some(preset => preset.id === committedPreset.id))
    ImageStudioStorage.prototype.savePreset = originalPresetWrite
    const presetDeleted = deferred(), presetDeleteReply = deferred()
    ImageStudioStorage.prototype.deletePreset = async function (owner, id) {
      await originalPresetDelete.call(this, owner, id)
      presetDeleted.resolve()
      await presetDeleteReply.promise
    }
    const deletingPreset = studio.deletePreset(committedPreset.id)
    await presetDeleted.promise
    setImageEnabled(false)
    presetDeleteReply.resolve()
    assert.equal(await deletingPreset, false)
    setImageEnabled(true)
    await until(() => !studio.presetsSaving.value, 'deleted preset reconciliation')
    assert.ok(!studio.presets.value.some(preset => preset.id === committedPreset.id))
    ImageStudioStorage.prototype.deletePreset = originalPresetDelete

    const lateWriteStarted = deferred(), lateWriteRelease = deferred()
    ImageStudioStorage.prototype.saveTagFavorites = async function (owner, value) {
      if (owner === user.id) { lateWriteStarted.resolve(); await lateWriteRelease.promise }
      await originalFavoritesWrite.call(this, owner, value)
    }
    const oldAccountSave = studio.saveTagFavorites([...studio.tagFavorites.value, d])
    await lateWriteStarted.promise
    setCurrentUser({ ...user, id: 502 })
    sources.at(-1).snapshot(actual = { ...enabled })
    await until(() => !studio.localLoading.value, 'account B local data')
    assert.equal(await studio.saveTagFavorites([favorite('account-B')]), true, 'account A does not hold account B collection writes')
    setCurrentUser(user)
    sources.at(-1).snapshot(actual = { ...enabled })
    await until(() => !studio.localLoading.value, 'return to account A')
    studio.draft.value.positivePrompt = 'new account session unsaved prompt'
    studio.connection.value = { ...dirtyConnection, apiKey: 'new-session-unsaved-key' }
    assert.equal(studio.tagFavoritesSaving.value, true, 'returning to A retains the in-flight write fence from its previous account generation')
    assert.equal(await studio.saveTagFavorites([...studio.tagFavorites.value, favorite('must-not-overtake')]), false)
    lateWriteRelease.resolve()
    assert.equal(await oldAccountSave, false)
    await until(() => !studio.tagFavoritesSaving.value, 'returned account reconciles the old transaction')
    assert.deepEqual(favoriteIds(), [a.id, b.id, c.id, d.id])
    assert.equal(studio.draft.value.positivePrompt, 'new account session unsaved prompt')
    assert.equal(studio.connection.value.apiKey, 'new-session-unsaved-key')
    ImageStudioStorage.prototype.saveTagFavorites = originalFavoritesWrite
    assert.equal(await studio.saveTagFavorites([...studio.tagFavorites.value, favorite('E')]), true)
    assert.deepEqual((await database.getTagFavorites(user.id)).tags.map(tag => tag.id), [a.id, b.id, c.id, d.id, favorite('E').id])
  } finally {
    ImageStudioStorage.prototype.saveTagFavorites = originalFavoritesWrite
    ImageStudioStorage.prototype.savePreset = originalPresetWrite
    ImageStudioStorage.prototype.deletePreset = originalPresetDelete
  }

  const source = sources.at(-1)
  source.onerror()
  assert.equal(source.closed, true)
  assert.equal(features.useFeatureState().imageStudio.value, 'unknown')
  const count = sources.length
  await flush()
  assert.equal(sources.length, count, 'disconnection never starts an implicit retry loop')
  const reconnect = features.reconnectFeatureState()
  sources.at(-1).snapshot(actual)
  await reconnect
  const originalSetTimeout = globalThis.setTimeout
  let handshakeTimeout
  try {
    globalThis.setTimeout = (callback, milliseconds) => { assert.equal(milliseconds, 20_000); handshakeTimeout = callback; return 0 }
    const unconfirmed = features.reconnectFeatureState()
    handshakeTimeout()
    await assert.rejects(unconfirmed)
    assert.equal(features.useFeatureState().state.value.connecting, false)
    assert.match(features.useFeatureState().state.value.error, /初始快照/)
  } finally { globalThis.setTimeout = originalSetTimeout }
  setCurrentUser(null)
  assert.equal(sources.at(-1).closed, true)
  const { setLanguage, localizedApiErrorMessage } = await server.ssrLoadModule('/src/shared/i18n/index.ts')
  setLanguage('en')
  assert.equal(localizedApiErrorMessage('feature_disabled', '该功能已由管理员关闭'), 'This feature has been disabled by an administrator')
  assert.equal(localizedApiErrorMessage('conflict', '功能配置已被其他管理员修改，请刷新后重试'), 'Feature settings were changed by another administrator. Reload and try again.')
  console.log('Feature center lifecycle smoke passed: real control/session events, model abort, runner cancellation, radar timer and settings recovery.')
} finally {
  await server.close()
  delete globalThis.__featureLifecycleSmoke
  for (const [key, descriptor] of globals) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key] }
}
