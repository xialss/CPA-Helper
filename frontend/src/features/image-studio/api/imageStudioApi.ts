import type { GeneratedImage, GenerationRequest, StudioConnection } from '../types'
import { readPngMetadata } from '../utils/pngMetadata'
import { cancelledError, ImageStudioError, redactSecret, toStudioProblem } from '../utils/studioErrors'

export const REQUEST_TIMEOUT_MS = 120_000

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function upstreamApiUrl(baseUrl: string, resource: 'chat/completions' | 'models'): string {
  let url: URL
  try {
    url = new URL(baseUrl.trim())
  } catch {
    throw new ImageStudioError('validation', '请填写完整的 HTTP(S) 上游地址', 'Enter a complete HTTP(S) upstream URL')
  }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new ImageStudioError('validation', '上游地址须使用 HTTP(S)，且不含账号密码、查询参数或片段', 'The upstream URL must use HTTP(S), with no user info, query or fragment')
  }
  url.pathname = `${url.pathname.replace(/\/+$/, '').replace(/\/v1$/i, '')}/v1/${resource}`
  return url.href
}

export function validateConnection(connection: StudioConnection): StudioConnection {
  const baseUrl = connection.baseUrl.trim()
  upstreamApiUrl(baseUrl, 'chat/completions')
  const apiKey = connection.apiKey.trim()
  if (!apiKey || /[\r\n]/.test(apiKey)) {
    throw new ImageStudioError('validation', '请填写有效的上游 API Key', 'Enter a valid upstream API key')
  }
  return { baseUrl, apiKey }
}

function errorDetail(value: unknown): string {
  if (typeof value === 'string') return value
  if (isObject(value)) {
    for (const key of ['message', 'detail', 'error']) {
      if (value[key] !== undefined && value[key] !== value) return errorDetail(value[key])
    }
  }
  return JSON.stringify(value) ?? ''
}

async function readPayload(response: Response): Promise<unknown> {
  const text = await response.text()
  if (!response.ok) {
    let detail = text
    try {
      detail = errorDetail(JSON.parse(text))
    } catch {
      // HTTP error bodies may be plain text; keep the actual failure detail.
    }
    throw new ImageStudioError('http', `上游返回 HTTP ${response.status}`, `Upstream returned HTTP ${response.status}`, detail, response.status)
  }
  let payload: unknown
  try {
    payload = JSON.parse(text)
  } catch {
    throw new ImageStudioError('response', '上游响应不是有效 JSON', 'The upstream response is not valid JSON')
  }
  if (isObject(payload) && payload.error !== undefined && payload.error !== null && payload.error !== false) {
    throw new ImageStudioError('upstream', '上游报告生成错误', 'The upstream reported an error', errorDetail(payload.error))
  }
  return payload
}

export function extractImageReference(content: string): string {
  const markdown = /!\[[^\]]*\]\(\s*<?((?:https?:\/\/|data:image\/)[^\s<>]+?)(?:>\s*|\s+["'][^\n]*["']\s*|)\)/i.exec(content)
  const reference = markdown?.[1]
    ?? /data:image\/[a-z0-9.+-]+;base64,[a-z0-9+/=\r\n]+/i.exec(content)?.[0]?.trim()
    ?? /https?:\/\/[^\s<>"`]+/i.exec(content)?.[0]?.replace(/[),.;\]]+$/, '')
  if (!reference) {
    throw new ImageStudioError('response', '响应中没有可读取的图片', 'The response contains no readable image')
  }
  if (/^data:/i.test(reference)) return `data:${reference.slice(5)}`
  let url: URL
  try {
    url = new URL(reference)
  } catch {
    throw new ImageStudioError('response', '返回的图片地址无效', 'The returned image URL is invalid')
  }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
    throw new ImageStudioError('response', '返回的图片地址不受支持', 'The returned image URL is not supported')
  }
  return url.href
}

function imageContent(payload: unknown): string {
  if (!isObject(payload) || !Array.isArray(payload.choices)) {
    throw new ImageStudioError('response', '响应缺少 choices', 'The response is missing choices')
  }
  const choice: unknown = payload.choices[0]
  if (!isObject(choice)) {
    throw new ImageStudioError('response', '响应缺少有效的 choice', 'The response has no valid choice')
  }
  if (choice.finish_reason === 'error') {
    const detail = isObject(choice.message) ? errorDetail(choice.message.content) : errorDetail(choice.error)
    throw new ImageStudioError('upstream', '上游在响应中报告失败', 'The upstream reported a failure inside the response', detail)
  }
  if (!isObject(choice.message) || typeof choice.message.content !== 'string') {
    throw new ImageStudioError('response', '响应缺少文本 message.content', 'The response is missing a string message.content')
  }
  return choice.message.content
}

export function imageDataUrlToBlob(reference: string): Blob {
  const match = /^data:(image\/[a-z0-9.+-]+);base64,([\s\S]+)$/i.exec(reference)
  const encoded = match?.[2]?.replace(/\s+/g, '') ?? ''
  if (!match?.[1] || !encoded || !/^[a-z0-9+/]*={0,2}$/i.test(encoded)) {
    throw new ImageStudioError('image', '返回的图片 data URL 无效', 'The returned image data URL is invalid')
  }
  let binary: string
  try {
    binary = atob(encoded)
  } catch {
    throw new ImageStudioError('image', '返回的图片 Base64 无效', 'The returned image Base64 is invalid')
  }
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return new Blob([bytes], { type: match[1].toLowerCase() })
}

async function imageDimensions(blob: Blob, signal: AbortSignal): Promise<{ width: number; height: number }> {
  if (typeof createImageBitmap !== 'undefined') {
    const bitmap = await createImageBitmap(blob)
    try {
      return { width: bitmap.width, height: bitmap.height }
    } finally {
      bitmap.close()
    }
  }
  // Image decoding also works in browsers without createImageBitmap.
  const url = URL.createObjectURL(blob)
  const image = new Image()
  try {
    return await new Promise((resolve, reject) => {
      const cleanup = () => signal.removeEventListener('abort', cancel)
      const cancel = () => {
        cleanup()
        image.src = ''
        reject(cancelledError())
      }
      image.onload = () => {
        cleanup()
        resolve({ width: image.naturalWidth, height: image.naturalHeight })
      }
      image.onerror = () => {
        cleanup()
        reject(new ImageStudioError('image', '返回数据无法解码为图片', 'The returned data could not be decoded as an image'))
      }
      signal.addEventListener('abort', cancel, { once: true })
      if (signal.aborted) cancel()
      else image.src = url
    })
  } finally {
    URL.revokeObjectURL(url)
  }
}

async function readImage(reference: string, signal: AbortSignal): Promise<GeneratedImage> {
  let blob: Blob
  if (reference.startsWith('data:')) blob = imageDataUrlToBlob(reference)
  else {
    try {
      // Resource hosts never receive the upstream key or CPA cookies.
      const response = await fetch(reference, { signal, credentials: 'omit', referrerPolicy: 'no-referrer' })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      blob = await response.blob()
    } catch (error) {
      if (signal.aborted) throw error
      throw new ImageStudioError('image', '无法读取返回的原图，请检查图片地址的 CORS 或有效期；图片尚未保存', 'Could not read the original image. Check the image URL\'s CORS or expiry; the image is not saved', error instanceof Error ? error.message : undefined)
    }
  }
  try {
    const png = await readPngMetadata(blob)
    const size = await imageDimensions(blob, signal)
    if (!size.width || !size.height) throw new Error('Empty image')
    return {
      blob,
      metadata: {
        mimeType: png ? 'image/png' : blob.type,
        width: png?.width ?? size.width,
        height: png?.height ?? size.height,
        returnedSeed: png?.returnedSeed ?? null,
        parameters: png?.parameters ?? {},
        warnings: png?.warnings ?? [],
      },
    }
  } catch (error) {
    if (error instanceof ImageStudioError || signal.aborted) throw error
    throw new ImageStudioError('image', '返回数据不是有效图片', 'The returned data is not a valid image')
  }
}

async function withDeadline<T>(signal: AbortSignal, secret: string, operation: (signal: AbortSignal) => Promise<T>): Promise<T> {
  const controller = new AbortController()
  const cancel = () => controller.abort(cancelledError())
  signal.addEventListener('abort', cancel, { once: true })
  if (signal.aborted) cancel()
  const timer = setTimeout(() => controller.abort(new ImageStudioError(
    'timeout',
    '已达到 120 秒本地等待上限；上游结果和计费状态待确认',
    'The 120-second local timeout was reached; the upstream result and billing remain unknown',
  )), REQUEST_TIMEOUT_MS)
  let onAbort: () => void = () => undefined
  try {
    const aborted = new Promise<never>((_, reject) => {
      onAbort = () => reject(controller.signal.reason)
      controller.signal.addEventListener('abort', onAbort, { once: true })
      if (controller.signal.aborted) onAbort()
    })
    if (controller.signal.aborted) return await aborted
    return await Promise.race([operation(controller.signal), aborted])
  } catch (error) {
    const problem = toStudioProblem(error, secret)
    if (error instanceof TypeError && !controller.signal.aborted) {
      throw new ImageStudioError('network', '网络请求失败，请检查上游地址、CORS 和连接', 'Network request failed. Check the upstream URL, CORS and connectivity', redactSecret(error.message, secret))
    }
    throw new ImageStudioError(problem.kind, problem.zh, problem.en, problem.detail, problem.httpStatus)
  } finally {
    clearTimeout(timer)
    signal.removeEventListener('abort', cancel)
    controller.signal.removeEventListener('abort', onAbort)
  }
}

export async function generateImage(request: GenerationRequest, apiKey: string, signal: AbortSignal): Promise<GeneratedImage> {
  return withDeadline(signal, apiKey, async (requestSignal) => {
    const response = await fetch(request.endpoint, {
      method: 'POST',
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
      signal: requestSignal,
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey}` },
      body: JSON.stringify({ model: request.model, messages: [{ role: 'user', content: request.content }], stream: false }),
    })
    const payload = await readPayload(response)
    const reference = extractImageReference(imageContent(payload))
    const image = await readImage(reference, requestSignal)
    for (const [key, value] of Object.entries(image.metadata.parameters)) {
      if (typeof value === 'string') image.metadata.parameters[key] = redactSecret(value, apiKey)
    }
    image.metadata.warnings = image.metadata.warnings.map((warning) => ({
      ...warning,
      ...(warning.detail ? { detail: redactSecret(warning.detail, apiKey) } : {}),
    }))
    return image
  })
}

export async function loadUpstreamModels(connection: StudioConnection, signal: AbortSignal): Promise<string[]> {
  const validated = validateConnection(connection)
  return withDeadline(signal, validated.apiKey, async (requestSignal) => {
    const response = await fetch(upstreamApiUrl(validated.baseUrl, 'models'), {
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
      signal: requestSignal,
      headers: { Authorization: `Bearer ${validated.apiKey}` },
    })
    const payload = await readPayload(response)
    if (!isObject(payload) || !Array.isArray(payload.data) || !payload.data.every((item: unknown) => isObject(item) && typeof item.id === 'string' && item.id.trim())) {
      throw new ImageStudioError('response', '模型列表格式无效', 'The model list has an invalid format')
    }
    return [...new Set(payload.data.map((item: { id: string }) => item.id))]
  })
}
