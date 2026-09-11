<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NInput, NInputNumber, NSelect, NFormItem } from 'naive-ui'
import type { PriceTimeRule } from '@/shared/types/api'
import { useI18n } from '@/shared/i18n'
const props = withDefaults(defineProps<{ modelValue: PriceTimeRule; disabled?: boolean }>(), { disabled: false })
const emit = defineEmits<{ 'update:modelValue': [PriceTimeRule] }>()
const { t } = useI18n()
const rule = computed(() => props.modelValue)
function change(patch: Partial<PriceTimeRule>) { emit('update:modelValue', { ...rule.value, ...patch }) }
function windowChange(index: number, patch: Partial<PriceTimeRule['peak_windows'][number]>) {
  change({ peak_windows: rule.value.peak_windows.map((w, i) => i === index ? { ...w, ...patch } : w) })
}
const days = computed(() => [t('周日', 'Sun'), t('周一', 'Mon'), t('周二', 'Tue'), t('周三', 'Wed'), t('周四', 'Thu'), t('周五', 'Fri'), t('周六', 'Sat')].map((label, value) => ({ label, value })))
const weekly = computed(() => days.value.map(day => {
  const intervals: string[] = []
  for (const window of rule.value.peak_windows) {
    if (window.weekdays.includes(day.value)) intervals.push(`${window.start}–${window.end < window.start ? '24:00' : window.end}`)
    if (window.end < window.start && window.weekdays.includes((day.value + 6) % 7) && window.end !== '00:00') intervals.push(`00:00–${window.end}`)
  }
  return { ...day, intervals: intervals.sort() }
}))
const rates = ['input_usd_per_million', 'output_usd_per_million', 'cache_read_usd_per_million', 'cache_creation_usd_per_million'] as const
const rateNames = computed(() => [t('空闲输入', 'Off-peak input'), t('空闲输出', 'Off-peak output'), t('空闲缓存读', 'Off-peak cache read'), t('空闲缓存写', 'Off-peak cache write')])
function setRate(key: typeof rates[number], value: number | null) {
  change({ offpeak_rates: { input_usd_per_million: 0, output_usd_per_million: 0, cache_read_usd_per_million: 0, cache_creation_usd_per_million: 0, ...rule.value.offpeak_rates, [key]: value ?? 0 } })
}
function setMode(mode: PriceTimeRule['offpeak_mode']) {
  change({ offpeak_mode: mode, ...(mode === 'explicit' && !rule.value.offpeak_rates ? { offpeak_rates: { input_usd_per_million: 0, output_usd_per_million: 0, cache_read_usd_per_million: 0, cache_creation_usd_per_million: 0 } } : {}) })
}
</script>
<template>
  <div class="time-rule-editor">
    <NFormItem :label="t('时区', 'Time zone')"><NInput :value="rule.timezone" @update:value="change({ timezone: $event })" /></NFormItem>
    <div v-for="(window, index) in rule.peak_windows" :key="index" class="peak-window">
      <NSelect multiple :value="window.weekdays" :options="days" :aria-label="t('高峰星期', 'Peak weekdays')" @update:value="windowChange(index, { weekdays: $event })" />
      <div class="window-times">
        <NInput :value="window.start" placeholder="09:00" :aria-label="t('高峰开始', 'Peak start')" @update:value="windowChange(index, { start: $event })" />
        <span>–</span>
        <NInput :value="window.end" placeholder="12:00" :aria-label="t('高峰结束', 'Peak end')" @update:value="windowChange(index, { end: $event })" />
        <NButton :disabled="props.disabled ?? false" @click="change({ peak_windows: rule.peak_windows.filter((_, i) => i !== index) })">{{ t('删除', 'Delete') }}</NButton>
      </div>
    </div>
    <NButton :disabled="props.disabled ?? false" @click="change({ peak_windows: [...rule.peak_windows, { weekdays: [1], start: '09:00', end: '12:00' }] })">{{ t('添加高峰时段', 'Add peak window') }}</NButton>
    <NFormItem :label="t('空闲定价', 'Off-peak pricing')">
      <NSelect :value="rule.offpeak_mode" :options="[{ label: t('统一倍率', 'Uniform multiplier'), value: 'multiplier' }, { label: t('独立价格', 'Explicit rates'), value: 'explicit' }]" @update:value="setMode" />
    </NFormItem>
    <NFormItem v-if="rule.offpeak_mode === 'multiplier'" :label="t('空闲倍率', 'Off-peak multiplier')"><NInputNumber :value="rule.offpeak_multiplier" :min="0" @update:value="change({ offpeak_multiplier: $event ?? 0 })" /></NFormItem>
    <template v-else><NFormItem v-for="(key, index) in rates" :key="key" :label="rateNames[index] + ' (USD / 1M)'"><NInputNumber :value="rule.offpeak_rates?.[key] ?? 0" :min="0" @update:value="setRate(key, $event)" /></NFormItem></template>
    <NFormItem :label="t('长上下文空闲倍率', 'Long-context off-peak multiplier')"><NInputNumber :value="rule.long_context_multiplier" :min="0" @update:value="change({ long_context_multiplier: $event ?? 0 })" /></NFormItem>
    <p>{{ t('长上下文直接乘此倍率，基础空闲优惠不再叠加；Fast 倍率最后应用。', 'Long-context rates use this multiplier only; Fast is applied last.') }}</p>
    <details open>
      <summary>{{ t('一周高峰预览', 'Weekly peak preview') }} · {{ rule.timezone }}</summary>
      <div v-for="day in weekly" :key="day.value"><strong>{{ day.label }}</strong> {{ day.intervals.join(' / ') || t('全天空闲', 'Off-peak all day') }}</div>
      <p>{{ t('未覆盖时间为空闲；跨午夜按开始日归属，结束时间不包含在内。', 'Uncovered times are off-peak. Overnight windows start on the selected day; the end is exclusive.') }}</p>
    </details>
  </div>
</template>
<style scoped>
.time-rule-editor { display: grid; gap: 12px; width: 100%; }
.peak-window { display: grid; gap: 8px; }
.window-times { display: flex; align-items: center; gap: 8px; }
p { margin: 0; color: var(--cpa-text-muted); font-size: 12px; }
@media(max-width: 480px) { .window-times { flex-wrap: wrap; } .window-times .n-input { flex: 1; min-width: 90px; } }
</style>
