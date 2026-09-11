import type { ModelPrice } from '@/shared/types/api'

const tokenRateFields = ['input_usd_per_million', 'output_usd_per_million', 'cache_read_usd_per_million', 'cache_creation_usd_per_million'] as const

function scaledPrice(value: number, multiplier: number): number {
  const scaled = value * multiplier
  const precisionScaled = scaled * 1e12
  // Decimal rounding must not overflow an otherwise finite monetary result.
  return Number.isFinite(precisionScaled) ? Math.round(precisionScaled) / 1e12 : scaled
}

export function multiplyModelPrice(template: ModelPrice, multiplier: number): ModelPrice | null {
  const result = { ...template, long_context: template.long_context ? { ...template.long_context } : null }
  if (template.billing_unit === 'request') {
    if (template.request_usd === null) return null
    result.request_usd = scaledPrice(template.request_usd, multiplier)
    return Number.isFinite(result.request_usd) && result.request_usd >= 0 ? result : null
  }
  for (const field of tokenRateFields) {
    result[field] = scaledPrice(template[field], multiplier)
    if (!Number.isFinite(result[field]) || result[field] < 0) return null
    if (result.long_context) {
      result.long_context[field] = scaledPrice(result.long_context[field], multiplier)
      if (!Number.isFinite(result.long_context[field]) || result.long_context[field] < 0) return null
    }
  }
  return result
}
