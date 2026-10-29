import type { AIProviderModel } from '@/shared/types/api'

export function matchesExcludedRule(model: string, rule: string): boolean {
  const expression = rule.split('*').map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('.*')
  return new RegExp(`^${expression}$`, 'i').test(model)
}

export function excludedModelCatalog(configured: AIProviderModel[], discovered: AIProviderModel[]): AIProviderModel[] {
  const entries = new Map<string, AIProviderModel>()
  for (const model of [...configured, ...discovered]) {
    const name = model.name.trim()
    if (name && !entries.has(name.toLowerCase())) entries.set(name.toLowerCase(), { ...model, name })
  }
  return [...entries.values()]
}

export function toggleExactExcludedModel(rules: string[], name: string, checked: boolean): string[] {
  const next = rules.filter((rule) => rule.toLowerCase() !== name.toLowerCase())
  if (checked) next.push(name)
  return next
}
