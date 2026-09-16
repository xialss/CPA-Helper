import { generateImage, upstreamApiUrl, validateConnection } from '../api/imageStudioApi'
import type {
  GeneratedImage,
  GenerationDraft,
  GenerationPersistence,
  GenerationRequest,
  GenerationTask,
  StoredGenerationTask,
  StudioConnection,
} from '../types'
import { createGenerationRequest, createStudioId, validateDraft } from '../utils/generationParameters'
import { cancelledError, ImageStudioError, storageProblem, toStudioProblem } from '../utils/studioErrors'

type Generate = (request: GenerationRequest, apiKey: string, signal: AbortSignal) => Promise<GeneratedImage>
interface Execution {
  ownerId: number
  epoch: number
  controller: AbortController
}

export function isTaskActive(task: StoredGenerationTask): boolean {
  return task.status === 'preparing' || task.status === 'running'
}

export function storedTask(task: GenerationTask): StoredGenerationTask {
  const { id, ownerId, batchId, batchSize, position, retryOf, createdAt, finishedAt, input, endpoint, request, status, problem, image } = task
  return { id, ownerId, batchId, batchSize, position, retryOf, createdAt, finishedAt, input, endpoint, request, status, problem, image }
}

export class GenerationRunner {
  private ownerId: number | null = null
  private epoch = 0
  private records = new Map<string, GenerationTask>()
  private executions = new Map<string, Execution>()
  private memoryImages = new Map<string, Blob>()
  private saveChains = new Map<string, Promise<boolean>>()
  private saveRevisions = new Map<string, number>()
  private sessionBatches = new Set<string>()
  private deleting = new Set<string>()
  private listeners = new Set<() => void>()

  constructor(private readonly persistence: GenerationPersistence, private readonly generate: Generate = generateImage) {}

  get tasks(): GenerationTask[] {
    return [...this.records.values()].sort((a, b) => b.createdAt - a.createdAt || b.id.localeCompare(a.id))
  }

  get batchIds(): string[] {
    return [...this.sessionBatches].reverse()
  }

  get activeCount(): number {
    return this.executions.size
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  private notify(): void {
    for (const listener of this.listeners) listener()
  }

  setOwner(ownerId: number | null): void {
    if (this.ownerId === ownerId) return
    this.epoch++
    this.ownerId = ownerId
    for (const execution of this.executions.values()) execution.controller.abort()
    this.executions.clear()
    this.records.clear()
    this.memoryImages.clear()
    this.saveChains.clear()
    this.saveRevisions.clear()
    this.sessionBatches.clear()
    this.deleting.clear()
    this.notify()
  }

  private owns(ownerId: number, epoch: number): boolean {
    return this.ownerId === ownerId && this.epoch === epoch
  }

  private update(id: string, patch: Partial<GenerationTask>): GenerationTask | undefined {
    const task = this.records.get(id)
    if (!task) return undefined
    const next = { ...task, ...patch }
    this.records.set(id, next)
    this.notify()
    return next
  }

  submit(draft: GenerationDraft, connection: StudioConnection, retryOf: string | null = null): string {
    if (this.ownerId === null) throw new ImageStudioError('validation', '请先登录', 'Sign in first')
    const input = Object.freeze(validateDraft(draft))
    const target = validateConnection(connection)
    const endpoint = upstreamApiUrl(target.baseUrl, 'chat/completions')
    const batchId = createStudioId()
    const ownerId = this.ownerId
    const epoch = this.epoch
    const createdAt = Date.now()
    const tasks = Array.from({ length: input.count }, (_, index): GenerationTask => ({
      id: `${batchId}-${index + 1}`,
      ownerId,
      batchId,
      batchSize: input.count,
      position: index + 1,
      retryOf,
      createdAt,
      finishedAt: null,
      input,
      endpoint,
      request: null,
      status: 'preparing',
      problem: null,
      image: null,
      save: { status: 'pending', problem: null },
    }))
    // Register the entire batch before any async work. There are no generation slots or shared busy flag.
    for (const task of tasks) this.records.set(task.id, task)
    this.sessionBatches.add(batchId)
    this.notify()
    for (const task of tasks) void this.execute(task.id, ownerId, epoch, target.apiKey)
    return batchId
  }

  private async execute(id: string, ownerId: number, epoch: number, apiKey: string): Promise<void> {
    const task = this.records.get(id)
    if (!task || task.status !== 'preparing' || !this.owns(ownerId, epoch)) return
    const execution: Execution = { ownerId, epoch, controller: new AbortController() }
    this.executions.set(id, execution)
    const isCurrent = () => this.owns(ownerId, epoch) && this.executions.get(id) === execution
    try {
      const request = createGenerationRequest(task.input, task.endpoint)
      this.update(id, { request, status: 'running' })
      void this.save(id)
      const result = await this.generate(request, apiKey, execution.controller.signal)
      if (!isCurrent()) return
      this.memoryImages.set(id, result.blob)
      this.update(id, { status: 'succeeded', image: result.metadata, problem: null, finishedAt: Date.now() })
      void this.save(id)
    } catch (error) {
      if (!isCurrent()) return
      const problem = toStudioProblem(error, apiKey)
      this.update(id, { status: problem.kind === 'cancelled' ? 'cancelled' : 'failed', problem, finishedAt: Date.now() })
      void this.save(id)
    } finally {
      // An old account/execution may never remove a newer controller or publish into its UI.
      if (isCurrent()) {
        this.executions.delete(id)
        this.notify()
      }
    }
  }

  private save(id: string): Promise<boolean> {
    const task = this.records.get(id)
    if (!task || this.ownerId === null) return Promise.resolve(false)
    const ownerId = this.ownerId
    const epoch = this.epoch
    const revision = (this.saveRevisions.get(id) ?? 0) + 1
    this.saveRevisions.set(id, revision)
    const record = storedTask(task)
    const image = this.memoryImages.get(id)
    const previous = this.saveChains.get(id) ?? Promise.resolve(true)
    this.update(id, { save: { status: 'saving', problem: null } })
    const isLatest = () => this.owns(ownerId, epoch) && this.saveRevisions.get(id) === revision && this.records.has(id)
    // Only writes for the same record are ordered, so a late "running" write cannot replace a result.
    // Generation requests never await this chain.
    const operation = previous.then(async () => {
      if (!this.owns(ownerId, epoch)) return false
      try {
        await this.persistence.saveTask(ownerId, record, image)
        if (isLatest()) {
          if (image) this.memoryImages.delete(id)
          this.update(id, { save: { status: 'saved', problem: null } })
        }
        return true
      } catch (error) {
        if (isLatest()) this.update(id, { save: { status: 'failed', problem: storageProblem(error) } })
        return false
      } finally {
        if (this.saveChains.get(id) === operation) this.saveChains.delete(id)
      }
    })
    this.saveChains.set(id, operation)
    return operation
  }

  cancelTask(id: string): void {
    const execution = this.executions.get(id)
    const task = this.records.get(id)
    if (!task || !isTaskActive(task) || !execution) return
    this.executions.delete(id)
    execution.controller.abort()
    this.update(id, { status: 'cancelled', problem: cancelledError().problem, finishedAt: Date.now() })
    void this.save(id)
  }

  cancelBatch(batchId: string): void {
    for (const task of this.records.values()) if (task.batchId === batchId) this.cancelTask(task.id)
  }

  retry(id: string, connection: StudioConnection): string {
    const task = this.records.get(id)
    if (!task || this.deleting.has(id) || isTaskActive(task) || task.status === 'succeeded') {
      throw new ImageStudioError('validation', '该记录当前无法重试生成', 'This record cannot be retried now')
    }
    return this.submit({ ...task.input, count: 1, seed: task.request?.submittedSeed ?? task.input.seed }, connection, id)
  }

  retrySave(id: string): Promise<boolean> {
    const task = this.records.get(id)
    if (!task || this.deleting.has(id) || task.save.status === 'saving' || isTaskActive(task)) return Promise.resolve(false)
    return this.save(id)
  }

  mergeHistory(ownerId: number, tasks: StoredGenerationTask[], replace = false): void {
    if (this.ownerId !== ownerId) return
    if (replace) {
      for (const [id, task] of this.records) if (!this.sessionBatches.has(task.batchId)) this.records.delete(id)
    }
    for (const stored of tasks) {
      if (stored.ownerId !== ownerId || this.records.has(stored.id)) continue
      const interrupted = isTaskActive(stored)
      this.records.set(stored.id, {
        ...stored,
        input: Object.freeze({ ...stored.input }),
        request: stored.request ? Object.freeze({ ...stored.request }) : null,
        status: interrupted ? 'interrupted' : stored.status,
        problem: interrupted ? {
          kind: 'interrupted',
          zh: '此页面未继续该请求，上游结果待确认；不会自动重新生成',
          en: 'This page is not continuing this request. The upstream result is unknown; it will not be replayed automatically',
        } : stored.problem,
        save: { status: 'saved', problem: null },
      })
    }
    this.notify()
  }

  async getImage(id: string): Promise<Blob> {
    if (this.ownerId === null || !this.records.has(id)) {
      throw new ImageStudioError('storage', '当前账号无法读取该图片', 'This image is not available to the current account')
    }
    const ownerId = this.ownerId
    const epoch = this.epoch
    const image = this.memoryImages.get(id) ?? await this.persistence.getImage(ownerId, id)
    if (!this.owns(ownerId, epoch)) throw cancelledError()
    return image
  }

  async deleteTasks(ids: string[]): Promise<void> {
    if (this.ownerId === null) throw cancelledError()
    const ownerId = this.ownerId
    const epoch = this.epoch
    const selected = [...new Set(ids)].filter((id) => this.records.has(id))
    if (selected.some((id) => {
      const task = this.records.get(id)
      return task && isTaskActive(task)
    })) throw new ImageStudioError('validation', '请先取消运行中的任务，再删除记录', 'Cancel running tasks before deleting their records')
    if (selected.some((id) => this.deleting.has(id))) return
    for (const id of selected) this.deleting.add(id)
    try {
      // Prevent a pending save from recreating a record after its explicit deletion.
      await Promise.all(selected.map((id) => this.saveChains.get(id)))
      if (!this.owns(ownerId, epoch)) throw cancelledError()
      await this.persistence.deleteTasks(ownerId, selected)
      if (!this.owns(ownerId, epoch)) return
      for (const id of selected) {
        this.records.delete(id)
        this.memoryImages.delete(id)
        this.saveRevisions.delete(id)
      }
      this.notify()
    } finally {
      if (this.owns(ownerId, epoch)) for (const id of selected) this.deleting.delete(id)
    }
  }
}
