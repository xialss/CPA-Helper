<script setup lang="ts">
import { computed, toRef } from 'vue'
import { NButton, NCheckbox } from 'naive-ui'
import { isApiRequestError } from '@/shared/api/apiClient'
import { useI18n } from '@/shared/i18n'
import { formatDateTime } from '@/shared/utils/format'
import { useUsageModelAudit } from '../useUsageModelAudit'

const props = defineProps<{ recordId: number; queueModel: string | null }>()
const { t, errorText } = useI18n()
const { allowed, context, preview, acknowledged, busy, error, downloadURL, ready, run, downloadClicked } = useUsageModelAudit(toRef(props, 'recordId'))
const audit = computed(() => context.value?.audit)
const unknown = computed(() => t('未采集', 'Not collected'))
function comparison(left: string | null | undefined, right: string | null | undefined) {
  if (!left || !right) return t('未知（缺少模型声明）', 'Unknown (model declaration missing)')
  return left === right ? t('名称一致', 'Names match') : t('名称不一致', 'Names differ')
}
function status(value: string): string {
  const labels: Record<string, [string, string]> = {
    complete: ['已解析', 'Parsed'], incomplete: ['响应不完整', 'Incomplete response'], unsupported: ['格式暂不支持', 'Unsupported format'],
    single: ['单次上游尝试', 'Single upstream attempt'], matched: ['已关联', 'Matched'], ambiguous: ['多个候选，无法唯一关联', 'Multiple candidates; association is ambiguous'], none: ['未找到上游响应', 'No upstream response found'],
    found: ['已解析签名', 'Signature decoded'], missing: ['未获得签名', 'No signature'], malformed: ['签名损坏或无法解码', 'Malformed signature'],
  }
  const pair = labels[value]
  return pair ? t(...pair) : value
}
const errorMessage = computed(() => {
  const labels: Record<string, [string, string]> = {
    audit_source_mismatch: ['记录来源与当前 CPA 不同，已拒绝读取', 'Record source differs from the current CPA; access refused'],
    audit_source_acknowledgement_required: ['请重新读取来源状态并确认历史记录来源', 'Reload source information and acknowledge the historical source'],
    audit_log_disabled: ['CPA 完整请求日志未开启', 'CPA request logging is disabled'],
    audit_log_unavailable: ['日志不可用，可能尚未落盘或已清理', 'Log unavailable; it may not yet be written or may have been removed'],
    audit_upstream_auth: ['CPA 管理接口鉴权失败', 'CPA management authentication failed'],
    audit_timeout: ['获取 CPA 日志超时', 'CPA log request timed out'],
    audit_busy: ['日志服务繁忙，请稍后重试', 'Log service is busy; retry later'],
    audit_request_id_missing: ['记录没有请求 ID，无法关联日志', 'This record has no request ID to locate a log'],
    audit_parse_failed: ['日志解析失败', 'Log parsing failed'],
    audit_source_invalid: ['CPA 来源配置无效', 'Invalid CPA source configuration'],
    audit_unsupported: ['日志格式暂不支持', 'Log format is not supported'],
    audit_cancelled: ['日志请求已取消', 'Log request was cancelled'],
    audit_upstream_unavailable: ['CPA 日志服务暂不可用', 'CPA log service is unavailable'],
  }
  const pair = isApiRequestError(error.value) ? labels[error.value.code ?? ''] : undefined
  return pair ? t(...pair) : errorText(error.value, '日志核查失败', 'Log audit failed')
})
</script>

<template>
  <section v-if="allowed" class="audit-panel">
    <h3>{{ t('深度核查', 'Deep audit') }}</h3>
    <p>{{ t('按需读取 CPA 日志；完整正文不会保存在 Helper 数据库。签名解码不验证模型身份。', 'Read CPA logs on demand. Full bodies are not stored in the Helper database. Signature decoding does not verify model identity.') }}</p>
    <NButton :loading="busy" :disabled="busy" @click="run('cache')">{{ t('读取核查缓存与来源状态', 'Load cached audit and source') }}</NButton>
    <p v-if="error" role="alert" class="audit-error">{{ errorMessage }}</p>
    <template v-if="context">
      <div v-if="context.source_status === 'unknown'" class="audit-warning">
        <p>{{ t('历史记录的 CPA 来源未知。获取当前 CPA 中相同请求 ID 的日志不保证属于这条记录。', 'The historical CPA source is unknown. A matching request ID on the current CPA does not prove the log belongs to this record.') }}</p>
        <NCheckbox v-model:checked="acknowledged" :disabled="busy">{{ t('我了解来源不确定，仍从当前 CPA 读取', 'I acknowledge the uncertain source and want to read from the current CPA') }}</NCheckbox>
      </div>
      <div class="audit-actions">
        <NButton :disabled="busy || !ready" @click="run(audit ? 'refresh' : 'audit')">{{ audit ? t('重新核查', 'Audit again') : t('获取日志并核查', 'Fetch log and audit') }}</NButton>
        <NButton :disabled="busy || !ready" @click="run('preview')">{{ t('查看日志', 'View log') }}</NButton>
        <NButton :disabled="busy || !ready" @click="run('download')">{{ t('准备完整日志下载', 'Prepare full log download') }}</NButton>
      </div>
      <a v-if="downloadURL" :href="downloadURL" :download="`cpa-request-${recordId}.log`" @click="downloadClicked">{{ t('保存完整日志', 'Save full log') }}</a>
      <p v-if="!audit">{{ t('尚无核查缓存', 'No cached audit yet') }}</p>
      <template v-else>
        <p>{{ formatDateTime(audit.checked_at) }} · {{ status(audit.status) }} · {{ status(audit.association) }}</p>
        <dl>
          <dt>{{ t('队列响应模型', 'Queue response model') }}</dt><dd>{{ queueModel || unknown }}</dd>
          <dt>{{ t('日志响应模型', 'Log response model') }}</dt><dd>{{ audit.log_response_model || unknown }}</dd>
          <dt>{{ t('签名解析模型', 'Signature model') }}</dt><dd>{{ audit.signature_model || unknown }}</dd>
          <dt>{{ t('队列 / 日志', 'Queue / log') }}</dt><dd>{{ comparison(queueModel, audit.log_response_model) }}</dd>
          <dt>{{ t('日志 / 签名', 'Log / signature') }}</dt><dd>{{ comparison(audit.log_response_model, audit.signature_model) }}</dd>
          <dt>{{ t('队列 / 签名', 'Queue / signature') }}</dt><dd>{{ comparison(queueModel, audit.signature_model) }}</dd>
        </dl>
        <div v-for="attempt in audit.attempts" :key="attempt.index" class="audit-attempt">
          <strong>{{ t('上游尝试', 'Upstream attempt') }} {{ attempt.index }}</strong>
          <p>{{ status(attempt.status) }} · {{ status(attempt.signature_status) }}</p>
          <dl>
            <dt>{{ t('发送模型', 'Sent model') }}</dt><dd>{{ attempt.request_model || unknown }}</dd>
            <dt>{{ t('响应模型', 'Response model') }}</dt><dd>{{ attempt.response_model || unknown }}</dd>
            <dt>{{ t('签名模型', 'Signature model') }}</dt><dd>{{ attempt.signature_model || unknown }}</dd>
          </dl>
          <p v-if="attempt.conflict" class="audit-warning">{{ t('响应中出现模型声明冲突：', 'Conflicting response model declarations: ') }}{{ attempt.models.join(', ') }}</p>
        </div>
      </template>
      <template v-if="preview">
        <p>{{ t('日志大小（字节）：', 'Log size (bytes): ') }}{{ preview.total_bytes }}</p>
        <p v-if="preview.truncated" class="audit-warning">{{ t('仅显示日志预览，正文已截断；请下载查看完整文件。', 'This preview is truncated. Download the full file to see all content.') }}</p>
        <pre class="audit-log">{{ preview.text }}</pre>
      </template>
    </template>
  </section>
</template>

<style scoped>
.audit-panel { margin-top: 24px; border-top: 1px solid var(--cpa-border); padding-top: 12px; overflow-wrap: anywhere; }
.audit-panel p { color: var(--cpa-text-muted); }
.audit-actions { display: flex; flex-wrap: wrap; gap: 8px; margin: 12px 0; }
.audit-panel dl { display: grid; grid-template-columns: minmax(100px, 1fr) minmax(0, 2fr); gap: 8px; }
.audit-panel dd { margin: 0; }
.audit-attempt { border: 1px solid var(--cpa-border); padding: 12px; margin-top: 8px; border-radius: 8px; }
.audit-panel .audit-error { color: var(--cpa-danger); }
.audit-warning { border-left: 3px solid var(--cpa-border); padding-left: 10px; }
.audit-log { max-height: 420px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; padding: 12px; background: var(--cpa-surface); }
</style>
