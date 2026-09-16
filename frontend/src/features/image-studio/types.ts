export interface GenerationDraft {
  positivePrompt: string
  artistPrompt: string
  negativePrompt: string
  model: string
  generationModel: string
  width: number
  height: number
  cfg: number | null
  steps: number | null
  sampler: string
  seed: string
  count: number | null
}

export interface GenerationInput extends GenerationDraft {
  cfg: number
  steps: number
  count: number
}

export interface StudioConnection {
  baseUrl: string
  apiKey: string
}

export interface StudioGalleryPreferences {
  previewBlurred: boolean
}

export interface StudioFavoriteTag {
  id: string
  labelZh: string
  prompt: string
  aliases?: readonly string[]
  nsfw?: boolean
  custom?: boolean
}

export interface StudioTagFavorites {
  tags: readonly StudioFavoriteTag[]
}

export interface GenerationRequest {
  endpoint: string
  model: string
  generationModel: string
  content: string
  submittedSeed: string
}

export type ProblemKind =
  | 'validation'
  | 'http'
  | 'upstream'
  | 'response'
  | 'network'
  | 'image'
  | 'metadata'
  | 'timeout'
  | 'cancelled'
  | 'interrupted'
  | 'storage'
  | 'unexpected'

export interface StudioProblem {
  kind: ProblemKind
  zh: string
  en: string
  detail?: string
  httpStatus?: number
}

export interface ImageMetadata {
  mimeType: string
  width: number
  height: number
  returnedSeed: string | null
  parameters: Record<string, string | number | boolean>
  warnings: StudioProblem[]
}

export interface GeneratedImage {
  blob: Blob
  metadata: ImageMetadata
}

export type TaskStatus = 'preparing' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'
export type SaveStatus = 'pending' | 'saving' | 'saved' | 'failed'

export interface SaveState {
  status: SaveStatus
  problem: StudioProblem | null
}

// Only connection settings and a running request's closure may contain the API key.
export interface StoredGenerationTask {
  id: string
  ownerId: number
  batchId: string
  batchSize: number
  position: number
  retryOf: string | null
  createdAt: number
  finishedAt: number | null
  input: Readonly<GenerationInput>
  endpoint: string
  request: Readonly<GenerationRequest> | null
  status: TaskStatus
  problem: StudioProblem | null
  image: ImageMetadata | null
}

export interface GenerationTask extends StoredGenerationTask {
  save: SaveState
}

export type PresetKind = 'artist' | 'negative'

export interface TextPreset {
  id: string
  ownerId: number
  kind: PresetKind
  name: string
  text: string
}

export interface HistoryCursor {
  createdAt: number
  id: string
}

export interface HistoryPage {
  tasks: StoredGenerationTask[]
  nextCursor: HistoryCursor | null
}

export interface GenerationPersistence {
  saveTask(ownerId: number, task: StoredGenerationTask, image?: Blob): Promise<void>
  getImage(ownerId: number, taskId: string): Promise<Blob>
  deleteTasks(ownerId: number, taskIds: string[]): Promise<void>
}
