import { localize } from '@/shared/i18n'
import type { FeatureCenterSnapshot } from '@/shared/types/api'

export type FeatureName = 'image_studio' | 'ai_radar'
export type FeatureAvailability = 'unknown' | 'enabled' | 'disabled'
export interface FeatureControlState {
  session: number | null
  snapshot: FeatureCenterSnapshot | null
  verified: boolean
  connecting: boolean
  error: string | null
}
export interface FeatureTransport {
  close(): void
}
export interface FeatureControlIO {
  read(signal: AbortSignal): Promise<FeatureCenterSnapshot>
  connect(snapshot: (value: FeatureCenterSnapshot) => void, failure: (error: Error) => void): FeatureTransport
}

export function featureAvailability(state: FeatureControlState, feature: FeatureName): FeatureAvailability {
  if (!state.verified || !state.snapshot) return 'unknown'
  return state.snapshot[`${feature}_enabled`] ? 'enabled' : 'disabled'
}

// Transport and request generations share ownership. A closed stream can never
// restore the last enabled snapshot through a late GET or event callback.
export class FeatureController {
  private value: FeatureControlState = { session: null, snapshot: null, verified: false, connecting: false, error: null }
  private generation = 0
  private transport: FeatureTransport | null = null
  private reads = new Set<AbortController>()
  private listeners = new Set<(state: FeatureControlState) => void>()
  private ready: Promise<void> | null = null
  private settle: { resolve: () => void; reject: (error: Error) => void } | null = null

  constructor(private readonly io: FeatureControlIO) {}

  get state(): FeatureControlState { return this.value }

  subscribe(listener: (state: FeatureControlState) => void): () => void {
    this.listeners.add(listener)
    listener(this.value)
    return () => this.listeners.delete(listener)
  }

  private publish(patch: Partial<FeatureControlState>): void {
    this.value = { ...this.value, ...patch }
    for (const listener of this.listeners) listener(this.value)
  }

  setSession(session: number | null): void {
    if (this.value.session === session) return
    this.stop()
    this.publish({ session, snapshot: null, verified: false, error: null })
    if (session !== null) void this.reconnect().catch(() => { /* Error is exposed in state. */ })
  }

  private stop(): void {
    this.generation++
    this.transport?.close()
    this.transport = null
    for (const controller of this.reads) controller.abort()
    this.reads.clear()
    this.settle?.reject(new Error(localize('功能状态连接已结束', 'The feature state connection ended')))
    this.settle = null
    this.ready = null
  }

  suspend(error: Error): void {
    this.stop()
    this.publish({ verified: false, connecting: false, error: error.message })
  }

  private accept(snapshot: FeatureCenterSnapshot): void {
    if (this.value.snapshot && snapshot.revision < this.value.snapshot.revision) return
    // A same-revision authoritative reconnect restores availability. Consumers
    // resume availability only; no task is submitted by accepting a snapshot.
    this.publish({ snapshot, verified: true, connecting: false, error: null })
    this.settle?.resolve()
    this.settle = null
  }

  reconnect(): Promise<void> {
    if (this.value.session === null) return Promise.reject(new Error(localize('请先登录', 'Sign in first')))
    this.stop()
    const generation = this.generation
    this.publish({ verified: false, connecting: true, error: null })
    const ready = new Promise<void>((resolve, reject) => { this.settle = { resolve, reject } })
    this.ready = ready
    // A session may start in a route guard before any page awaits readiness.
    void ready.catch(() => { /* The same rejection is visible to awaiting callers and in state. */ })
    try {
      this.transport = this.io.connect((snapshot) => {
        if (generation === this.generation) this.accept(snapshot)
      }, (error) => {
        if (generation === this.generation) this.suspend(error)
      })
    } catch (error) {
      this.suspend(error instanceof Error ? error : new Error(String(error)))
    }
    return ready
  }

  async verify(feature?: FeatureName): Promise<FeatureCenterSnapshot> {
    if (this.ready) await this.ready
    if (!this.transport || this.value.session === null) throw new Error(this.value.error ?? localize('功能状态未验证，请重新连接', 'Feature state is unverified. Reconnect to continue'))
    const generation = this.generation
    const controller = new AbortController()
    this.reads.add(controller)
    try {
      const snapshot = await this.io.read(controller.signal)
      if (generation !== this.generation) throw new Error(localize('功能状态连接已结束', 'The feature state connection ended'))
      this.accept(snapshot)
      if (feature && featureAvailability(this.value, feature) !== 'enabled') throw new FeatureDisabledError()
      return this.value.snapshot!
    } catch (error) {
      if (generation === this.generation && !(error instanceof FeatureDisabledError)) {
        this.suspend(error instanceof Error ? error : new Error(String(error)))
      }
      throw error
    } finally {
      this.reads.delete(controller)
    }
  }
}

export class FeatureDisabledError extends Error {
  constructor() { super(localize('此功能已被管理员关闭', 'This feature has been disabled by an administrator')) }
}
