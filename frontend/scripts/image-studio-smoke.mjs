import assert from 'node:assert/strict'
import { Buffer } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import process from 'node:process'
import { setImmediate } from 'node:timers'
import { fileURLToPath } from 'node:url'
import { deflateSync } from 'node:zlib'

import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import cssRendererSsr from '@css-render/vue3-ssr'
import 'fake-indexeddb/auto'
import naiveUI from 'naive-ui'
import { createServer, transformWithEsbuild } from 'vite'
import { createRenderer, nextTick, reactive, ref, watch } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

const root = fileURLToPath(new URL('..', import.meta.url))
const originalBrowserWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
// Installed slider binders remove browser listeners on unmount even with tooltips disabled.
Object.defineProperty(globalThis, 'window', { configurable: true, value: new EventTarget() })
const bridge = { controls: new Set(), messages: [], dialogs: [], slider: naiveUI.NSlider }
globalThis.__imageStudioSmoke = bridge
const virtualPrefix = '\0image-studio-smoke:'
const uiNames = ['NAlert', 'NButton', 'NCollapse', 'NCollapseItem', 'NForm', 'NFormItem', 'NIcon', 'NInput', 'NInputNumber', 'NSelect', 'NSlider', 'NSwitch', 'NTag', 'NDrawer', 'NDrawerContent', 'NModal', 'NSpin', 'NEmpty', 'NLayout', 'NLayoutContent', 'NLayoutHeader', 'NLayoutSider', 'NMenu', 'NTooltip']
const iconNames = ['ChevronDown', 'ChevronUp', 'RefreshCw', 'Save', 'Sparkles', 'Copy', 'Download', 'Image', 'RotateCcw', 'Trash2', 'Images', 'Settings2', 'SlidersHorizontal', 'Activity', 'BarChart3', 'Cpu', 'DollarSign', 'Github', 'Gauge', 'KeyRound', 'Languages', 'List', 'ListChecks', 'LogOut', 'Menu', 'Monitor', 'Moon', 'Network', 'Settings', 'Shield', 'Sun', 'UserRound', 'Users']

const server = await createServer({
  root,
  logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'lucide-vue-next', 'vue-router'] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'image-studio-smoke',
    enforce: 'pre',
    resolveId(id, importer) {
      if (id === 'naive-ui' || id === 'lucide-vue-next' || id === 'vue-router') return `\0studio-ui:${id}`
      if (/\/shared\/composables\/useThemePreference(?:\.ts)?$/.test(id.replaceAll('\\', '/'))) return '\0studio-ui:theme'
      const parent = importer?.startsWith(virtualPrefix) ? importer.slice(virtualPrefix.length) : importer
      let filename
      if (id.startsWith('/src/features/image-studio/') || id === '/src/app/layout/AppShell.vue') filename = path.join(root, id.slice(1))
      else if (id.startsWith('@/features/image-studio/')) filename = path.join(root, 'src', id.slice(2))
      else if (id.startsWith('.') && parent) filename = path.resolve(path.dirname(parent), id)
      else filename = id
      const normalized = filename.replaceAll('\\', '/')
      if ((normalized.includes('/features/image-studio/') || normalized.endsWith('/app/layout/AppShell.vue')) && filename.endsWith('.vue')) {
        return `${virtualPrefix}${filename.slice(0, -4)}.ts`
      }
      if (importer?.startsWith(virtualPrefix) && id.startsWith('.')) return this.resolve(id, parent, { skipSelf: true })
    },
    async load(id) {
      if (id.startsWith(virtualPrefix)) {
        const filename = `${id.slice(virtualPrefix.length, -3)}.vue`
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: filename, genDefaultAs: '__studioView' })
        const template = compileTemplate({ id: filename, filename, source: descriptor.template.content, compilerOptions: { bindingMetadata: script.bindings } })
        assert.deepEqual(template.errors, [])
        return (await transformWithEsbuild(`${script.content}\n${template.code}\n__studioView.render = render; export default __studioView`, filename, { loader: 'ts' })).code
      }
      if (id === '\0studio-ui:lucide-vue-next') return iconNames.map((name) => `export const ${name} = { render: () => null };`).join('\n')
      if (id === '\0studio-ui:vue-router') return `
        export const useRoute = () => globalThis.__imageStudioSmoke.shell.route;
        export const useRouter = () => globalThis.__imageStudioSmoke.shell.router;
        export const isNavigationFailure = () => false;
        export const NavigationFailureType = { cancelled: 'cancelled' };
      `
      if (id === '\0studio-ui:theme') return `
        import { ref } from 'vue';
        export const useThemePreference = () => ({ isDark: ref(false), preference: ref('system'), setThemePreference() {}, toggleTheme() {} });
      `
      if (id === '\0studio-ui:naive-ui') return `
        import { h, getCurrentInstance, onBeforeUnmount } from 'vue';
        const bridge = globalThis.__imageStudioSmoke;
        function text(nodes) {
          if (Array.isArray(nodes)) return nodes.map(text).join('');
          if (typeof nodes === 'string' || typeof nodes === 'number') return String(nodes);
          return nodes && typeof nodes === 'object' ? text(nodes.children) : '';
        }
        function control(name) {
          return { name, inheritAttrs: false,
            props: { value: null, options: Array, show: Boolean, disabled: Boolean, loading: Boolean, label: String, labelProps: Object, inputProps: Object, title: String, type: String, attrType: String, filterable: Boolean, tag: Boolean, min: Number, max: Number, step: Number },
            emits: ['update:value', 'update:show', 'click', 'submit'],
            setup(props, { attrs, slots, emit }) {
              const entry = { name, props, attrs, emit, instance: getCurrentInstance(), text: '' };
              bridge.controls.add(entry);
              onBeforeUnmount(() => bridge.controls.delete(entry));
              return () => {
                if ((name === 'NModal' || name === 'NDrawer') && !props.show) return null;
                if (name === 'NSlider') return h(bridge.slider, {
                  ...attrs, value: props.value, min: props.min, max: props.max, step: props.step,
                  disabled: props.disabled, tooltip: false,
                  'onUpdate:value': (value) => emit('update:value', value)
                });
                const children = slots.default?.() ?? [];
                entry.text = text(children).trim();
                const tag = name === 'NButton' ? 'button' : name === 'NForm' ? 'form' : 'div';
                return h(tag, { ...attrs, 'data-control': name, disabled: props.disabled }, children);
              };
            }
          };
        }
        ${uiNames.map((name) => `export const ${name} = control('${name}');`).join('\n')}
        export const useMessage = () => ({ success: (text) => bridge.messages.push({ type: 'success', text }), error: (text) => bridge.messages.push({ type: 'error', text }) });
        export const useDialog = () => ({ warning: (options) => { bridge.dialogs.push(options); return {}; } });
      `
    },
  }],
})

// SFCs and installed sliders run against Vue's renderer; other UI, pixels and external IO are substituted.
const renderer = createRenderer({
  insert(node, parent, anchor) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    node.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
  },
  remove(node) { node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  createElement: (tag) => ({
    tag, children: [], scrollIntoView() {},
    getAttribute(name) { return this[name] ?? null },
    setAttribute(name, value) { this[name] = String(value) },
    querySelector(selector) {
      const match = /^\[([\w-]+)="([^"]+)"\]$/.exec(selector)
      assert.ok(match, `Unsupported test-host selector: ${selector}`)
      return this.children.flatMap((node) => findNodes(node, (item) => item[match[1]] === match[2]))[0] ?? null
    },
  }),
  createText: (text) => ({ text }),
  createComment: (text) => ({ text }),
  setText(node, text) { node.text = text },
  setElementText(node, text) { node.text = text; node.children = [] },
  parentNode: (node) => node.parent,
  nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  patchProp(node, key, _previous, value) { node[key] = value },
})

function uiControl(name, matches = () => true) {
  const control = [...bridge.controls].find((item) => item.name === name && matches(item))
  assert.ok(control, `Missing ${name} control; rendered labels: ${[...bridge.controls].filter((item) => item.name === name).map((item) => item.text).join(' | ')}`)
  return control
}

function parentProp(control, key) {
  let instance = control.instance.parent
  while (instance) {
    if (instance.props[key] !== undefined) return instance.props[key]
    instance = instance.parent
  }
}

function inputControl(id) {
  return [...bridge.controls].find((item) => item.props.inputProps?.id === id)
}

function nodeText(node) {
  return `${node.text ?? ''}${(node.children ?? []).map(nodeText).join('')}`
}

function findNodes(node, matches) {
  return [...(matches(node) ? [node] : []), ...(node.children ?? []).flatMap((child) => findNodes(child, matches))]
}

function until(predicate, label) {
  if (predicate()) return Promise.resolve()
  return new Promise((resolve, reject) => {
    const stop = watch(predicate, (ready) => {
      if (!ready) return
      clearTimeout(timer)
      stop()
      resolve()
    }, { flush: 'sync' })
    const timer = setTimeout(() => { stop(); reject(new Error(`Timed out: ${label}`)) }, 5000)
  })
}

const originalFetch = globalThis.fetch
const originalBitmap = globalThis.createImageBitmap
let bitmapClosed = 0
// Browser pixel decoding is a boundary here; PNG parsing below uses the real module and valid bytes.
globalThis.createImageBitmap = async () => ({ width: 1, height: 1, close: () => { bitmapClosed++ } })

function deferred() {
  let resolve
  let reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function waitFor(runner, predicate, label) {
  if (predicate()) return Promise.resolve()
  return new Promise((resolve, reject) => {
    const stop = runner.subscribe(() => {
      if (!predicate()) return
      clearTimeout(timer)
      stop()
      resolve()
    })
    const timer = setTimeout(() => {
      stop()
      reject(new Error(`Timed out: ${label}`))
    }, 5000)
  })
}

// Fixture construction is independent of the production PNG reader.
function pngChunk(type, content) {
  const data = Buffer.concat([Buffer.from(type), content])
  let crc = 0xffffffff
  for (const byte of data) {
    crc ^= byte
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0)
  }
  const header = Buffer.alloc(4)
  const footer = Buffer.alloc(4)
  header.writeUInt32BE(content.length)
  footer.writeUInt32BE((crc ^ 0xffffffff) >>> 0)
  return Buffer.concat([header, data, footer])
}

function pngBytes(seed = 123, options = {}) {
  const header = Buffer.alloc(13)
  header.writeUInt32BE(1, 0)
  header.writeUInt32BE(1, 4)
  header[8] = 8
  header[9] = 6
  const chunks = [
    pngChunk('IHDR', header),
    pngChunk('tEXt', Buffer.from('Software\0NovelAI')),
    pngChunk('tEXt', Buffer.from('Source\0NovelAI test fixture')),
    pngChunk('tEXt', Buffer.from('Description\0seed:999 is descriptive text, not generation evidence')),
  ]
  if (options.comment !== false) {
    const comment = options.comment ?? JSON.stringify({ seed, scale: 5.5, steps: 28, sampler: 'k_euler_ancestral', uc: 'lowres,blurry', prompt: 'apple\nwatercolor' })
    if (options.compression === 'zTXt') chunks.push(pngChunk('zTXt', Buffer.concat([Buffer.from('Comment\0\0'), deflateSync(comment)])))
    else if (options.compression === 'iTXt') chunks.push(pngChunk('iTXt', Buffer.concat([Buffer.from('Comment\0\x01\0\0\0'), deflateSync(comment)])))
    else chunks.push(pngChunk('tEXt', Buffer.from(`Comment\0${comment}`)))
  }
  for (const entry of options.compressedTexts ?? []) {
    const keyword = entry.keyword ?? 'Extra'
    const text = entry.text ?? ''
    if (entry.type === 'iTXt') {
      chunks.push(pngChunk('iTXt', Buffer.concat([Buffer.from(`${keyword}\0\x01\0\0\0`), deflateSync(text)])))
    } else {
      chunks.push(pngChunk('zTXt', Buffer.concat([Buffer.from(`${keyword}\0\0`), deflateSync(text)])))
    }
  }
  chunks.push(pngChunk('IDAT', deflateSync(Buffer.from([0, 255, 0, 0, 255]))), pngChunk('IEND', Buffer.alloc(0)))
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), ...chunks])
}

function imageResult(seed = '123') {
  return {
    blob: new Blob([pngBytes(seed)], { type: 'image/png' }),
    metadata: { mimeType: 'image/png', width: 1, height: 1, returnedSeed: seed, parameters: {}, warnings: [] },
  }
}

const asDataUrl = (bytes) => `data:image/png;base64,${bytes.toString('base64')}`
const chatResponse = (content) => new Response(JSON.stringify({ choices: [{ message: { content }, finish_reason: 'stop' }] }), { headers: { 'Content-Type': 'application/json' } })
let passed = 0
async function test(name, action) {
  await action()
  passed++
  process.stdout.write(`PASS ${name}\n`)
}

try {
  const parameters = await server.ssrLoadModule('/src/features/image-studio/utils/generationParameters.ts')
  const { appendPromptTags, promptTagCategories } = await server.ssrLoadModule('/src/features/image-studio/utils/promptTags.ts')
  const api = await server.ssrLoadModule('/src/features/image-studio/api/imageStudioApi.ts')
  const { readPngMetadata } = await server.ssrLoadModule('/src/features/image-studio/utils/pngMetadata.ts')
  const { ImageStudioStorage } = await server.ssrLoadModule('/src/features/image-studio/storage/imageStudioStorage.ts')
  const { GenerationRunner, storedTask } = await server.ssrLoadModule('/src/features/image-studio/services/generationRunner.ts')
  const { ImageStudioError } = await server.ssrLoadModule('/src/features/image-studio/utils/studioErrors.ts')
  const storage = new ImageStudioStorage()
  const connection = { baseUrl: 'https://images.example.invalid/custom/v1/', apiKey: 'fixture-secret-not-a-real-key' }
  const draft = () => ({ ...parameters.createDefaultDraft(), positivePrompt: 'apple\nwatercolor', artistPrompt: '1.15::art nouveau, historic painter::', negativePrompt: 'lowres,blurry\ntext,watermark', cfg: 5.5 })
  const requestFor = (value = draft()) => parameters.createGenerationRequest(parameters.validateDraft(value), api.upstreamApiUrl(connection.baseUrl, 'chat/completions'))
  const hasKind = (kind) => (error) => error instanceof ImageStudioError && error.problem.kind === kind

  await test('URL normalization, two model fields, resolutions, decimal CFG and exact seeds', async () => {
    assert.equal(parameters.createDefaultDraft().model, 'nai-diffusion-4-5-curated')
    assert.equal(parameters.createDefaultDraft().generationModel, '')
    assert.equal(parameters.createDefaultDraft().cfg, 5)
    assert.equal(parameters.createDefaultDraft().steps, 28)
    assert.deepEqual([parameters.createDefaultDraft().width, parameters.createDefaultDraft().height], [832, 1216])
    assert.deepEqual(parameters.imageResolutions.map(({ width, height }) => [width, height]), [[1216, 832], [832, 1216], [1024, 1024]])
    for (const base of ['https://images.example.invalid', 'https://images.example.invalid/', 'https://images.example.invalid/v1', 'https://images.example.invalid/v1/']) {
      assert.equal(api.upstreamApiUrl(base, 'chat/completions'), 'https://images.example.invalid/v1/chat/completions')
    }
    assert.equal(api.upstreamApiUrl(connection.baseUrl, 'models'), 'https://images.example.invalid/custom/v1/models')
    assert.throws(() => api.upstreamApiUrl('javascript:alert(1)', 'models'), hasKind('validation'))
    const defaultRequest = requestFor()
    assert.equal(defaultRequest.model, 'nai-diffusion-4-5-curated')
    assert.equal(defaultRequest.generationModel, 'nai-diffusion-4-5-curated')
    assert.match(defaultRequest.content, /Parameter\{model:nai-diffusion-4-5-curated,/)
    const input = { ...draft(), model: 'nai-diffusion-4.5-full', generationModel: 'nai-diffusion-5-full', seed: '900719925474099312345' }
    for (const size of parameters.imageResolutions) {
      const request = requestFor({ ...input, ...size })
      assert.equal(request.model, 'nai-diffusion-4.5-full')
      assert.equal(request.generationModel, 'nai-diffusion-5-full')
      assert.match(request.content, /Parameter\{model:nai-diffusion-5-full,/)
      assert.match(request.content, new RegExp(`width:${size.width}, height:${size.height},`))
      assert.match(request.content, /scale:5\.5,/)
      assert.equal(request.submittedSeed, input.seed)
      assert.ok(request.content.includes(`seed:${input.seed},`))
    }
    assert.equal(requestFor({ ...draft(), seed: '0' }).submittedSeed, '0')
    assert.equal(requestFor({ ...draft(), seed: '-1' }).submittedSeed, '-1')
    assert.match(requestFor().submittedSeed, /^\d+$/)
    assert.equal(requestFor().generationModel, draft().model)
    assert.throws(() => requestFor({ ...draft(), seed: '1.2' }), hasKind('validation'))
    assert.throws(() => requestFor({ ...draft(), count: 0 }), hasKind('validation'))
    assert.throws(() => requestFor({ ...draft(), steps: 2.5 }), hasKind('validation'))
    assert.throws(() => requestFor({ ...draft(), cfg: NaN }), hasKind('validation'))
  })

  await test('prompt segments, weighted artists, commas, conflicts and repeated reuse', async () => {
    const input = { ...draft(), generationModel: 'nai-diffusion-5-full', seed: '100' }
    const request = requestFor(input)
    assert.ok(request.content.startsWith(`${input.artistPrompt}, ${input.positivePrompt}`))
    assert.ok(request.content.endsWith(`negative_prompt:${input.negativePrompt}}`))
    const task = { input, request, image: { returnedSeed: '101' } }
    const reused = parameters.draftFromTask(task)
    assert.equal(reused.seed, '101')
    assert.equal(reused.generationModel, 'nai-diffusion-5-full')
    const twice = parameters.draftFromTask({ ...task, input: reused, request: requestFor(reused) })
    assert.equal(requestFor(reused).content, requestFor(twice).content)
    assert.equal((requestFor(twice).content.match(/historic painter/g) ?? []).length, 1)
    assert.equal(parameters.seedComparison(task), 'different')
    assert.equal(parameters.seedComparison({ ...task, image: { returnedSeed: '0100' } }), 'matches')
    assert.equal(parameters.seedComparison({ ...task, image: { returnedSeed: null } }), 'missing')
    const weightedNegative = '{worst quality}, {{grandfathered content}}, [flat colors]'
    assert.ok(requestFor({ ...draft(), negativePrompt: weightedNegative }).content.endsWith(`negative_prompt:${weightedNegative}}`), 'NovelAI negative-prompt weights remain byte-for-byte intact')
    for (const field of ['positivePrompt', 'artistPrompt', 'negativePrompt']) {
      assert.throws(() => requestFor({ ...draft(), [field]: 'example Parameter{seed:2}' }), hasKind('validation'))
    }
  })

  await test('compact request content adds no line breaks and preserves every handwritten prompt segment', async () => {
    const input = { ...draft(), artistPrompt: '1.2::oil painting::', positivePrompt: 'apple, watercolor', negativePrompt: 'blurry, text', generationModel: 'nai-diffusion-5-full', seed: '900719925474099312345' }
    const compact = requestFor(input).content
    assert.equal(compact, '1.2::oil painting::, apple, watercolor Parameter{model:nai-diffusion-5-full, width:832, height:1216, sampler:Euler Ancestral, scale:5.5, steps:28, seed:900719925474099312345, return_base64:true, negative_prompt:blurry, text}')
    assert.doesNotMatch(compact, /[\r\n]/)
    const multiline = { ...input, artistPrompt: '1.2::oil painting::,\r\n0.8::ink drawing::,', positivePrompt: 'apple\n1.1::still life::', negativePrompt: 'lowres,\nblurry\r\ntext' }
    const content = requestFor(multiline).content
    for (const field of ['artistPrompt', 'positivePrompt', 'negativePrompt']) assert.ok(content.includes(multiline[field]), `${field} retains its exact multiline text and weights`)
    const withoutUserText = content.replace(multiline.artistPrompt, '').replace(multiline.positivePrompt, '').replace(multiline.negativePrompt, '')
    assert.doesNotMatch(withoutUserText, /[\r\n]/, 'Only user-supplied line breaks remain')
    assert.ok(content.includes('0.8::ink drawing::, apple\n'), 'A trailing artist comma is not duplicated')
    assert.ok(requestFor({ ...input, artistPrompt: '水彩画师，' }).content.startsWith('水彩画师， apple'), 'A trailing full-width comma is not duplicated')
    assert.ok(content.endsWith(`negative_prompt:${multiline.negativePrompt}}`))
    assert.doesNotMatch(requestFor({ ...input, artistPrompt: '', negativePrompt: '' }).content, /[\r\n]/)
  })

  await test('tag catalog identities and append preserve existing text, weights and simple tag boundaries', async () => {
    assert.deepEqual(promptTagCategories.map((category) => category.id), ['subjects', 'appearance', 'scenes', 'composition', 'styles', 'ip', 'nsfw'])
    assert.equal(new Set(promptTagCategories.map((category) => category.id)).size, promptTagCategories.length)
    const tags = promptTagCategories.flatMap((category) => {
      assert.ok(category.labelZh && category.labelEn)
      assert.ok(category.tags.length > 0)
      return category.tags
    })
    assert.equal(new Set(tags.map((tag) => tag.id)).size, tags.length)
    assert.equal(new Set(tags.map((tag) => tag.prompt)).size, tags.length)
    const appearance = promptTagCategories.find((category) => category.id === 'appearance')
    assert.deepEqual(appearance.sections.map((section) => [section.parentZh, section.labelZh]), [
      ['头发', '发色'], ['头发', '发型'], ['头发', '头发细节与发饰'],
      ['脸部', '眼睛与瞳'], ['脸部', '表情与情绪'], ['脸部', '脸部细节与妆容'],
      ['服饰', '服装'], ['服饰', '配饰与非人特征'], ['服饰', '头部穿戴'], ['服饰', '鞋袜与腿部'],
      ['姿态动作', '姿态动作'], ['身份', '身份'],
    ])
    assert.equal(appearance.tags.length, 500, 'Non-NSFW reference tags and earlier exclusive terms are retained without duplicates')
    assert.deepEqual(appearance.sections.map((section) => section.tags.length), [32, 46, 30, 38, 44, 21, 82, 51, 26, 28, 70, 32])
    assert.ok(appearance.tags.some((tag) => tag.prompt === 'hair spread out'), 'The reference hair-spread tag keeps its exact generation term')
    assert.ok(!promptTagCategories.some((category) => ['clothing', 'actions'].includes(category.id)))
    assert.deepEqual(promptTagCategories.find((category) => category.id === 'subjects').tags.map((tag) => tag.prompt), [
      '1girl', 'solo', '1boy', '2girls', 'multiple girls', '1other', '2boys', 'multiple boys', 'solo focus',
      'couple', '3girls', '1girl 1boy', 'group', 'crowd', 'no humans', 'animal focus', 'mature female',
      'younger sister', '4girls', '3boys', '6+girls', 'sisters', 'twins', 'siblings', 'male focus',
      'androgynous', 'tomboy', 'mature male', 'adult', 'furry', 'monster girl',
    ])
    const nsfw = promptTagCategories.find((category) => category.id === 'nsfw')
    assert.ok(nsfw.nsfw)
    assert.deepEqual(nsfw.sections.map((section) => [section.labelZh, section.tags.length]), [['裸露状态', 26], ['姿势', 13], ['行为', 24], ['体液与状态', 21]])
    assert.ok(nsfw.tags.every((tag) => tag.nsfw), 'Every opt-in tag participates in search, selection and clear-backup filtering')
    for (const term of ['seductive smile', 'sultry', 'bedroom eyes', 'naughty face', 'heavy breathing', 'ahegao', 'orgasm']) {
      assert.ok(nsfw.tags.some((tag) => tag.prompt === term), `Earlier NSFW term retained: ${term}`)
    }
    assert.ok(promptTagCategories.filter((category) => category.id !== 'nsfw').every((category) => category.tags.every((tag) => !tag.nsfw)))
    const scenes = promptTagCategories.find((category) => category.id === 'scenes')
    assert.deepEqual(scenes.sections.map((section) => [section.labelZh, section.tags.length]), [
      ['场景与背景', 76], ['季节与节日', 23], ['光照氛围与天气', 60], ['特效', 25],
    ])
    for (const term of ['classroom', 'post-apocalypse', 'fantasy', 'valentine', 'tea ceremony', 'chromatic aberration', 'ethereal', 'double exposure', 'outline', 'street at night', 'natural light', 'neon lighting', 'overcast lighting']) {
      assert.ok(scenes.tags.some((tag) => tag.prompt === term), `Scene catalog retains ${term}`)
    }
    assert.deepEqual(promptTagCategories.find((category) => category.id === 'composition').sections.map((section) => section.labelZh), ['构图视角', '镜头景深'])
    const styles = promptTagCategories.find((category) => category.id === 'styles')
    assert.deepEqual(styles.sections.map((section) => [section.labelZh, section.tags.length]), [['质量', 11], ['画风', 33], ['媒介与技法', 36]])
    for (const term of ['masterpiece', 'best quality', 'absurdres', 'highres', 'very aesthetic', 'ultra-detailed', 'realistic', 'photorealistic', '1990s (style)', '4koma', 'traditional media', 'impasto', 'stained glass', 'texture overlay', 'grainy']) {
      assert.ok(styles.tags.some((tag) => tag.prompt === term), `Style reference includes ${term}`)
    }
    for (const term of ['warm colors', 'cool colors', 'blue and gold', 'earth tones', 'woodcut', 'crisp edges', 'realistic texture', 'detailed background', 'detailed reflections']) {
      assert.ok(styles.tags.some((tag) => tag.prompt === term), `Distinct earlier style term retained: ${term}`)
    }
    const ip = promptTagCategories.find((category) => category.id === 'ip')
    assert.deepEqual(ip.sections.map((section) => [section.labelZh, section.tags.length]), [
      ['原神', 132], ['崩坏：星穹铁道', 90], ['绝区零', 66], ['鸣潮', 57],
    ])
    assert.equal(ip.tags.length, 345)
    assert.equal(new Set(ip.tags.map((tag) => tag.id)).size, 345)
    assert.equal(new Set(ip.tags.map((tag) => tag.prompt)).size, 345)
    assert.equal(ip.tags.filter((tag) => tag.aliases?.length).length, 69)
    assert.equal(ip.tags.flatMap((tag) => tag.aliases ?? []).length, 117)
    for (const [term, label] of [
      ['Sigewinne (genshin impact)', '希格雯'], ['Sparkle (Honkai: Star Rail)', '花火'],
      ['Hoshimi Miyabi (Zenless Zone Zero)', '星见雅'], ['The Shorekeeper (Wuthering Waves)', '守岸人'],
      ['Vesna (genshin impact)', '薇斯纳'], ['Pearl (Honkai: Star Rail)', '真珠'],
      ['Roxy (Zenless Zone Zero)', '洛克茜'], ['Hsin (Wuthering Waves)', '心'],
    ]) assert.equal(ip.tags.find((tag) => tag.prompt === term)?.labelZh, label)
    for (const [id, ipName, count] of [
      ['ip-genshin', 'genshin impact', 132],
      ['ip-star-rail', 'Honkai: Star Rail', 90],
      ['ip-zzz', 'Zenless Zone Zero', 66],
      ['ip-wuwa', 'Wuthering Waves', 57],
    ]) {
      const suffix = ` (${ipName})`
      const gameTags = ip.tags.filter((tag) => tag.id.startsWith(`${id}-`))
      assert.equal(gameTags.length, count)
      assert.ok(gameTags.every((tag) => tag.prompt.endsWith(suffix)), `${ipName} terms retain the requested IP suffix`)
      for (const tag of gameTags) {
        const englishName = tag.prompt.slice(0, -suffix.length)
        const slug = englishName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '')
        assert.equal(tag.id, `${id}-${slug}`, `${tag.prompt} uses its deterministic normalized ID`)
      }
    }
    for (const [id, oldTerm, replacement] of [
      ['style-watercolor', 'watercolor', 'watercolor (medium)'], ['style-oil', 'oil painting', 'oil painting (medium)'],
      ['style-pencil', 'pencil drawing', 'sketch'], ['style-ink', 'ink drawing', 'ink wash painting'],
      ['style-flat', 'flat illustration', 'flat color'], ['style-3d', '3d render', '3d'], ['style-paper', 'paper cutout', 'papercraft'],
      ['detail-high', 'high detail', 'highly detailed'], ['detail-lineart', 'clean lineart', 'lineart'],
      ['detail-brushstrokes', 'soft brushstrokes', 'soft brush'], ['detail-shading', 'smooth shading', 'soft shading'],
    ]) {
      assert.equal(styles.tags.find((tag) => tag.id === id)?.prompt, replacement)
      assert.ok(!styles.tags.some((tag) => tag.prompt === oldTerm), `Superseded wording removed: ${oldTerm}`)
    }
    assert.ok(scenes.sections.find((section) => section.id === 'scene-effects').tags.some((tag) => tag.prompt === 'soft focus'))
    assert.deepEqual(tags.find((tag) => tag.id === 'scene-forest'), { id: 'scene-forest', labelZh: '森林', prompt: 'forest' })
    assert.equal(appendPromptTags('', ['forest', 'watercolor']), 'forest, watercolor')
    assert.equal(appendPromptTags('cat', []), 'cat')
    assert.equal(appendPromptTags('cat, watercolor', ['cat', 'forest', 'forest']), 'cat, watercolor, forest')
    assert.equal(appendPromptTags('cat,', ['forest']), 'cat, forest')
    assert.equal(appendPromptTags('cat,\n', ['cat', 'forest']), 'cat,\nforest')
    assert.equal(appendPromptTags('cat， ', ['cat', 'forest']), 'cat， forest')
    assert.equal(appendPromptTags(' \n', ['forest']), ' \nforest')
    const weighted = ' 1.2::cat::,\n{watercolor}, [soft lighting] '
    assert.equal(appendPromptTags(weighted, ['cat', 'forest']), `${weighted}, cat, forest`)
    assert.equal(appendPromptTags('cathedral', ['cat']), 'cathedral, cat', 'A substring is not an identical tag')
  })

  await test('multi-row gallery keeps session batches in a non-shrinking results row', async () => {
    const source = await readFile(path.join(root, 'src/features/image-studio/views/ImageStudioView.vue'), 'utf8')
    assert.ok(source.indexOf('class="batch-panel panel"') < source.indexOf('class="studio-gallery"'), 'Session batches remain before the gallery in the results scroller')
    assert.match(source, /\.studio-results\s*\{[^}]*grid-auto-rows:\s*max-content;[^}]*overflow-y:\s*auto;/s)
  })

  await test('empty upstream URL uses instructional copy instead of suggesting a default endpoint', async () => {
    const source = await readFile(path.join(root, 'src/features/image-studio/components/StudioConnectionDrawer.vue'), 'utf8')
    assert.match(source, /t\('填写上游 Base URL', 'Enter upstream Base URL'\)/)
    assert.doesNotMatch(source, /placeholder="https:\/\/api1\.netwx\.cn\//)
  })

  await test('valid PNG bytes, compressed text, seed evidence, missing and malformed metadata', async () => {
    for (const compression of [undefined, 'zTXt', 'iTXt']) {
      const metadata = await readPngMetadata(new Blob([pngBytes(24680, { compression })]))
      assert.equal(metadata.returnedSeed, '24680')
      assert.equal(metadata.width, 1)
      assert.equal(metadata.height, 1)
      assert.equal(metadata.parameters.scale, 5.5)
      assert.equal(metadata.parameters.sampler, 'k_euler_ancestral')
    }
    assert.equal((await readPngMetadata(new Blob([pngBytes(1, { comment: false })]))).returnedSeed, null)
    const imprecise = await readPngMetadata(new Blob([pngBytes(1, { comment: '{"seed":9007199254740993}' })]))
    assert.equal(imprecise.returnedSeed, null)
    assert.equal(imprecise.warnings.length, 1)
    const malformed = await readPngMetadata(new Blob([pngBytes(1, { comment: '{broken' })]))
    assert.equal(malformed.returnedSeed, null)
    assert.equal(malformed.warnings.length, 1)
    const oversized = await readPngMetadata(new Blob([pngBytes(1, { compression: 'zTXt', comment: 'x'.repeat(5 * 1024 * 1024 + 1) })]))
    assert.equal(oversized.returnedSeed, null)
    assert.equal(oversized.warnings.length, 1)
    assert.equal(oversized.warnings[0].kind, 'metadata')
    assert.match(oversized.warnings[0].zh, /5 MiB/)
    const splitOversized = await readPngMetadata(new Blob([pngBytes(1, {
      comment: false,
      compressedTexts: [
        { keyword: 'First', text: 'x'.repeat(2 * 1024 * 1024) },
        { keyword: 'Second', type: 'iTXt', text: 'x'.repeat(2 * 1024 * 1024) },
        { keyword: 'Third', text: 'x'.repeat(2 * 1024 * 1024) },
      ],
    })]))
    assert.ok(splitOversized.warnings.some((warning) => /5 MiB/.test(warning.zh)), 'The expanded PNG text budget is shared across compressed chunks')
    const corrupted = pngBytes()
    corrupted[20] ^= 1
    await assert.rejects(readPngMetadata(new Blob([corrupted])), hasKind('image'))
    await assert.rejects(readPngMetadata(new Blob([pngBytes().subarray(0, 60)])), hasKind('image'))
  })

  await test('actual HTTP adapter, Markdown/data/URL responses, credential boundary and original bytes', async () => {
    const calls = []
    const original = pngBytes(24680)
    const foldedDataUrl = `data:image/png;base64,${original.toString('base64').match(/.{1,60}/g).join('\r\n')}`
    const request = requestFor({ ...draft(), model: 'nai-diffusion-4.5-full', generationModel: 'nai-diffusion-5-full', seed: '24680' })
    for (const content of [asDataUrl(original), foldedDataUrl, asDataUrl(original).replace('data:', 'DATA:'), `![image](${asDataUrl(original)})`, '![image](https://cdn.example.invalid/image.png)', 'https://cdn.example.invalid/image.png']) {
      globalThis.fetch = async (url, options) => {
        calls.push({ url, options })
        return options.method === 'POST' ? chatResponse(content) : new Response(original, { headers: { 'Content-Type': 'image/png' } })
      }
      const result = await api.generateImage(request, connection.apiKey, new AbortController().signal)
      assert.deepEqual(Buffer.from(await result.blob.arrayBuffer()), original)
      assert.equal(result.metadata.returnedSeed, '24680')
      const post = calls.findLast((call) => call.options.method === 'POST')
      const body = JSON.parse(post.options.body)
      assert.deepEqual(Object.keys(body).sort(), ['messages', 'model', 'stream'])
      assert.equal(body.model, 'nai-diffusion-4.5-full')
      assert.ok(body.messages[0].content.includes('model:nai-diffusion-5-full,'))
      assert.equal(body.stream, false)
      assert.equal(post.options.credentials, 'omit')
      assert.equal(post.options.headers.Authorization, `Bearer ${connection.apiKey}`)
      const resource = calls.at(-1)
      if (resource.options.method !== 'POST') {
        assert.equal(resource.options.credentials, 'omit')
        assert.equal(resource.options.headers, undefined)
        assert.equal(resource.options.referrerPolicy, 'no-referrer')
      }
    }
    assert.ok(bitmapClosed >= 5)
    assert.deepEqual(Buffer.from(await api.imageDataUrlToBlob(`data:image/png;base64,${original.toString('base64').replace(/.{40}/g, '$& \n\t')}`).arrayBuffer()), original)
    assert.throws(() => api.imageDataUrlToBlob('data:image/png;base64,bad!'), hasKind('image'))
    globalThis.fetch = async () => new Response(JSON.stringify({ data: [{ id: 'nai-diffusion-4.5-full' }, { id: 'custom-model' }] }))
    assert.deepEqual(await api.loadUpstreamModels(connection, new AbortController().signal), ['nai-diffusion-4.5-full', 'custom-model'])
  })

  await test('HTTP errors, embedded failures, malformed responses and CORS/resource failure stay explicit', async () => {
    const cases = [
      [() => new Response('quota exhausted', { status: 402 }), 'http'],
      [() => new Response('{bad'), 'response'],
      [() => new Response(JSON.stringify({ error: { message: `bad token ${connection.apiKey}` } })), 'upstream'],
      [() => new Response(JSON.stringify({ choices: [{ finish_reason: 'error', message: { content: 'upstream failure' } }] })), 'upstream'],
      [() => new Response('{}'), 'response'],
      [() => new Response('{"choices":[]}'), 'response'],
      [() => new Response('{"choices":[{"message":{"content":null}}]}'), 'response'],
      [() => chatResponse('There is no image.'), 'response'],
      [() => { throw new TypeError('CORS failure') }, 'network'],
    ]
    for (const [respond, kind] of cases) {
      let calls = 0
      globalThis.fetch = async () => { calls++; return respond() }
      await assert.rejects(api.generateImage(requestFor(), connection.apiKey, new AbortController().signal), (error) => {
        assert.equal(error.problem.kind, kind)
        assert.ok(!JSON.stringify(error.problem).includes(connection.apiKey))
        return true
      })
      assert.equal(calls, 1, 'failures must not retry automatically')
    }
    globalThis.fetch = async (_url, options) => {
      if (options.method === 'POST') return chatResponse('https://cdn.example.invalid/image.png')
      throw new TypeError('No CORS access to original')
    }
    await assert.rejects(api.generateImage(requestFor(), connection.apiKey, new AbortController().signal), hasKind('image'))
  })

  await test('120-second deadline and manual cancel are distinct, with timer cleanup', async () => {
    const nativeSet = globalThis.setTimeout
    const nativeClear = globalThis.clearTimeout
    const timers = new Set()
    let timeoutCallback
    globalThis.setTimeout = (callback, milliseconds, ...args) => {
      if (milliseconds !== api.REQUEST_TIMEOUT_MS) return nativeSet(callback, milliseconds, ...args)
      assert.equal(milliseconds, 120000)
      const timer = { callback }
      timeoutCallback = callback
      timers.add(timer)
      return timer
    }
    globalThis.clearTimeout = (timer) => {
      if (!timers.delete(timer)) nativeClear(timer)
    }
    try {
      globalThis.fetch = async () => new Promise(() => {})
      const expired = api.generateImage(requestFor(), connection.apiKey, new AbortController().signal)
      timeoutCallback()
      await assert.rejects(expired, hasKind('timeout'))
      assert.equal(timers.size, 0)
      const controller = new AbortController()
      const cancelled = api.generateImage(requestFor(), connection.apiKey, controller.signal)
      controller.abort()
      await assert.rejects(cancelled, hasKind('cancelled'))
      assert.equal(timers.size, 0)
      const beforeStart = new AbortController()
      beforeStart.abort()
      let requests = 0
      globalThis.fetch = async () => { requests++; return chatResponse(asDataUrl(pngBytes())) }
      await assert.rejects(api.generateImage(requestFor(), connection.apiKey, beforeStart.signal), hasKind('cancelled'))
      assert.equal(requests, 0)
    } finally {
      globalThis.setTimeout = nativeSet
      globalThis.clearTimeout = nativeClear
    }
  })

  await test('5 requests plus 3 overlapping requests start immediately; snapshots, order and cancellation are independent', async () => {
    const calls = []
    const runner = new GenerationRunner(storage, (request, apiKey, signal) => {
      const pending = deferred()
      calls.push({ request, apiKey, signal, ...pending })
      return pending.promise
    })
    runner.setOwner(101)
    const firstDraft = { ...draft(), count: 5 }
    const first = runner.submit(firstDraft, connection)
    assert.equal(calls.length, 5)
    assert.equal(runner.activeCount, 5)
    firstDraft.positivePrompt = 'changed after submission'
    firstDraft.cfg = 8
    const secondConnection = { baseUrl: 'https://second.example.invalid/v1', apiKey: 'another-fixture-secret' }
    const second = runner.submit({ ...firstDraft, count: 3, generationModel: 'nai-diffusion-5-full' }, secondConnection)
    assert.equal(calls.length, 8)
    assert.equal(runner.activeCount, 8)
    assert.ok(calls.slice(0, 5).every((call) => !call.request.content.includes('changed after submission') && call.apiKey === connection.apiKey))
    assert.ok(calls.slice(5).every((call) => call.request.content.includes('changed after submission') && call.apiKey === secondConnection.apiKey))
    const firstTasks = runner.tasks.filter((task) => task.batchId === first).sort((a, b) => a.position - b.position)
    assert.ok(firstTasks.every((task) => Object.isFrozen(task.input) && Object.isFrozen(task.request)))
    assert.equal(new Set(firstTasks.map((task) => task.request.submittedSeed)).size, 5)
    calls[6].resolve(imageResult(calls[6].request.submittedSeed))
    await waitFor(runner, () => runner.tasks.some((task) => task.batchId === second && task.position === 2 && task.status === 'succeeded'), 'out-of-order result')
    assert.equal(runner.activeCount, 7)
    calls[0].reject(new Error('one upstream failure'))
    await waitFor(runner, () => runner.tasks.find((task) => task.id === firstTasks[0].id)?.status === 'failed', 'one failed item')
    assert.equal(calls.length, 8)
    runner.cancelTask(firstTasks[1].id)
    assert.equal(calls[1].signal.aborted, true)
    runner.cancelBatch(first)
    assert.ok(calls.slice(1, 5).every((call) => call.signal.aborted))
    assert.ok(calls.slice(5).every((call) => !call.signal.aborted))
    assert.equal(runner.activeCount, 2)
    for (const index of [1, 2, 3, 4, 5, 7]) calls[index].resolve(imageResult(calls[index].request.submittedSeed))
    await waitFor(runner, () => runner.activeCount === 0, 'remaining second batch')
    assert.ok(runner.tasks.filter((task) => task.batchId === first && task.position > 1).every((task) => task.status === 'cancelled'))
    const retryBatch = runner.retry(firstTasks[0].id, secondConnection)
    assert.equal(calls.length, 9)
    assert.equal(calls[8].request.submittedSeed, firstTasks[0].request.submittedSeed)
    assert.equal(runner.tasks.find((task) => task.batchId === retryBatch).retryOf, firstTasks[0].id)
    calls[8].resolve(imageResult(calls[8].request.submittedSeed))
    runner.submit({ ...draft(), seed: '0' }, connection)
    assert.equal(calls.length, 10)
    calls[9].resolve(imageResult('0'))
    await waitFor(runner, () => runner.activeCount === 0 && runner.tasks.every((task) => task.save.status === 'saved'), 'all per-item saves')
    assert.ok(!JSON.stringify(runner.tasks).includes(connection.apiKey))
    assert.ok(!JSON.stringify(runner.tasks).includes(secondConnection.apiKey))
  })

  await test('synchronous preparation and immediate request rejection cannot short-circuit a batch', async () => {
    const originalRandom = globalThis.crypto.getRandomValues.bind(globalThis.crypto)
    let seeds = 0
    globalThis.crypto.getRandomValues = (array) => {
      if (array.length === 1 && ++seeds === 2) throw new Error('random source failed for one item')
      return originalRandom(array)
    }
    const calls = []
    const runner = new GenerationRunner(storage, (request) => {
      calls.push(request)
      if (calls.length === 1) throw new Error('synchronous request preparation failure')
      if (calls.length === 2) return Promise.reject(new Error('fast asynchronous failure'))
      return Promise.resolve(imageResult(request.submittedSeed))
    })
    try {
      runner.setOwner(102)
      runner.submit({ ...draft(), count: 5 }, connection)
      assert.equal(calls.length, 4)
      await waitFor(runner, () => runner.activeCount === 0 && runner.tasks.every((task) => task.save.status === 'saved'), 'mixed preparation outcomes')
      assert.equal(runner.tasks.filter((task) => task.status === 'failed').length, 3)
      assert.equal(runner.tasks.filter((task) => task.status === 'succeeded').length, 2)
      const fixed = runner.submit({ ...draft(), count: 3, seed: '12345' }, connection)
      assert.ok(runner.tasks.filter((task) => task.batchId === fixed).every((task) => task.request.submittedSeed === '12345'))
      await waitFor(runner, () => runner.activeCount === 0, 'new batch after failures')
    } finally {
      globalThis.crypto.getRandomValues = originalRandom
    }
  })

  await test('account epochs reject old success/error/finally; recovered tasks do not replay', async () => {
    const calls = []
    const runner = new GenerationRunner(storage, (request, apiKey, signal) => {
      const pending = deferred()
      calls.push({ request, apiKey, signal, ...pending })
      return pending.promise
    })
    runner.setOwner(103)
    runner.submit({ ...draft(), count: 2 }, connection)
    await waitFor(runner, () => runner.tasks.every((task) => task.save.status === 'saved'), 'persist old in-flight metadata')
    runner.setOwner(104)
    runner.submit(draft(), { ...connection, apiKey: 'owner-104-only' })
    assert.ok(calls[0].signal.aborted && calls[1].signal.aborted)
    calls[0].resolve(imageResult('1'))
    calls[1].reject(new Error('late previous account error'))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(runner.activeCount, 1)
    assert.equal(runner.tasks.length, 1)
    assert.equal(runner.tasks[0].ownerId, 104)
    assert.equal(runner.tasks[0].status, 'running')
    calls[2].resolve(imageResult('2'))
    await waitFor(runner, () => runner.tasks[0].status === 'succeeded' && runner.tasks[0].save.status === 'saved', 'new owner result')
    runner.setOwner(103)
    const previous = await storage.listHistory(103)
    runner.mergeHistory(103, previous.tasks)
    assert.equal(runner.tasks.length, 2)
    assert.ok(runner.tasks.every((task) => task.status === 'interrupted'))
    assert.equal(calls.length, 3)
    assert.equal(runner.activeCount, 0)
  })

  await test('save failures retain original bytes; retry saving never generates again', async () => {
    let fail = true
    let requests = 0
    const result = imageResult('42')
    const persistence = {
      saveTask: (...args) => fail ? Promise.reject(new DOMException('Storage quota reached', 'QuotaExceededError')) : storage.saveTask(...args),
      getImage: (...args) => storage.getImage(...args),
      deleteTasks: (...args) => storage.deleteTasks(...args),
    }
    const runner = new GenerationRunner(persistence, async () => { requests++; return result })
    runner.setOwner(105)
    runner.submit({ ...draft(), seed: '42' }, connection)
    await waitFor(runner, () => runner.tasks[0]?.status === 'succeeded' && runner.tasks[0].save.status === 'failed', 'quota failure')
    const task = runner.tasks[0]
    assert.equal(task.request.submittedSeed, '42')
    assert.equal((await runner.getImage(task.id)).size, result.blob.size)
    assert.equal((await storage.listHistory(105)).tasks.length, 0)
    fail = false
    assert.equal(await runner.retrySave(task.id), true)
    assert.equal(requests, 1)
    assert.equal(runner.tasks[0].save.status, 'saved')
    assert.deepEqual(Buffer.from(await (await storage.getImage(105, task.id)).arrayBuffer()), Buffer.from(await result.blob.arrayBuffer()))
    runner.submit(draft(), connection)
    assert.equal(requests, 2)
    await waitFor(runner, () => runner.activeCount === 0 && runner.tasks.every((item) => item.save.status === 'saved'), 'submit after storage failure')
  })

  await test('late running/result saves stay ordered and deletion cannot be undone by an in-flight save', async () => {
    const runningWrite = deferred()
    const resultWrite = deferred()
    const runningWriteStarted = deferred()
    const resultWriteStarted = deferred()
    const generation = deferred()
    const writes = []
    let deletes = 0
    let requests = 0
    const persistence = {
      async saveTask(owner, task, image) {
        writes.push(task.status)
        if (task.status === 'running') {
          runningWriteStarted.resolve()
          await runningWrite.promise
        } else {
          assert.equal(task.status, 'succeeded')
          resultWriteStarted.resolve()
          await resultWrite.promise
        }
        await storage.saveTask(owner, task, image)
      },
      getImage: (...args) => storage.getImage(...args),
      async deleteTasks(...args) { deletes++; await storage.deleteTasks(...args) },
    }
    const runner = new GenerationRunner(persistence, () => { requests++; return generation.promise })
    runner.setOwner(106)
    runner.submit({ ...draft(), seed: '86' }, connection)
    const id = runner.tasks[0].id
    await runningWriteStarted.promise
    generation.resolve(imageResult('86'))
    await waitFor(runner, () => runner.activeCount === 0, 'generation finishes while its first save is still pending')
    assert.deepEqual(writes, ['running'], 'The final result write waits for the older running write')
    assert.equal(runner.tasks[0].status, 'succeeded')
    assert.equal(runner.tasks[0].save.status, 'saving')
    const deletion = runner.deleteTasks([id])
    assert.equal(await runner.retrySave(id), false, 'A new save cannot race an explicit deletion')
    assert.equal(deletes, 0)
    runningWrite.resolve()
    await resultWriteStarted.promise
    assert.deepEqual(writes, ['running', 'succeeded'])
    assert.equal(deletes, 0, 'Deletion also waits for the final image transaction')
    assert.deepEqual(Buffer.from(await (await runner.getImage(id)).arrayBuffer()), pngBytes('86'))
    resultWrite.resolve()
    await deletion
    assert.equal(deletes, 1)
    assert.deepEqual(runner.tasks, [])
    assert.deepEqual((await storage.listHistory(106)).tasks, [])
    await assert.rejects(storage.getImage(106, id), hasKind('storage'))
    assert.equal(requests, 1)
  })

  await test('real IndexedDB module isolates settings/presets/images and paginates identical timestamps', async () => {
    await storage.saveConnection(201, connection)
    assert.equal(await storage.getConnection(202), null)
    await storage.saveConnection(202, { ...connection, apiKey: 'account-202' })
    assert.equal((await storage.getConnection(201)).apiKey, connection.apiKey)
    await storage.clearConnection(201)
    assert.equal(await storage.getConnection(201), null)
    assert.equal((await storage.getConnection(202)).apiKey, 'account-202')
    await storage.saveDraft(201, draft())
    assert.equal(await storage.getDraft(202), null)
    assert.equal(await storage.getGalleryPreferences(201), null)
    await storage.saveGalleryPreferences(201, { previewBlurred: true })
    await storage.saveGalleryPreferences(202, { previewBlurred: false })
    assert.deepEqual(await storage.getGalleryPreferences(201), { previewBlurred: true })
    assert.deepEqual(await storage.getGalleryPreferences(202), { previewBlurred: false })
    assert.equal(await storage.getTagFavorites(201), null)
    await storage.saveTagFavorites(201, { tags: [{ id: 'scene-forest', labelZh: '森林', prompt: 'forest' }] })
    await storage.saveTagFavorites(202, { tags: [{ id: 'favorite-custom:test', labelZh: 'silver moon', prompt: 'silver moon', custom: true }] })
    assert.deepEqual(await storage.getTagFavorites(201), { tags: [{ id: 'scene-forest', labelZh: '森林', prompt: 'forest' }] })
    assert.deepEqual(await storage.getTagFavorites(202), { tags: [{ id: 'favorite-custom:test', labelZh: 'silver moon', prompt: 'silver moon', custom: true }] })
    await storage.savePreset(201, { ownerId: 201, id: 'same', kind: 'artist', name: 'Personal', text: '1.1::style::' })
    await storage.savePreset(202, { ownerId: 202, id: 'same', kind: 'negative', name: 'Personal', text: 'lowres' })
    await storage.deletePreset(201, 'same')
    assert.equal((await storage.listPresets(201)).length, 0)
    assert.equal((await storage.listPresets(202))[0].text, 'lowres')
    const runner = new GenerationRunner(storage, async () => imageResult('7'))
    runner.setOwner(201)
    runner.submit(draft(), connection)
    await waitFor(runner, () => runner.tasks[0]?.status === 'succeeded' && runner.tasks[0].save.status === 'saved', 'storage fixture')
    const template = storedTask(runner.tasks[0])
    for (const id of ['same', 'page-a', 'page-b']) await storage.saveTask(201, { ...template, id, createdAt: 1 }, imageResult('1').blob)
    await storage.saveTask(202, { ...template, ownerId: 202, id: 'same', createdAt: 1 }, imageResult('2').blob)
    const seen = []
    let cursor = null
    do {
      const page = await storage.listHistory(201, cursor, 1)
      seen.push(...page.tasks.map((task) => task.id))
      cursor = page.nextCursor
    } while (cursor)
    assert.equal(seen.length, 4)
    assert.equal(new Set(seen).size, 4)
    assert.ok((await storage.listHistory(201)).tasks.every((task) => task.ownerId === 201))
    await storage.deleteTasks(201, ['same'])
    await assert.rejects(storage.getImage(201, 'same'), hasKind('storage'))
    assert.deepEqual(Buffer.from(await (await storage.getImage(202, 'same')).arrayBuffer()), pngBytes('2'))
    assert.throws(() => storage.saveTask(201, { ...template, ownerId: 202 }), hasKind('storage'))
  })

  await test('request success followed by abort and quota throw cannot report committed data', async () => {
    const source = (await storage.listHistory(202)).tasks[0]
    const originalPut = IDBObjectStore.prototype.put
    IDBObjectStore.prototype.put = function (value, ...args) {
      const request = originalPut.call(this, value, ...args)
      if (this.name === 'tasks' && value.id === 'abort-after-request-success') request.addEventListener('success', () => this.transaction.abort())
      return request
    }
    try {
      await assert.rejects(storage.saveTask(203, { ...source, ownerId: 203, id: 'abort-after-request-success' }, imageResult().blob), hasKind('storage'))
      assert.equal((await storage.listHistory(203)).tasks.length, 0)
    } finally {
      IDBObjectStore.prototype.put = originalPut
    }
    IDBObjectStore.prototype.put = function (value, ...args) {
      if (this.name === 'images' && value.ownerId === 203) throw new DOMException('Fixture quota failure', 'QuotaExceededError')
      return originalPut.call(this, value, ...args)
    }
    try {
      await assert.rejects(storage.saveTask(203, { ...source, ownerId: 203, id: 'quota-failure' }, imageResult().blob), (error) => error.name === 'QuotaExceededError')
      assert.equal((await storage.listHistory(203)).tasks.length, 0)
    } finally {
      IDBObjectStore.prototype.put = originalPut
    }
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB')
    Object.defineProperty(globalThis, 'indexedDB', { configurable: true, get() { throw new DOMException('Blocked by browser policy', 'SecurityError') } })
    try {
      await assert.rejects(new ImageStudioStorage().getConnection(204), (error) => error.name === 'SecurityError')
    } finally {
      Object.defineProperty(globalThis, 'indexedDB', descriptor)
    }
  })

  const { setCurrentUser } = await server.ssrLoadModule('/src/features/auth/state/currentUser.ts')
  const { useImageStudio } = await server.ssrLoadModule('/src/features/image-studio/state/useImageStudio.ts')
  const { setLanguage } = await server.ssrLoadModule('/src/shared/i18n/index.ts')
  const studio = useImageStudio()
  const user = (id) => ({ id, username: `fixture-${id}`, is_admin: false, is_super_admin: false, must_change_password: false })
  const modelsResponse = (models) => new Response(JSON.stringify({ data: models.map((id) => ({ id })) }))
  const modelNames = ['nai-diffusion-4.5-full', 'nai-diffusion-5-full', 'NAI-Test-Model', 'gpt-image-1']
  async function switchOwner(id) {
    setCurrentUser(user(id))
    await until(() => !studio.localLoading.value, 'account data loaded')
    await nextTick()
  }
  async function mountStudio() {
    setLanguage('zh')
    const { default: component } = await server.ssrLoadModule('/src/features/image-studio/views/ImageStudioView.vue')
    const app = renderer.createApp(component)
    cssRendererSsr.setup(app)
    const element = { children: [] }
    app.mount(element)
    await nextTick()
    return { app, element }
  }

  await test('new connections start empty while explicitly saved URLs survive account reloads', async () => {
    await switchOwner(317)
    assert.deepEqual(studio.connection.value, { baseUrl: '', apiKey: '' })
    assert.equal(studio.previewBlurred.value, false)
    await storage.saveConnection(317, connection)
    await studio.setPreviewBlurred(true)
    await switchOwner(318)
    assert.equal(studio.connection.value.baseUrl, '')
    assert.equal(studio.previewBlurred.value, false)
    await switchOwner(317)
    assert.deepEqual(studio.connection.value, connection)
    assert.equal(studio.previewBlurred.value, true)
    setCurrentUser(null)
  })

  await test('saved model choices survive the new default; real sliders and inputs share exact values and ordered resolution labels', async () => {
    const saved = { ...draft(), model: 'saved-request-model', generationModel: 'nai-diffusion-5-full', cfg: 33.375, steps: 240 }
    await storage.saveDraft(307, saved)
    await switchOwner(307)
    const calls = []
    const pending = deferred()
    globalThis.fetch = (url, options) => {
      assert.equal(options.method, 'POST')
      calls.push({ url, body: JSON.parse(options.body) })
      return pending.promise
    }
    const { app, element } = await mountStudio()
    try {
      assert.equal(inputControl('studio-request-model').props.value, saved.model)
      assert.equal(inputControl('studio-generation-model').props.value, saved.generationModel)
      const cfgSlider = uiControl('NSlider', (item) => item.attrs['aria-label'] === 'CFG 滑条')
      const stepsSlider = uiControl('NSlider', (item) => item.attrs['aria-label'] === '步数滑条')
      assert.equal([...bridge.controls].filter((item) => item.name === 'NSlider').length, 2)
      const handles = findNodes(element, (node) => node.role === 'slider')
      assert.equal(handles.length, 2, 'The installed sliders render two focusable handles')
      assert.deepEqual(handles.map((node) => node['aria-label']), ['CFG 滑条', '步数滑条'])
      assert.equal(uiControl('NFormItem', (item) => item.props.labelProps?.for === 'studio-cfg').props.label, 'CFG (默认5)')
      assert.equal(uiControl('NFormItem', (item) => item.props.labelProps?.for === 'studio-steps').props.label, '步数 (默认28)')
      assert.equal(handles[0]['aria-valuenow'], 33.375, 'The installed slider keeps the saved decimal')
      const resolutionButtons = () => [...bridge.controls].filter((item) => item.name === 'NButton' && typeof item.attrs['aria-pressed'] === 'boolean')
      assert.deepEqual(resolutionButtons().map((item) => item.text), ['1216 x 832 (横版)', '832 x 1216 (竖版)', '1024 x 1024 (1:1)'])
      assert.ok(![...bridge.controls].some((item) => item.name === 'NButton' && ['CFG 3', 'CFG 5', 'CFG 7', '28 步', '40 步', '50 步'].includes(item.attrs['aria-label'])), 'Numeric shortcut buttons have been removed')
      setLanguage('en')
      await nextTick()
      assert.deepEqual(handles.map((node) => node['aria-label']), ['CFG slider', 'Steps slider'])
      assert.equal(uiControl('NFormItem', (item) => item.props.labelProps?.for === 'studio-cfg').props.label, 'CFG (default 5)')
      assert.equal(uiControl('NFormItem', (item) => item.props.labelProps?.for === 'studio-steps').props.label, 'Steps (default 28)')
      assert.deepEqual(resolutionButtons().map((item) => item.text), ['1216 x 832 (Landscape)', '832 x 1216 (Portrait)', '1024 x 1024 (1:1)'])
      setLanguage('zh')
      await nextTick()
      assert.deepEqual(handles.map((node) => node['aria-label']), ['CFG 滑条', '步数滑条'])
      assert.equal(cfgSlider.props.value, 33.375, 'The saved decimal is not rounded to the slider step')
      assert.ok(cfgSlider.props.min <= 33.375 && cfgSlider.props.max >= 33.375)
      assert.equal(cfgSlider.props.step, 0.1)
      assert.equal(stepsSlider.props.value, 240)
      assert.ok(stepsSlider.props.max >= 240)
      assert.equal(stepsSlider.props.step, 1)
      assert.equal(inputControl('studio-cfg').props.max, undefined)
      assert.equal(inputControl('studio-steps').props.max, undefined)

      const expandedCfgMax = cfgSlider.props.max
      cfgSlider.emit('update:value', 6.2)
      stepsSlider.emit('update:value', 40)
      await nextTick()
      assert.equal(inputControl('studio-cfg').props.value, 6.2)
      assert.equal(inputControl('studio-steps').props.value, 40)
      assert.equal(cfgSlider.props.max, expandedCfgMax, 'Dragging does not shrink the range under the pointer')
      const rails = findNodes(element, (node) => node.onKeydown !== undefined)
      let prevented = 0
      for (const [index, handle] of handles.entries()) {
        assert.equal(handle.tabindex, 0)
        handle.onFocus()
        rails[index].onKeydown({ key: 'ArrowRight', preventDefault() { prevented++ } })
        handle.onBlur()
      }
      await nextTick()
      assert.equal(prevented, 2)
      assert.equal(inputControl('studio-cfg').props.value, 6.3)
      assert.equal(inputControl('studio-steps').props.value, 41)
      inputControl('studio-cfg').emit('update:value', -0.375)
      await nextTick()
      assert.equal(cfgSlider.props.value, -0.375)
      assert.ok(cfgSlider.props.min <= -0.375)
      assert.equal(studio.draft.value.cfg, -0.375)

      studio.connection.value = { ...connection }
      const form = uiControl('NForm', (item) => item.attrs.class === 'studio-parameters')
      inputControl('studio-steps').emit('update:value', 2.5)
      await nextTick()
      assert.equal(studio.draft.value.steps, 2.5, 'An invalid fractional step is not silently rounded')
      assert.equal(stepsSlider.props.disabled, true)
      form.emit('submit', { preventDefault() {} })
      assert.equal(calls.length, 0)
      await nextTick()
      assert.ok(nodeText(element).includes('步数必须为有效正整数'))
      inputControl('studio-cfg').emit('update:value', null)
      inputControl('studio-steps').emit('update:value', 5000)
      await nextTick()
      assert.equal(studio.draft.value.cfg, null, 'The disabled slider does not fill a cleared input')
      assert.equal(cfgSlider.props.disabled, true)
      assert.equal(stepsSlider.props.value, 5000)
      assert.ok(stepsSlider.props.max >= 5000)
      form.emit('submit', { preventDefault() {} })
      assert.equal(calls.length, 0)
      inputControl('studio-cfg').emit('update:value', 27.375)
      inputControl('studio-seed').emit('update:value', '100')
      await nextTick()
      assert.equal(cfgSlider.props.value, 27.375)

      for (const [label, width, height] of [
        ['1216 x 832 (横版)', 1216, 832],
        ['832 x 1216 (竖版)', 832, 1216],
        ['1024 x 1024 (1:1)', 1024, 1024],
      ]) {
        const button = uiControl('NButton', (item) => item.text === label)
        button.emit('click')
        await nextTick()
        assert.equal(studio.draft.value.width, width)
        assert.equal(studio.draft.value.height, height)
        assert.equal(button.attrs['aria-pressed'], true)
      }
      form.emit('submit', { preventDefault() {} })
      assert.equal(calls.length, 1)
      assert.equal(calls[0].body.model, saved.model)
      const content = calls[0].body.messages[0].content
      assert.match(content, /model:nai-diffusion-5-full,/)
      assert.match(content, /scale:27\.375,/)
      assert.match(content, /steps:5000,/)
      assert.match(content, /width:1024, height:1024,/)
      inputControl('studio-cfg').emit('update:value', 9.4)
      inputControl('studio-steps').emit('update:value', 28)
      assert.equal(studio.tasks.value[0].input.cfg, 27.375)
      assert.equal(studio.tasks.value[0].input.steps, 5000)
      pending.resolve(chatResponse(asDataUrl(pngBytes('100'))))
      await until(() => studio.tasks.value[0]?.status === 'succeeded' && studio.tasks.value[0].save.status === 'saved', 'slider request saved')

      await switchOwner(308)
      assert.equal(inputControl('studio-request-model').props.value, 'nai-diffusion-4-5-curated')
      assert.equal(studio.draft.value.generationModel, '')
      assert.equal(cfgSlider.props.min, 0)
      assert.equal(cfgSlider.props.max, 20)
      assert.equal(stepsSlider.props.max, 50, 'Another account does not inherit the previous slider range')
      await switchOwner(307)
      assert.equal(studio.draft.value.model, saved.model)
      assert.equal(studio.draft.value.generationModel, saved.generationModel)
      assert.equal(cfgSlider.props.value, 33.375)
      assert.equal(stepsSlider.props.value, 240)
      assert.equal(calls.length, 1, 'Account restoration never replays generation')
    } finally {
      app.unmount()
      setCurrentUser(null)
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('history responses cannot resurrect deleted records or publish old-account errors and loading cleanup', async () => {
    const source = (await storage.listHistory(202)).tasks[0]
    const id = 'history-delete-race'
    await storage.saveTask(305, { ...source, ownerId: 305, id }, imageResult().blob)
    await switchOwner(305)
    const originalList = ImageStudioStorage.prototype.listHistory
    const oldPage = await storage.listHistory(305)
    const deletedRead = deferred()
    const oldAccountRead = deferred()
    const newAccountRead = deferred()
    let reads = 0
    ImageStudioStorage.prototype.listHistory = function (owner, ...args) {
      if (owner === 305) return ++reads === 1 ? deletedRead.promise : oldAccountRead.promise
      return originalList.call(this, owner, ...args)
    }
    try {
      const beforeDeletion = studio.refreshHistory()
      assert.equal(studio.historyLoading.value, true)
      assert.equal(await studio.deleteTasks([id]), true)
      assert.deepEqual(studio.tasks.value, [])
      deletedRead.resolve(oldPage)
      await beforeDeletion
      assert.deepEqual(studio.tasks.value, [], 'A read started before deletion cannot restore the record')
      assert.equal(studio.historyLoading.value, false)
      await assert.rejects(storage.getImage(305, id), hasKind('storage'))

      const beforeSwitch = studio.refreshHistory()
      await switchOwner(306)
      ImageStudioStorage.prototype.listHistory = function (owner, ...args) {
        return owner === 306 ? newAccountRead.promise : originalList.call(this, owner, ...args)
      }
      const afterSwitch = studio.refreshHistory()
      oldAccountRead.reject(new Error('late old-account history failure'))
      await beforeSwitch
      assert.equal(studio.historyProblem.value, null)
      assert.equal(studio.historyLoading.value, true, 'Old finally cannot clear the current history read')
      newAccountRead.resolve({ tasks: [], nextCursor: null })
      await afterSwitch
      assert.equal(studio.historyLoading.value, false)
      assert.deepEqual(studio.tasks.value, [])
    } finally {
      ImageStudioStorage.prototype.listHistory = originalList
    }
  })

  await test('local load errors stay complete in a named keyboard-accessible region and clear for a healthy account', async () => {
    await switchOwner(314)
    globalThis.fetch = async () => { assert.fail('Local read failures must not issue upstream requests') }
    const { app, element } = await mountStudio()
    const methods = ['getConnection', 'getDraft', 'listPresets', 'getGalleryPreferences', 'getTagFavorites']
    const originals = new Map(methods.map((name) => [name, ImageStudioStorage.prototype[name]]))
    const regions = () => findNodes(element, (node) => node.class === 'studio-load-problems')
    try {
      assert.equal(regions().length, 0, 'A healthy load adds no error region or layout space')
      for (const name of methods) {
        ImageStudioStorage.prototype[name] = async function (owner, ...args) {
          if (owner === 315) throw new DOMException('Blocked by browser policy', 'SecurityError')
          return originals.get(name).call(this, owner, ...args)
        }
      }
      await switchOwner(315)
      assert.equal(regions().length, 1)
      const region = regions()[0]
      assert.equal(region.tag, 'section')
      assert.equal(region.tabindex, '0')
      assert.equal(region['aria-label'], '本地数据读取错误')
      const alerts = () => findNodes(region, (node) => node['data-control'] === 'NAlert').map(nodeText)
      assert.deepEqual(alerts(), [
        '读取本地连接设置失败: Blocked by browser policy',
        '读取本地生成设置失败: Blocked by browser policy',
        '读取个人预设失败: Blocked by browser policy',
        '读取图库偏好失败: Blocked by browser policy',
        '读取标签收藏失败: Blocked by browser policy',
      ], 'All three independent failures retain their complete original messages')
      assert.equal(uiControl('NButton', (item) => item.props.attrType === 'submit').props.disabled, false)
      setLanguage('en')
      await nextTick()
      assert.equal(region['aria-label'], 'Local data loading errors')
      assert.deepEqual(alerts(), [
        'Failed to read the local connection settings: Blocked by browser policy',
        'Failed to read the local generation settings: Blocked by browser policy',
        'Failed to read personal presets: Blocked by browser policy',
        'Failed to read gallery preferences: Blocked by browser policy',
        'Failed to read tag favorites: Blocked by browser policy',
      ])
      await switchOwner(316)
      assert.equal(regions().length, 0, 'Cleared errors release the region and cannot leak across accounts')
    } finally {
      for (const [name, original] of originals) ImageStudioStorage.prototype[name] = original
      app.unmount()
      setCurrentUser(null)
      setLanguage('zh')
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('actual composable invalidates model candidates and stale success/error/finally when URL or key changes', async () => {
    await switchOwner(301)
    const calls = []
    globalThis.fetch = (url, options) => {
      assert.ok(url.endsWith('/models'))
      const pending = deferred()
      calls.push({ url, options, ...pending })
      return pending.promise
    }
    studio.connection.value = { ...connection }
    const initial = studio.loadModels()
    calls[0].resolve(modelsResponse([...modelNames, modelNames[0]]))
    await initial
    assert.deepEqual(studio.visibleModels.value, modelNames.slice(0, 3))
    studio.otherModelsExpanded.value = true
    assert.deepEqual(studio.visibleModels.value, modelNames)
    studio.draft.value.model = 'gpt-image-1'
    studio.otherModelsExpanded.value = false
    assert.equal(studio.draft.value.model, 'gpt-image-1', 'Collapsing candidates preserves the selected model')

    const oldLoad = studio.loadModels()
    studio.connection.value.baseUrl = 'https://different.example.invalid/custom/'
    assert.equal(calls[1].options.signal.aborted, true)
    assert.deepEqual(studio.models.value, [])
    assert.equal(studio.modelsLoaded.value, false)
    const replacement = studio.loadModels()
    calls[1].resolve(modelsResponse(['nai-stale-success']))
    await oldLoad
    assert.equal(studio.modelsLoading.value, true, 'Old finally cannot clear the new loading state')
    calls[2].resolve(modelsResponse(['nai-new-connection']))
    await replacement
    assert.deepEqual(studio.visibleModels.value, ['nai-new-connection'])
    assert.equal(calls[2].url, 'https://different.example.invalid/custom/v1/models')

    const oldFailure = studio.loadModels()
    studio.connection.value.apiKey = 'changed-fixture-key'
    assert.deepEqual(studio.models.value, [])
    const newKeyLoad = studio.loadModels()
    calls[4].resolve(modelsResponse(['NAI-new-key']))
    await newKeyLoad
    calls[3].reject(new Error('late old-connection failure'))
    await oldFailure
    assert.deepEqual(studio.visibleModels.value, ['NAI-new-key'])
    assert.equal(studio.modelProblem.value, null)
    assert.equal(calls[4].options.headers.Authorization, 'Bearer changed-fixture-key')
    const failed = studio.loadModels()
    calls[5].resolve(new Response('{"error":{"message":"fixture failure"}}', { status: 503 }))
    await failed
    assert.equal(studio.modelProblem.value.kind, 'http')
    assert.equal(studio.modelsLoaded.value, false)
    assert.deepEqual(studio.models.value, [], 'Failed fetch does not resurrect cached candidates')
  })

  await test('real SFC controls preserve models, 5+3 submissions, cancellation, reuse, download, clipboard and image cleanup', async () => {
    await switchOwner(302)
    const calls = []
    globalThis.fetch = (url, options) => {
      if (url.endsWith('/models')) return Promise.resolve(modelsResponse(modelNames))
      assert.equal(options.method, 'POST')
      const pending = deferred()
      calls.push({ url, options, body: JSON.parse(options.body), ...pending })
      return pending.promise
    }
    const originalCreate = URL.createObjectURL
    const originalRevoke = URL.revokeObjectURL
    const openUrls = new Set()
    const createdCount = ref(0)
    const lastRevokedUrl = ref('')
    URL.createObjectURL = (blob) => {
      const url = originalCreate(blob)
      openUrls.add(url)
      createdCount.value++
      return url
    }
    URL.revokeObjectURL = (url) => { openUrls.delete(url); lastRevokedUrl.value = url; originalRevoke(url) }
    const { app, element } = await mountStudio()
    try {
      uiControl('NButton', (item) => item.text === '上游连接').emit('click')
      await nextTick()
      assert.equal(inputControl('studio-base-url').attrs.placeholder, '填写上游 Base URL')
      inputControl('studio-base-url').emit('update:value', connection.baseUrl)
      inputControl('studio-api-key').emit('update:value', connection.apiKey)
      for (const id of ['studio-base-url', 'studio-api-key']) {
        assert.ok([...bridge.controls].some((item) => item.name === 'NFormItem' && item.props.labelProps?.for === id), `Label targets the native input ${id}`)
      }
      uiControl('NButton', (item) => item.text === '保存连接').emit('click')
      await until(() => studio.connectionSave.value.status === 'saved', 'connection saved through UI')
      uiControl('NDrawer').emit('update:show', false)
      uiControl('NButton', (item) => item.text === '拉取模型').emit('click')
      await until(() => studio.modelsLoaded.value, 'models loaded through UI')
      await nextTick()
      assert.deepEqual(inputControl('studio-request-model').props.options.map((item) => item.value), modelNames.slice(0, 3))
      assert.equal(inputControl('studio-request-model').props.tag, true)
      uiControl('NButton', (item) => item.text.startsWith('展开其他模型')).emit('click')
      await nextTick()
      assert.deepEqual(inputControl('studio-generation-model').props.options.map((item) => item.value), modelNames)
      inputControl('studio-request-model').emit('update:value', 'gpt-image-1')
      uiControl('NButton', (item) => item.text === '收起其他模型').emit('click')
      await nextTick()
      assert.equal(inputControl('studio-request-model').props.value, 'gpt-image-1')
      assert.ok(!inputControl('studio-request-model').props.options.some((item) => item.value === 'gpt-image-1'))

      inputControl('studio-request-model').emit('update:value', 'nai-diffusion-4.5-full')
      inputControl('studio-generation-model').emit('update:value', 'nai-diffusion-5-full')
      inputControl('studio-positive-prompt').emit('update:value', 'first batch apple')
      inputControl('studio-artist-prompt').emit('update:value', '1.15::historic painter::')
      inputControl('studio-negative-prompt').emit('update:value', 'lowres,blurry\ntext,watermark')
      inputControl('studio-cfg').emit('update:value', 5.5)
      inputControl('studio-image-count').emit('update:value', 5)
      await nextTick()
      assert.ok(nodeText(element).includes('"model": "nai-diffusion-4.5-full"'))
      assert.ok(nodeText(element).includes('model:nai-diffusion-5-full'))
      const form = uiControl('NForm', (item) => item.attrs.class === 'studio-parameters')
      const generateButton = uiControl('NButton', (item) => item.text === '开始生成')
      assert.equal(generateButton.props.attrType, 'submit')
      form.emit('submit', { preventDefault() {} })
      assert.equal(calls.length, 5, 'The actual form dispatches all 5 before any response')
      const firstBatch = studio.batchIds.value[0]
      await nextTick()
      const firstBatchCancel = uiControl('NButton', (item) => item.text === '取消本批')
      assert.equal(generateButton.props.disabled, false)
      inputControl('studio-positive-prompt').emit('update:value', 'second batch pear')
      inputControl('studio-request-model').emit('update:value', 'NAI-Test-Model')
      inputControl('studio-image-count').emit('update:value', 3)
      form.emit('submit', { preventDefault() {} })
      assert.equal(calls.length, 8, 'A second form submission starts all 3 while 5 are still in flight')
      assert.equal(studio.activeCount.value, 8)
      await nextTick()
      assert.equal(findNodes(element, (node) => node.class === 'image-status').length, 8, 'Running gallery cards retain their status overlays')
      const secondBatch = studio.batchIds.value[0]
      assert.ok(calls.slice(0, 5).every((call) => call.body.model === 'nai-diffusion-4.5-full' && call.body.messages[0].content.includes('first batch apple')))
      assert.ok(calls.slice(5).every((call) => call.body.model === 'NAI-Test-Model' && call.body.messages[0].content.includes('second batch pear')))
      assert.ok(calls.every((call) => call.body.messages[0].content.includes('model:nai-diffusion-5-full')))
      await nextTick()
      uiControl('NButton', (item) => item.text === '取消' && parentProp(item, 'task')?.batchId === secondBatch).emit('click')
      assert.equal(studio.activeCount.value, 7)
      firstBatchCancel.emit('click')
      assert.equal(studio.activeCount.value, 2)
      assert.ok(studio.tasks.value.filter((task) => task.batchId === firstBatch).every((task) => task.status === 'cancelled'))
      const failIndex = calls.findIndex((call, index) => index >= 5 && !call.options.signal.aborted)
      for (const [index, call] of calls.entries()) {
        const seed = /, seed:([^,]+),/.exec(call.body.messages[0].content)[1]
        call.resolve(index === failIndex ? new Response('{"error":"fixture rate limit"}', { status: 429 }) : chatResponse(asDataUrl(pngBytes(seed))))
      }
      await until(() => studio.activeCount.value === 0 && studio.tasks.value.every((task) => task.save.status === 'saved'), 'UI batch outcomes persisted')
      assert.equal(studio.tasks.value.filter((task) => task.status === 'succeeded').length, 1)
      assert.equal(studio.tasks.value.filter((task) => task.status === 'failed').length, 1)
      assert.equal(calls.length, 8, 'UI failures do not automatically retry')
      await until(() => createdCount.value > 0, 'gallery original loaded')
      const successful = studio.tasks.value.find((task) => task.status === 'succeeded')
      await nextTick()
      const previewToggle = uiControl('NSwitch', (item) => item.attrs['aria-label'] === '预览模糊')
      assert.equal(previewToggle.props.value, false)
      const originalSaveGalleryPreferences = ImageStudioStorage.prototype.saveGalleryPreferences
      const pendingBlurSave = deferred()
      ImageStudioStorage.prototype.saveGalleryPreferences = function (ownerId, preferences) {
        return ownerId === 302 ? pendingBlurSave.promise : originalSaveGalleryPreferences.call(this, ownerId, preferences)
      }
      try {
        previewToggle.emit('update:value', true)
        await nextTick()
        assert.equal(studio.previewBlurred.value, true)
        assert.equal(previewToggle.props.disabled, true, 'The preview switch stays disabled while its preference is saving')
        previewToggle.emit('update:value', false)
        await nextTick()
        assert.equal(studio.previewBlurred.value, true, 'A second toggle cannot race the in-flight preference save')
        pendingBlurSave.resolve()
        await until(() => !studio.previewBlurSaving.value, 'preview preference save settled')
      } finally {
        ImageStudioStorage.prototype.saveGalleryPreferences = originalSaveGalleryPreferences
      }
      assert.ok(findNodes(element, (node) => String(node.class ?? '').split(' ').includes('preview-blurred')).length > 0, 'The gallery card applies the blur state to a loaded preview')
      assert.equal(findNodes(element, (node) => node.class === 'image-status').length, 7, 'Only the successful image loses its status overlay')
      assert.ok([...bridge.controls].some((item) => item.name === 'NTag' && item.text === '已保存' && parentProp(item, 'task')?.id === successful.id), 'The successful image retains its separate save status')
      uiControl('NButton', (item) => item.text === '详情' && parentProp(item, 'task')?.id === successful.id).emit('click')
      await nextTick()
      assert.ok(nodeText(element).includes('PNG 种子与请求一致'))
      assert.ok(nodeText(element).includes(successful.request.submittedSeed))
      const documentDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'document')
      const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
      const clickedLink = ref(null)
      const copiedText = ref('')
      const attachedLinks = new Set()
      Object.defineProperty(globalThis, 'document', {
        configurable: true,
        value: {
          documentElement: { lang: 'zh-CN' },
          body: { appendChild(link) { attachedLinks.add(link) } },
          createElement(tag) {
            assert.equal(tag, 'a')
            return {
              href: '', download: '',
              click() {
                assert.ok(attachedLinks.has(this), 'The download link is attached before clicking')
                clickedLink.value = { href: this.href, filename: this.download }
              },
              remove() { attachedLinks.delete(this) },
            }
          },
        },
      })
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { async writeText(text) { copiedText.value = text } } })
      try {
        uiControl('NButton', (item) => item.text === '下载原图').emit('click')
        await until(() => clickedLink.value !== null, 'detail download invokes the native link')
        const { href, filename } = clickedLink.value
        assert.match(href, /^blob:/)
        assert.equal(filename, `image-${successful.request.submittedSeed}.png`)
        const original = await originalFetch(href)
        assert.deepEqual(Buffer.from(await original.arrayBuffer()), pngBytes(successful.request.submittedSeed))
        await until(() => lastRevokedUrl.value === href, 'download URL released after its click')
        assert.equal(attachedLinks.size, 0)
        uiControl('NButton', (item) => item.attrs['aria-label'] === '复制请求种子').emit('click')
        await until(() => copiedText.value === successful.request.submittedSeed, 'detail copies the submitted seed')
        assert.equal(calls.length, 8, 'Download and copy do not generate another image')
      } finally {
        if (documentDescriptor) Object.defineProperty(globalThis, 'document', documentDescriptor)
        else delete globalThis.document
        if (clipboardDescriptor) Object.defineProperty(navigator, 'clipboard', clipboardDescriptor)
        else delete navigator.clipboard
      }
      previewToggle.emit('update:value', false)
      await nextTick()
      assert.equal(findNodes(element, (node) => String(node.class ?? '').split(' ').includes('preview-blurred')).length, 0)
      assert.deepEqual(await storage.getGalleryPreferences(302), { previewBlurred: false })
      uiControl('NButton', (item) => item.text === '复用设置').emit('click')
      await nextTick()
      assert.equal(studio.draft.value.seed, successful.image.returnedSeed)
      assert.equal(studio.draft.value.model, 'NAI-Test-Model')
      assert.equal(studio.draft.value.generationModel, 'nai-diffusion-5-full')
      assert.equal(studio.draft.value.count, 1)
      uiControl('NButton', (item) => item.text === '复用' && parentProp(item, 'task')?.id === successful.id).emit('click')
      await nextTick()
      assert.equal(studio.draft.value.artistPrompt, '1.15::historic painter::')
      assert.equal(studio.draft.value.positivePrompt, 'second batch pear')
      assert.ok(!JSON.stringify(studio.tasks.value).includes(connection.apiKey))
      setLanguage('en')
      await nextTick()
      assert.ok(nodeText(element).includes('Image Studio'))
      assert.ok(nodeText(element).includes('Generation settings'))
    } finally {
      app.unmount()
      assert.equal(bridge.controls.size, 0, 'All UI instances unmount cleanly')
      assert.equal(openUrls.size, 0, 'Gallery and detail release their object URLs')
      URL.createObjectURL = originalCreate
      URL.revokeObjectURL = originalRevoke
    }
  })

  await test('built-in negative presets preserve exact text, empty None requests, read-only choices and account-local copies', async () => {
    const expected = [
      ['Heavy', 'lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blankpage'],
      ['Light', 'lowres, bad hands, bad anatomy, artistic error, sepia, white haze, worst quality, very displeasing, jpeg artifacts, 0::ai-generated::'],
      ['Furry Focus', '{worst quality}, distracting watermark, unfinished, bad quality, {widescreen}, upscale, {sequence}, {{grandfathered content}}, blurred foreground, chromaticaberration, sketch, everyone, [sketchbackground], simple, [flat colors], ych(character), outline, multiple scenes, [[horror(theme)]], comic'],
      ['Human Focus', 'lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blank page, @_@, mismatched pupils, glowing eyes, bad anatomy'],
      ['None', ''],
    ]
    const personalId = '11111111111111111111111111111111'
    const artistId = '22222222222222222222222222222222'
    const savedNegative = 'keep my saved negative, custom'
    await storage.saveDraft(309, { ...draft(), negativePrompt: savedNegative, seed: '123' })
    await storage.savePreset(309, { id: personalId, ownerId: 309, kind: 'negative', name: 'Heavy', text: 'my personal negative' })
    await storage.savePreset(309, { id: artistId, ownerId: 309, kind: 'artist', name: 'Personal artist', text: '1.2::watercolor::' })
    await storage.saveDraft(310, { ...draft(), negativePrompt: 'account 310 saved negative' })
    await storage.savePreset(310, { id: personalId, ownerId: 310, kind: 'negative', name: 'Heavy', text: 'account 310 personal negative' })
    await switchOwner(309)
    const calls = []
    globalThis.fetch = async (_url, options) => {
      assert.equal(options.method, 'POST')
      calls.push(JSON.parse(options.body))
      return chatResponse(asDataUrl(pngBytes('123')))
    }
    const { app } = await mountStudio()
    try {
      const negativeSelect = uiControl('NSelect', (item) => parentProp(item, 'kind') === 'negative')
      const artistSelect = uiControl('NSelect', (item) => parentProp(item, 'kind') === 'artist')
      const builtins = () => negativeSelect.props.options.find((group) => group.key === 'builtin-negative').children
      const personal = () => negativeSelect.props.options.find((group) => group.key === 'personal-negative').children
      const editButton = uiControl('NButton', (item) => item.text === '编辑' && parentProp(item, 'kind') === 'negative')
      const copyButton = uiControl('NButton', (item) => item.text === '另存预设' && parentProp(item, 'kind') === 'negative')
      const editor = uiControl('NModal', (item) => parentProp(item, 'kind') === 'negative')
      const builtinIds = builtins().map((option) => option.value)
      assert.equal(negativeSelect.props.value, null, 'There is no default preset selection')
      assert.equal(inputControl('studio-negative-prompt').props.value, savedNegative, 'Mounting the picker does not overwrite saved text')
      assert.deepEqual(negativeSelect.props.options.map((group) => group.label), ['内置预设', '个人预设'])
      assert.deepEqual(builtins().map((option) => option.label), expected.map(([name]) => name))
      assert.equal(new Set(builtinIds).size, 5)
      assert.ok(builtinIds.every((id) => id.startsWith('builtin-negative:')))
      assert.deepEqual(artistSelect.props.options, [{ label: 'Personal artist', value: artistId }], 'Artist choices do not include negative built-ins')
      assert.deepEqual(personal(), [{ label: 'Heavy', value: personalId }], 'An identically named personal preset remains distinct')
      studio.connection.value = { ...connection }
      const form = uiControl('NForm', (item) => item.attrs.class === 'studio-parameters')
      for (const [index, [name, text]] of expected.entries()) {
        negativeSelect.emit('update:value', builtinIds[index])
        await nextTick()
        assert.equal(inputControl('studio-negative-prompt').props.value, text, `${name} retains the provided spelling, spaces and weights`)
        assert.equal(editButton.props.disabled, true)
        editButton.emit('click')
        await nextTick()
        assert.equal(editor.props.show, false, 'Built-ins cannot open the personal editor')
        assert.equal(inputControl('studio-negative-preset-name'), undefined)
        assert.ok(![...bridge.controls].some((item) => item.name === 'NButton' && item.text === '删除预设' && parentProp(item, 'kind') === 'negative'))
        form.emit('submit', { preventDefault() {} })
        assert.equal(calls.length, index + 1)
        assert.ok(calls[index].messages[0].content.endsWith(`negative_prompt:${text}}`), `${name} is serialized verbatim`)
        const task = studio.tasks.value.find((item) => item.batchId === studio.batchIds.value[0])
        assert.equal(task.input.negativePrompt, text)
      }
      assert.ok(calls[4].messages[0].content.endsWith('negative_prompt:}'))
      assert.ok(!calls[4].messages[0].content.includes('negative_prompt:None'))
      await until(() => studio.activeCount.value === 0 && studio.tasks.value.every((task) => task.save.status === 'saved'), 'built-in requests saved')
      assert.equal((await storage.listPresets(309)).length, 2, 'Selecting built-ins never persists them as personal presets')

      negativeSelect.emit('update:value', personalId)
      await nextTick()
      assert.equal(inputControl('studio-negative-prompt').props.value, 'my personal negative')
      assert.equal(editButton.props.disabled, false)
      negativeSelect.emit('update:value', builtinIds[0])
      await nextTick()
      assert.equal(copyButton.props.disabled, false)
      copyButton.emit('click')
      await nextTick()
      assert.equal(inputControl('studio-negative-preset-text').props.value, expected[0][1])
      assert.ok(![...bridge.controls].some((item) => item.name === 'NButton' && item.text === '删除预设' && parentProp(item, 'kind') === 'negative'), 'A new personal copy cannot delete the built-in')
      inputControl('studio-negative-preset-name').emit('update:value', 'My Heavy copy')
      uiControl('NButton', (item) => item.text === '保存' && parentProp(item, 'kind') === 'negative').emit('click')
      await until(() => studio.presets.value.length === 3 && !editor.props.show, 'personal copy saved')
      await nextTick()
      const copy = studio.presets.value.find((preset) => preset.name === 'My Heavy copy')
      assert.match(copy.id, /^[a-f0-9]{32}$/)
      assert.ok(!builtinIds.includes(copy.id))
      assert.equal(copy.text, expected[0][1])
      assert.equal(negativeSelect.props.value, copy.id)
      assert.equal(editButton.props.disabled, false)
      const customText = `${copy.text}, custom addition`
      inputControl('studio-negative-prompt').emit('update:value', customText)
      await nextTick()
      editButton.emit('click')
      await nextTick()
      assert.equal(inputControl('studio-negative-preset-text').props.value, customText)
      uiControl('NButton', (item) => item.text === '保存' && parentProp(item, 'kind') === 'negative').emit('click')
      await until(() => studio.presets.value.find((preset) => preset.id === copy.id)?.text === customText && !editor.props.show, 'personal copy edited')
      await nextTick()
      negativeSelect.emit('update:value', builtinIds[0])
      await nextTick()
      assert.equal(inputControl('studio-negative-prompt').props.value, expected[0][1], 'Editing the copy cannot change Heavy')
      negativeSelect.emit('update:value', copy.id)
      await nextTick()
      editButton.emit('click')
      await nextTick()
      uiControl('NButton', (item) => item.text === '删除预设' && parentProp(item, 'kind') === 'negative').emit('click')
      await bridge.dialogs.at(-1).onPositiveClick()
      await nextTick()
      assert.deepEqual(builtins().map((option) => option.value), builtinIds)
      assert.deepEqual(personal(), [{ label: 'Heavy', value: personalId }])
      assert.equal((await storage.listPresets(309)).length, 2)

      await switchOwner(310)
      assert.equal(negativeSelect.props.value, null)
      assert.equal(inputControl('studio-negative-prompt').props.value, 'account 310 saved negative')
      assert.deepEqual(builtins().map((option) => option.value), builtinIds, 'Built-in IDs are stable across accounts')
      assert.deepEqual(artistSelect.props.options, [])
      negativeSelect.emit('update:value', personalId)
      await nextTick()
      assert.equal(inputControl('studio-negative-prompt').props.value, 'account 310 personal negative')
      negativeSelect.emit('update:value', builtinIds[4])
      await nextTick()
      assert.equal(studio.draft.value.negativePrompt, '')
      await switchOwner(309)
      assert.equal(negativeSelect.props.value, null)
      assert.equal(studio.draft.value.negativePrompt, savedNegative, 'Other-account selection cannot change the saved draft')
      assert.equal((await storage.listPresets(309)).find((preset) => preset.id === personalId).text, 'my personal negative')
      setLanguage('en')
      await nextTick()
      assert.deepEqual(negativeSelect.props.options.map((group) => group.label), ['Built-in presets', 'Personal presets'])
      assert.deepEqual(builtins().map((option) => option.label), expected.map(([name]) => name))
      assert.equal(calls.length, 5, 'Preset edits and account switches issue no generation requests')
    } finally {
      app.unmount()
      setCurrentUser(null)
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('tag market browses categories, searches both languages, preserves selections and cancels without editing', async () => {
    await switchOwner(311)
    studio.draft.value = { ...draft(), positivePrompt: '1.2::portrait::,\ncat, watercolor' }
    const original = { ...studio.draft.value }
    globalThis.fetch = async () => { assert.fail('Browsing tags must not issue upstream requests') }
    const { app, element } = await mountStudio()
    try {
      const open = async () => {
        uiControl('NButton', (item) => item.text === '标签市场').emit('click')
        await nextTick()
      }
      const market = () => uiControl('NModal', (item) => item.props.title === '标签市场')
      const action = (label) => uiControl('NButton', (item) => item.text === label)
      const tag = (label) => uiControl('NButton', (item) => item.attrs['aria-label'] === label)
      await open()
      assert.equal(market().props.show, true)
      assert.equal(inputControl('studio-tag-search').props.value, '')
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      assert.equal(action('追加到提示词').props.disabled, true)
      assert.equal(action('覆盖提示词').props.disabled, true)
      action('追加到提示词').emit('click')
      action('覆盖提示词').emit('click')
      assert.deepEqual(studio.draft.value, original, 'Empty selections cannot erase or update a prompt')
      assert.equal(market().props.show, true)

      uiControl('NButton', (item) => item.text.startsWith('场景环境') && typeof item.attrs['aria-pressed'] === 'boolean').emit('click')
      await nextTick()
      tag('森林（forest）').emit('click')
      await nextTick()
      assert.equal(tag('森林（forest）').attrs['aria-pressed'], true)
      assert.ok(nodeText(element).includes('已选 1 个标签'))
      inputControl('studio-tag-search').emit('update:value', 'WATERColor')
      await nextTick()
      tag('水彩画（watercolor (medium)）').emit('click')
      await nextTick()
      assert.ok(nodeText(element).includes('已选 2 个标签'), 'Search spans categories and retains earlier choices')
      assert.ok(tag('取消选择：森林'))
      inputControl('studio-tag-search').emit('update:value', '单人画面')
      await nextTick()
      tag('单人画面（solo）').emit('click')
      await nextTick()
      uiControl('NButton', (item) => item.text.startsWith('人数与主体') && typeof item.attrs['aria-pressed'] === 'boolean').emit('click')
      await nextTick()
      assert.equal(inputControl('studio-tag-search').props.value, '')
      assert.equal(tag('单人画面（solo）').attrs['aria-pressed'], true)
      tag('单人画面（solo）').emit('click')
      tag('取消选择：森林').emit('click')
      await nextTick()
      assert.ok(nodeText(element).includes('已选 1 个标签'), 'Either the catalog or selected list can deselect a tag')
      assert.deepEqual(studio.draft.value, original, 'Selections remain provisional')
      action('清空选择').emit('click')
      await nextTick()
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      assert.equal(action('追加到提示词').props.disabled, true)
      inputControl('studio-tag-search').emit('update:value', 'not-a-catalog-term')
      await nextTick()
      assert.ok(uiControl('NEmpty', (item) => item.attrs.description === '没有找到标签，试试其他关键词'))
      inputControl('studio-tag-search').emit('update:value', 'forest')
      await nextTick()
      tag('森林（forest）').emit('click')
      action('取消').emit('click')
      await nextTick()
      assert.equal(market().props.show, false)
      assert.deepEqual(studio.draft.value, original)
      await open()
      assert.equal(inputControl('studio-tag-search').props.value, '')
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      assert.equal(tag('森林（forest）').attrs['aria-pressed'], false)
      assert.equal(uiControl('NButton', (item) => item.text.startsWith('全部分类')).attrs['aria-pressed'], true)
      setLanguage('en')
      await nextTick()
      assert.ok(nodeText(element).includes('0 tags selected'))
      assert.ok(uiControl('NButton', (item) => item.text.startsWith('Style and details')))
      assert.ok(tag('watercolor (medium)'))
      assert.equal(inputControl('studio-tag-search').props.inputProps['aria-label'], 'Search tags')
      assert.ok(action('Append to prompt'))
      assert.ok(action('Replace prompt'))
    } finally {
      app.unmount()
      setCurrentUser(null)
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('tag favorites persist per account and custom favorites remain selectable prompt terms', async () => {
    await switchOwner(323)
    studio.draft.value = { ...draft(), positivePrompt: 'existing favorite prompt' }
    globalThis.fetch = async () => { assert.fail('Tag favorites must not issue upstream requests') }
    const { app } = await mountStudio()
    try {
      const button = (label) => uiControl('NButton', (item) => item.text === label)
      const category = (label) => uiControl('NButton', (item) => item.text.startsWith(label) && typeof item.attrs['aria-pressed'] === 'boolean')
      const hasTag = (prompt) => [...bridge.controls].some((item) => item.name === 'NButton' && item.attrs['aria-label']?.endsWith(`（${prompt}）`))
      const tag = (prompt) => uiControl('NButton', (item) => item.attrs['aria-label']?.endsWith(`（${prompt}）`))
      const favorite = (label) => uiControl('NButton', (item) => item.attrs['aria-label'] === label)
      const open = async () => { button('标签市场').emit('click'); await nextTick() }

      await open()
      assert.equal(category('我的收藏').text, '我的收藏0')
      favorite('收藏：森林').emit('click')
      await until(() => studio.tagFavorites.value.length === 1, 'catalog tag favorited')
      assert.deepEqual(studio.tagFavorites.value.map((item) => [item.id, item.prompt]), [['scene-forest', 'forest']])
      category('我的收藏').emit('click')
      await nextTick()
      assert.ok(tag('forest'))
      assert.ok(favorite('取消收藏：森林'))

      inputControl('studio-custom-favorite').emit('update:value', 'silver moon portrait')
      button('添加').emit('click')
      await until(() => studio.tagFavorites.value.length === 2, 'custom tag favorited')
      await nextTick()
      assert.ok(tag('silver moon portrait'))
      assert.ok(studio.tagFavorites.value.some((item) => item.custom === true && item.prompt === 'silver moon portrait'))
      tag('forest').emit('click')
      tag('silver moon portrait').emit('click')
      button('追加到提示词').emit('click')
      await nextTick()
      assert.equal(studio.draft.value.positivePrompt, 'existing favorite prompt, forest, silver moon portrait')

      await switchOwner(324)
      assert.deepEqual(studio.tagFavorites.value, [])
      await open()
      assert.equal(category('我的收藏').text, '我的收藏0')
      await switchOwner(323)
      assert.deepEqual(studio.tagFavorites.value.map((item) => item.prompt), ['forest', 'silver moon portrait'])
      await open()
      category('我的收藏').emit('click')
      await nextTick()
      tag('forest').emit('click')
      favorite('取消收藏：森林').emit('click')
      await until(() => studio.tagFavorites.value.length === 1, 'catalog favorite removed')
      assert.equal(hasTag('forest'), false)
      assert.ok(favorite('取消选择：森林'), 'Removing a favorite keeps its current transient selection')
      assert.ok(tag('silver moon portrait'))
      favorite('取消收藏：silver moon portrait').emit('click')
      await until(() => studio.tagFavorites.value.length === 0, 'custom favorite removed')
      assert.equal(category('我的收藏').text, '我的收藏0')
      assert.ok(uiControl('NEmpty', (item) => item.attrs.description === '没有找到标签，试试其他关键词'))
    } finally {
      app.unmount()
      setCurrentUser(null)
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('restore cleared merges one clear operation without undoing individual removals or leaking hidden tags', async () => {
    await switchOwner(321)
    studio.draft.value.positivePrompt = 'untouched prompt'
    const { app, element } = await mountStudio()
    try {
      const button = (label) => uiControl('NButton', (item) => item.text === label)
      const tag = (prompt) => uiControl('NButton', (item) => item.attrs['aria-label']?.endsWith(`（${prompt}）`))
      const open = async () => { button('标签市场').emit('click'); await nextTick() }
      const selected = (label) => [...bridge.controls].some((item) => item.attrs['aria-label'] === `取消选择：${label}`)
      await open()
      assert.equal(button('复原清空').props.disabled, true)
      tag('forest').emit('click')
      tag('watercolor (medium)').emit('click')
      await nextTick()
      tag('forest').emit('click')
      await nextTick()
      assert.equal(button('复原清空').props.disabled, true, 'Removing an individual tag creates no undo history')
      button('清空选择').emit('click')
      await nextTick()
      assert.equal(button('复原清空').props.disabled, false)
      tag('watercolor (medium)').emit('click')
      tag('solo').emit('click')
      await nextTick()
      button('复原清空').emit('click')
      await nextTick()
      assert.ok(selected('水彩画') && selected('单人画面'))
      assert.equal(selected('森林'), false, 'An earlier individual removal is not restored')
      assert.ok(nodeText(element).includes('已选 2 个标签'), 'Restoration merges without duplicate selections')
      assert.equal(button('复原清空').props.disabled, true, 'A successful restore consumes its backup')
      for (const removeFromSelection of [false, true]) {
        button('清空选择').emit('click')
        await nextTick()
        tag('watercolor (medium)').emit('click')
        await nextTick()
        if (removeFromSelection) uiControl('NButton', (item) => item.attrs['aria-label'] === '取消选择：水彩画').emit('click')
        else tag('watercolor (medium)').emit('click')
        await nextTick()
        button('复原清空').emit('click')
        await nextTick()
        assert.equal(selected('水彩画'), false, 'Restoration must not undo a manual removal after reselecting a cleared tag')
        assert.ok(selected('单人画面'), 'Other cleared tags remain restorable')
        tag('watercolor (medium)').emit('click')
        await nextTick()
      }
      button('清空选择').emit('click')
      await nextTick()
      tag('forest').emit('click')
      await nextTick()
      button('清空选择').emit('click')
      await nextTick()
      button('复原清空').emit('click')
      await nextTick()
      assert.ok(selected('森林'))
      assert.equal(selected('水彩画'), false, 'Only the latest clear operation is restored')
      uiControl('NSwitch', (item) => item.attrs['aria-label'] === '显示 NSFW 标签').emit('update:value', true)
      await nextTick()
      tag('ahegao').emit('click')
      await nextTick()
      button('清空选择').emit('click')
      await nextTick()
      uiControl('NSwitch', (item) => item.attrs['aria-label'] === '显示 NSFW 标签').emit('update:value', false)
      await nextTick()
      uiControl('NSwitch', (item) => item.attrs['aria-label'] === '显示 NSFW 标签').emit('update:value', true)
      await nextTick()
      button('复原清空').emit('click')
      await nextTick()
      assert.ok(selected('森林'))
      assert.equal(selected('高潮脸'), false, 'Turning NSFW off also removes those terms from the clear backup')
      button('清空选择').emit('click')
      await nextTick()
      button('取消').emit('click')
      await nextTick()
      await open()
      assert.equal(button('复原清空').props.disabled, true)
      tag('solo').emit('click')
      await nextTick()
      button('清空选择').emit('click')
      await nextTick()
      await switchOwner(322)
      await open()
      assert.equal(button('复原清空').props.disabled, true)
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      await switchOwner(321)
      assert.equal(studio.draft.value.positivePrompt, '', 'No selection operation persists a draft')
    } finally {
      app.unmount()
      setCurrentUser(null)
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('subcategory layouts and NSFW visibility keep counts, filters, search, selections and owner resets consistent', async () => {
    const marketSource = await readFile(path.join(root, 'src/features/image-studio/components/StudioTagMarket.vue'), 'utf8')
    assert.match(marketSource, /\.market-subcategories\.is-flat\s*\{[^}]*display:\s*flex;[^}]*flex-wrap:\s*wrap;/s)
    await switchOwner(319)
    studio.draft.value.positivePrompt = 'existing user text'
    const { app, element } = await mountStudio()
    try {
      const button = (label) => uiControl('NButton', (item) => item.text === label)
      const hasTag = (prompt) => [...bridge.controls].some((item) => item.name === 'NButton' && item.attrs['aria-label']?.endsWith(`（${prompt}）`))
      const tag = (prompt) => uiControl('NButton', (item) => item.attrs['aria-label']?.endsWith(`（${prompt}）`))
      const toggle = () => uiControl('NSwitch', (item) => item.attrs['aria-label'] === '显示 NSFW 标签')
      const appearance = () => uiControl('NButton', (item) => item.text.startsWith('人物特征') && typeof item.attrs['aria-pressed'] === 'boolean')
      const category = (label) => uiControl('NButton', (item) => item.text.startsWith(label) && typeof item.attrs['aria-pressed'] === 'boolean')
      const hasNsfwCategory = () => [...bridge.controls].some((item) => item.name === 'NButton' && item.text === 'NSFW84')
      const usesFlatSubcategories = () => findNodes(element, (node) => node.tag === 'nav' && String(node.class ?? '').split(' ').includes('market-subcategories'))
        .some((node) => String(node.class ?? '').split(' ').includes('is-flat'))
      const open = async () => { button('标签市场').emit('click'); await nextTick() }
      await open()
      assert.equal(toggle().props.value, false)
      assert.equal(appearance().text, '人物特征500')
      assert.equal(hasTag('ahegao'), false)
      assert.equal(hasNsfwCategory(), false)
      appearance().emit('click')
      await nextTick()
      assert.equal(usesFlatSubcategories(), false, 'Appearance keeps its parent and child group rows')
      category('场景环境').emit('click')
      await nextTick()
      assert.equal(usesFlatSubcategories(), true, 'Sibling scene sections share one wrapping row')
      assert.ok(button('全部小分类'))
      button('季节与节日').emit('click')
      await nextTick()
      assert.ok(hasTag('tea ceremony'))
      assert.equal(hasTag('forest'), false)
      tag('tea ceremony').emit('click')
      category('构图与镜头').emit('click')
      await nextTick()
      assert.equal(usesFlatSubcategories(), true, 'Sibling composition sections share one wrapping row')
      button('镜头景深').emit('click')
      await nextTick()
      assert.ok(hasTag('bokeh'))
      assert.equal(hasTag('soft focus'), false)
      category('风格与细节').emit('click')
      await nextTick()
      assert.equal(usesFlatSubcategories(), true, 'Sibling style sections share one wrapping row')
      button('媒介与技法').emit('click')
      await nextTick()
      assert.ok(hasTag('watercolor (medium)'))
      assert.equal(hasTag('warm colors'), false)
      button('质量').emit('click')
      await nextTick()
      assert.ok(hasTag('masterpiece'))
      assert.equal(hasTag('watercolor (medium)'), false)
      button('画风').emit('click')
      await nextTick()
      assert.ok(hasTag('warm colors') && hasTag('anime style'))
      assert.equal(hasTag('masterpiece'), false)
      category('IP').emit('click')
      await nextTick()
      assert.equal(category('IP').text, 'IP345')
      assert.equal(usesFlatSubcategories(), true, 'Sibling IP sections share one wrapping row')
      const ipFilters = [
        ['原神', 'Sigewinne (genshin impact)'],
        ['崩坏：星穹铁道', 'Sparkle (Honkai: Star Rail)'],
        ['绝区零', 'Hoshimi Miyabi (Zenless Zone Zero)'],
        ['鸣潮', 'The Shorekeeper (Wuthering Waves)'],
      ]
      for (const [label, prompt] of ipFilters) {
        button(label).emit('click')
        await nextTick()
        assert.ok(hasTag(prompt), `${label} exposes its own character terms`)
        for (const [, otherPrompt] of ipFilters) {
          if (otherPrompt !== prompt) assert.equal(hasTag(otherPrompt), false, `${label} filters out characters from sibling IP sections`)
        }
      }
      button('原神').emit('click')
      await nextTick()
      tag('Sigewinne (genshin impact)').emit('click')
      inputControl('studio-tag-search').emit('update:value', '花火')
      await nextTick()
      assert.ok(hasTag('Sparkle (Honkai: Star Rail)'), 'Chinese search reaches IP character terms')
      tag('Sparkle (Honkai: Star Rail)').emit('click')
      inputControl('studio-tag-search').emit('update:value', 'The Rooster')
      await nextTick()
      assert.ok(hasTag('Pulcinella (genshin impact)'), 'English aliases are searchable without becoming selectable prompt terms')
      assert.equal(hasTag('The Rooster (genshin impact)'), false)
      inputControl('studio-tag-search').emit('update:value', '珍珠')
      await nextTick()
      assert.ok(hasTag('Pearl (Honkai: Star Rail)'), 'Chinese aliases are searchable without replacing the official label')
      button('清空选择').emit('click')
      await nextTick()
      toggle().emit('update:value', true)
      await nextTick()
      category('NSFW').emit('click')
      await nextTick()
      assert.equal(usesFlatSubcategories(), true, 'Sibling NSFW sections share one wrapping row')
      assert.ok(hasTag('ahegao'))
      assert.equal(hasTag('smile'), false)
      for (const [label, term] of [['裸露状态', 'nude'], ['姿势', 'leg lift'], ['行为', 'kissing'], ['体液与状态', 'saliva']]) {
        button(label).emit('click')
        await nextTick()
        assert.ok(hasTag(term), `The ${label} subcategory exposes its reference tag`)
        assert.equal(hasTag('forest'), false)
        tag(term).emit('click')
        await nextTick()
      }
      assert.ok(nodeText(element).includes('已选 4 个标签'))
      button('清空选择').emit('click')
      await nextTick()
      assert.equal(button('复原清空').props.disabled, false)
      toggle().emit('update:value', false)
      await nextTick()
      assert.equal(hasNsfwCategory(), false)
      assert.equal(button('复原清空').props.disabled, true, 'Disabling NSFW removes the cleared backup from every new subcategory')
      for (const term of ['nude', 'leg lift', 'kissing', 'saliva']) assert.equal(hasTag(term), false)
      assert.equal(category('全部分类').attrs['aria-pressed'], true, 'Disabling the active NSFW category returns to All categories')
      assert.ok(hasTag('forest'))
      appearance().emit('click')
      await nextTick()
      button('头发').emit('click')
      await nextTick()
      assert.ok(hasTag('roots'))
      assert.equal(hasTag('smile'), false)
      button('发色 (32)').emit('click')
      await nextTick()
      assert.ok(hasTag('split-color hair'))
      assert.equal(hasTag('ponytail'), false)
      tag('roots').emit('click')
      button('脸部').emit('click')
      await nextTick()
      assert.ok(hasTag('makeup'))
      assert.ok(nodeText(element).includes('已选 1 个标签'))
      button('服饰').emit('click')
      await nextTick()
      assert.ok(hasTag('school uniform') && hasTag('cat ears') && hasTag('cap') && hasTag('boots'))
      button('头部穿戴 (26)').emit('click')
      await nextTick()
      assert.ok(hasTag('cap'))
      assert.equal(hasTag('boots'), false)
      button('姿态动作').emit('click')
      await nextTick()
      assert.ok(hasTag('knees together'))
      assert.equal(hasTag('cap'), false)
      button('身份').emit('click')
      await nextTick()
      assert.ok(hasTag('college student') && hasTag('goddess'))
      inputControl('studio-tag-search').emit('update:value', 'AHEGAO')
      await nextTick()
      assert.equal(hasTag('ahegao'), false)
      toggle().emit('update:value', true)
      await nextTick()
      assert.equal(appearance().text, '人物特征500')
      assert.ok(hasTag('ahegao'))
      tag('ahegao').emit('click')
      await nextTick()
      assert.ok(nodeText(element).includes('已选 2 个标签'))
      toggle().emit('update:value', false)
      await nextTick()
      assert.equal(hasTag('ahegao'), false)
      assert.ok(nodeText(element).includes('已选 1 个标签'))
      button('追加到提示词').emit('click')
      await nextTick()
      assert.equal(studio.draft.value.positivePrompt, 'existing user text, roots')
      await open()
      toggle().emit('update:value', true)
      inputControl('studio-tag-search').emit('update:value', '高潮脸')
      await nextTick()
      assert.ok(hasTag('ahegao'), 'Chinese search reaches enabled NSFW terms')
      tag('ahegao').emit('click')
      button('追加到提示词').emit('click')
      await nextTick()
      const applied = studio.draft.value.positivePrompt
      await open()
      assert.equal(toggle().props.value, false)
      assert.equal(studio.draft.value.positivePrompt, applied, 'Resetting visibility never edits existing prompt content')
      toggle().emit('update:value', true)
      await nextTick()
      tag('ahegao').emit('click')
      await switchOwner(320)
      await open()
      assert.equal(toggle().props.value, false)
      assert.equal(hasTag('ahegao'), false)
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      setLanguage('en')
      await nextTick()
      uiControl('NButton', (item) => item.text.startsWith('Appearance') && typeof item.attrs['aria-pressed'] === 'boolean').emit('click')
      await nextTick()
      assert.ok(button('Hair'))
      assert.ok(button('Face'))
      assert.ok(button('Facial details and makeup (21)'))
      assert.equal(uiControl('NSwitch', (item) => item.attrs['aria-label'] === 'Show NSFW tags').attrs['aria-label'], 'Show NSFW tags')
    } finally {
      app.unmount()
      setCurrentUser(null)
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('tag market applies explicit append or replace only to the positive prompt and resets on account changes', async () => {
    const saved = { ...draft(), positivePrompt: '1.2::portrait::,\nsolo, watercolor,', seed: '456' }
    const nextAccount = { ...draft(), positivePrompt: 'next account prompt', artistPrompt: 'next artist', negativePrompt: 'next negative' }
    await storage.saveDraft(312, saved)
    await storage.saveDraft(313, nextAccount)
    await switchOwner(312)
    studio.connection.value = { ...connection }
    globalThis.fetch = async () => { assert.fail('Applying tags must not submit a generation request') }
    const { app, element } = await mountStudio()
    try {
      const open = async () => {
        uiControl('NButton', (item) => item.text === '标签市场').emit('click')
        await nextTick()
      }
      const market = () => uiControl('NModal', (item) => item.props.title === '标签市场')
      const action = (label) => uiControl('NButton', (item) => item.text === label)
      const tag = (label) => uiControl('NButton', (item) => item.attrs['aria-label'] === label)
      await open()
      tag('单人画面（solo）').emit('click')
      tag('森林（forest）').emit('click')
      await nextTick()
      action('追加到提示词').emit('click')
      await nextTick()
      assert.equal(market().props.show, false)
      assert.deepEqual(studio.draft.value, { ...saved, positivePrompt: `${saved.positivePrompt} forest` }, 'Append keeps weighted and multiline text, joins its trailing comma and skips a duplicate solo')
      const preview = JSON.parse(findNodes(element, (node) => node.class === 'request-preview')[0].text)
      assert.ok(preview.messages[0].content.startsWith(`${saved.artistPrompt}, ${saved.positivePrompt} forest Parameter{`))
      assert.ok(preview.messages[0].content.endsWith(`negative_prompt:${saved.negativePrompt}}`))

      await open()
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      tag('油画质感（oil painting (medium)）').emit('click')
      tag('背景散景（bokeh）').emit('click')
      await nextTick()
      const dialogCount = bridge.dialogs.length
      action('覆盖提示词').emit('click')
      await nextTick()
      assert.equal(bridge.dialogs.length, dialogCount + 1)
      assert.equal(market().props.show, true, 'Replace waits for confirmation when the prompt is not empty')
      await bridge.dialogs.at(-1).onPositiveClick()
      await nextTick()
      assert.equal(market().props.show, false)
      assert.deepEqual(studio.draft.value, { ...saved, positivePrompt: 'oil painting (medium), bokeh' }, 'Replace emits the selected English tags in selection order and changes no other field')

      await open()
      tag('花田（flower field）').emit('click')
      action('覆盖提示词').emit('click')
      const staleConfirmation = bridge.dialogs.at(-1)
      market().emit('update:show', false)
      await nextTick()
      await open()
      await staleConfirmation.onPositiveClick()
      await nextTick()
      assert.equal(market().props.show, true)
      assert.equal(studio.draft.value.positivePrompt, 'oil painting (medium), bokeh', 'A confirmation from a closed modal session cannot replace a reopened prompt')
      market().emit('update:show', false)
      await nextTick()

      await open()
      tag('花田（flower field）').emit('click')
      inputControl('studio-tag-search').emit('update:value', 'flower')
      await nextTick()
      const oldAppend = action('追加到提示词')
      await switchOwner(313)
      assert.equal(market().props.show, false)
      assert.equal(inputControl('studio-tag-search'), undefined)
      oldAppend.emit('click')
      await nextTick()
      assert.deepEqual(studio.draft.value, nextAccount, 'A stale selection cannot write into the new account')
      await open()
      assert.equal(inputControl('studio-tag-search').props.value, '')
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      tag('单人画面（solo）').emit('click')
      market().emit('update:show', false)
      await nextTick()
      assert.deepEqual(studio.draft.value, nextAccount, 'The modal close/Escape event discards pending selection')
      await open()
      assert.equal(tag('单人画面（solo）').attrs['aria-pressed'], false)
      assert.ok(nodeText(element).includes('已选 0 个标签'))
      await switchOwner(312)
      assert.equal(market().props.show, false)
      assert.deepEqual(studio.draft.value, saved, 'Another account never overwrites this account’s saved prompt')
    } finally {
      app.unmount()
      setCurrentUser(null)
      assert.equal(bridge.controls.size, 0)
    }
  })

  await test('real preset editor preserves weighted multiline text and suppresses delayed feedback after account changes', async () => {
    await switchOwner(303)
    globalThis.fetch = async () => { assert.fail('Preset editing must not issue upstream requests') }
    const { app } = await mountStudio()
    const originalSave = ImageStudioStorage.prototype.savePreset
    try {
      const originalText = '1.15::historic painter::,\nwatercolor, detailed'
      inputControl('studio-artist-prompt').emit('update:value', originalText)
      await nextTick()
      uiControl('NButton', (item) => item.text === '另存预设' && parentProp(item, 'kind') === 'artist').emit('click')
      await nextTick()
      inputControl('studio-artist-preset-name').emit('update:value', 'Personal watercolor')
      uiControl('NButton', (item) => item.text === '保存' && parentProp(item, 'kind') === 'artist').emit('click')
      await until(() => studio.presets.value.length === 1, 'preset created')
      const artistEditor = uiControl('NModal', (item) => parentProp(item, 'kind') === 'artist')
      await until(() => !artistEditor.props.show, 'preset editor finished saving')
      await nextTick()
      assert.equal(studio.presets.value[0].text, originalText)
      const updatedText = `${originalText}\n1.2::soft lighting::`
      inputControl('studio-artist-prompt').emit('update:value', updatedText)
      await nextTick()
      uiControl('NButton', (item) => item.text === '编辑' && parentProp(item, 'kind') === 'artist').emit('click')
      await nextTick()
      assert.equal(inputControl('studio-artist-preset-text').props.value, updatedText)
      uiControl('NButton', (item) => item.text === '保存' && parentProp(item, 'kind') === 'artist').emit('click')
      await until(() => studio.presets.value[0].text === updatedText, 'preset edited')
      await until(() => !artistEditor.props.show, 'preset editor finished updating')
      await nextTick()
      uiControl('NButton', (item) => item.text === '编辑' && parentProp(item, 'kind') === 'artist').emit('click')
      await nextTick()
      const pending = deferred()
      ImageStudioStorage.prototype.savePreset = function (ownerId, preset) {
        return ownerId === 303 ? pending.promise : originalSave.call(this, ownerId, preset)
      }
      const messageCount = bridge.messages.length
      uiControl('NButton', (item) => item.text === '保存' && parentProp(item, 'kind') === 'artist').emit('click')
      assert.equal(studio.presetsSaving.value, true)
      await switchOwner(304)
      pending.reject(new Error('delayed previous-account failure'))
      await new Promise((resolve) => setImmediate(resolve))
      assert.equal(bridge.messages.length, messageCount, 'No old-account toast leaks into the next account')
      assert.deepEqual(studio.presets.value, [])
      assert.equal(studio.connection.value.apiKey, '')
      assert.equal(inputControl('studio-artist-prompt').props.value, '')
      assert.equal(inputControl('studio-artist-preset-name'), undefined, 'Account change closes the previous editor')
      assert.equal(studio.modelsLoaded.value, false)
      assert.deepEqual(studio.models.value, [])
      await switchOwner(303)
      assert.equal(studio.presets.value[0].text, updatedText)
      assert.equal(studio.activeCount.value, 0)
    } finally {
      ImageStudioStorage.prototype.savePreset = originalSave
      app.unmount()
      assert.equal(bridge.controls.size, 0)
      setCurrentUser(null)
    }
  })

  await test('real AppShell orders Feature Center for both roles and keeps desktop/mobile route selection', async () => {
    setCurrentUser(null)
    setLanguage('zh')
    const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
    const events = new Map()
    const mediaListeners = new Set()
    const timers = new Map()
    let nextTimer = 1
    const media = {
      matches: false,
      addEventListener(_event, callback) { mediaListeners.add(callback) },
      removeEventListener(_event, callback) { mediaListeners.delete(callback) },
    }
    Object.defineProperty(globalThis, 'window', { configurable: true, value: {
      matchMedia(query) { assert.equal(query, '(max-width: 860px)'); return media },
      addEventListener(event, callback) {
        const listeners = events.get(event) ?? new Set()
        listeners.add(callback)
        events.set(event, listeners)
      },
      removeEventListener(event, callback) {
        const listeners = events.get(event)
        listeners?.delete(callback)
        if (!listeners?.size) events.delete(event)
      },
      setTimeout(callback) { const id = nextTimer++; timers.set(id, callback); return id },
      clearTimeout(id) { timers.delete(id) },
    } })
    const routeMatcher = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/account/image-studio', name: 'account-image-studio', component: {} },
        { path: '/account/keys', name: 'account-keys', component: {} },
      ],
    })
    const route = reactive(routeMatcher.resolve('/account/image-studio'))
    const navigations = []
    bridge.shell = {
      route,
      router: {
        resolve(target) { return routeMatcher.resolve(target) },
        async push(target) { navigations.push(target); Object.assign(route, routeMatcher.resolve(target)) },
      },
    }
    const admin = { ...user(401), is_admin: true, is_super_admin: true }
    globalThis.fetch = async (url, options) => {
      assert.equal(url, '/api/auth/me')
      assert.equal(options.credentials, 'include')
      return new Response(JSON.stringify(admin))
    }
    const { default: component } = await server.ssrLoadModule('/src/app/layout/AppShell.vue')
    let state
    const app = renderer.createApp({
      ...component,
      setup(props, context) { state = component.setup(props, context); return state },
    })
    app.component('RouterView', { render: () => null })
    try {
      app.mount({ children: [] })
      await until(() => state.hasLoadedUser.value, 'shell authentication loaded')
      await nextTick()
      const desktopMenu = uiControl('NMenu', (item) => item.attrs.class === 'sider-menu')
      assert.deepEqual(desktopMenu.props.options.map((group) => group.key), ['admin-group', 'account-inspection-group', 'feature-group', 'account-group'])
      assert.deepEqual(desktopMenu.props.options.map((group) => group.label), ['管理中心', '渠道巡检', '功能中心', '我的账户'])
      const featureGroup = desktopMenu.props.options.find((group) => group.key === 'feature-group')
      assert.deepEqual(featureGroup.children.map((item) => item.key), ['/account/image-studio'])
      assert.ok(!desktopMenu.props.options.find((group) => group.key === 'account-group').children.some((item) => item.key === '/account/image-studio'))
      assert.equal(desktopMenu.props.value, '/account/image-studio')
      assert.equal(state.leafMenuOptions.value.filter((item) => item.key === '/account/image-studio').length, 1)
      assert.equal(state.isStudioScrollMode.value, true)
      assert.equal(state.isRecordsScrollMode.value, false)

      for (const path of ['/account/image-studio/', '/account/Image-Studio']) {
        Object.assign(route, routeMatcher.resolve(path))
        await nextTick()
        assert.equal(route.name, 'account-image-studio')
        assert.equal(desktopMenu.props.value, '/account/image-studio', `The studio menu remains selected at ${path}`)
        assert.equal(state.isStudioScrollMode.value, true, `The matched studio route stays bounded at ${path}`)
      }
      Object.assign(route, routeMatcher.resolve('/account/image-studio'))
      await nextTick()

      await switchOwner(402)
      assert.deepEqual(desktopMenu.props.options.map((group) => group.key), ['feature-group', 'account-group'])
      assert.equal(desktopMenu.props.value, '/account/image-studio', 'A regular user selects the studio instead of falling back to usage')
      setLanguage('en')
      await nextTick()
      assert.deepEqual(desktopMenu.props.options.map((group) => group.label), ['Feature Center', 'My Account'])
      assert.equal(desktopMenu.props.options[0].children[0].label, 'Image Studio')

      media.matches = true
      for (const callback of mediaListeners) callback({ matches: true })
      await nextTick()
      const openNavigation = () => uiControl('NButton', (item) => item.attrs['aria-label'] === 'Open navigation').emit('click')
      openNavigation()
      await nextTick()
      let mobileMenu = uiControl('NMenu', (item) => item.attrs.class !== 'sider-menu')
      assert.deepEqual(mobileMenu.props.options.map((group) => group.key), ['feature-group', 'account-group'])
      assert.equal(mobileMenu.props.value, '/account/image-studio')
      mobileMenu.emit('update:value', '/account/keys')
      await until(() => route.path === '/account/keys', 'mobile account navigation')
      await nextTick()
      assert.equal(state.drawerOpen.value, false)
      assert.equal(state.isStudioScrollMode.value, true, 'Studio layout stays active while its route component leaves')
      state.finishRouteLeave()
      await nextTick()
      assert.equal(state.isStudioScrollMode.value, false, 'The bounded workspace mode stays specific to Image Studio')
      openNavigation()
      await nextTick()
      mobileMenu = uiControl('NMenu', (item) => item.attrs.class !== 'sider-menu')
      assert.equal(mobileMenu.props.value, '/account/keys')
      mobileMenu.emit('update:value', '/account/image-studio')
      await until(() => route.path === '/account/image-studio', 'mobile feature navigation')
      await nextTick()
      assert.equal(state.isStudioScrollMode.value, true)
      openNavigation()
      await nextTick()
      assert.equal(uiControl('NMenu', (item) => item.attrs.class !== 'sider-menu').props.value, '/account/image-studio')
      assert.deepEqual(navigations, ['/account/keys', '/account/image-studio'])
    } finally {
      app.unmount()
      assert.equal(events.size, 0)
      assert.equal(mediaListeners.size, 0)
      assert.equal(timers.size, 0)
      assert.equal(bridge.controls.size, 0)
      if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
      else delete globalThis.window
      delete bridge.shell
      setCurrentUser(null)
    }
  })

  process.stdout.write(`Image Studio smoke: ${passed} scenarios passed.\n`)
} finally {
  globalThis.fetch = originalFetch
  if (originalBitmap === undefined) delete globalThis.createImageBitmap
  else globalThis.createImageBitmap = originalBitmap
  delete globalThis.__imageStudioSmoke
  await server.close()
  if (originalBrowserWindow) Object.defineProperty(globalThis, 'window', originalBrowserWindow)
  else delete globalThis.window
}
