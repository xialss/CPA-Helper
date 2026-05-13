import { computed, ref, watch } from 'vue'
import { darkTheme } from 'naive-ui'

import type { ThemePreference } from '@/shared/types/api'

const storageKey = 'cpa-helper-theme'
const themeSwitchingClass = 'theme-switching'
const themeSwitchingDurationMs = 180
const preference = ref<ThemePreference>(
  (localStorage.getItem(storageKey) as ThemePreference | null) ?? 'system',
)
const prefersDark = ref(window.matchMedia('(prefers-color-scheme: dark)').matches)
const media = window.matchMedia('(prefers-color-scheme: dark)')
const root = document.documentElement
const naiveThemeIsDark = ref(false)
let themeSwitchingTimer: number | undefined
let naiveThemeFrame: number | undefined

function handleMediaChange(event: MediaQueryListEvent) {
  prefersDark.value = event.matches
}

function markThemeSwitching() {
  root.classList.add(themeSwitchingClass)
  if (themeSwitchingTimer !== undefined) {
    window.clearTimeout(themeSwitchingTimer)
  }
  themeSwitchingTimer = window.setTimeout(() => {
    root.classList.remove(themeSwitchingClass)
    themeSwitchingTimer = undefined
  }, themeSwitchingDurationMs)
}

function syncNaiveTheme(value: boolean) {
  if (naiveThemeFrame !== undefined) {
    window.cancelAnimationFrame(naiveThemeFrame)
  }
  naiveThemeFrame = window.requestAnimationFrame(() => {
    naiveThemeIsDark.value = value
    naiveThemeFrame = undefined
  })
}

media.addEventListener('change', handleMediaChange)

watch(
  preference,
  (value) => {
    localStorage.setItem(storageKey, value)
  },
  { immediate: true },
)

const isDark = computed(() =>
  preference.value === 'system' ? prefersDark.value : preference.value === 'dark',
)

watch(
  isDark,
  (value, oldValue) => {
    root.classList.toggle('dark', value)
    if (oldValue === undefined) {
      naiveThemeIsDark.value = value
      return
    }
    markThemeSwitching()
    syncNaiveTheme(value)
  },
  { immediate: true },
)

export function useThemePreference() {
  const naiveTheme = computed(() => (naiveThemeIsDark.value ? darkTheme : null))

  function setThemePreference(value: ThemePreference) {
    preference.value = value
  }

  function toggleTheme() {
    preference.value = isDark.value ? 'light' : 'dark'
  }

  return {
    isDark,
    naiveTheme,
    naiveThemeIsDark,
    preference,
    setThemePreference,
    toggleTheme,
  }
}
