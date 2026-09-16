import { localize } from '@/shared/i18n'

import type { ProblemKind, StudioProblem } from '../types'

export class ImageStudioError extends Error {
  readonly problem: StudioProblem

  constructor(kind: ProblemKind, zh: string, en: string, detail?: string, httpStatus?: number) {
    super(localize(zh, en))
    this.name = 'ImageStudioError'
    this.problem = {
      kind,
      zh,
      en,
      ...(detail ? { detail } : {}),
      ...(httpStatus === undefined ? {} : { httpStatus }),
    }
  }
}

export function problemMessage(problem: StudioProblem): string {
  const label = localize(problem.zh, problem.en)
  return problem.detail ? `${label}: ${problem.detail}` : label
}

export function redactSecret(text: string, secret: string): string {
  return secret ? text.split(secret).join('[redacted]') : text
}

export function toStudioProblem(error: unknown, secret = ''): StudioProblem {
  const problem: StudioProblem = error instanceof ImageStudioError
    ? error.problem
    : {
        kind: 'unexpected',
        zh: '操作失败',
        en: 'The operation failed',
        ...(error instanceof Error ? { detail: error.message } : {}),
      }
  return {
    ...problem,
    zh: redactSecret(problem.zh, secret),
    en: redactSecret(problem.en, secret),
    ...(problem.detail ? { detail: redactSecret(problem.detail, secret) } : {}),
  }
}

export function storageProblem(error: unknown): StudioProblem {
  if (error instanceof ImageStudioError) return error.problem
  return {
    kind: 'storage',
    zh: '本地保存失败；当前结果尚未持久保存，请下载原图或重试保存',
    en: 'Local save failed. This result is not persisted; download the original or retry saving',
    ...(error instanceof Error ? { detail: `${error.name}: ${error.message}` } : {}),
  }
}

export function cancelledError(): ImageStudioError {
  return new ImageStudioError(
    'cancelled',
    '已取消本地请求；上游仍可能继续生成或计费',
    'Local request cancelled; the upstream may still generate or charge for it',
  )
}
