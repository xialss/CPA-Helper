interface ApiErrorPayload {
  detail?: {
    code?: string
    message?: string
  }
}

function isApiErrorPayload(value: unknown): value is ApiErrorPayload {
  if (!value || typeof value !== 'object') {
    return false
  }
  const detail = (value as Record<string, unknown>).detail
  return typeof detail === 'object' && detail !== null
}

async function parseError(response: Response): Promise<string> {
  try {
    const data: unknown = await response.json()
    if (isApiErrorPayload(data) && data.detail?.message) {
      return data.detail.message
    }
  } catch {
    return response.statusText || '请求失败'
  }
  return response.statusText || '请求失败'
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
    throw new Error(await parseError(response))
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
  get<T>(path: string, params: Record<string, string | number | boolean | undefined> = {}) {
    return request<T>(`${path}${toQuery(params)}`)
  },
  post<T>(path: string, body?: unknown) {
    const init: RequestInit = { method: 'POST' }
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
