<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { NModal, NButton, NForm, NFormItem, NSelect, NSwitch, NSpace, NAlert, NSpin, useMessage } from 'naive-ui'
import type { ModelPrice, PriceTimeRule, TimePricingBatchPayload, TimePricingBatchResult } from '@/shared/types/api'
import { getDeepSeekTemplate, saveDeepSeekTemplate, batchDeepSeekTemplate } from '../api/pricingApi'
import PriceTimeRuleEditor from './PriceTimeRuleEditor.vue'
import PriceRatesMatrix from './PriceRatesMatrix.vue'
import { useI18n } from '@/shared/i18n'
import { formatDateTime } from '@/shared/utils/format'
const props = defineProps<{ show: boolean; prices: ModelPrice[]; labels: Record<number, string> }>()
const emit = defineEmits<{ 'update:show': [boolean]; applied: [] }>()
const { t, errorText } = useI18n()
const message = useMessage()
const rule = ref<PriceTimeRule | null>(null)
const busy = ref(false)
const error = ref('')
const selected = ref<number[]>([])
const enabled = ref(true)
const scheduled = ref(false)
const effectiveInput = ref('')
const preview = ref<TimePricingBatchResult | null>(null)
const reviewed = ref<TimePricingBatchPayload | null>(null)
const options = computed(() => props.prices.filter(p => p.price_scope === 'channel' && p.billing_unit === 'token' && p.model.split('/').pop()?.toLowerCase().startsWith('deepseek-')).map(p => ({ value: p.id, label: `${props.labels[p.id] || p.channel_key} · ${p.model}` })))
watch(() => props.show, async show => {
  if (!show) return
  busy.value = true; error.value = ''; preview.value = null; reviewed.value = null; selected.value = []; rule.value = null
  enabled.value = true; scheduled.value = false; effectiveInput.value = ''
  try { rule.value = await getDeepSeekTemplate() } catch (e) { error.value = errorText(e, '加载峰谷模板失败', 'Failed to load time pricing template') } finally { busy.value = false }
})
watch([rule, selected, enabled, scheduled, effectiveInput], () => { preview.value = null; reviewed.value = null }, { deep: true })
async function saveTemplate() {
  if (!rule.value || busy.value) return
  busy.value = true; error.value = ''
  try { rule.value = await saveDeepSeekTemplate(rule.value); message.success(t('模板已保存，渠道价格未改变', 'Template saved; channel prices are unchanged')) } catch (e) { error.value = errorText(e, '保存模板失败', 'Failed to save template') } finally { busy.value = false }
}
async function review() {
  if (busy.value || !selected.value.length || (enabled.value && !rule.value)) return
  const effective = scheduled.value ? new Date(effectiveInput.value) : null
  if (effective && (!Number.isFinite(effective.getTime()) || effective.getTime() <= Date.now())) { error.value = t('请选择有效的未来生效时间（本机时区）', 'Choose a valid future time (local device timezone)'); return }
  const payload: TimePricingBatchPayload = { price_ids: [...selected.value], rule: enabled.value ? rule.value : null, ...(effective ? { effective_at: effective.toISOString() } : {}) }
  busy.value = true; error.value = ''
  try { preview.value = await batchDeepSeekTemplate(payload); reviewed.value = JSON.parse(JSON.stringify(payload)) } catch (e) { error.value = errorText(e, '预览失败', 'Preview failed') } finally { busy.value = false }
}
async function apply() {
  if (!reviewed.value || busy.value) return
  busy.value = true; error.value = ''
  try { await batchDeepSeekTemplate(reviewed.value, true); message.success(t('峰谷规则已应用', 'Time pricing rules applied')); emit('applied'); emit('update:show', false) } catch (e) { error.value = errorText(e, '应用失败，请重新预览', 'Apply failed; preview again'); reviewed.value = null; preview.value = null } finally { busy.value = false }
}
</script>
<template>
  <NModal :show="show" preset="card" :title="t('DeepSeek 峰谷模板', 'DeepSeek time pricing template')" :style="{ width: 'min(820px, calc(100vw - 32px))' }" :closable="!busy" :mask-closable="!busy" :close-on-esc="!busy" @update:show="emit('update:show', $event)">
    <NSpin :show="busy">
      <NAlert v-if="error" type="error">{{ error }}</NAlert>
      <NForm label-placement="top" :disabled="busy">
        <p>{{ t('保存模板不会改变渠道价格；选择渠道并预览后应用，才会创建独立价格版本。', 'Saving the template does not change channel prices. Select channels, preview, and apply to create independent price versions.') }}</p>
        <PriceTimeRuleEditor v-if="rule" v-model="rule" :disabled="busy" />
        <NButton :disabled="busy || !rule" @click="saveTemplate">{{ t('仅保存模板', 'Save template only') }}</NButton>
        <NFormItem :label="t('应用到渠道模型', 'Apply to channel models')"><NSelect v-model:value="selected" multiple filterable :options="options" /></NFormItem>
        <NFormItem :label="t('启用峰谷期', 'Enable time pricing')"><NSwitch v-model:value="enabled" /></NFormItem>
        <NFormItem :label="t('指定未来生效时间', 'Schedule a future effective time')"><NSwitch v-model:value="scheduled" /></NFormItem>
        <NFormItem v-if="scheduled" :label="t('生效时间（本机时区）', 'Effective time (device timezone)')"><input v-model="effectiveInput" type="datetime-local" :disabled="busy" :aria-label="t('生效时间', 'Effective time')"></NFormItem>
        <NButton :disabled="busy || !selected.length || (enabled && !rule)" @click="review">{{ t('预览变更', 'Preview changes') }}</NButton>
      </NForm>
      <section v-if="preview" class="batch-preview">
        <strong>{{ t('生效时间', 'Effective from') }}: {{ scheduled ? formatDateTime(preview.effective_at) : t('确认应用时立即生效', 'Immediately upon applying') }}</strong>
        <article v-for="item in preview.items" :key="item.before.id">
          <h4>{{ labels[item.before.id] || item.before.channel_key }} · {{ item.before.model }}</h4>
          <div class="diff-grid">
            <div><strong>{{ t('原配置', 'Before') }}</strong><PriceRatesMatrix :rule="item.before.time_pricing" :rates="item.before" :long-context="item.before.long_context" :preserved-long-context="item.before.preserved_long_context" show-time-disabled /></div>
            <div><strong>{{ t('应用后', 'After') }}</strong><PriceRatesMatrix :rule="item.after.time_pricing" :rates="item.after" :long-context="item.after.long_context" :preserved-long-context="item.after.preserved_long_context" show-time-disabled /></div>
          </div>
        </article>
      </section>
    </NSpin>
    <template #footer><NSpace justify="end"><NButton :disabled="busy" @click="emit('update:show', false)">{{ t('关闭', 'Close') }}</NButton><NButton type="primary" :disabled="!reviewed || busy" :loading="busy" @click="apply">{{ t('确认应用', 'Confirm apply') }}</NButton></NSpace></template>
  </NModal>
</template>
<style scoped>
.batch-preview { margin-top: 16px; }
input[type='datetime-local'] { width: 100%; padding: 8px; color: var(--cpa-text); background: var(--cpa-surface); border: 1px solid var(--cpa-border); border-radius: 4px; }
.batch-preview article { border-top: 1px solid var(--cpa-border); margin-top: 12px; padding-top: 8px; }
.diff-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
@media(max-width: 600px) { .diff-grid { grid-template-columns: 1fr; } }
</style>
