import { onScopeDispose, watch } from 'vue'
import { useMessage } from 'naive-ui'

import { localize } from '@/shared/i18n'

import { problemMessage, toStudioProblem } from '../utils/studioErrors'
import { useImageStudio } from './useImageStudio'

// Dialog callbacks and delayed UI feedback must belong to the same account and mounted view.
export function useStudioFeedback() {
  const { ownerId, connection } = useImageStudio()
  const message = useMessage()
  let version = 0
  watch(ownerId, () => { version++ }, { flush: 'sync' })
  onScopeDispose(() => { version++ })

  return function beginAction() {
    const captured = version
    const secret = connection.value.apiKey
    const current = () => captured === version && ownerId.value !== null
    return {
      current,
      success(zh: string, en: string) {
        if (current()) message.success(localize(zh, en))
      },
      error(error: unknown) {
        if (current()) message.error(problemMessage(toStudioProblem(error, secret)))
      },
    }
  }
}
