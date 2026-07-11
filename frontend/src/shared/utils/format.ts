import { currentLanguage } from '@/shared/i18n'

export function formatInteger(value: number): string {
  return new Intl.NumberFormat(currentLanguage.value === 'zh' ? 'zh-CN' : 'en-US', {
    maximumFractionDigits: 0,
  }).format(value)
}

export function formatCompact(value: number): string {
  return new Intl.NumberFormat('en-US', {
    notation: 'compact',
    compactDisplay: 'short',
    maximumFractionDigits: 1,
  }).format(value)
}

export function formatMultiplier(value: number): string {
  return String(value)
}

export function formatUsd(value: number | null | undefined): string {
  const normalized = typeof value === 'number' && Number.isFinite(value) ? value : 0
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: normalized < 1 ? 6 : 2,
  }).format(normalized)
}

export const BEIJING_TIME_ZONE = 'Asia/Shanghai'
const BEIJING_OFFSET = '+08:00'
const BEIJING_OFFSET_MS = 8 * 60 * 60 * 1000

function parseDisplayDate(value: string): Date | null {
  const localMatch = value.match(
    /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3})\d*)?)?)?$/,
  )
  if (localMatch) {
    const [, year, month, day, hour = '0', minute = '0', second = '0', millisecond = '0'] =
      localMatch
    return new Date(
      `${year}-${month}-${day}T${hour.padStart(2, '0')}:${minute.padStart(2, '0')}:${second.padStart(2, '0')}.${millisecond.padEnd(3, '0')}${BEIJING_OFFSET}`,
    )
  }
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? null : parsed
}

interface DateTimeFormatOptions {
  includeSecond?: boolean
}

export function formatDateTime(
  value: string | null,
  options: DateTimeFormatOptions = {},
): string {
  if (!value) {
    return '-'
  }
  const date = parseDisplayDate(value)
  if (!date) {
    return '-'
  }
  const formatOptions: Intl.DateTimeFormatOptions = {
    timeZone: BEIJING_TIME_ZONE,
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }
  if (options.includeSecond !== false) {
    formatOptions.second = '2-digit'
  }
  return new Intl.DateTimeFormat(
    currentLanguage.value === 'zh' ? 'zh-CN' : 'en-US',
    formatOptions,
  ).format(date)
}

export function formatLocalDateTimeParam(value: number): string {
  const date = new Date(value + BEIJING_OFFSET_MS)
  const pad = (part: number) => String(part).padStart(2, '0')
  return [
    `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`,
    `${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())}${BEIJING_OFFSET}`,
  ].join('T')
}

export function jsonPretty(value: unknown): string {
  return JSON.stringify(value, null, 2)
}
