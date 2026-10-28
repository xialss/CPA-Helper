import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { setImmediate as nextTurn } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { createRenderer, nextTick } from 'vue'
import { createServer, transformWithEsbuild } from 'vite'

const root = fileURLToPath(new URL('..', import.meta.url))
const bridge = {
  errors: [],
  successes: [],
  loginCalls: [],
  loginResolvers: [],
  route: { query: {} },
  navigations: [],
  language: 'zh',
}
globalThis.__loginViewSmoke = bridge
globalThis.localStorage = { getItem: () => 'zh', setItem() {} }
globalThis.document = { documentElement: { lang: 'zh-CN' } }

const server = await createServer({
  root,
  logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'vue-router'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'login-view-smoke',
    enforce: 'pre',
    resolveId(id) {
      const normalized = id.replaceAll('\\', '/')
      if (id === '/__login-view.ts') return '\0login-view-smoke:view'
      if (id === 'naive-ui') return '\0login-view-smoke:naive-ui'
      if (id === 'vue-router') return '\0login-view-smoke:vue-router'
      if (normalized.endsWith('/features/auth/api/authApi') || normalized.endsWith('/features/auth/api/authApi.ts')) return '\0login-view-smoke:auth-api'
      if (normalized.endsWith('/shared/i18n') || normalized.endsWith('/shared/i18n/index.ts')) return '\0login-view-smoke:i18n'
      if (normalized.endsWith('/shared/utils/assets') || normalized.endsWith('/shared/utils/assets.ts')) return '\0login-view-smoke:assets'
    },
    async load(id) {
      if (id === '\0login-view-smoke:view') {
        const filename = `${root}/src/features/auth/views/LoginView.vue`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: 'login-view', genDefaultAs: '__view' })
        const template = compileTemplate({
          id: 'login-view',
          filename,
          source: descriptor.template.content,
          compilerOptions: { bindingMetadata: script.bindings },
        })
        assert.deepEqual(template.errors, [])
        return (await transformWithEsbuild(
          `${script.content}\n${template.code}\n__view.render = render; export default __view`,
          filename,
          { loader: 'ts' },
        )).code
      }
      if (id === '\0login-view-smoke:naive-ui') return `
        import { h } from 'vue';
        const passthrough = name => ({ inheritAttrs: false, setup(_, { attrs, slots }) { return () => h('stub-' + name, attrs, slots.default?.()); } });
        export const NAlert = passthrough('NAlert');
        export const NButton = passthrough('NButton');
        export const NCard = passthrough('NCard');
        export const NForm = passthrough('NForm');
        export const NFormItem = passthrough('NFormItem');
        export const NInput = passthrough('NInput');
        export const useMessage = () => ({
          error: value => globalThis.__loginViewSmoke.errors.push(value),
          success: value => globalThis.__loginViewSmoke.successes.push(value),
        });`
      if (id === '\0login-view-smoke:vue-router') return `
        export const useRoute = () => globalThis.__loginViewSmoke.route;
        export const useRouter = () => ({ push: value => { globalThis.__loginViewSmoke.navigations.push(value); return Promise.resolve(); } });`
      if (id === '\0login-view-smoke:auth-api') return `
        export const getSetupState = async () => ({ setup_required: false });
        export const login = payload => {
          const current = globalThis.__loginViewSmoke;
          current.loginCalls.push(payload);
          return new Promise(resolve => current.loginResolvers.push(resolve));
        };
        export const setupFirstAdmin = async () => { throw new Error('Unexpected first-admin setup'); };`
      if (id === '\0login-view-smoke:i18n') return `
        export const useI18n = () => ({
          t: (zh, en) => globalThis.__loginViewSmoke.language === 'zh' ? zh : en,
          errorText: error => error instanceof Error ? error.message : String(error),
        });`
      if (id === '\0login-view-smoke:assets') return `export const logoUrl = '/logo.png'`
    },
  }],
})

const renderer = createRenderer({
  createElement: (type) => ({ type, props: {}, children: [] }),
  createText: (text) => ({ type: 'text', text }),
  createComment: () => ({ type: 'comment' }),
  insert(node, parent, anchor = null) {
    if (node.parent) {
      const oldIndex = node.parent.children.indexOf(node)
      if (oldIndex >= 0) node.parent.children.splice(oldIndex, 1)
    }
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index >= 0 ? index : parent.children.length, 0, node)
    node.parent = parent
  },
  remove(node) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
  },
  setText(node, text) { node.text = text },
  setElementText(node, text) { node.text = text; node.children = [] },
  patchProp(node, key, _previous, value) { node.props[key] = value },
  parentNode: (node) => node.parent,
  nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
})
const settle = async () => { await nextTick(); await nextTurn(); await nextTick() }
const nodes = (node, type) => [
  ...(node.type === type ? [node] : []),
  ...(node.children ?? []).flatMap((child) => nodes(child, type)),
]
let app

try {
  const { default: component } = await server.ssrLoadModule('/__login-view.ts')
  const host = { children: [] }
  app = renderer.createApp(component)
  app.mount(host)
  await settle()

  const form = nodes(host, 'stub-NForm')[0]
  const inputs = nodes(host, 'stub-NInput')
  const username = inputs.find((input) => input.props.autocomplete === 'username')
  const password = inputs.find((input) => input.props.autocomplete === 'current-password')
  assert.ok(form?.props['onSubmit'], 'The form owns the only submit handler')
  assert.ok(username && password, 'Username and password are distinct input fields')
  assert.equal(password.props.onKeyup, undefined, 'Enter is handled through native form submission only')
  username.props['onUpdate:value']('operator@example.test')
  password.props['onUpdate:value']('correct-horse-battery-staple')

  const submitEvent = { prevented: false, preventDefault() { this.prevented = true } }
  form.props['onSubmit'](submitEvent)
  form.props['onSubmit']({ preventDefault() {} })
  await settle()
  assert.equal(submitEvent.prevented, true, 'The form prevents browser navigation')
  assert.deepEqual(bridge.loginCalls, [{ username: 'operator@example.test', password: 'correct-horse-battery-staple' }], 'Duplicate submit events do not dispatch a second login request')

  bridge.loginResolvers[0]({ id: 7, username: 'operator@example.test', is_admin: false, is_super_admin: false, must_change_password: false })
  await settle()
  assert.deepEqual(bridge.successes, ['登录成功'], 'A completed login shows one success toast')
  assert.deepEqual(bridge.navigations, ['/account/usage'], 'Successful login navigates once to the account home')
  assert.deepEqual(bridge.errors, [])
  console.log('Login view smoke passed: distinct credentials, one form submit, duplicate-submit guard and single success notification')
} finally {
  app?.unmount()
  await server.close()
  delete globalThis.__loginViewSmoke
  delete globalThis.localStorage
  delete globalThis.document
}
