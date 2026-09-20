import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, nextTick } from 'vue'

const root = fileURLToPath(new URL('..', import.meta.url))
const env = { inputs: [], forms: [], pushes: [], messages: [], requests: [] }
globalThis.__credentialsSmoke = env
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN' } }
const user = { id: 2, username: 'newbie', is_admin: false, is_super_admin: false, must_change_password: true }
globalThis.fetch = async (url, init) => {
  if (url === '/api/auth/me') return Response.json(user)
  assert.equal(url, '/api/auth/change-credentials')
  return new Promise(resolve => env.requests.push({ body: JSON.parse(init.body), resolve }))
}
const server = await createServer({
  root, logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'vue-router'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'credentials-smoke', enforce: 'pre',
    resolveId(id) {
      if (id === '/__credentials-smoke.ts') return '\0credentials-view'
      if (id === 'naive-ui' || id === 'vue-router') return `\0credentials:${id}`
    },
    async load(id) {
      if (id === '\0credentials-view') {
        const filename = `${root}/src/features/auth/views/ChangeCredentialsView.vue`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: 'credentials', genDefaultAs: '__view' })
        const template = compileTemplate({ id: 'credentials', filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        return (await transformWithEsbuild(`${script.content}\n${template.code}\n__view.render = render; export default __view`, filename, { loader: 'ts' })).code
      }
      if (id === '\0credentials:vue-router') return 'export const useRouter = () => ({ push: async path => globalThis.__credentialsSmoke.pushes.push(path) })'
      if (id === '\0credentials:naive-ui') return `
        import { h } from 'vue';
        const wrapper = { setup(_, { slots }) { return () => h('div', slots.default?.()) } };
        export const NCard = wrapper, NAlert = wrapper, NFormItem = wrapper, NButton = wrapper;
        export const useMessage = () => ({ success: message => globalThis.__credentialsSmoke.messages.push(message) });
        export const NForm = { setup(_, { attrs, slots }) { globalThis.__credentialsSmoke.forms.push(attrs); return () => h('form', slots.default?.()) } };
        export const NInput = { props: ['value', 'autocomplete', 'disabled'], emits: ['update:value'], setup(props, { emit, attrs }) { globalThis.__credentialsSmoke.inputs.push({ props, emit, attrs }); return () => h('input') } };
      `
    },
  }],
})
const renderer = createRenderer({
  createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
  insert() {}, remove() {}, setText() {}, setElementText() {}, patchProp() {}, parentNode: () => null, nextSibling: () => null,
})
const settle = async () => { await nextTick(); await nextTurn(); await nextTick() }
let app
try {
  const { default: component } = await server.ssrLoadModule('/__credentials-smoke.ts')
  let state
  app = renderer.createApp({ ...component, setup(props, context) { state = component.setup(props, context); return state } })
  app.mount({})
  await settle()
  assert.deepEqual(env.inputs.map(input => input.props.autocomplete), ['username', 'new-password', 'new-password'])
  assert.equal(env.inputs[0].props.value, 'newbie')
  const fill = async (index, value) => { env.inputs[index].emit('update:value', value); await nextTick() }
  const submit = () => env.forms[0].onSubmit({ preventDefault() {} })
  await submit()
  assert.match(state.errorMessage.value, /请输入新密码/)
  assert.equal(env.requests.length, 0)
  await fill(1, 'new-password')
  await fill(2, 'different-password')
  await submit()
  assert.match(state.errorMessage.value, /不一致/)
  assert.equal(env.requests.length, 0)
  await fill(2, 'new-password')
  const pending = submit()
  await submit()
  assert.equal(env.requests.length, 1, 'Only one in-flight password update is allowed')
  assert.deepEqual(env.requests[0].body, { password: 'new-password' })
  assert.ok(env.inputs.every(input => !input.attrs.onKeyup), 'Enter is handled only by form submission')
  env.requests[0].resolve(Response.json({ ...user, must_change_password: false }))
  await pending
  assert.deepEqual(env.pushes, ['/account/usage'])
  assert.equal(state.isLoading.value, false)
  const { useCurrentUser } = await server.ssrLoadModule('/src/features/auth/state/currentUser.ts')
  assert.equal(useCurrentUser().currentUser.value.must_change_password, false)
  console.log('change-credentials smoke passed: field order, validation, payload, duplicate submission, successful redirect')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.__credentialsSmoke
}
