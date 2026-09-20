import { localizedApiErrorMessage } from '@/shared/i18n'

interface ApiErrorDetail {
  code: string | null
  message: string | null
}

export class ApiRequestError extends Error {
  readonly status: number
  readonly code: string | null

  constructor(message: string, status: number, code: string | null) {
    super(message)
    this.name = 'ApiRequestError'
    this.status = status
    this.code = code
  }
}

export function isApiRequestError(error: unknown): error is ApiRequestError {
  return error instanceof ApiRequestError
}

function apiErrorDetail(value: unknown): ApiErrorDetail | null {
  if (!value || typeof value !== 'object') {
    return null
  }
  const detail = (value as Record<string, unknown>).detail
  if (!detail || typeof detail !== 'object') {
    return null
  }
  const fields = detail as Record<string, unknown>
  return {
    code: typeof fields.code === 'string' ? fields.code : null,
    message: typeof fields.message === 'string' ? fields.message : null,
  }
}

async function parseError(response: Response): Promise<ApiErrorDetail & { localizedMessage: string }> {
  try {
    const data: unknown = await response.json()
    const detail = apiErrorDetail(data)
    if (detail) {
      return {
        ...detail,
        localizedMessage: localizedApiErrorMessage(detail.code, detail.message),
      }
    }
  } catch {
    // Use the normal fallback below when an error body is not JSON.
  }
  return { code: null, message: null, localizedMessage: localizedApiErrorMessage(null, null) }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api${path}`, {
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      ...(init.headers ?? {}),
    },
    ...init,
  })

  if (!response.ok) {
    const error = await parseError(response)
    throw new ApiRequestError(error.localizedMessage, response.status, error.code)
  }

  if (response.status === 204) {
    return undefined as T
  }

  return (await response.json()) as T
}

function toQuery(params: Record<string, string | number | boolean | undefined>): string {
  const query = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value !== undefined && value !== '') {
      query.set(key, String(value))
    }
  })
  const text = query.toString()
  return text ? `?${text}` : ''
}

export const apiClient = {
  async getBlob(path: string, params: Record<string, string | number | boolean | undefined> = {}, signal?: AbortSignal): Promise<Blob> {
    const response = await fetch(`/api${path}${toQuery(params)}`, {
      credentials: 'include', cache: 'no-store', signal: signal ?? null,
    })
    if (!response.ok) {
      const error = await parseError(response)
      throw new ApiRequestError(error.localizedMessage, response.status, error.code)
    }
    return response.blob()
  },
  get<T>(path: string, params: Record<string, string | number | boolean | undefined> = {}, signal?: AbortSignal) {
    return request<T>(`${path}${toQuery(params)}`, { signal: signal ?? null })
  },
  post<T>(path: string, body?: unknown, signal?: AbortSignal) {
    const init: RequestInit = { method: 'POST' }
    if (signal) init.signal = signal
    if (body !== undefined) {
      init.body = JSON.stringify(body)
    }
    return request<T>(path, init)
  },
  put<T>(path: string, body?: unknown) {
    const init: RequestInit = { method: 'PUT' }
    if (body !== undefined) {
      init.body = JSON.stringify(body)
    }
    return request<T>(path, init)
  },
  patch<T>(path: string, body?: unknown) {
    const init: RequestInit = { method: 'PATCH' }
    if (body !== undefined) {
      init.body = JSON.stringify(body)
    }
    return request<T>(path, init)
  },
  delete(path: string) {
    return request<void>(path, { method: 'DELETE' })
  },
}
