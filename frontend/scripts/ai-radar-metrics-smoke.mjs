import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

import { createServer } from 'vite'

const root = fileURLToPath(new URL('..', import.meta.url))

const server = await createServer({
  root,
  logLevel: 'error',
  // SSR-only assertions do not need client dependency scanning or listeners.
  optimizeDeps: { noDiscovery: true, include: [] },
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
})

function tier(model, effort, iq, extra = {}) {
  return {
    model,
    effort,
    iq,
    passed: 10,
    total: 40,
    average_price_usd: 1,
    average_minutes: 5,
    combined_cost_index: 10,
    average_agent_steps: 20,
    average_total_tokens: 1000,
    cache_hit_rate: 0.5,
    runs_24h: 3,
    runs_48h: 6,
    runs_total: 40,
    low_confidence: false,
    ...extra,
  }
}

try {
  const {
    compareRadarModels,
    defaultRadarSeriesSelection,
    escapeRadarTooltipText,
    formatRadarCount,
    formatRadarIQ,
    formatRadarIndex,
    formatRadarMetric,
    formatRadarMinutes,
    formatRadarPassed,
    formatRadarPercent,
    formatRadarUsd,
    groupRadarTiers,
    normalizeRadarValues,
    radarEffortOrder,
    radarEffortRank,
    radarHistoryMetrics,
    radarLogExtent,
    radarModels,
    radarSeriesKey,
    radarSeriesLabel,
  } = await server.ssrLoadModule('/src/features/ai-radar/utils/radarMetrics.ts')

  // Grouping: known models keep their order; tiers are deepest first, regardless of IQ.
  const groups = groupRadarTiers([
    tier('gpt-5.6-sol', 'low', 40),
    tier('gpt-6-astra', 'high', 90),
    tier('gpt-6-astra', 'low', 120),
    tier('gpt-6-astra', 'medium', 105),
    tier('gpt-6-astra', 'xhigh', 95),
    tier('gpt-6-astra', 'max', 92),
    tier('gpt-6-astra', 'ultra', 88),
    tier('gpt-5.6-luna', 'high', 70),
    tier('gpt-5.6-terra', 'high', 80),
    tier('gpt-5.5', 'high', 65),
    tier('gpt-5.5', 'low', 75),
    tier('dsh-experimental', 'high', 50),
  ])
  assert.deepEqual(
    groups.map((group) => group.model),
    ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'dsh-experimental'],
  )
  // Deliberately non-monotonic IQ values must not reorder reasoning tiers.
  assert.deepEqual(
    groups[0].tiers.map((item) => item.effort),
    ['ultra', 'max', 'xhigh', 'high', 'medium', 'low'],
  )
  assert.deepEqual(
    groups[0].tiers.map((item) => item.iq),
    [88, 92, 95, 90, 105, 120],
  )
  // Missing IQ values keep their tier position and never get read as zero.
  const withMissingIQ = groupRadarTiers([
    tier('gpt-5.6-luna', 'low', null),
    tier('gpt-5.6-luna', 'ultra', null),
    tier('gpt-5.6-luna', 'medium', 12),
  ])
  assert.deepEqual(
    withMissingIQ[0].tiers.map((item) => item.effort),
    ['ultra', 'medium', 'low'],
  )
  // The retired model is absent even when an older server still returns it.
  assert.equal(radarModels.length, 4)
  assert.deepEqual(radarModels.map((item) => item.model), [
    'gpt-6-astra',
    'gpt-5.6-sol',
    'gpt-5.6-terra',
    'gpt-5.6-luna',
  ])
  assert.equal(groups.find((group) => group.model === 'gpt-5.5'), undefined)

  // Replay an older wire shape: the two retired entries must not become cards.
  const contractEfforts = new Map([
    ['gpt-6-astra', radarEffortOrder],
    ['gpt-5.6-sol', radarEffortOrder],
    ['gpt-5.6-terra', radarEffortOrder],
    ['gpt-5.6-luna', radarEffortOrder.slice(0, 5)],
    ['gpt-5.5', ['low', 'high']],
  ])
  const contractGroups = groupRadarTiers(
    [...contractEfforts].flatMap(([model, efforts]) =>
      efforts.map((effort, index) => tier(model, effort, 100 - index)),
    ),
  )
  assert.equal(contractGroups.length, 4)
  assert.equal(contractGroups.reduce((count, group) => count + group.tiers.length, 0), 23)
  assert.equal(contractGroups.find((group) => group.model === 'gpt-5.5'), undefined)

  // Efficiency trajectories walk from low to ultra.
  assert.deepEqual(
    [...radarEffortOrder].reverse().sort((left, right) => radarEffortRank(left) - radarEffortRank(right)),
    ['low', 'medium', 'high', 'xhigh', 'max', 'ultra'],
  )

  // Normalization applies to the plotted set only and never fabricates a value.
  const normalize = normalizeRadarValues([5, 10, 2.5, null])
  assert.equal(normalize(10), 100)
  assert.equal(normalize(5), 50)
  assert.equal(normalize(2.5), 25)
  assert.equal(normalize(null), null)
  assert.equal(normalize(0), null)
  assert.equal(normalize(-4), null)
  const emptyNormalize = normalizeRadarValues([null, 0])
  assert.equal(emptyNormalize(5), null)

  // Default history selection is one series per model: its strongest tier.
  // Retired entries cannot enter the initial selection.
  const series = [
    { model: 'gpt-6-astra', effort: 'low', points: [{ observed_at: '2026-09-17T00:00:00+08:00' }] },
    { model: 'gpt-6-astra', effort: 'ultra', points: [{ observed_at: '2026-09-17T00:00:00+08:00' }] },
    { model: 'gpt-6-astra', effort: 'high', points: [{ observed_at: '2026-09-17T00:00:00+08:00' }] },
    { model: 'gpt-5.5', effort: 'low', points: [{ observed_at: '2026-09-17T00:00:00+08:00' }] },
    { model: 'gpt-5.6-sol', effort: 'high', points: [] },
  ]
  assert.deepEqual(defaultRadarSeriesSelection(series), [
    radarSeriesKey({ model: 'gpt-6-astra', effort: 'ultra' }),
  ])
  assert.equal(
    radarSeriesLabel({ model: 'gpt-6-astra', effort: 'high' }),
    'GPT-6 Astra · high',
  )

  // Missing measurements format as an em dash, never as zero.
  assert.equal(formatRadarIQ(null), '—')
  assert.equal(formatRadarIQ(108.333), '108.33')
  assert.equal(formatRadarIndex(null), '—')
  assert.equal(formatRadarUsd(null), '—')
  assert.equal(formatRadarUsd(1.962449), '$1.96')
  assert.equal(formatRadarUsd(0.0042), '$0.0042')
  assert.equal(formatRadarMinutes(null), '—')
  assert.equal(formatRadarMinutes(8.641, ' min'), '8.64 min')
  assert.equal(formatRadarPercent(null), '—')
  assert.equal(formatRadarPercent(0.9446), '94.5%')
  assert.equal(formatRadarCount(null), '—')
  assert.equal(formatRadarCount(137), '137')
  assert.equal(formatRadarPassed(null, 136), '—')
  assert.equal(formatRadarPassed(89, null), '—')
  assert.equal(formatRadarPassed(89, 136), '89 / 136')

  // One presentation path per quantity: a cost keeps its currency symbol in the
  // live chart and the history chart alike.
  assert.equal(formatRadarMetric('price', 1.962449), '$1.96')
  assert.equal(formatRadarMetric('price', null), '—')
  assert.equal(formatRadarMetric('minutes', 8.641), '8.64 min')
  assert.equal(formatRadarMetric('combined', 1743.74), '1743.74')
  assert.equal(formatRadarMetric('combined', null), '—')
  assert.equal(formatRadarMetric('iq', 108.333), '108.33')
  assert.equal(formatRadarMetric('agent_steps', 57.0433), '57.0')
  assert.equal(formatRadarMetric('agent_steps', null), '—')

  // The history chart offers every persisted metric required by the contract.
  assert.deepEqual(Object.keys(radarHistoryMetrics), ['iq', 'price', 'minutes', 'agent_steps'])
  assert.equal(radarHistoryMetrics.agent_steps.value({ average_agent_steps: 42.5 }), 42.5)
  assert.equal(radarHistoryMetrics.agent_steps.value({ average_agent_steps: null }), null)

  // Model order is shared by the wall, the legend and the series picker.
  assert.deepEqual(
    ['gpt-5.6-luna', 'gpt-6-astra', 'dsh-x', 'gpt-5.6-sol', 'gpt-5.6-terra'].sort(compareRadarModels),
    ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'dsh-x'],
  )

  assert.equal(
    escapeRadarTooltipText(`gpt-<img src=x onerror="alert(1)"> & 'quoted'`),
    'gpt-&lt;img src=x onerror=&quot;alert(1)&quot;&gt; &amp; &#39;quoted&#39;',
  )

  const { parseDisplayDate } = await server.ssrLoadModule('/src/shared/utils/format.ts')
  assert.equal(
    parseDisplayDate('2026-09-17T10:10:47.123456+08:00')?.getTime(),
    Date.parse('2026-09-17T10:10:47.123+08:00'),
  )
  assert.equal(parseDisplayDate('not-a-date'), null)

  // Log-axis bounds must be decade aligned and must never be derived from an
  // empty set: ECharts does not draw a blank axis, so a domain built only from
  // the visible series disappears the moment the legend hides the last one.
  assert.deepEqual(radarLogExtent([0.0002767595281292624, 100]), { min: 0.0001, max: 100 })
  assert.deepEqual(radarLogExtent([7.31, 53.06]), { min: 1, max: 100 })
  assert.deepEqual(radarLogExtent([0.028738, 24.450903]), { min: 0.01, max: 100 })
  assert.equal(radarLogExtent([]), null)
  assert.deepEqual(radarLogExtent([100]), { min: 100, max: 1000 })
  assert.equal(radarLogExtent([0, -1, Number.NaN, Number.POSITIVE_INFINITY]), null)

  // Regression guard for the real bug: replay the chart's axis config and hide
  // every series through the legend, exactly as clicking the legend does.
  const echarts = await import('echarts/core')
  const { LineChart } = await import('echarts/charts')
  const { GridComponent, LegendComponent } = await import('echarts/components')
  const { SVGRenderer } = await import('echarts/renderers')
  echarts.use([LineChart, GridComponent, LegendComponent, SVGRenderer])

  const plotted = [
    { name: 'A', data: [[0.0002767, 90], [2.5, 100]] },
    { name: 'B', data: [[2.5, 95], [100, 105]] },
  ]
  const extent = radarLogExtent(plotted.flatMap((item) => item.data.map(([x]) => x)))
  assert.deepEqual(extent, { min: 0.0001, max: 100 })

  for (const [label, xAxis] of [
    ['without explicit bounds', { type: 'log' }],
    ['with explicit bounds', { type: 'log', ...extent }],
  ]) {
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 600, height: 380 })
    chart.setOption({
      animation: false,
      legend: { data: ['A', 'B'] },
      grid: { left: 8, right: 22, top: 44, bottom: 24, containLabel: true },
      xAxis,
      yAxis: { type: 'value', min: 0, max: 120 },
      series: plotted.map((item) => ({ ...item, type: 'line' })),
    })
    // Re-read the axis after every update: ECharts rebuilds the axis model, so a
    // reference captured before the toggle reports the old, still-drawn scale.
    const axisBlank = () => chart.getModel().getComponent('xAxis').axis.scale.isBlank()
    const textCount = () => (chart.renderToSVGString().match(/<text/g) ?? []).length
    const labelsBefore = textCount()
    for (const name of ['A', 'B']) chart.dispatchAction({ type: 'legendToggleSelect', name })
    const blank = axisBlank()
    const labelsAfter = textCount()
    chart.dispose()

    if (label === 'with explicit bounds') {
      assert.equal(blank, false, `${label}: the axis went blank, so ECharts stops drawing it`)
      // Only the tick sitting exactly on the rounded min may drop.
      assert.ok(
        labelsAfter >= labelsBefore - 1,
        `${label}: ${labelsBefore} -> ${labelsAfter} labels, the axis stopped being drawn`,
      )
    } else {
      // Documents exactly why the explicit bounds exist.
      assert.equal(blank, true, 'expected the unbounded log axis to go blank')
      assert.ok(labelsAfter < labelsBefore, 'expected the unbounded x-axis labels to vanish')
    }
  }

  // Auto-refresh must land on wall-clock :00 / :05 / :10.
  const {
    msUntilNextBoundary,
    radarAutoRefreshIntervalMs,
  } = await server.ssrLoadModule('/src/features/ai-radar/utils/radarSchedule.ts')
  assert.equal(radarAutoRefreshIntervalMs, 300000)
  const beijing = (epochMs) =>
    new Intl.DateTimeFormat('en-CA', {
      timeZone: 'Asia/Shanghai',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
    }).format(new Date(epochMs))
  // Every second of a five-minute span, in a +08:00 zone, must resolve to a
  // boundary whose local time is :00 / :05 / :10 and whose seconds are zero.
  for (let offset = 0; offset < radarAutoRefreshIntervalMs; offset += 1000) {
    const now = Date.UTC(2026, 8, 17, 7, 0, 0) + offset // 15:00 Beijing
    const delay = msUntilNextBoundary(now)
    assert.ok(delay > 0 && delay <= radarAutoRefreshIntervalMs, `delay ${delay} out of range`)
    const target = now + delay
    assert.equal(target % radarAutoRefreshIntervalMs, 0, `landed off-boundary at ${beijing(target)}`)
    assert.match(beijing(target), /:\d[05]:00$/, `local time ${beijing(target)} is not :00/:05/:10`)
  }
  // Already on a boundary: a full interval, never zero (no busy loop).
  assert.equal(msUntilNextBoundary(Date.UTC(2026, 8, 17, 7, 0, 0)), radarAutoRefreshIntervalMs)
  // A throttled or slept tab fires late; re-arming must still snap to the clock.
  const lateBy = 37_000
  const firstTarget = Date.UTC(2026, 8, 17, 7, 5, 0)
  const rearmed = firstTarget + lateBy + msUntilNextBoundary(firstTarget + lateBy)
  assert.equal(rearmed % radarAutoRefreshIntervalMs, 0)
  assert.equal(beijing(rearmed), '15:10:00')

  // Component-level contract guards for behavior that is intentionally owned
  // by Vue lifecycle and ECharts option construction rather than the utilities.
  const aiRadarView = await readFile(
    new URL('../src/features/ai-radar/views/AiRadarView.vue', import.meta.url),
    'utf8',
  )
  const efficiencyChart = await readFile(
    new URL('../src/features/ai-radar/components/EfficiencyPkChart.vue', import.meta.url),
    'utf8',
  )
  const historyChart = await readFile(
    new URL('../src/features/ai-radar/components/IqHistoryChart.vue', import.meta.url),
    'utf8',
  )
  const tierCards = await readFile(
    new URL('../src/features/ai-radar/components/IqTierCards.vue', import.meta.url),
    'utf8',
  )
  assert.match(aiRadarView, /isRadarModelVisible/)
  assert.match(aiRadarView, /if \(sourceUpdatedAt === null\) return/)
  assert.match(aiRadarView, /if \(!isMounted\) return\s+scheduleAutoRefresh\(\)/)
  assert.match(
    aiRadarView,
    /if \(response\.available && !response\.stale\) \{\s+message\.success/,
  )
  assert.match(efficiencyChart, /escapeRadarTooltipText/)
  assert.match(efficiencyChart, /radarEffortRank\(left\.effort\) - radarEffortRank\(right\.effort\)/)
  assert.match(historyChart, /showSymbol: item\.data\.length === 1/)
  assert.match(historyChart, /parseDisplayDate\(value\)/)
  assert.doesNotMatch(tierCards, /formatRadarPassed\(tier\.passed, tier\.total\)/)
  assert.doesNotMatch(tierCards, /formatRadarCount\(tier\.runs_total\)/)
} finally {
  await server.close()
}

await import('./ai-radar-interactions-smoke.mjs')
