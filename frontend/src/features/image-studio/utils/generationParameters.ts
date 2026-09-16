import type { GenerationDraft, GenerationInput, GenerationRequest, GenerationTask } from '../types'
import { ImageStudioError } from './studioErrors'

export const imageResolutions = [
  { width: 1216, height: 832 },
  { width: 832, height: 1216 },
  { width: 1024, height: 1024 },
]

export const novelAISamplers = [
  'Euler Ancestral',
  'Euler',
  'DPM++ 2S Ancestral',
  'DPM++ 2M SDE',
  'DPM++ 2M',
  'DPM++ SDE',
]

export function createDefaultDraft(): GenerationDraft {
  return {
    positivePrompt: '',
    artistPrompt: '',
    negativePrompt: '',
    model: 'nai-diffusion-4-5-curated',
    generationModel: '',
    width: 832,
    height: 1216,
    cfg: 5,
    steps: 28,
    sampler: 'Euler Ancestral',
    seed: '',
    count: 1,
  }
}

export function isDecimalSeed(value: string): boolean {
  return /^[+-]?\d+$/.test(value)
}

export function validateDraft(draft: GenerationDraft): GenerationInput {
  if (!draft.positivePrompt.trim()) {
    throw new ImageStudioError('validation', '请填写正向提示词', 'Enter a positive prompt')
  }
  if ([draft.positivePrompt, draft.artistPrompt, draft.negativePrompt].some((text) => /\bParameter\s*\{/i.test(text))) {
    throw new ImageStudioError(
      'validation',
      '提示词已包含 Parameter 块，请移除该块并使用参数面板，避免参数冲突',
      'A prompt already contains a Parameter block. Remove it and use the controls to avoid conflicting parameters',
    )
  }
  if (!draft.model.trim()) {
    throw new ImageStudioError('validation', '请选择或填写模型', 'Select or enter a model')
  }
  if (/[,{}\r\n]/.test(draft.generationModel.trim() || draft.model.trim())) {
    throw new ImageStudioError('validation', '生成模型名称不能包含参数分隔符或换行', 'The generation model name cannot contain parameter delimiters or line breaks')
  }
  if (!imageResolutions.some(({ width, height }) => draft.width === width && draft.height === height)) {
    throw new ImageStudioError('validation', '请选择文档支持的分辨率', 'Select a documented resolution')
  }
  if (draft.cfg === null || !Number.isFinite(draft.cfg)) {
    throw new ImageStudioError('validation', 'CFG 必须为有效数值，支持小数', 'CFG must be a finite number; decimals are supported')
  }
  if (draft.steps === null || !Number.isSafeInteger(draft.steps) || draft.steps < 1) {
    throw new ImageStudioError('validation', '步数必须为有效正整数', 'Steps must be a valid positive integer')
  }
  if (!novelAISamplers.includes(draft.sampler)) {
    throw new ImageStudioError('validation', '请选择文档支持的采样器', 'Select a documented sampler')
  }
  const seed = draft.seed.trim()
  if (seed && !isDecimalSeed(seed)) {
    throw new ImageStudioError('validation', '种子须为十进制整数，或留空逐张随机', 'Use a decimal integer seed, or leave it empty to randomize each image')
  }
  if (draft.count === null || !Number.isSafeInteger(draft.count) || draft.count < 1) {
    throw new ImageStudioError('validation', '生成张数必须为有效正整数', 'Image count must be a valid positive integer')
  }
  return { ...draft, model: draft.model.trim(), generationModel: draft.generationModel.trim(), seed, cfg: draft.cfg, steps: draft.steps, count: draft.count }
}

export function randomSeed(): string {
  const values = new Uint32Array(1)
  globalThis.crypto.getRandomValues(values)
  return String(values[0])
}

export function createStudioId(): string {
  const values = new Uint32Array(4)
  globalThis.crypto.getRandomValues(values)
  return Array.from(values, (value) => value.toString(16).padStart(8, '0')).join('')
}

export function assemblePrompt(input: GenerationInput, seed: string): string {
  const artist = input.artistPrompt.trim()
  const positive = input.positivePrompt.trim()
  const combined = artist ? `${artist}${/[,，]$/.test(artist) ? '' : ','} ${positive}` : positive
  return `${combined} Parameter{model:${input.generationModel || input.model}, width:${input.width}, height:${input.height}, sampler:${input.sampler}, scale:${input.cfg}, steps:${input.steps}, seed:${seed}, return_base64:true, negative_prompt:${input.negativePrompt}}`
}

export function createGenerationRequest(input: GenerationInput, endpoint: string): Readonly<GenerationRequest> {
  const submittedSeed = input.seed || randomSeed()
  return Object.freeze({
    endpoint,
    model: input.model,
    generationModel: input.generationModel || input.model,
    content: assemblePrompt(input, submittedSeed),
    submittedSeed,
  })
}

export function draftFromTask(task: GenerationTask): GenerationDraft {
  return {
    ...task.input,
    seed: task.image?.returnedSeed ?? task.request?.submittedSeed ?? task.input.seed,
    count: 1,
  }
}

export function seedComparison(task: GenerationTask): 'missing' | 'matches' | 'different' {
  const submitted = task.request?.submittedSeed
  const returned = task.image?.returnedSeed
  if (!submitted || !returned) return 'missing'
  return BigInt(submitted) === BigInt(returned) ? 'matches' : 'different'
}
