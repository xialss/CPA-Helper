import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { compileScript, parse } from '@vue/compiler-sfc'
import { createRenderer, nextTick, ref } from 'vue'
import { createServer, transformWithEsbuild } from 'vite'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { AxisPointerComponent, GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { SVGRenderer } from 'echarts/renderers'

echarts.use([LineChart, AxisPointerComponent, GridComponent, LegendComponent, TooltipComponent, SVGRenderer])
const root = fileURLToPath(new URL('..', import.meta.url)).replaceAll('\\', '/')
const frames = new Map()
const listeners = new Map()
const charts = []
let nextFrame = 0
const savedGlobals = new Map(['window', 'document', 'getComputedStyle'].map((key) => [key, globalThis[key]]))
globalThis.window = {
  requestAnimationFrame(callback) { frames.set(++nextFrame, callback); return nextFrame },
  cancelAnimationFrame(id) { frames.delete(id) },
  addEventListener(name, callback) { listeners.set(name, callback) },
  removeEventListener(name) { listeners.delete(name) },
}
globalThis.document = { documentElement: {}, createElement: () => ({ getContext: () => null }) }
globalThis.getComputedStyle = () => ({ getPropertyValue: () => '' })
globalThis.__radarInteractions = {
  echarts,
  dark: ref(false),
  init() {
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 800, height: 380 })
    // SSR has no animation clock; measure the final geometry after every update.
    const setOption = chart.setOption.bind(chart)
    chart.setOption = (option, ...args) => setOption({ ...option, animation: false }, ...args)
    charts.push(chart)
    return chart
  },
}

// Compile the real components, including ChartPanel and the legend. Only the
// DOM renderer, UI library, theme and ECharts renderer initialization are replaced.
const server = await createServer({
  root,
  logLevel: 'error',
  ssr: { noExternal: ['naive-ui', 'echarts/core'] },
  resolve: { alias: [{ find: 'echarts/core', replacement: '/__radar_echarts' }] },
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
  plugins: [{
    name: 'radar-interactions-smoke',
    enforce: 'pre',
    resolveId(id) {
      if (id.startsWith('\0radar')) return id
      const path = id.replaceAll('\\', '/')
      if (path.endsWith('.vue')) return `\0radar-sfc:${path.replace(/^@/, `${root}/src`)}`
      if (id === '/__radar_echarts') return '\0radar:echarts/core'
      if (id === 'naive-ui') return '\0radar:naive-ui'
      if (path.endsWith('/shared/composables/useThemePreference')) return '\0radar:theme'
      if (path.endsWith('/shared/i18n')) return '\0radar:i18n'
    },
    async load(id) {
      if (id.startsWith('\0radar-sfc:')) {
        const filename = id.slice('\0radar-sfc:'.length)
        const { descriptor, errors } = parse(await readFile(filename, 'utf8'), { filename })
        assert.deepEqual(errors, [])
        const script = compileScript(descriptor, { id: filename, inlineTemplate: true })
        return (await transformWithEsbuild(script.content, filename, { loader: 'ts' })).code
      }
      if (id === '\0radar:echarts/core') return 'export const use = () => {}; export const init = globalThis.__radarInteractions.init'
      if (id === '\0radar:theme') return 'export const useThemePreference = () => ({ isDark: globalThis.__radarInteractions.dark })'
      if (id === '\0radar:i18n') return 'export const useI18n = () => ({ t: (zh) => zh })'
      if (id === '\0radar:naive-ui') return `import { h } from 'vue'; const UI = { render() { return h('div', Object.values(this.$slots).flatMap(slot => slot())) } }; export { UI as NEmpty, UI as NSpin, UI as NRadioButton, UI as NRadioGroup, UI as NTag, UI as NTooltip }`
    },
  }],
})

const renderer = createRenderer({
  insert(node, parent) { node.parent = parent; parent.children.push(node) },
  remove(node) { node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  createElement: (tag) => ({ tag, children: [], props: {} }),
  createText: (text) => ({ text, children: [] }),
  createComment: (text) => ({ text, children: [] }),
  setText(node, text) { node.text = text },
  setElementText(node, text) { node.text = text },
  parentNode: (node) => node.parent,
  nextSibling: () => null,
  patchProp(node, key, _old, value) { node.props[key] = value },
})
const allNodes = (node) => [node, ...node.children.flatMap(allNodes)]
const historyPoint = { observed_at: '2026-09-17T00:00:00+08:00', iq: 100, average_price_usd: 1, average_minutes: 5, average_agent_steps: 20 }
const series = ['gpt-5.6-sol', 'gpt-5.5', 'gpt-6-astra'].map((model) => ({ model, effort: 'ultra', points: [historyPoint] }))
const points = ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.5'].flatMap((model) => ['low', 'high', 'ultra'].map((effort, index) => ({ model, effort, iq: 80 + index * 10, combined_cost_index: 10 + index * 5, average_minutes: 5 + index, average_price_usd: 1 + index })))
points.find((point) => point.model === 'gpt-5.6-sol' && point.effort === 'high').average_minutes = null
points.find((point) => point.model === 'gpt-5.6-sol' && point.effort === 'high').average_price_usd = null

function assertAxisTitleFits(chart, label) {
  chart.renderToSVGString()
  const titles = chart.getZr().storage.getDisplayList(true).filter((item) => item.style?.text === label)
  assert.equal(titles.length, 1, `Expected one rendered ${label} axis title`)
  const bounds = titles[0].getBoundingRect().clone()
  bounds.applyTransform(titles[0].getComputedTransform())
  assert.ok(bounds.x >= 0, `${label} clips on the left: x=${bounds.x}`)
  assert.ok(bounds.x + bounds.width <= chart.getWidth(), `${label} clips on the right`)
  assert.ok(bounds.y >= 0, `${label} clips above canvas: y=${bounds.y}`)
  assert.ok(bounds.y + bounds.height <= chart.getHeight(), `${label} clips below canvas`)
}

async function flushChartUpdate() {
  await nextTick()
  const pending = [...frames.values()]
  frames.clear()
  for (const callback of pending) callback()
}
let app
try {
  // Exercise the shared component with a real, selectable native legend.
  const Panel = (await server.ssrLoadModule(`${root}/src/shared/components/ChartPanel.vue`)).default
  app = renderer.createApp(Panel, {
    title: 'test', empty: false,
    option: { animation: false, legend: {}, xAxis: { type: 'value' }, yAxis: {}, series: [{ name: 'A', type: 'line', data: [[1, 2], [2, 3]] }] },
  })
  app.mount({ children: [] })
  const native = charts.at(-1)
  assert.doesNotThrow(() => {
    for (let index = 0; index < 4; index++) native.dispatchAction({ type: 'legendToggleSelect', name: 'A' })
    listeners.get('resize')()
  }, 'Raw legend event dispatch must remain compatible with the instance used by ChartPanel')
  app.unmount()
  app = null

  for (const [name, props] of [
    ['EfficiencyPkChart', { points, loading: false }],
    ['IqHistoryChart', { series, window: '30d', loading: false }],
  ]) {
    const Component = (await server.ssrLoadModule(`${root}/src/features/ai-radar/components/${name}.vue`)).default
    const tree = { children: [] }
    app = renderer.createApp(Component, props)
    app.mount(tree)
    const chart = charts.at(-1)
    chart.setOption({ animation: false })
    const nodes = allNodes(tree)
    const legends = nodes.filter((node) => node.tag === 'button' && node.props['aria-label']?.startsWith('突出显示'))
    assert.equal(legends.length, 2, 'Retired model must not appear in either legend')
    assert.ok(legends[0].props['aria-label'].includes('GPT-6 Astra'), 'Legend order matches the matrix, not incoming SQL order')
    const highlighted = []
    chart.on('highlight', (event) => highlighted.push(event.seriesName))
    for (const button of legends) {
      button.props.onMouseenter()
      button.props.onClick()
      button.props.onMouseleave()
      button.props.onFocus()
      button.props.onBlur()
    }
    assert.equal(highlighted.length, 6, 'Hover, click and keyboard focus all highlight the named curve')
    assert.ok(highlighted.every((value) => value.startsWith('GPT-')))
    assert.equal(chart.getModel().getSeries().length, 2)
    assert.ok(chart.getModel().getSeries().every((item) => !chart.getModel().isSeriesFiltered(item)), 'Legend interaction never hides a curve')
    if (name === 'EfficiencyPkChart') {
      const option = chart.getOption()
      assert.equal(option.tooltip[0].trigger, 'item')
      assert.equal(option.tooltip[0].triggerOn, 'mousemove|click')
      for (const [seriesIndex, item] of option.series.entries()) {
        assert.equal(item.showSymbol, true)
        assert.equal(item.showAllSymbol, true)
        assert.equal(item.data.length, 3)
        for (const [dataIndex, effort] of ['low', 'high', 'ultra'].entries()) {
          const tooltip = option.tooltip[0].formatter({ seriesIndex, dataIndex })
          assert.match(tooltip, new RegExp(` · ${effort}`))
          assert.match(tooltip, new RegExp(`综合成本: ${(10 + dataIndex * 5).toFixed(2)}`))
          const missing = seriesIndex === 1 && effort === 'high'
          assert.ok(tooltip.includes(`耗时: ${missing ? '—' : `${(5 + dataIndex).toFixed(2)} 分钟`}`))
          assert.ok(tooltip.includes(`费用: ${missing ? '—' : `$${(1 + dataIndex).toFixed(2)}`}`))
          assert.ok(chart.getModel().getSeriesByIndex(seriesIndex).getData().getItemGraphicEl(dataIndex), `Every effort has an interactive symbol: ${seriesIndex}/${dataIndex}`)
        }
      }
    } else {
      assert.ok(!nodes.some((node) => node.text?.includes('综合成本')))
      assert.ok(nodes.some((node) => node.text?.includes('Agent steps')))
    }
    // Use the actual component's metric controls and rendered text bounds, not
    // a duplicate option fixture. Both desktop and narrow layouts must fit.
    const metricControl = nodes.find((node) => node.props?.['onUpdate:value'] && ['combined', 'iq'].includes(node.props.value))
    assert.ok(metricControl)
    const metrics = name === 'EfficiencyPkChart'
      ? ['combined', 'minutes', 'price']
      : ['iq', 'price', 'minutes', 'agent_steps']
    for (const metric of metrics) {
      metricControl.props['onUpdate:value'](metric)
      await flushChartUpdate()
      chart.setOption({ animation: false })
      for (const width of [800, 320]) {
        chart.resize({ width, height: 380 })
        assertAxisTitleFits(chart, chart.getOption().yAxis[0].name)
      }
      if (name === 'EfficiencyPkChart') {
        const tooltip = chart.getOption().tooltip[0].formatter({ seriesIndex: 0, dataIndex: 0 })
        for (const detail of ['综合成本: 10.00', '耗时: 5.00 分钟', '费用: $1.00']) {
          assert.ok(tooltip.includes(detail), `${metric} tooltip lost ${detail}`)
        }
      }
    }
    assert.doesNotThrow(() => listeners.get('resize')())
    await nextTick()
    app.unmount()
    app = null
  }
  const Cards = (await server.ssrLoadModule(`${root}/src/features/ai-radar/components/IqTierCards.vue`)).default
  const cardTree = { children: [] }
  app = renderer.createApp(Cards, { points: points.map((point) => ({ ...point, low_confidence: true })), lowSampleRuns: 20 })
  app.mount(cardTree)
  const cardNodes = allNodes(cardTree)
  assert.equal(cardNodes.filter((node) => node.tag === 'article').length, 6)
  assert.ok(cardNodes.some((node) => node.text?.includes('样本不足')))
  assert.ok(!cardNodes.some((node) => /通过 \/ 样本|累计运行|GPT-5.5/.test(node.text ?? '')))
  app.unmount()
  app = null
  assert.equal(listeners.size, 0)
  assert.equal(frames.size, 0)
} finally {
  app?.unmount()
  for (const chart of charts) if (!chart.isDisposed()) chart.dispose()
  await server.close()
  delete globalThis.__radarInteractions
  for (const [key, value] of savedGlobals) {
    if (value === undefined) delete globalThis[key]
    else globalThis[key] = value
  }
}
