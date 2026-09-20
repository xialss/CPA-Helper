import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { useCurrentUser } from '@/features/auth/state/currentUser'
import { auditUsageModel, downloadUsageLog, getUsageLogPreview, getUsageModelAudit } from './api/usageApi'
import type { UsageLogPreview, UsageModelAuditResponse } from '@/shared/types/api'

// All restricted state belongs to this mounted panel, never to ordinary records.
export function useUsageModelAudit(recordId: Ref<number>) {
  const { currentUser } = useCurrentUser()
  const allowed = computed(() => currentUser.value?.is_super_admin === true)
  const context = ref<UsageModelAuditResponse | null>(null)
  const preview = ref<UsageLogPreview | null>(null)
  const acknowledged = ref(false)
  const busy = ref(false)
  const error = ref<unknown>(null)
  const downloadURL = ref<string | null>(null)
  let generation = 0
  let abort: AbortController | null = null
  let downloadCleanup: ReturnType<typeof setTimeout> | null = null
  const ready = computed(() => allowed.value && !!context.value && (
    context.value.source_status === 'matched' || (acknowledged.value && !!context.value.acknowledgement_token)
  ))

  function clearDownload() {
    if (downloadCleanup !== null) clearTimeout(downloadCleanup)
    downloadCleanup = null
    if (downloadURL.value) URL.revokeObjectURL(downloadURL.value)
    downloadURL.value = null
  }
  function downloadClicked() {
    // Let the browser's anchor default action consume the URL before revocation.
    downloadCleanup = setTimeout(clearDownload, 60_000)
  }
  function reset() {
    generation++
    abort?.abort()
    abort = null
    context.value = null
    preview.value = null
    acknowledged.value = false
    error.value = null
    busy.value = false
    clearDownload()
  }
  watch([recordId, () => currentUser.value?.id, allowed], reset, { flush: 'sync' })
  onBeforeUnmount(reset)

  async function run(action: 'cache' | 'audit' | 'refresh' | 'preview' | 'download') {
    if (!allowed.value || busy.value || (action !== 'cache' && !ready.value)) return
    const version = ++generation
    const id = recordId.value
    const owns = () => version === generation && allowed.value && recordId.value === id
    const acknowledgement = acknowledged.value ? context.value?.acknowledgement_token ?? undefined : undefined
    busy.value = true
    error.value = null
    abort = new AbortController()
    try {
      if (action === 'cache') {
        const result = await getUsageModelAudit(id, abort.signal)
        if (owns()) { context.value = result; acknowledged.value = false }
      } else if (action === 'audit' || action === 'refresh') {
        const result = await auditUsageModel(id, action === 'refresh', acknowledgement, abort.signal)
        if (owns()) context.value = result
      } else if (action === 'preview') {
        const result = await getUsageLogPreview(id, acknowledgement, abort.signal)
        if (owns()) preview.value = result
      } else {
        const result = await downloadUsageLog(id, acknowledgement, abort.signal)
        if (owns()) { clearDownload(); downloadURL.value = URL.createObjectURL(result) }
      }
    } catch (cause) {
      if (owns()) {
        error.value = cause
        // A failed permission/source check must not leave old sensitive results visible.
        context.value = null
        preview.value = null
        acknowledged.value = false
        clearDownload()
      }
    } finally {
      if (owns()) { busy.value = false; abort = null }
    }
  }
  return { allowed, context, preview, acknowledged, busy, error, downloadURL, ready, run, downloadClicked }
}
