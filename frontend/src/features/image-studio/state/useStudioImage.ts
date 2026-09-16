import { onScopeDispose, ref, watch } from 'vue'

import type { StudioProblem } from '../types'
import { toStudioProblem } from '../utils/studioErrors'
import { useImageStudio } from './useImageStudio'

export function useStudioImage(taskId: () => string | null, visible: () => boolean) {
  const studio = useImageStudio()
  const url = ref('')
  const loading = ref(false)
  const problem = ref<StudioProblem | null>(null)
  let version = 0

  function clear(): void {
    version++
    if (url.value) URL.revokeObjectURL(url.value)
    url.value = ''
    loading.value = false
    problem.value = null
  }

  async function load(): Promise<void> {
    clear()
    const id = taskId()
    if (!id || !visible()) return
    const owner = studio.ownerId.value
    const current = version
    loading.value = true
    try {
      const blob = await studio.getImage(id)
      if (version !== current || studio.ownerId.value !== owner) return
      url.value = URL.createObjectURL(blob)
    } catch (error) {
      if (version === current && studio.ownerId.value === owner) problem.value = toStudioProblem(error)
    } finally {
      if (version === current && studio.ownerId.value === owner) loading.value = false
    }
  }

  watch([taskId, visible, () => studio.ownerId.value], () => { void load() }, { immediate: true })
  onScopeDispose(clear)
  return { url, loading, problem, reload: load }
}
