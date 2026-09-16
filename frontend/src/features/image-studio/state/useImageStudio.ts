import { computed, ref, shallowReadonly, shallowRef, watch } from 'vue'

import { useCurrentUser } from '@/features/auth/state/currentUser'

import { loadUpstreamModels, validateConnection } from '../api/imageStudioApi'
import { GenerationRunner } from '../services/generationRunner'
import { ImageStudioStorage } from '../storage/imageStudioStorage'
import type { GenerationTask, HistoryCursor, PresetKind, SaveState, StudioConnection, StudioFavoriteTag, StudioProblem, TextPreset } from '../types'
import { createDefaultDraft, createStudioId, draftFromTask } from '../utils/generationParameters'
import { cancelledError, ImageStudioError, toStudioProblem } from '../utils/studioErrors'

const storage = new ImageStudioStorage()
const runner = new GenerationRunner(storage)
const { currentUser } = useCurrentUser()
const defaultConnection = (): StudioConnection => ({ baseUrl: '', apiKey: '' })
const ownerId = shallowRef<number | null>(null)
const tasks = shallowRef<GenerationTask[]>([])
const batchIds = shallowRef<string[]>([])
const activeCount = ref(0)
const draft = ref(createDefaultDraft())
const connection = ref(defaultConnection())
const previewBlurred = ref(false)
const previewBlurSaving = ref(false)
const tagFavorites = shallowRef<StudioFavoriteTag[]>([])
const connectionSave = shallowRef<SaveState>({ status: 'pending', problem: null })
const draftSave = shallowRef<SaveState>({ status: 'pending', problem: null })
const presets = shallowRef<TextPreset[]>([])
const localLoading = ref(false)
const loadProblems = shallowRef<StudioProblem[]>([])
const historyLoading = ref(false)
const nextCursor = shallowRef<HistoryCursor | null>(null)
const historyProblem = shallowRef<StudioProblem | null>(null)
const models = shallowRef<string[]>([])
const modelsLoading = ref(false)
const modelsLoaded = ref(false)
const modelProblem = shallowRef<StudioProblem | null>(null)
const otherModelsExpanded = ref(false)
const presetsSaving = ref(false)
const tagFavoritesSaving = ref(false)
const deleting = ref(false)
let epoch = 0
let localReadVersion = 0
let historyVersion = 0
let connectionRevision = 0
let draftRevision = 0
let galleryPreferencesRevision = 0
let tagFavoritesRevision = 0
let modelVersion = 0
let modelController: AbortController | null = null
let applyingSaved = false

runner.subscribe(() => {
  tasks.value = runner.tasks
  batchIds.value = runner.batchIds
  activeCount.value = runner.activeCount
})

function context() {
  if (ownerId.value === null) throw cancelledError()
  return { owner: ownerId.value, epoch }
}

function isCurrent(captured: { owner: number; epoch: number }): boolean {
  return ownerId.value === captured.owner && epoch === captured.epoch
}

function invalidateModels(): void {
  modelVersion++
  modelController?.abort()
  modelController = null
  models.value = []
  modelsLoading.value = false
  modelsLoaded.value = false
  modelProblem.value = null
  otherModelsExpanded.value = false
}

const stopConnectionWatch = watch(connection, () => {
  connectionRevision++
  invalidateModels()
  if (!applyingSaved) connectionSave.value = { status: 'pending', problem: null }
}, { deep: true, flush: 'sync' })

const stopDraftWatch = watch(draft, () => {
  draftRevision++
  if (!applyingSaved) draftSave.value = { status: 'pending', problem: null }
}, { deep: true, flush: 'sync' })

function readProblem(error: unknown, zh: string, en: string): StudioProblem {
  const cause = toStudioProblem(error)
  return { kind: 'storage', zh, en, ...(cause.detail ? { detail: cause.detail } : { detail: cause.en }) }
}

async function refreshLocalData(): Promise<void> {
  const captured = context()
  const version = ++localReadVersion
  const historyRead = ++historyVersion
  const connectionRead = connectionRevision
  const draftRead = draftRevision
  const galleryPreferencesRead = galleryPreferencesRevision
  const tagFavoritesRead = tagFavoritesRevision
  localLoading.value = true
  loadProblems.value = []
  const results = await Promise.allSettled([
    storage.getConnection(captured.owner),
    storage.getDraft(captured.owner),
    storage.listPresets(captured.owner),
    storage.getGalleryPreferences(captured.owner),
    storage.getTagFavorites(captured.owner),
    storage.listHistory(captured.owner),
  ])
  if (!isCurrent(captured) || localReadVersion !== version) return
  const [savedConnection, savedDraft, savedPresets, savedGalleryPreferences, savedTagFavorites, history] = results
  applyingSaved = true
  try {
    if (savedConnection.status === 'fulfilled') {
      if (savedConnection.value && connectionRevision === connectionRead) {
        connection.value = savedConnection.value
        connectionSave.value = { status: 'saved', problem: null }
      }
    } else loadProblems.value.push(readProblem(savedConnection.reason, '读取本地连接设置失败', 'Failed to read the local connection settings'))
    if (savedDraft.status === 'fulfilled') {
      if (savedDraft.value && draftRevision === draftRead) {
        draft.value = { ...createDefaultDraft(), ...savedDraft.value }
        draftSave.value = { status: 'saved', problem: null }
      }
    } else loadProblems.value.push(readProblem(savedDraft.reason, '读取本地生成设置失败', 'Failed to read the local generation settings'))
    if (savedPresets.status === 'fulfilled') presets.value = savedPresets.value
    else loadProblems.value.push(readProblem(savedPresets.reason, '读取个人预设失败', 'Failed to read personal presets'))
    if (savedGalleryPreferences.status === 'fulfilled') {
      if (!previewBlurSaving.value && galleryPreferencesRevision === galleryPreferencesRead) previewBlurred.value = savedGalleryPreferences.value?.previewBlurred === true
    } else loadProblems.value.push(readProblem(savedGalleryPreferences.reason, '读取图库偏好失败', 'Failed to read gallery preferences'))
    if (savedTagFavorites.status === 'fulfilled') {
      if (tagFavoritesRevision === tagFavoritesRead) tagFavorites.value = [...(savedTagFavorites.value?.tags ?? [])]
    } else loadProblems.value.push(readProblem(savedTagFavorites.reason, '读取标签收藏失败', 'Failed to read tag favorites'))
    if (historyVersion === historyRead) {
      if (history.status === 'fulfilled') {
        runner.mergeHistory(captured.owner, history.value.tasks, true)
        nextCursor.value = history.value.nextCursor
        historyProblem.value = null
      } else historyProblem.value = readProblem(history.reason, '读取本地图库失败', 'Failed to read the local gallery')
    }
  } finally {
    applyingSaved = false
    localLoading.value = false
  }
}

async function readHistory(cursor: HistoryCursor | null, replace: boolean): Promise<void> {
  if (historyLoading.value || deleting.value) return
  const captured = context()
  const version = ++historyVersion
  historyLoading.value = true
  historyProblem.value = null
  try {
    const page = await storage.listHistory(captured.owner, cursor)
    if (!isCurrent(captured) || version !== historyVersion) return
    runner.mergeHistory(captured.owner, page.tasks, replace)
    nextCursor.value = page.nextCursor
  } catch (error) {
    if (isCurrent(captured) && version === historyVersion) historyProblem.value = readProblem(error, '读取更多历史记录失败', 'Failed to read more history')
  } finally {
    if (isCurrent(captured) && version === historyVersion) historyLoading.value = false
  }
}

function loadMoreHistory(): Promise<void> {
  return nextCursor.value ? readHistory(nextCursor.value, false) : Promise.resolve()
}

function refreshHistory(): Promise<void> {
  return readHistory(null, true)
}

async function loadModels(): Promise<void> {
  const captured = context()
  modelController?.abort()
  const controller = new AbortController()
  modelController = controller
  const version = ++modelVersion
  models.value = []
  modelsLoading.value = true
  modelsLoaded.value = false
  modelProblem.value = null
  const ownsLoad = () => isCurrent(captured) && modelVersion === version && modelController === controller
  try {
    const result = await loadUpstreamModels({ ...connection.value }, controller.signal)
    if (ownsLoad()) {
      models.value = result
      modelsLoaded.value = true
    }
  } catch (error) {
    if (ownsLoad()) modelProblem.value = toStudioProblem(error, connection.value.apiKey)
  } finally {
    if (ownsLoad()) {
      modelsLoading.value = false
      modelController = null
    }
  }
}

async function saveConnection(): Promise<boolean> {
  if (connectionSave.value.status === 'saving') return false
  const captured = context()
  const value = validateConnection(connection.value)
  const revision = connectionRevision
  connectionSave.value = { status: 'saving', problem: null }
  try {
    await storage.saveConnection(captured.owner, value)
    if (!isCurrent(captured) || revision !== connectionRevision) return false
    connectionSave.value = { status: 'saved', problem: null }
    return true
  } catch (error) {
    if (isCurrent(captured) && revision === connectionRevision) {
      connectionSave.value = { status: 'failed', problem: readProblem(error, '连接仅用于当前页面，保存到浏览器失败', 'The connection is available in this page, but could not be saved in the browser') }
    }
    return false
  }
}

async function clearConnection(): Promise<boolean> {
  if (connectionSave.value.status === 'saving') return false
  const captured = context()
  connection.value = defaultConnection()
  const revision = connectionRevision
  connectionSave.value = { status: 'saving', problem: null }
  try {
    await storage.clearConnection(captured.owner)
    if (!isCurrent(captured) || revision !== connectionRevision) return false
    connectionSave.value = { status: 'pending', problem: null }
    return true
  } catch (error) {
    if (isCurrent(captured) && revision === connectionRevision) {
      connectionSave.value = { status: 'failed', problem: readProblem(error, '当前表单已清空，但清除已保存的连接失败；请重试清除', 'The current form was cleared, but the saved connection could not be removed. Retry clearing it') }
    }
    return false
  }
}

async function saveDraft(): Promise<boolean> {
  if (draftSave.value.status === 'saving') return false
  const captured = context()
  const revision = draftRevision
  const value = { ...draft.value }
  draftSave.value = { status: 'saving', problem: null }
  try {
    await storage.saveDraft(captured.owner, value)
    if (!isCurrent(captured) || revision !== draftRevision) return false
    draftSave.value = { status: 'saved', problem: null }
    return true
  } catch (error) {
    if (isCurrent(captured) && revision === draftRevision) draftSave.value = { status: 'failed', problem: readProblem(error, '生成设置尚未保存到浏览器，请重试保存', 'Generation settings were not saved in this browser. Retry saving them') }
    return false
  }
}

async function setPreviewBlurred(enabled: boolean): Promise<boolean> {
  if (previewBlurSaving.value) return false
  const captured = context()
  const revision = ++galleryPreferencesRevision
  const previous = previewBlurred.value
  previewBlurred.value = enabled
  previewBlurSaving.value = true
  try {
    await storage.saveGalleryPreferences(captured.owner, { previewBlurred: enabled })
    return isCurrent(captured) && revision === galleryPreferencesRevision
  } catch (error) {
    if (!isCurrent(captured) || revision !== galleryPreferencesRevision) return false
    previewBlurred.value = previous
    throw error
  } finally {
    if (isCurrent(captured) && revision === galleryPreferencesRevision) previewBlurSaving.value = false
  }
}

async function saveTagFavorites(tags: readonly StudioFavoriteTag[]): Promise<boolean> {
  if (tagFavoritesSaving.value) return false
  const captured = context()
  const revision = ++tagFavoritesRevision
  const value = tags.map((tag) => ({ ...tag, ...(tag.aliases ? { aliases: [...tag.aliases] } : {}) }))
  tagFavoritesSaving.value = true
  try {
    await storage.saveTagFavorites(captured.owner, { tags: value })
    if (!isCurrent(captured) || revision !== tagFavoritesRevision) return false
    tagFavorites.value = value
    return true
  } finally {
    if (isCurrent(captured) && revision === tagFavoritesRevision) tagFavoritesSaving.value = false
  }
}

async function savePreset(kind: PresetKind, name: string, text: string, id: string | null): Promise<TextPreset | null> {
  if (presetsSaving.value) return null
  const captured = context()
  if (!name.trim() || !text.trim()) throw new ImageStudioError('validation', '请填写预设名称和内容', 'Enter a preset name and content')
  if (id && !presets.value.some((preset) => preset.id === id && preset.kind === kind)) throw new ImageStudioError('validation', '请重新选择当前账号的预设', 'Select a preset belonging to the current account')
  const preset: TextPreset = { ownerId: captured.owner, id: id ?? createStudioId(), kind, name: name.trim(), text }
  presetsSaving.value = true
  try {
    await storage.savePreset(captured.owner, preset)
    if (!isCurrent(captured)) return null
    presets.value = [...presets.value.filter((item) => item.id !== preset.id), preset]
    return preset
  } finally {
    if (isCurrent(captured)) presetsSaving.value = false
  }
}

async function deletePreset(id: string): Promise<boolean> {
  if (presetsSaving.value) return false
  const captured = context()
  presetsSaving.value = true
  try {
    await storage.deletePreset(captured.owner, id)
    if (!isCurrent(captured)) return false
    presets.value = presets.value.filter((preset) => preset.id !== id)
    return true
  } finally {
    if (isCurrent(captured)) presetsSaving.value = false
  }
}

function reuseTask(id: string): boolean {
  const task = tasks.value.find((item) => item.id === id && item.ownerId === ownerId.value)
  if (!task) return false
  draft.value = draftFromTask(task)
  return true
}

async function deleteTasks(ids: string[]): Promise<boolean> {
  if (deleting.value) return false
  const captured = context()
  historyVersion++
  historyLoading.value = false
  deleting.value = true
  try {
    await runner.deleteTasks(ids)
    return isCurrent(captured)
  } finally {
    if (isCurrent(captured)) deleting.value = false
  }
}

const stopOwnerWatch = watch(() => currentUser.value?.id ?? null, (next) => {
  epoch++
  localReadVersion++
  historyVersion++
  galleryPreferencesRevision++
  tagFavoritesRevision++
  ownerId.value = next
  runner.setOwner(next)
  connection.value = defaultConnection()
  draft.value = createDefaultDraft()
  previewBlurred.value = false
  previewBlurSaving.value = false
  tagFavorites.value = []
  connectionSave.value = { status: 'pending', problem: null }
  draftSave.value = { status: 'pending', problem: null }
  presets.value = []
  loadProblems.value = []
  historyProblem.value = null
  nextCursor.value = null
  localLoading.value = false
  historyLoading.value = false
  presetsSaving.value = false
  tagFavoritesSaving.value = false
  deleting.value = false
  invalidateModels()
  if (next !== null) void refreshLocalData()
}, { immediate: true, flush: 'sync' })

const naiModels = computed(() => models.value.filter((model) => /nai/i.test(model)))
const otherModels = computed(() => models.value.filter((model) => !/nai/i.test(model)))
const visibleModels = computed(() => otherModelsExpanded.value ? [...naiModels.value, ...otherModels.value] : naiModels.value)

// Module ownership keeps requests alive across routes; auth changes above invalidate the entire context.
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    stopOwnerWatch()
    stopConnectionWatch()
    stopDraftWatch()
    modelController?.abort()
    runner.setOwner(null)
  })
}

export function useImageStudio() {
  return {
    ownerId: shallowReadonly(ownerId),
    tasks: shallowReadonly(tasks),
    batchIds: shallowReadonly(batchIds),
    activeCount: shallowReadonly(activeCount),
    draft,
    connection,
    previewBlurred: shallowReadonly(previewBlurred),
    previewBlurSaving: shallowReadonly(previewBlurSaving),
    tagFavorites: shallowReadonly(tagFavorites),
    connectionSave: shallowReadonly(connectionSave),
    draftSave: shallowReadonly(draftSave),
    presets: shallowReadonly(presets),
    localLoading: shallowReadonly(localLoading),
    loadProblems: shallowReadonly(loadProblems),
    historyLoading: shallowReadonly(historyLoading),
    historyProblem: shallowReadonly(historyProblem),
    nextCursor: shallowReadonly(nextCursor),
    models: shallowReadonly(models),
    naiModels,
    otherModels,
    visibleModels,
    otherModelsExpanded,
    modelsLoaded: shallowReadonly(modelsLoaded),
    modelsLoading: shallowReadonly(modelsLoading),
    modelProblem: shallowReadonly(modelProblem),
    presetsSaving: shallowReadonly(presetsSaving),
    tagFavoritesSaving: shallowReadonly(tagFavoritesSaving),
    deleting: shallowReadonly(deleting),
    refreshLocalData,
    refreshHistory,
    loadMoreHistory,
    loadModels,
    saveConnection,
    clearConnection,
    saveDraft,
    setPreviewBlurred,
    saveTagFavorites,
    savePreset,
    deletePreset,
    reuseTask,
    deleteTasks,
    submit: () => runner.submit(draft.value, connection.value),
    retry: (id: string) => runner.retry(id, connection.value),
    retrySave: (id: string) => runner.retrySave(id),
    cancelTask: (id: string) => runner.cancelTask(id),
    cancelBatch: (id: string) => runner.cancelBatch(id),
    getImage: (id: string) => runner.getImage(id),
  }
}
