<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { useRoute } from 'vue-router'

import { useI18n } from '@/shared/i18n'
import FeatureStatusNotice from '../components/FeatureStatusNotice.vue'
import { useFeatureState } from '../state/useFeatureState'
import type { FeatureName } from '../state/featureController'

const route = useRoute()
const { t } = useI18n()
const { imageStudio, aiRadar } = useFeatureState()
const feature = computed<FeatureName>(() => route.meta.feature === 'image_studio' ? 'image_studio' : 'ai_radar')
const availability = computed(() => feature.value === 'image_studio' ? imageStudio.value : aiRadar.value)
// Async loaders are never invoked while disabled/unknown. The shell does not
// import either business module, its catalog, or its IndexedDB singleton.
const studioView = defineAsyncComponent(() => import('@/features/image-studio/views/ImageStudioView.vue'))
const radarView = defineAsyncComponent(() => import('@/features/ai-radar/views/AiRadarView.vue'))
</script>

<template>
  <component :is="feature === 'image_studio' ? studioView : radarView" v-if="availability === 'enabled'" />
  <div v-else class="page">
    <header class="page-header"><h1 class="page-title">{{ feature === 'image_studio' ? t('生图广场', 'Image Studio') : t('AI 雷达', 'AI Radar') }}</h1></header>
    <FeatureStatusNotice :feature="feature" />
  </div>
</template>
