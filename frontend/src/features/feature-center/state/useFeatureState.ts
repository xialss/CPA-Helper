import { computed, readonly, shallowRef } from 'vue'

import { localize } from '@/shared/i18n'
import { getFeatures, parseFeatureSnapshot } from '../api/featureCenterApi'
import { FeatureController, featureAvailability } from './featureController'

const controller = new FeatureController({
  read: getFeatures,
  connect(snapshot, failure) {
    if (typeof EventSource === 'undefined') throw new Error(localize('当前环境不支持功能状态连接', 'Feature state connections are not supported in this environment'))
    const source = new EventSource('/api/features/events', { withCredentials: true })
    // An initial snapshot is mandatory. A proxy buffering the stream must
    // become an actionable connection error, rather than an endless spinner.
    const initialSnapshotDeadline = setTimeout(() => {
      source.close()
      failure(new Error(localize('未收到功能状态初始快照，请检查连接后重新连接', 'No initial feature snapshot arrived. Check the connection and reconnect')))
    }, 20_000)
    source.addEventListener('snapshot', (event) => {
      try {
        const value = parseFeatureSnapshot(JSON.parse((event as MessageEvent<string>).data))
        if ((event as MessageEvent<string>).lastEventId !== String(value.revision)) throw new Error(localize('功能状态事件版本无效', 'Invalid feature state event revision'))
        clearTimeout(initialSnapshotDeadline)
        snapshot(value)
      } catch (error) { failure(error instanceof Error ? error : new Error(String(error))) }
    })
    source.onerror = () => {
      // Native EventSource retries forever by default; require an explicit
      // reconnect after failure so old enabled state cannot keep work running.
      source.close()
      clearTimeout(initialSnapshotDeadline)
      failure(new Error(localize('功能状态连接已断开，相关功能已暂停。请重新连接。', 'Feature state connection lost. Features are paused. Reconnect to continue.')))
    }
    return { close: () => { clearTimeout(initialSnapshotDeadline); source.close() } }
  },
})
const state = shallowRef(controller.state)
controller.subscribe((next) => { state.value = next })
const imageStudio = computed(() => featureAvailability(state.value, 'image_studio'))
const aiRadar = computed(() => featureAvailability(state.value, 'ai_radar'))

export const setFeatureSession = (session: number | null): void => controller.setSession(session)
export const verifyFeatureState = controller.verify.bind(controller)
export const reconnectFeatureState = controller.reconnect.bind(controller)

if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
  const suspend = () => controller.suspend(new Error(localize('功能状态尚未重新验证，相关功能已暂停', 'Feature state needs verification. Features are paused')))
  const resume = () => {
    if (state.value.session === null) return
    void controller.reconnect().catch(() => { /* State displays the failure. */ })
  }
  const pageShow = (event: PageTransitionEvent) => { if (event.persisted) resume() }
  const visibility = () => { if (document.visibilityState === 'visible' && state.value.verified) resume() }
  window.addEventListener('offline', suspend)
  window.addEventListener('pagehide', suspend)
  window.addEventListener('pageshow', pageShow)
  if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') {
    document.addEventListener('freeze', suspend)
    document.addEventListener('resume', resume)
    document.addEventListener('visibilitychange', visibility)
  }
  // Returning online is intentionally user-driven through the reconnect UI.
  if (import.meta.hot) import.meta.hot.dispose(() => {
    controller.setSession(null)
    window.removeEventListener('offline', suspend)
    window.removeEventListener('pagehide', suspend)
    window.removeEventListener('pageshow', pageShow)
    if (typeof document !== 'undefined' && typeof document.removeEventListener === 'function') {
      document.removeEventListener('freeze', suspend)
      document.removeEventListener('resume', resume)
      document.removeEventListener('visibilitychange', visibility)
    }
  })
}

export function useFeatureState() {
  return { state: readonly(state), imageStudio, aiRadar, verify: verifyFeatureState, reconnect: reconnectFeatureState }
}
