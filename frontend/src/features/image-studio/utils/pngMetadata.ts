import type { StudioProblem } from '../types'
import { isDecimalSeed } from './generationParameters'
import { ImageStudioError, toStudioProblem } from './studioErrors'

interface PngMetadata {
  width: number
  height: number
  returnedSeed: string | null
  parameters: Record<string, string | number | boolean>
  warnings: StudioProblem[]
}

const pngSignature = [137, 80, 78, 71, 13, 10, 26, 10]
const maxInflatedMetadataBytes = 5 * 1024 * 1024
interface InflateBudget { used: number }
const crcTable = new Uint32Array(256).map((_, value) => {
  let crc = value
  for (let bit = 0; bit < 8; bit++) crc = (crc & 1) ? 0xedb88320 ^ (crc >>> 1) : crc >>> 1
  return crc >>> 0
})

function crc32(bytes: Uint8Array): number {
  let crc = 0xffffffff
  for (const byte of bytes) crc = (crcTable[(crc ^ byte) & 255] ?? 0) ^ (crc >>> 8)
  return (crc ^ 0xffffffff) >>> 0
}

function invalidPng(): ImageStudioError {
  return new ImageStudioError('image', '返回的 PNG 结构不完整或校验失败', 'The returned PNG is incomplete or failed its integrity check')
}

function invalidText(): ImageStudioError {
  return new ImageStudioError('metadata', 'PNG 文本元数据结构无效，无法核实这部分参数', 'PNG text metadata is malformed; these parameters could not be verified')
}

function metadataLimitExceeded(): ImageStudioError {
  return new ImageStudioError(
    'metadata',
    'PNG 文本元数据解压后超过 5 MiB，已停止读取',
    'PNG text metadata exceeds 5 MiB after decompression and was stopped',
  )
}

async function inflate(bytes: Uint8Array, budget: InflateBudget): Promise<Uint8Array> {
  if (budget.used >= maxInflatedMetadataBytes) throw metadataLimitExceeded()
  if (typeof DecompressionStream === 'undefined') {
    throw new ImageStudioError('metadata', '当前浏览器无法解压 PNG 元数据', 'This browser cannot decompress PNG metadata')
  }
  const stream = new Blob([new Uint8Array(bytes)]).stream().pipeThrough(new DecompressionStream('deflate'))
  const reader = stream.getReader()
  const chunks: Uint8Array[] = []
  let total = 0
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      total += value.byteLength
      if (budget.used + total > maxInflatedMetadataBytes) {
        const error = metadataLimitExceeded()
        budget.used = maxInflatedMetadataBytes + 1
        await reader.cancel(error)
        throw error
      }
      chunks.push(value)
    }
  } finally {
    reader.releaseLock()
  }
  budget.used += total
  const inflated = new Uint8Array(total)
  let offset = 0
  for (const chunk of chunks) {
    inflated.set(chunk, offset)
    offset += chunk.byteLength
  }
  return inflated
}

async function readTextChunk(type: string, bytes: Uint8Array, budget: InflateBudget): Promise<[string, string]> {
  const keywordEnd = bytes.indexOf(0)
  if (keywordEnd < 1 || keywordEnd > 79) throw invalidText()
  const keyword = new TextDecoder('latin1').decode(bytes.subarray(0, keywordEnd))
  let textBytes = bytes.subarray(keywordEnd + 1)
  let encoding = 'latin1'
  if (type === 'zTXt') {
    if (textBytes[0] !== 0) throw invalidText()
    textBytes = await inflate(textBytes.subarray(1), budget)
  } else if (type === 'iTXt') {
    const flag = textBytes[0]
    if ((flag !== 0 && flag !== 1) || textBytes[1] !== 0) throw invalidText()
    const languageEnd = textBytes.indexOf(0, 2)
    const translatedEnd = languageEnd < 0 ? -1 : textBytes.indexOf(0, languageEnd + 1)
    if (translatedEnd < 0) throw invalidText()
    textBytes = textBytes.subarray(translatedEnd + 1)
    if (flag === 1) textBytes = await inflate(textBytes, budget)
    encoding = 'utf-8'
  }
  return [keyword, new TextDecoder(encoding, { fatal: true }).decode(textBytes)]
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export async function readPngMetadata(blob: Blob): Promise<PngMetadata | null> {
  const bytes = new Uint8Array(await blob.arrayBuffer())
  if (!pngSignature.every((value, index) => bytes[index] === value)) return null
  const view = new DataView(bytes.buffer)
  const metadata: PngMetadata = { width: 0, height: 0, returnedSeed: null, parameters: {}, warnings: [] }
  const inflateBudget: InflateBudget = { used: 0 }
  const text = new Map<string, string>()
  let offset = 8
  let imageDataSeen = false
  let ended = false
  while (offset < bytes.length) {
    if (bytes.length - offset < 12) throw invalidPng()
    const length = view.getUint32(offset)
    if (length > bytes.length - offset - 12) throw invalidPng()
    const type = String.fromCharCode(...bytes.subarray(offset + 4, offset + 8))
    if (!/^[a-zA-Z]{4}$/.test(type)) throw invalidPng()
    const dataEnd = offset + 8 + length
    if (crc32(bytes.subarray(offset + 4, dataEnd)) !== view.getUint32(dataEnd)) throw invalidPng()
    const data = bytes.subarray(offset + 8, dataEnd)
    if (offset === 8 && type !== 'IHDR') throw invalidPng()
    if (type === 'IHDR') {
      if (length !== 13 || offset !== 8) throw invalidPng()
      metadata.width = view.getUint32(offset + 8)
      metadata.height = view.getUint32(offset + 12)
      if (!metadata.width || !metadata.height) throw invalidPng()
    } else if (type === 'tEXt' || type === 'iTXt' || type === 'zTXt') {
      try {
        const [key, value] = await readTextChunk(type, data, inflateBudget)
        text.set(key, value)
      } catch (error) {
        const problem = toStudioProblem(error)
        metadata.warnings.push({ ...problem, kind: 'metadata' })
      }
    } else if (type === 'IDAT') {
      imageDataSeen = true
    } else if (type === 'IEND') {
      if (length !== 0) throw invalidPng()
      ended = true
      break
    }
    offset = dataEnd + 4
  }
  if (!ended || !imageDataSeen) throw invalidPng()
  for (const key of ['Software', 'Source']) {
    const value = text.get(key)
    if (value) metadata.parameters[key] = value
  }
  const comment = text.get('Comment')
  if (comment) {
    try {
      const parsed: unknown = JSON.parse(comment)
      if (!isObject(parsed)) throw invalidText()
      // Only structured generation metadata is evidence; Description/prompt text is not a seed source.
      for (const key of ['sampler', 'scale', 'steps', 'model', 'prompt', 'uc', 'negative_prompt', 'width', 'height']) {
        const value = parsed[key]
        if (typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value))) {
          metadata.parameters[key] = value
        }
      }
      const seed = parsed.seed
      if (typeof seed === 'string' && isDecimalSeed(seed)) metadata.returnedSeed = seed
      else if (typeof seed === 'number' && Number.isSafeInteger(seed)) metadata.returnedSeed = String(seed)
      else if (seed !== undefined) {
        metadata.warnings.push({
          kind: 'metadata',
          zh: '返回种子格式无效或超出 JSON 精确整数范围，未将近似值作为实际种子',
          en: 'The returned seed is invalid or exceeds the exact JSON integer range; an approximate value was not used',
        })
      }
    } catch (error) {
      metadata.warnings.push({
        kind: 'metadata',
        zh: '无法解析 PNG 中的生成参数；原图仍然保留',
        en: 'PNG generation parameters could not be parsed; the original image is preserved',
        ...(error instanceof ImageStudioError ? { detail: error.message } : {}),
      })
    }
  }
  return metadata
}
