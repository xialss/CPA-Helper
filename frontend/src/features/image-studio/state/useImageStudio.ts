import { computed, ref, shallowReadonly, shallowRef, watch } from 'vue'

import { useCurrentUser } from '@/features/auth/state/currentUser'
import { useFeatureState } from '@/features/feature-center/state/useFeatureState'

import { loadUpstreamModels, validateConnection } from '../api/imageStudioApi'
import { GenerationRunner } from '../services/generationRunner'
import { ImageStudioStorage } from '../storage/imageStudioStorage'
import type { GenerationTask, HistoryCursor, PresetKind, SaveState, StudioConnection, StudioFavoriteTag, StudioProblem, TextPreset } from '../types'
import { copyDraft, createDefaultDraft, createStudioId, draftFromTask, validateDraft } from '../utils/generationParameters'
import { cancelledError, ImageStudioError, toStudioProblem } from '../utils/studioErrors'

const storage = new ImageStudioStorage()
const runner = new GenerationRunner(storage)
const { currentUser } = useCurrentUser()
const { imageStudio: availability, verify } = useFeatureState()
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
let presetsRevision = 0
let modelVersion = 0
let modelController: AbortController | null = null
let applyingSaved = false
let loadedOwner: number | null = null
type CollectionKind = 'presets' | 'favorites'
interface CollectionWrite { kind: CollectionKind; promise: Promise<void> }
const collectionWrites = new Map<number, Set<CollectionWrite>>()
const collectionsNeedRefresh = new Set<number>()
let collectionRead: { owner: number; epoch: number } | null = null

runner.subscribe(() => {
  tasks.value = runner.tasks
  batchIds.value = runner.batchIds
  activeCount.value = runner.activeCount
})

function context() {
  if (ownerId.value === null) throw cancelledError()
  if (availability.value !== 'enabled') throw new ImageStudioError('cancelled', '生图功能已暂停，请重新验证功能状态', 'Image Studio is paused. Verify feature state to continue')
  return { owner: ownerId.value, epoch }
}

function isCurrent(captured: { owner: number; epoch: number }): boolean {
  return ownerId.value === captured.owner && epoch === captured.epoch
}

function syncCollectionBusy(): void {
  const owner = ownerId.value
  const writes = owner === null ? [] : [...(collectionWrites.get(owner) ?? [])]
  const reading = collectionRead !== null && isCurrent(collectionRead)
  presetsSaving.value = reading || writes.some((write) => write.kind === 'presets')
  tagFavoritesSaving.value = reading || writes.some((write) => write.kind === 'favorites')
}

function trackCollectionWrite(captured: { owner: number; epoch: number }, kind: CollectionKind, promise: Promise<void>): Promise<void> {
  const writes = collectionWrites.get(captured.owner) ?? new Set<CollectionWrite>()
  collectionWrites.set(captured.owner, writes)
  const operation: CollectionWrite = { kind, promise: promise.finally(() => {
    writes.delete(operation)
    if (!writes.size) collectionWrites.delete(captured.owner)
    // A transaction may already have committed when its feature/account UI
    // epoch ends. Keep its account fenced until the canonical collections sync.
    if (!isCurrent(captured)) collectionsNeedRefresh.add(captured.owner)
    syncCollectionBusy()
    if (ownerId.value === captured.owner && availability.value === 'enabled' && collectionsNeedRefresh.has(captured.owner)) void refreshLocalCollections()
  }) }
  writes.add(operation)
  syncCollectionBusy()
  return operation.promise
}

async function refreshLocalCollections(): Promise<void> {
  const owner = ownerId.value
  if (owner === null || availability.value !== 'enabled' || !collectionsNeedRefresh.has(owner) || (collectionRead && isCurrent(collectionRead))) return
  const captured = context()
  const operation = { ...captured }
  collectionRead = operation
  const presetsRead = ++presetsRevision
  const favoritesRead = ++tagFavoritesRevision
  syncCollectionBusy()
  const ownsRead = () => isCurrent(captured) && collectionRead === operation
  try {
    // This fence survives A -> B -> A. Account B has its own independent set,
    // while returning A cannot write a stale full list ahead of A's old commit.
    await Promise.allSettled([...(collectionWrites.get(owner) ?? [])].map((write) => write.promise))
    if (!ownsRead()) return
    const [savedPresets, savedFavorites] = await Promise.allSettled([storage.listPresets(owner), storage.getTagFavorites(owner)])
    if (!ownsRead()) return
    if (savedPresets.status === 'fulfilled') {
      if (presetsRead === presetsRevision) presets.value = savedPresets.value
    } else loadProblems.value.push(readProblem(savedPresets.reason, '读取个人预设失败', 'Failed to read personal presets'))
    if (savedFavorites.status === 'fulfilled') {
      if (favoritesRead === tagFavoritesRevision) tagFavorites.value = [...(savedFavorites.value?.tags ?? [])]
    } else loadProblems.value.push(readProblem(savedFavorites.reason, '读取标签收藏失败', 'Failed to read tag favorites'))
    if (savedPresets.status === 'fulfilled' && savedFavorites.status === 'fulfilled') collectionsNeedRefresh.delete(owner)
  } finally {
    if (collectionRead === operation) { collectionRead = null; syncCollectionBusy() }
  }
}

async function reviewRefreshedCollections(): Promise<never> {
  await refreshLocalCollections()
  if (ownerId.value !== null && collectionsNeedRefresh.has(ownerId.value)) {
    throw new ImageStudioError('storage', '本地收藏和预设尚无法确认，请重试后再修改', 'Local favorites and presets could not be verified. Retry before editing them')
  }
  throw new ImageStudioError('storage', '本地收藏和预设已更新，请根据最新内容重试操作', 'Local favorites and presets were updated. Review the current entries and retry the action')
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
  const presetsRead = presetsRevision
  const collectionsBlocked = collectionsNeedRefresh.has(captured.owner) || (collectionWrites.get(captured.owner)?.size ?? 0) > 0
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
  loadedOwner = captured.owner
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
        draft.value = copyDraft(savedDraft.value)
        draftSave.value = { status: 'saved', problem: null }
      }
    } else loadProblems.value.push(readProblem(savedDraft.reason, '读取本地生成设置失败', 'Failed to read the local generation settings'))
    if (!collectionsBlocked && presetsRevision === presetsRead) {
      if (savedPresets.status === 'fulfilled') presets.value = savedPresets.value
      else loadProblems.value.push(readProblem(savedPresets.reason, '读取个人预设失败', 'Failed to read personal presets'))
    }
    if (savedGalleryPreferences.status === 'fulfilled') {
      if (!previewBlurSaving.value && galleryPreferencesRevision === galleryPreferencesRead) previewBlurred.value = savedGalleryPreferences.value?.previewBlurred === true
    } else loadProblems.value.push(readProblem(savedGalleryPreferences.reason, '读取图库偏好失败', 'Failed to read gallery preferences'))
    if (!collectionsBlocked && savedTagFavorites.status === 'fulfilled') {
      if (tagFavoritesRevision === tagFavoritesRead) tagFavorites.value = [...(savedTagFavorites.value?.tags ?? [])]
    } else if (!collectionsBlocked && savedTagFavorites.status === 'rejected') loadProblems.value.push(readProblem(savedTagFavorites.reason, '读取标签收藏失败', 'Failed to read tag favorites'))
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
    await verify('image_studio')
    if (!ownsLoad()) return
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
  const value = copyDraft(draft.value)
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
  if (collectionsNeedRefresh.has(captured.owner)) return reviewRefreshedCollections()
  const revision = ++tagFavoritesRevision
  const value = tags.map((tag) => ({ ...tag, ...(tag.aliases ? { aliases: [...tag.aliases] } : {}) }))
  tagFavoritesSaving.value = true
  try {
    await trackCollectionWrite(captured, 'favorites', storage.saveTagFavorites(captured.owner, { tags: value }))
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
  if (collectionsNeedRefresh.has(captured.owner)) return reviewRefreshedCollections()
  if (!name.trim() || !text.trim()) throw new ImageStudioError('validation', '请填写预设名称和内容', 'Enter a preset name and content')
  if (id && !presets.value.some((preset) => preset.id === id && preset.kind === kind)) throw new ImageStudioError('validation', '请重新选择当前账号的预设', 'Select a preset belonging to the current account')
  const preset: TextPreset = { ownerId: captured.owner, id: id ?? createStudioId(), kind, name: name.trim(), text }
  presetsRevision++
  presetsSaving.value = true
  try {
    await trackCollectionWrite(captured, 'presets', storage.savePreset(captured.owner, preset))
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
  if (collectionsNeedRefresh.has(captured.owner)) return reviewRefreshedCollections()
  presetsRevision++
  presetsSaving.value = true
  try {
    await trackCollectionWrite(captured, 'presets', storage.deletePreset(captured.owner, id))
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
  loadedOwner = null
  localReadVersion++
  historyVersion++
  galleryPreferencesRevision++
  tagFavoritesRevision++
  presetsRevision++
  collectionRead = null
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
  if (next !== null && (collectionWrites.get(next)?.size ?? 0) > 0) collectionsNeedRefresh.add(next)
  syncCollectionBusy()
  deleting.value = false
  invalidateModels()
  if (next !== null && availability.value === 'enabled') { void refreshLocalData(); void refreshLocalCollections() }
}, { immediate: true, flush: 'sync' })

function deactivate(): void {
  if (ownerId.value !== null && ((collectionWrites.get(ownerId.value)?.size ?? 0) > 0 || collectionRead?.owner === ownerId.value)) collectionsNeedRefresh.add(ownerId.value)
  epoch++
  collectionRead = null
  localReadVersion++
  historyVersion++
  invalidateModels()
  runner.deactivate()
  localLoading.value = false
  historyLoading.value = false
  syncCollectionBusy()
  previewBlurSaving.value = false
  deleting.value = false
  if (connectionSave.value.status === 'saving') connectionSave.value = { status: 'pending', problem: null }
  if (draftSave.value.status === 'saving') draftSave.value = { status: 'pending', problem: null }
}

const stopFeatureWatch = watch(availability, (next) => {
  if (next !== 'enabled') { deactivate(); return }
  runner.activate()
  void refreshLocalCollections()
  if (ownerId.value !== null && loadedOwner !== ownerId.value && !localLoading.value) void refreshLocalData()
}, { immediate: true, flush: 'sync' })

async function submit(): Promise<string> {
  const captured = context()
  const input = copyDraft(draft.value)
  const target = { ...connection.value }
  validateDraft(input)
  validateConnection(target)
  await verify('image_studio')
  if (!isCurrent(captured)) throw cancelledError()
  return runner.submit(input, target)
}

async function retry(id: string): Promise<string> {
  const captured = context()
  const target = { ...connection.value }
  await verify('image_studio')
  if (!isCurrent(captured)) throw cancelledError()
  return runner.retry(id, target)
}

const naiModels = computed(() => models.value.filter((model) => /nai/i.test(model)))
const otherModels = computed(() => models.value.filter((model) => !/nai/i.test(model)))
const visibleModels = computed(() => otherModelsExpanded.value ? [...naiModels.value, ...otherModels.value] : naiModels.value)

// Module ownership keeps requests alive across routes; auth changes above invalidate the entire context.
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    stopOwnerWatch()
    stopConnectionWatch()
    stopDraftWatch()
    stopFeatureWatch()
    modelController?.abort()
    runner.setOwner(null)
  })
}

export function useImageStudio() {
  return {
    ownerId: shallowReadonly(ownerId),
    availability,
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
    submit,
    retry,
    retrySave: (id: string) => runner.retrySave(id),
    cancelTask: (id: string) => runner.cancelTask(id),
    cancelBatch: (id: string) => runner.cancelBatch(id),
    getImage: (id: string) => runner.getImage(id),
  }
}
