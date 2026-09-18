<script setup lang="ts">
import { useI18n } from '@/shared/i18n'

defineProps<{ items: Array<{ name: string; color: string }> }>()
const emit = defineEmits<{ (event: 'highlight', name: string | null): void }>()
const { t } = useI18n()
</script>

<template>
  <div class="radar-chart-legend" role="group" :aria-label="t('图例', 'Legend')">
    <button
      v-for="item in items"
      :key="item.name"
      type="button"
      :aria-label="t(`突出显示 ${item.name}`, `Highlight ${item.name}`)"
      @mouseenter="emit('highlight', item.name)"
      @mouseleave="emit('highlight', null)"
      @focus="emit('highlight', item.name)"
      @blur="emit('highlight', null)"
      @click="emit('highlight', item.name)"
    >
      <span :style="{ background: item.color }" aria-hidden="true" />
      {{ item.name }}
    </button>
  </div>
</template>

<style scoped>
.radar-chart-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
}

.radar-chart-legend button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 0;
  border: 0;
  background: transparent;
  color: var(--cpa-text-muted);
  font: inherit;
  font-size: 11px;
  cursor: pointer;
}

.radar-chart-legend button:hover,
.radar-chart-legend button:focus-visible {
  color: var(--cpa-text-strong);
}

.radar-chart-legend button:focus-visible {
  outline: 2px solid var(--cpa-primary);
  outline-offset: 2px;
}

.radar-chart-legend span {
  width: 14px;
  height: 4px;
  border-radius: 2px;
}
</style>
