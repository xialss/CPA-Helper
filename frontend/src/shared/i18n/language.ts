import { computed, ref, watch } from 'vue'

export type AppLanguage = 'zh' | 'en'

const storageKey = 'cpa-helper-language'
const supportedLanguages = new Set<AppLanguage>(['zh', 'en'])

function normalizeLanguage(value: string | null | undefined): AppLanguage | null {
  if (!value) {
    return null
  }
  const normalized = value.toLowerCase()
  if (normalized.startsWith('zh')) {
    return 'zh'
  }
  if (normalized.startsWith('en')) {
    return 'en'
  }
  return null
}

function getStoredLanguage(): AppLanguage | null {
  if (typeof localStorage === 'undefined') {
    return null
  }
  try {
    const stored = localStorage.getItem(storageKey)
    return supportedLanguages.has(stored as AppLanguage) ? (stored as AppLanguage) : null
  } catch {
    return null
  }
}

function getBrowserLanguage(): AppLanguage {
  if (typeof navigator === 'undefined') {
    return 'en'
  }
  const candidates = navigator.languages?.length ? navigator.languages : [navigator.language]
  for (const candidate of candidates) {
    const language = normalizeLanguage(candidate)
    if (language) {
      return language
    }
  }
  return 'en'
}

export const currentLanguage = ref<AppLanguage>(getStoredLanguage() ?? getBrowserLanguage())
export const isEnglish = computed(() => currentLanguage.value === 'en')

watch(
  currentLanguage,
  (value) => {
    try {
      localStorage.setItem(storageKey, value)
    } catch {
      // Storage can be unavailable in private or embedded contexts.
    }
    if (typeof document !== 'undefined') {
      document.documentElement.lang = value === 'zh' ? 'zh-CN' : 'en'
    }
  },
  { immediate: true },
)

export function setLanguage(value: AppLanguage) {
  currentLanguage.value = value
}

export function toggleLanguage() {
  currentLanguage.value = currentLanguage.value === 'zh' ? 'en' : 'zh'
}

export function localize(zh: string, en: string): string {
  return currentLanguage.value === 'zh' ? zh : en
}

export function useLanguagePreference() {
  return {
    isEnglish,
    language: currentLanguage,
    localize,
    setLanguage,
    toggleLanguage,
  }
}
