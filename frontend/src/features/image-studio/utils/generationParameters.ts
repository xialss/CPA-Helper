import type { GenerationDraft, GenerationInput, GenerationRequest, GenerationTask } from '../types'
import { ImageStudioError } from './studioErrors'

const characterPositionRows = ['at the top', 'slightly above center', '', 'slightly below center', 'at the bottom']
const characterPositionColumns = ['on the left', 'slightly left of center', '', 'slightly right of center', 'on the right']
const characterPositionHints = new Set(
  characterPositionRows.flatMap((row) => characterPositionColumns.map((column) => [row, column].filter(Boolean).join(', ') || 'in the center')),
)

/** Return a position from the fixed 5×5 vocabulary, or empty for legacy/unsupported values. */
export function normalizePositionHint(value: unknown): string {
  const hint = typeof value === 'string' ? value.trim() : ''
  return characterPositionHints.has(hint) ? hint : ''
}

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
    characterPromptsEnabled: false,
    characters: [{ prompt: '', positionHint: '' }],
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

export function copyDraft(draft: Partial<GenerationDraft> | GenerationInput): GenerationDraft {
  const characters = draft.characters?.length ? draft.characters : [{ prompt: '', positionHint: '' }]
  return {
    ...createDefaultDraft(), ...draft,
    characterPromptsEnabled: draft.characterPromptsEnabled ?? false,
    characters: characters.map((character) => ({ ...character, positionHint: normalizePositionHint(character.positionHint) })),
  }
}

export function isDecimalSeed(value: string): boolean {
  return /^[+-]?\d+$/.test(value)
}

export function validateDraft(draft: GenerationDraft): GenerationInput {
  if (!draft.positivePrompt.trim()) {
    throw new ImageStudioError('validation', '请填写正向提示词', 'Enter a positive prompt')
  }
  const characters = (draft.characters ?? [{ prompt: '', positionHint: '' }]).map((character) => ({
    ...character,
    positionHint: normalizePositionHint(character.positionHint),
  }))
  const characterTexts = draft.characterPromptsEnabled ? characters.flatMap((character) => [character.prompt, character.positionHint]) : []
  if (draft.characterPromptsEnabled) {
    if (!characters.length || characters.some((character) => !character.prompt.trim())) {
      throw new ImageStudioError('validation', '请为每个角色填写提示词，至少保留一个角色', 'Enter a prompt for every character and keep at least one character')
    }
    if ([draft.positivePrompt, draft.artistPrompt, ...characterTexts].some((text) => text.includes('|'))) {
      throw new ImageStudioError('validation', '独立角色模式的全局、画师和角色提示不能包含 | 分隔符', 'Global, artist and character prompts cannot contain the | delimiter in character mode')
    }
  }
  if ([draft.positivePrompt, draft.artistPrompt, draft.negativePrompt, ...characterTexts].some((text) => /\bParameter\s*\{/i.test(text))) {
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
  return { ...draft, characterPromptsEnabled: draft.characterPromptsEnabled ?? false, characters: Object.freeze(characters.map((character) => Object.freeze({ ...character }))), model: draft.model.trim(), generationModel: draft.generationModel.trim(), seed, cfg: draft.cfg, steps: draft.steps, count: draft.count }
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
  const global = artist ? `${artist}${/[,，]$/.test(artist) ? '' : ','} ${positive}` : positive
  const combined = input.characterPromptsEnabled ? [global, ...input.characters.map((character) => {
    const prompt = character.prompt.trim()
    const position = normalizePositionHint(character.positionHint)
    return position ? `${prompt}${/[,，]$/.test(prompt) ? '' : ','} ${position}` : prompt
  })].join(' | ') : global
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
    ...copyDraft(task.input),
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
