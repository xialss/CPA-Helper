<script setup lang="ts">
import { computed } from 'vue'
import type {
  ModelPriceBillingUnit,
  ModelPriceLibraryConflictLongContext,
  ModelPriceLongContext,
  PriceTimeRule,
} from '@/shared/types/api'
import { useI18n } from '@/shared/i18n'
import { formatInteger, formatPreservedLongContextPrice, formatPriceValue } from '@/shared/utils/format'

type Rates = Pick<ModelPriceLongContext, 'input_usd_per_million' | 'output_usd_per_million' | 'cache_read_usd_per_million' | 'cache_creation_usd_per_million'>
type RateField = keyof Rates

// One compact matrix for a price snapshot: base / off-peak / long-context rows
// share the same four columns, so a version or preview reads as one table.
const props = withDefaults(defineProps<{
  rates: Rates
  longContext?: ModelPriceLongContext | null | undefined
  preservedLongContext?: ModelPriceLibraryConflictLongContext | null | undefined
  rule?: PriceTimeRule | null | undefined
  billingUnit?: ModelPriceBillingUnit | undefined
  requestUsd?: number | null | undefined
  priorityMultiplier?: number | null | undefined
  showFast?: boolean | undefined
  showTimeDisabled?: boolean | undefined
}>(), {
  longContext: null,
  preservedLongContext: null,
  rule: null,
  billingUnit: 'token',
  requestUsd: null,
  priorityMultiplier: null,
  showFast: false,
  showTimeDisabled: false,
})

const { t } = useI18n()
const fields: readonly RateField[] = ['input_usd_per_million', 'output_usd_per_million', 'cache_read_usd_per_million', 'cache_creation_usd_per_million']
const columnNames = computed(() => [t('输入', 'Input'), t('输出', 'Output'), t('缓存读', 'Cache read'), t('缓存写', 'Cache write')])
const dayNames = computed(() => [t('日', 'Sun'), t('一', 'Mon'), t('二', 'Tue'), t('三', 'Wed'), t('四', 'Thu'), t('五', 'Fri'), t('六', 'Sat')])

function money(value: number | null | undefined): string {
  return formatPriceValue(value)
}

function scaled(base: Rates, multiplier: number): Rates {
  return {
    input_usd_per_million: base.input_usd_per_million * multiplier,
    output_usd_per_million: base.output_usd_per_million * multiplier,
    cache_read_usd_per_million: base.cache_read_usd_per_million * multiplier,
    cache_creation_usd_per_million: base.cache_creation_usd_per_million * multiplier,
  }
}

function offpeakRates(base: Rates, rule: PriceTimeRule): Rates {
  if (rule.offpeak_mode === 'explicit') {
    return {
      input_usd_per_million: rule.offpeak_rates?.input_usd_per_million ?? 0,
      output_usd_per_million: rule.offpeak_rates?.output_usd_per_million ?? 0,
      cache_read_usd_per_million: rule.offpeak_rates?.cache_read_usd_per_million ?? 0,
      cache_creation_usd_per_million: rule.offpeak_rates?.cache_creation_usd_per_million ?? 0,
    }
  }
  return scaled(base, rule.offpeak_multiplier)
}

const rows = computed(() => {
  const rule = props.rule
  const list: { key: string; label: string; rates: Rates; muted: boolean }[] = [
    { key: 'base', label: rule ? t('高峰', 'Peak') : t('标准', 'Standard'), rates: props.rates, muted: false },
  ]
  if (rule) {
    list.push({ key: 'offpeak', label: t('空闲', 'Off-peak'), rates: offpeakRates(props.rates, rule), muted: true })
  }
  if (props.longContext) {
    const threshold = formatInteger(props.longContext.threshold_input_tokens)
    list.push({ key: 'long', label: t(`长上下文 > ${threshold}`, `Long context > ${threshold}`), rates: props.longContext, muted: false })
    if (rule) {
      list.push({ key: 'long-offpeak', label: t('长上下文空闲', 'Long-context off-peak'), rates: scaled(props.longContext, rule.long_context_multiplier), muted: true })
    }
  }
  return list
})

// "一–五" instead of "一, 二, 三, 四, 五": consecutive runs collapse to a range.
function weekdayLabel(days: number[]): string {
  const sorted = [...new Set(days)].sort((a, b) => a - b)
  const parts: string[] = []
  let start = 0
  for (let index = 1; index <= sorted.length; index += 1) {
    if (index === sorted.length || sorted[index] !== (sorted[index - 1] ?? Number.NaN) + 1) {
      const first = sorted[start] ?? 0
      const last = sorted[index - 1] ?? first
      const firstName = dayNames.value[first] ?? String(first)
      const lastName = dayNames.value[last] ?? String(last)
      if (last - first >= 2) parts.push(`${firstName}–${lastName}`)
      else if (last === first) parts.push(firstName)
      else parts.push(`${firstName}, ${lastName}`)
      start = index
    }
  }
  return parts.join(', ')
}

const ruleSummary = computed(() => {
  const rule = props.rule
  if (!rule) return []
  const grouped = new Map<string, string[]>()
  for (const window of rule.peak_windows) {
    const days = weekdayLabel(window.weekdays)
    const time = `${window.start}–${window.end}${window.end < window.start ? t('（次日）', ' (next day)') : ''}`
    grouped.set(days, [...(grouped.get(days) ?? []), time])
  }
  const windows = [...grouped].map(([days, times]) => `${days} ${times.join(', ')}`).join(' · ')
  const mode = rule.offpeak_mode === 'multiplier'
    ? t(`空闲 ${rule.offpeak_multiplier}x`, `Off-peak ${rule.offpeak_multiplier}x`)
    : t('空闲独立定价', 'Explicit off-peak rates')
  return [
    `${t('高峰', 'Peak')} ${windows} · ${rule.timezone}`,
    `${mode} · ${t(`长上下文空闲 ${rule.long_context_multiplier}x`, `Long-context off-peak ${rule.long_context_multiplier}x`)}`,
  ]
})

const fastLabel = computed(() => props.priorityMultiplier === null ? t('默认', 'Default') : `${props.priorityMultiplier}x`)
const hasMeta = computed(() => props.showFast || !!props.rule || props.showTimeDisabled)
</script>

<template>
  <div class="rates-matrix">
    <div v-if="billingUnit === 'request'" class="rates-line">
      {{ requestUsd === null ? t('按次 · 未定价', 'Per call · unpriced') : t(`按次 ${money(requestUsd)} USD`, `Per call ${money(requestUsd)} USD`) }}
    </div>
    <div v-else class="rates-scroll" tabindex="0" role="region" :aria-label="t('价格明细', 'Price details')">
      <table class="rates-table">
        <thead>
          <tr>
            <th class="rates-corner">USD / 1M</th>
            <th v-for="(name, index) in columnNames" :key="fields[index]">{{ name }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.key" :class="{ 'rates-muted': row.muted }">
            <th>{{ row.label }}</th>
            <td v-for="field in fields" :key="field">{{ money(row.rates[field]) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="preservedLongContext" class="rates-preserved">
      <strong>{{ t('历史部分长上下文（不参与计费）', 'Historical partial long-context (not billable)') }}</strong>
      <span>{{ t('阈值', 'Threshold') }}: {{ preservedLongContext.threshold_input_tokens === null ? t('未设置', 'Not set') : formatInteger(preservedLongContext.threshold_input_tokens) }}</span>
      <span>{{ t('输入', 'Input') }}: {{ formatPreservedLongContextPrice(preservedLongContext, 'input_usd_per_million') }}</span>
      <span>{{ t('输出', 'Output') }}: {{ formatPreservedLongContextPrice(preservedLongContext, 'output_usd_per_million') }}</span>
      <span>{{ t('缓存读', 'Cache read') }}: {{ formatPreservedLongContextPrice(preservedLongContext, 'cache_read_usd_per_million') }}</span>
      <span>{{ t('缓存写', 'Cache write') }}: {{ formatPreservedLongContextPrice(preservedLongContext, 'cache_creation_usd_per_million') }}</span>
    </div>
    <div v-if="hasMeta" class="rates-meta">
      <span v-if="showFast">Fast: {{ fastLabel }}</span>
      <template v-if="rule">
        <span v-for="line in ruleSummary" :key="line">{{ line }}</span>
      </template>
      <span v-else-if="showTimeDisabled">{{ t('峰谷期关闭', 'Time pricing disabled') }}</span>
    </div>
  </div>
</template>

<style scoped>
.rates-matrix {
  display: grid;
  gap: 6px;
  width: 100%;
  min-width: 0;
  font-size: 12px;
}

.rates-line {
  color: var(--cpa-text);
  font-variant-numeric: tabular-nums;
}

.rates-scroll {
  min-width: 0;
  overflow-x: auto;
}

.rates-table {
  width: 100%;
  border-collapse: collapse;
  font-variant-numeric: tabular-nums;
}

.rates-table th,
.rates-table td {
  padding: 4px 6px;
  border-bottom: 1px solid var(--cpa-border);
  text-align: right;
  white-space: nowrap;
}

.rates-table thead th {
  color: var(--cpa-text-muted);
  font-weight: 600;
}

.rates-table tbody th {
  color: var(--cpa-text);
  font-weight: 600;
  text-align: left;
}

.rates-table .rates-corner {
  text-align: left;
  width: 34%;
}

.rates-table tbody tr:last-child th,
.rates-table tbody tr:last-child td {
  border-bottom: 0;
}

.rates-muted th,
.rates-muted td {
  color: var(--cpa-text-muted);
  font-weight: 500;
}

.rates-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  color: var(--cpa-text-muted);
  line-height: 1.4;
}

.rates-preserved {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  padding: 6px 8px;
  border: 1px solid var(--cpa-warning);
  border-radius: var(--cpa-radius);
  background: var(--cpa-warning-weak);
  color: var(--cpa-text-muted);
  line-height: 1.4;
}

.rates-preserved strong {
  width: 100%;
  color: var(--cpa-warning);
}
</style>
