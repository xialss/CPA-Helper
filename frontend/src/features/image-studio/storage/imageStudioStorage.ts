import type {
  GenerationDraft,
  GenerationPersistence,
  HistoryCursor,
  HistoryPage,
  StoredGenerationTask,
  StudioConnection,
  StudioTagFavorites,
  StudioGalleryPreferences,
  TextPreset,
} from '../types'
import { ImageStudioError } from '../utils/studioErrors'

const databaseName = 'cpa-helper-image-studio'
const databaseVersion = 1

function requireOwner(ownerId: number): void {
  if (!Number.isSafeInteger(ownerId) || ownerId < 1) {
    throw new ImageStudioError('storage', '缺少有效的当前账号', 'A valid current account is required')
  }
}

function transactionError(error: DOMException | null): ImageStudioError {
  return new ImageStudioError(
    'storage',
    '浏览器存储事务失败，数据未保存',
    'The browser storage transaction failed; data was not saved',
    error ? `${error.name}: ${error.message}` : undefined,
  )
}

export class ImageStudioStorage implements GenerationPersistence {
  private opening: Promise<IDBDatabase> | null = null

  private database(): Promise<IDBDatabase> {
    if (this.opening) return this.opening
    const opening = new Promise<IDBDatabase>((resolve, reject) => {
      // The indexedDB property itself can throw under a browser storage policy.
      const factory = globalThis.indexedDB
      if (!factory) {
        reject(new ImageStudioError('storage', '当前浏览器无法使用 IndexedDB，本地数据不可持久保存', 'IndexedDB is unavailable in this browser; local data cannot be persisted'))
        return
      }
      const request = factory.open(databaseName, databaseVersion)
      let blocked = false
      request.onblocked = () => {
        blocked = true
        reject(new ImageStudioError('storage', '本地图库升级被其他标签页阻止，请关闭旧标签页后重试', 'Another tab is blocking the local gallery upgrade. Close the older tab and retry'))
      }
      request.onupgradeneeded = () => {
        if (blocked) {
          request.transaction?.abort()
          return
        }
        const db = request.result
        // Additive, versioned schema: upgrades must preserve existing originals.
        if (!db.objectStoreNames.contains('tasks')) {
          const tasks = db.createObjectStore('tasks', { keyPath: ['ownerId', 'id'] })
          tasks.createIndex('ownerCreated', ['ownerId', 'createdAt', 'id'])
        }
        if (!db.objectStoreNames.contains('images')) db.createObjectStore('images', { keyPath: ['ownerId', 'id'] })
        if (!db.objectStoreNames.contains('settings')) db.createObjectStore('settings', { keyPath: ['ownerId', 'kind'] })
        if (!db.objectStoreNames.contains('presets')) {
          const presets = db.createObjectStore('presets', { keyPath: ['ownerId', 'id'] })
          presets.createIndex('owner', 'ownerId')
        }
      }
      request.onerror = () => reject(transactionError(request.error))
      request.onsuccess = () => {
        const db = request.result
        if (blocked) {
          db.close()
          return
        }
        db.onversionchange = () => {
          db.close()
          if (this.opening === opening) this.opening = null
        }
        db.onclose = () => {
          if (this.opening === opening) this.opening = null
        }
        resolve(db)
      }
    })
    this.opening = opening
    void opening.catch(() => {
      if (this.opening === opening) this.opening = null
    })
    return opening
  }

  private async transaction<T>(stores: string[], mode: IDBTransactionMode, work: (transaction: IDBTransaction) => () => T): Promise<T> {
    const db = await this.database()
    return new Promise<T>((resolve, reject) => {
      const transaction = db.transaction(stores, mode)
      let result: () => T
      // Request success is not commit success; publish writes only on complete.
      transaction.oncomplete = () => resolve(result())
      transaction.onabort = () => reject(transactionError(transaction.error))
      transaction.onerror = () => reject(transactionError(transaction.error))
      try {
        result = work(transaction)
      } catch (error) {
        transaction.abort()
        reject(error)
      }
    })
  }

  private readSetting<T>(ownerId: number, kind: string): Promise<T | null> {
    requireOwner(ownerId)
    return this.transaction(['settings'], 'readonly', (transaction) => {
      let value: T | null = null
      const request = transaction.objectStore('settings').get([ownerId, kind])
      request.onsuccess = () => {
        const record = request.result as { value: T } | undefined
        value = record?.value ?? null
      }
      return () => value
    })
  }

  private writeSetting(ownerId: number, kind: string, value: StudioConnection | GenerationDraft | StudioGalleryPreferences | StudioTagFavorites): Promise<void> {
    requireOwner(ownerId)
    return this.transaction(['settings'], 'readwrite', (transaction) => {
      transaction.objectStore('settings').put({ ownerId, kind, value: { ...value } })
      return () => undefined
    })
  }

  getConnection(ownerId: number): Promise<StudioConnection | null> {
    return this.readSetting(ownerId, 'connection')
  }

  saveConnection(ownerId: number, connection: StudioConnection): Promise<void> {
    return this.writeSetting(ownerId, 'connection', connection)
  }

  clearConnection(ownerId: number): Promise<void> {
    requireOwner(ownerId)
    return this.transaction(['settings'], 'readwrite', (transaction) => {
      transaction.objectStore('settings').delete([ownerId, 'connection'])
      return () => undefined
    })
  }

  getDraft(ownerId: number): Promise<GenerationDraft | null> {
    return this.readSetting(ownerId, 'draft')
  }

  saveDraft(ownerId: number, draft: GenerationDraft): Promise<void> {
    return this.writeSetting(ownerId, 'draft', draft)
  }

  getGalleryPreferences(ownerId: number): Promise<StudioGalleryPreferences | null> {
    return this.readSetting(ownerId, 'gallery-preferences')
  }

  saveGalleryPreferences(ownerId: number, preferences: StudioGalleryPreferences): Promise<void> {
    return this.writeSetting(ownerId, 'gallery-preferences', preferences)
  }

  getTagFavorites(ownerId: number): Promise<StudioTagFavorites | null> {
    return this.readSetting(ownerId, 'tag-favorites')
  }

  saveTagFavorites(ownerId: number, favorites: StudioTagFavorites): Promise<void> {
    return this.writeSetting(ownerId, 'tag-favorites', favorites)
  }

  listPresets(ownerId: number): Promise<TextPreset[]> {
    requireOwner(ownerId)
    return this.transaction(['presets'], 'readonly', (transaction) => {
      let presets: TextPreset[] = []
      const request = transaction.objectStore('presets').index('owner').getAll(ownerId)
      request.onsuccess = () => { presets = request.result as TextPreset[] }
      return () => presets
    })
  }

  savePreset(ownerId: number, preset: TextPreset): Promise<void> {
    requireOwner(ownerId)
    if (preset.ownerId !== ownerId) throw new ImageStudioError('storage', '预设不属于当前账号', 'This preset does not belong to the current account')
    return this.transaction(['presets'], 'readwrite', (transaction) => {
      transaction.objectStore('presets').put({ ...preset })
      return () => undefined
    })
  }

  deletePreset(ownerId: number, presetId: string): Promise<void> {
    requireOwner(ownerId)
    return this.transaction(['presets'], 'readwrite', (transaction) => {
      transaction.objectStore('presets').delete([ownerId, presetId])
      return () => undefined
    })
  }

  saveTask(ownerId: number, task: StoredGenerationTask, image?: Blob): Promise<void> {
    requireOwner(ownerId)
    if (task.ownerId !== ownerId) throw new ImageStudioError('storage', '记录不属于当前账号', 'This record does not belong to the current account')
    return this.transaction(['tasks', 'images'], 'readwrite', (transaction) => {
      transaction.objectStore('tasks').put(task)
      if (image) transaction.objectStore('images').put({ ownerId, id: task.id, blob: image })
      return () => undefined
    })
  }

  listHistory(ownerId: number, cursor: HistoryCursor | null = null, pageSize = 24): Promise<HistoryPage> {
    requireOwner(ownerId)
    return this.transaction(['tasks'], 'readonly', (transaction) => {
      const tasks: StoredGenerationTask[] = []
      let hasMore = false
      const upper = cursor ? [ownerId, cursor.createdAt, cursor.id] : [ownerId, Number.MAX_SAFE_INTEGER, '\uffff']
      const range = IDBKeyRange.bound([ownerId, 0, ''], upper, false, cursor !== null)
      const request = transaction.objectStore('tasks').index('ownerCreated').openCursor(range, 'prev')
      request.onsuccess = () => {
        const item = request.result
        if (!item) return
        if (tasks.length === pageSize) {
          hasMore = true
          return
        }
        tasks.push(item.value as StoredGenerationTask)
        item.continue()
      }
      return () => {
        const last = tasks[tasks.length - 1]
        return { tasks, nextCursor: hasMore && last ? { createdAt: last.createdAt, id: last.id } : null }
      }
    })
  }

  async getImage(ownerId: number, taskId: string): Promise<Blob> {
    requireOwner(ownerId)
    const image = await this.transaction(['images'], 'readonly', (transaction) => {
      let blob: Blob | null = null
      const request = transaction.objectStore('images').get([ownerId, taskId])
      request.onsuccess = () => {
        const record = request.result as { blob: Blob } | undefined
        blob = record?.blob ?? null
      }
      return () => blob
    })
    if (!(image instanceof Blob)) {
      throw new ImageStudioError('storage', '当前账号的本地原图不存在或无法读取', 'The original image is missing or unreadable in this account\'s local gallery')
    }
    return image
  }

  deleteTasks(ownerId: number, taskIds: string[]): Promise<void> {
    requireOwner(ownerId)
    return this.transaction(['tasks', 'images'], 'readwrite', (transaction) => {
      for (const id of taskIds) {
        transaction.objectStore('tasks').delete([ownerId, id])
        transaction.objectStore('images').delete([ownerId, id])
      }
      return () => undefined
    })
  }
}
