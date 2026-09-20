import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { compileScript, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { Comment, createRenderer, h, nextTick, ref } from 'vue'

globalThis.localStorage = { getItem: () => 'en', setItem() {} }
globalThis.document = { documentElement: { lang: 'en' } }
const requests = []
globalThis.fetch = (url, init) => new Promise((resolve, reject) => requests.push({ url, init, resolve, reject }))
const revoked = []
const originalCreate = URL.createObjectURL
const originalRevoke = URL.revokeObjectURL
URL.createObjectURL = () => 'blob:audit-smoke'
URL.revokeObjectURL = (url) => revoked.push(url)
const server = await createServer({
  root: fileURLToPath(new URL('..', import.meta.url)), logLevel: 'error',
  ssr: { noExternal: ['naive-ui'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'audit-panel-render-smoke',
    enforce: 'pre',
    async load(id) {
      if (id === '\0audit-panel:naive-ui') return "export const NButton = { render: () => null }, NCheckbox = { render: () => null }"
      if (!id.endsWith('/src/features/usage/components/__audit-panel-smoke__.ts')) return
      const filename = id.replace('__audit-panel-smoke__.ts', 'UsageModelAuditPanel.vue')
      const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
      assert.deepEqual(errors, [])
      const script = compileScript(descriptor, { id: 'audit-panel-smoke', inlineTemplate: true })
      return (await transformWithEsbuild(script.content, filename, { loader: 'ts' })).code
    },
    resolveId(id) {
      if (id === 'naive-ui') return '\0audit-panel:naive-ui'
      if (id === '/src/features/usage/components/__audit-panel-smoke__.ts') return fileURLToPath(new URL('../src/features/usage/components/__audit-panel-smoke__.ts', import.meta.url))
    },
  }],
})
const renderer = createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  insert() {}, remove() {}, setText() {}, setElementText() {}, patchProp() {}, parentNode: () => null, nextSibling: () => null,
})
const user = { id: 1, username: 'owner', is_admin: true, is_super_admin: true, must_change_password: false }
const matched = { audit: null, source_status: 'matched', acknowledgement_token: null }
const reply = (request, body, status = 200) => request.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
let app
let panelApp
try {
  const { useUsageModelAudit } = await server.ssrLoadModule('/src/features/usage/useUsageModelAudit.ts')
  const { setCurrentUser } = await server.ssrLoadModule('/src/features/auth/state/currentUser.ts')
  const recordId = ref(1)
  let state
  setCurrentUser(user)
  app = renderer.createApp({ setup() { state = useUsageModelAudit(recordId); return () => h('div') } })
  app.mount({})
  assert.equal(requests.length, 0, 'Opening details never fetches audit or logs')
  let pending = state.run('audit')
  await pending
  assert.equal(requests.length, 0, 'Remote audit requires loaded source context')
  pending = state.run('cache')
  reply(requests.at(-1), { ...matched, source_status: 'unknown', acknowledgement_token: 'source-record-token' })
  await pending
  await state.run('preview')
  assert.equal(requests.length, 1, 'Unknown origin requires explicit acknowledgement')
  state.acknowledged.value = true
  pending = state.run('audit')
  assert.equal(JSON.parse(requests.at(-1).init.body).acknowledge_source, 'source-record-token')
  recordId.value = 2
  reply(requests.at(-1), { ...matched, audit: { log_response_model: 'old secret' } })
  await pending
  assert.equal(state.context.value, null, 'Old record success cannot publish restricted data')
  pending = state.run('cache')
  const old = requests.at(-1)
  recordId.value = 1
  const fresh = state.run('cache')
  old.reject(new Error('stale failure'))
  await pending
  assert.equal(state.error.value, null, 'Stale rejection must not publish an error')
  assert.equal(state.busy.value, true, 'Stale finally must not clear current loading')
  reply(requests.at(-1), matched)
  await fresh
  pending = state.run('preview')
  setCurrentUser({ ...user, is_super_admin: false })
  reply(requests.at(-1), { text: 'secret response', truncated: false, total_bytes: 15 })
  await pending
  assert.equal(state.preview.value, null, 'Role revocation clears and rejects sensitive results')
  const count = requests.length
  await state.run('cache')
  await state.run('download')
  assert.equal(requests.length, count, 'Ordinary administrator cannot request logs or audit cache')
  assert.equal(state.allowed.value, false, 'Ordinary administrator cannot display the audit panel')
  setCurrentUser({ ...user, is_admin: false, is_super_admin: false })
  assert.equal(state.allowed.value, false, 'Ordinary user cannot display the audit panel')
  for (const action of ['cache', 'audit', 'refresh', 'preview', 'download']) await state.run(action)
  assert.equal(requests.length, count, 'Ordinary user cannot initiate privileged operations')
  setCurrentUser(user)
  pending = state.run('cache'); reply(requests.at(-1), matched); await pending
  pending = state.run('download')
  assert.equal(requests.at(-1).init.cache, 'no-store')
  requests.at(-1).resolve(new Response('full log'))
  await pending
  assert.equal(state.downloadURL.value, 'blob:audit-smoke')
  pending = state.run('download')
  reply(requests.at(-1), { detail: { code: 'forbidden', message: 'Forbidden' } }, 403)
  await pending
  assert.equal(state.error.value.status, 403, 'Blob errors preserve typed HTTP status')
  assert.equal(state.context.value, null, 'Permission errors erase cached audit data')
  assert.equal(state.downloadURL.value, null, 'Permission errors revoke prior downloads')
  pending = state.run('cache'); reply(requests.at(-1), matched); await pending
  pending = state.run('download'); requests.at(-1).resolve(new Response('full log')); await pending
  app.unmount(); app = null
  await nextTick()
  assert.deepEqual(revoked, ['blob:audit-smoke', 'blob:audit-smoke'], 'Errors and unmount revoke sensitive download URLs')
  const { default: AuditPanel } = await server.ssrLoadModule('/src/features/usage/components/__audit-panel-smoke__.ts')
  for (const isAdmin of [false, true]) {
    setCurrentUser({ ...user, is_admin: isAdmin, is_super_admin: false })
    panelApp = renderer.createApp(AuditPanel, { recordId: 1, queueModel: 'reported' })
    const panel = panelApp.mount({})
    assert.equal(panel.$.subTree.type, Comment, `${isAdmin ? 'Ordinary admin' : 'Ordinary user'} renders no audit section`)
    panelApp.unmount(); panelApp = null
  }
  setCurrentUser(user)
  panelApp = renderer.createApp(AuditPanel, { recordId: 1, queueModel: 'reported' })
  const panel = panelApp.mount({})
  assert.equal(panel.$.subTree.type, 'section', 'Super administrator renders the audit section')
  assert.equal(panel.$.subTree.children[0].children, 'Deep audit', 'Title omits the role suffix')
  setCurrentUser({ ...user, is_super_admin: false })
  await nextTick()
  assert.equal(panel.$.subTree.type, Comment, 'Downgrading hides the already mounted audit panel')
  console.log('Usage model audit smoke passed: lazy access, source acknowledgement, stale success/error, role revocation, download cleanup.')
} finally {
  panelApp?.unmount()
  app?.unmount()
  URL.createObjectURL = originalCreate
  URL.revokeObjectURL = originalRevoke
  await server.close()
}
