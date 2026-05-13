<script setup lang="ts">
import { computed } from 'vue'
import {
  NConfigProvider,
  NDialogProvider,
  NGlobalStyle,
  NMessageProvider,
  dateZhCN,
  zhCN,
  type GlobalThemeOverrides,
} from 'naive-ui'

import { useThemePreference } from '@/shared/composables/useThemePreference'

const { naiveTheme, naiveThemeIsDark } = useThemePreference()

const themeOverrides = computed<GlobalThemeOverrides>(() => ({
  common: {
    borderRadius: '8px',
    borderRadiusSmall: '6px',
    primaryColor: naiveThemeIsDark.value ? '#22c1c8' : '#009aa8',
    primaryColorHover: naiveThemeIsDark.value ? '#45d4da' : '#07adba',
    primaryColorPressed: naiveThemeIsDark.value ? '#1499a3' : '#047b87',
    primaryColorSuppl: naiveThemeIsDark.value ? '#45d4da' : '#07adba',
    successColor: naiveThemeIsDark.value ? '#45c478' : '#18a058',
    warningColor: naiveThemeIsDark.value ? '#e4ad45' : '#b7791f',
    errorColor: naiveThemeIsDark.value ? '#ff7878' : '#d34b4b',
    infoColor: naiveThemeIsDark.value ? '#58a6ff' : '#1d8dff',
    fontFamily:
      'Aptos, Segoe UI, Microsoft YaHei UI, Microsoft YaHei, Helvetica Neue, sans-serif',
  },
  Button: {
    fontWeight: '600',
  },
  Card: {
    borderRadius: '8px',
  },
  DataTable: {
    thFontWeight: '700',
    borderRadius: '8px',
  },
  Layout: {
    color: naiveThemeIsDark.value ? '#0e171b' : '#f4f8f9',
    colorEmbedded: naiveThemeIsDark.value ? '#0e171b' : '#f4f8f9',
    headerColor: naiveThemeIsDark.value ? '#111f24' : '#fbfdfe',
    siderColor: naiveThemeIsDark.value ? '#162227' : '#ffffff',
  },
}))
</script>

<template>
  <NConfigProvider
    :theme="naiveTheme"
    :theme-overrides="themeOverrides"
    :locale="zhCN"
    :date-locale="dateZhCN"
  >
    <NDialogProvider>
      <NMessageProvider>
        <RouterView />
      </NMessageProvider>
    </NDialogProvider>
    <NGlobalStyle />
  </NConfigProvider>
</template>
