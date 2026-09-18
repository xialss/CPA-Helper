<script setup lang="ts">
import { computed } from 'vue'
import { NTag, NTooltip } from 'naive-ui'

import {
  formatRadarIQ,
  formatRadarMinutes,
  formatRadarUsd,
  groupRadarTiers,
} from '@/features/ai-radar/utils/radarMetrics'
import { useI18n } from '@/shared/i18n'
import type { AIRadarPoint } from '@/shared/types/api'

const props = defineProps<{
  points: AIRadarPoint[]
  lowSampleRuns: number
}>()

const { t } = useI18n()

const groups = computed(() => groupRadarTiers(props.points))

const lowSampleHint = computed(
  () => t(`样本少于 ${props.lowSampleRuns} 次运行`, `Fewer than ${props.lowSampleRuns} runs`),
)
</script>

<template>
  <div class="radar-groups">
    <section v-for="group in groups" :key="group.model" class="panel radar-group">
      <header class="radar-group-heading">
        <span class="radar-model-dot" :style="{ background: group.color }" aria-hidden="true" />
        <h2>{{ group.label }}</h2>
        <span class="radar-group-count">
          {{ t(`${group.tiers.length} 个档位`, `${group.tiers.length} tiers`) }}
        </span>
      </header>
      <div class="radar-tier-grid">
        <article v-for="tier in group.tiers" :key="tier.effort" class="radar-tier-card">
          <div class="radar-tier-top">
            <span class="radar-tier-effort">{{ tier.effort }}</span>
            <NTooltip v-if="tier.low_confidence" trigger="hover">
              <template #trigger>
                <NTag size="small" type="warning" :bordered="false">
                  {{ t('样本不足', 'Low sample') }}
                </NTag>
              </template>
              {{ lowSampleHint }}
            </NTooltip>
          </div>
          <p class="radar-tier-iq">
            <strong>{{ formatRadarIQ(tier.iq) }}</strong>
            <span>IQ</span>
          </p>
          <dl class="radar-tier-facts">
            <div>
              <dt>{{ t('平均耗时', 'Avg duration') }}</dt>
              <dd>{{ formatRadarMinutes(tier.average_minutes, t(' 分钟', ' min')) }}</dd>
            </div>
            <div>
              <dt>{{ t('平均费用', 'Avg cost') }}</dt>
              <dd>{{ formatRadarUsd(tier.average_price_usd) }}</dd>
            </div>
          </dl>
        </article>
      </div>
    </section>
  </div>
</template>

<style scoped>
.radar-groups {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.radar-group-heading {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 12px 16px 10px;
  border-bottom: 1px solid var(--cpa-border);
}

.radar-group-heading h2 {
  margin: 0;
  color: var(--cpa-text-strong);
  font-size: 15px;
  font-weight: 750;
}

.radar-model-dot {
  width: 10px;
  height: 10px;
  flex: none;
  border-radius: 50%;
}

.radar-group-count {
  margin-left: auto;
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.radar-tier-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(148px, 1fr));
  gap: 10px;
  padding: 12px 16px 14px;
}

.radar-tier-card {
  display: grid;
  gap: 6px;
  min-width: 0;
  padding: 10px 11px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface-raised);
}

.radar-tier-top {
  display: flex;
  min-height: 22px;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}

.radar-tier-effort {
  color: var(--cpa-text-muted);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

.radar-tier-iq {
  display: flex;
  align-items: baseline;
  gap: 5px;
  margin: 0;
  color: var(--cpa-text-muted);
  font-size: 11px;
}

.radar-tier-iq strong {
  color: var(--cpa-text-strong);
  font-size: 23px;
  font-weight: 750;
  line-height: 1.05;
  letter-spacing: 0;
}

.radar-tier-facts {
  display: grid;
  gap: 3px;
  margin: 0;
  padding-top: 7px;
  border-top: 1px solid var(--cpa-border);
}

.radar-tier-facts > div {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.radar-tier-facts dt {
  color: var(--cpa-text-muted);
  font-size: 11px;
  white-space: nowrap;
}

.radar-tier-facts dd {
  margin: 0;
  color: var(--cpa-text);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  text-align: right;
  white-space: nowrap;
}
</style>
