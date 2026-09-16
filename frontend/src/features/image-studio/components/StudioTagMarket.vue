<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NEmpty, NIcon, NInput, NModal, NSwitch, useDialog } from 'naive-ui'
import { Plus, Star } from 'lucide-vue-next'

import { useI18n } from '@/shared/i18n'

import { useImageStudio } from '../state/useImageStudio'
import { useStudioFeedback } from '../state/useStudioFeedback'
import { createStudioId } from '../utils/generationParameters'
import { appendPromptTags, promptTagCategories, type PromptTag, type PromptTagCategory } from '../utils/promptTags'

const props = defineProps<{ show: boolean; value: string; ownerId: number | null }>()
const emit = defineEmits<{ 'update:show': [show: boolean]; 'update:value': [value: string] }>()
const { language, t } = useI18n()
const dialog = useDialog()
const beginAction = useStudioFeedback()
const { tagFavorites, tagFavoritesSaving, saveTagFavorites } = useImageStudio()
const keyword = ref('')
const customTag = ref('')
const categoryId = ref('all')
const sectionId = ref('all')
const showNsfw = ref(false)
const selectedTags = ref<PromptTag[]>([])
const clearedTags = ref<PromptTag[]>([])
const openedOwner = ref<number | null>(null)
const marketSession = ref(0)
const catalogTags = promptTagCategories.flatMap((category) => category.tags)
const catalogTagsById = new Map(catalogTags.map((tag) => [tag.id, tag]))
const favoriteIds = computed(() => new Set(tagFavorites.value.map((tag) => tag.id)))
const resolvedFavorites = computed<PromptTag[]>(() => tagFavorites.value.map((tag) => tag.custom ? tag : (catalogTagsById.get(tag.id) ?? tag)))
const favoritesCategory = computed<PromptTagCategory>(() => ({
  id: 'favorites', labelZh: '我的收藏', labelEn: 'My favorites',
  tags: resolvedFavorites.value.filter((tag) => showNsfw.value || !tag.nsfw),
}))
const availableCategories = computed(() => [favoritesCategory.value, ...promptTagCategories.filter((category) => showNsfw.value || !category.nsfw).map((category) => ({
  ...category,
  tags: category.tags.filter((tag) => showNsfw.value || !tag.nsfw),
  ...(category.sections ? { sections: category.sections.map((section) => ({ ...section, tags: section.tags.filter((tag) => showNsfw.value || !tag.nsfw) })) } : {}),
}))])
const totalTags = computed(() => new Set(availableCategories.value.flatMap((category) => category.tags.map((tag) => tag.id))).size)
const search = computed(() => keyword.value.trim().toLowerCase())
const activeCategory = computed(() => search.value ? 'all' : categoryId.value)
const selectedIds = computed(() => new Set(selectedTags.value.map((tag) => tag.id)))
const activeSections = computed(() => availableCategories.value.find((category) => category.id === activeCategory.value)?.sections ?? [])
const hasNestedSections = computed(() => activeSections.value.some((section) => section.parentId !== section.id))
const sectionParents = computed(() => [...new Map(activeSections.value.map((section) => [section.parentId, {
  id: section.parentId, labelZh: section.parentZh, labelEn: section.parentEn,
}])).values()])
const visibleGroups = computed(() => availableCategories.value
  .map((category) => activeCategory.value === 'all' && category.id === 'favorites'
    ? { ...category, tags: category.tags.filter((tag) => tag.custom) }
    : category)
  .filter((category) => activeCategory.value === 'all' || category.id === activeCategory.value)
  .flatMap((category): PromptTagCategory[] => category.sections
    ? category.sections.filter((section) => search.value || sectionId.value === 'all' || section.id === sectionId.value || section.parentId === sectionId.value)
      .map((section) => ({ ...section, labelZh: `${category.labelZh} / ${section.parentId === section.id ? section.labelZh : `${section.parentZh} / ${section.labelZh}`}`, labelEn: `${category.labelEn} / ${section.parentId === section.id ? section.labelEn : `${section.parentEn} / ${section.labelEn}`}` }))
    : [category])
  .map((group) => ({
    ...group,
    tags: group.tags.filter((tag) => !search.value
      || tag.labelZh.toLowerCase().includes(search.value)
      || tag.prompt.toLowerCase().includes(search.value)
      || tag.aliases?.some((alias) => alias.toLowerCase().includes(search.value))),
  }))
  .filter((category) => category.tags.length))

function resetSelection(): void {
  keyword.value = ''
  categoryId.value = 'all'
  sectionId.value = 'all'
  showNsfw.value = false
  customTag.value = ''
  selectedTags.value = []
  clearedTags.value = []
}

watch(showNsfw, (enabled) => {
  if (!enabled) {
    selectedTags.value = selectedTags.value.filter((tag) => !tag.nsfw)
    clearedTags.value = clearedTags.value.filter((tag) => !tag.nsfw)
    if (!availableCategories.value.some((category) => category.id === categoryId.value)) {
      categoryId.value = 'all'
      sectionId.value = 'all'
    }
  }
}, { flush: 'sync' })

function clearSelection(): void {
  if (!selectedTags.value.length) return
  clearedTags.value = [...selectedTags.value]
  selectedTags.value = []
}

function restoreCleared(): void {
  if (!props.show || openedOwner.value !== props.ownerId) return
  selectedTags.value.push(...clearedTags.value.filter((tag) => !selectedIds.value.has(tag.id) && (showNsfw.value || !tag.nsfw)))
  clearedTags.value = []
}

watch(() => props.show, (show) => {
  marketSession.value += 1
  resetSelection()
  openedOwner.value = show ? props.ownerId : null
}, { immediate: true, flush: 'sync' })
watch(() => props.ownerId, () => {
  marketSession.value += 1
  resetSelection()
  openedOwner.value = null
  emit('update:show', false)
}, { flush: 'sync' })

function selectCategory(id: string): void {
  categoryId.value = id
  sectionId.value = 'all'
  keyword.value = ''
}

function toggleTag(tag: PromptTag): void {
  if (tag.nsfw && !showNsfw.value) return
  if (selectedIds.value.has(tag.id)) {
    selectedTags.value = selectedTags.value.filter((item) => item.id !== tag.id)
    clearedTags.value = clearedTags.value.filter((item) => item.id !== tag.id)
  } else selectedTags.value.push(tag)
}

async function toggleFavorite(tag: PromptTag): Promise<void> {
  if (props.ownerId === null || tagFavoritesSaving.value) return
  const action = beginAction()
  const next = favoriteIds.value.has(tag.id)
    ? tagFavorites.value.filter((item) => item.id !== tag.id)
    : [...tagFavorites.value, { ...tag, custom: tag.custom === true }]
  try {
    await saveTagFavorites(next)
  } catch (error) { action.error(error) }
}

async function addCustomFavorite(): Promise<void> {
  const prompt = customTag.value.trim()
  if (!prompt || props.ownerId === null || tagFavoritesSaving.value) return
  const catalogTag = catalogTags.find((tag) => (!tag.nsfw || showNsfw.value) && tag.prompt.toLowerCase() === prompt.toLowerCase())
  const existing = tagFavorites.value.find((tag) => tag.prompt.toLowerCase() === prompt.toLowerCase())
  if (existing) {
    customTag.value = ''
    return
  }
  const favorite: PromptTag = catalogTag ?? {
    id: `favorite-custom:${createStudioId()}`,
    labelZh: prompt,
    prompt,
    custom: true,
  }
  const action = beginAction()
  try {
    if (await saveTagFavorites([...tagFavorites.value, favorite]) && action.current()) customTag.value = ''
  } catch (error) { action.error(error) }
}

function applySelection(mode: 'append' | 'replace'): void {
  if (!props.show || props.ownerId === null || openedOwner.value !== props.ownerId || !selectedTags.value.length) return
  const terms = selectedTags.value.filter((tag) => showNsfw.value || !tag.nsfw).map((tag) => tag.prompt)
  if (!terms.length) return
  const ownerId = props.ownerId
  const session = marketSession.value
  const commit = () => {
    if (!props.show || props.ownerId !== ownerId || openedOwner.value !== ownerId || marketSession.value !== session) return
    emit('update:value', mode === 'append' ? appendPromptTags(props.value, terms) : terms.join(', '))
    emit('update:show', false)
  }
  if (mode === 'replace' && props.value.trim()) {
    dialog.warning({
      title: t('覆盖现有提示词？', 'Replace the existing prompt?'),
      content: t('当前正向提示词将被所选标签替换。', 'The current positive prompt will be replaced by the selected tags.'),
      positiveText: t('确认覆盖', 'Replace'),
      negativeText: t('取消', 'Cancel'),
      onPositiveClick: commit,
    })
    return
  }
  commit()
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    class="studio-tag-market"
    :title="t('标签市场', 'Tag market')"
    :aria-label="t('标签市场', 'Tag market')"
    :style="{ width: 'min(960px, calc(100vw - 24px))', height: 'min(760px, calc(100dvh - 24px))' }"
    :header-style="{ padding: '16px 18px' }"
    :content-style="{ padding: '0', minHeight: '0', display: 'flex', flexDirection: 'column', overflow: 'hidden' }"
    :auto-focus="true"
    :trap-focus="true"
    :close-on-esc="true"
    @update:show="emit('update:show', $event)"
  >
    <div class="market-search">
      <NInput v-model:value="keyword" clearable :placeholder="t('搜索中文名称或英文标签', 'Search Chinese names or English tags')" :input-props="{ id: 'studio-tag-search', 'aria-label': t('搜索标签', 'Search tags') }" />
      <div class="market-nsfw"><span id="studio-nsfw-label">{{ t('显示 NSFW 标签', 'Show NSFW tags') }}</span><NSwitch v-model:value="showNsfw" aria-labelledby="studio-nsfw-label" :aria-label="t('显示 NSFW 标签', 'Show NSFW tags')" /></div>
      <p>{{ t('选好标签，再追加或覆盖正向提示词。搜索覆盖所有分类。', 'Select tags, then append to or replace your positive prompt. Search covers all categories.') }}</p>
    </div>

    <div class="market-workspace">
      <nav class="market-categories" :aria-label="t('标签分类', 'Tag categories')">
        <NButton size="small" :type="activeCategory === 'all' ? 'primary' : 'default'" :secondary="activeCategory !== 'all'" :aria-pressed="activeCategory === 'all'" @click="selectCategory('all')">{{ t('全部分类', 'All categories') }}<span>{{ totalTags }}</span></NButton>
        <NButton v-for="category in availableCategories" :key="category.id" size="small" :type="activeCategory === category.id ? 'primary' : 'default'" :secondary="activeCategory !== category.id" :aria-pressed="activeCategory === category.id" @click="selectCategory(category.id)">{{ t(category.labelZh, category.labelEn) }}<span>{{ category.tags.length }}</span></NButton>
      </nav>

      <div class="market-catalog" tabindex="0" :aria-label="t('可选标签', 'Available tags')">
        <div v-if="activeCategory === 'favorites'" class="favorite-editor">
          <NInput v-model:value="customTag" :disabled="tagFavoritesSaving || ownerId === null" :placeholder="t('输入自定义标签', 'Enter a custom tag')" :input-props="{ id: 'studio-custom-favorite', 'aria-label': t('自定义收藏标签', 'Custom favorite tag') }" @keyup.enter="addCustomFavorite" />
          <NButton type="primary" secondary :loading="tagFavoritesSaving" :disabled="tagFavoritesSaving || !customTag.trim() || ownerId === null" @click="addCustomFavorite"><template #icon><NIcon :component="Plus" /></template>{{ t('添加', 'Add') }}</NButton>
        </div>
        <nav v-if="activeSections.length" class="market-subcategories" :class="{ 'is-flat': !hasNestedSections }" :aria-label="t('标签小分类', 'Tag subcategories')">
          <NButton size="small" :type="sectionId === 'all' ? 'primary' : 'default'" :aria-pressed="sectionId === 'all'" @click="sectionId = 'all'">{{ t('全部小分类', 'All subcategories') }}</NButton>
          <template v-if="hasNestedSections">
            <div v-for="parent in sectionParents" :key="parent.id" class="market-subcategory-row">
              <NButton size="small" :type="sectionId === parent.id ? 'primary' : 'default'" :aria-pressed="sectionId === parent.id" @click="sectionId = parent.id">{{ t(parent.labelZh, parent.labelEn) }}</NButton>
              <NButton v-for="section in activeSections.filter((item) => item.parentId === parent.id && item.id !== parent.id)" :key="section.id" size="small" secondary :type="sectionId === section.id ? 'primary' : 'default'" :aria-pressed="sectionId === section.id" @click="sectionId = section.id">{{ t(section.labelZh, section.labelEn) }} ({{ section.tags.length }})</NButton>
            </div>
          </template>
          <template v-else>
            <NButton v-for="section in activeSections" :key="section.id" size="small" :type="sectionId === section.id ? 'primary' : 'default'" :aria-pressed="sectionId === section.id" @click="sectionId = section.id">{{ t(section.labelZh, section.labelEn) }}</NButton>
          </template>
        </nav>
        <NEmpty v-if="!visibleGroups.length" :description="t('没有找到标签，试试其他关键词', 'No tags found. Try another search.')" />
        <section v-for="category in visibleGroups" :key="category.id" class="market-group" :aria-label="t(category.labelZh, category.labelEn)">
          <h3>{{ t(category.labelZh, category.labelEn) }} ({{ category.tags.length }})</h3>
          <div class="market-options">
            <div v-for="tag in category.tags" :key="tag.id" class="market-tag-item">
              <NButton class="market-tag" :type="selectedIds.has(tag.id) ? 'primary' : 'default'" secondary :aria-pressed="selectedIds.has(tag.id)" :aria-label="t(`${tag.labelZh}（${tag.prompt}）`, tag.prompt)" @click="toggleTag(tag)">
                <span class="tag-copy"><span>{{ t(tag.labelZh, tag.prompt) }}</span><code v-if="language === 'zh'">{{ tag.prompt }}</code></span>
                <span class="tag-indicator" aria-hidden="true">{{ selectedIds.has(tag.id) ? '✓' : '+' }}</span>
              </NButton>
              <NButton class="favorite-toggle" size="small" quaternary circle :disabled="tagFavoritesSaving || ownerId === null" :aria-label="favoriteIds.has(tag.id) ? t(`取消收藏：${tag.labelZh}`, `Remove ${tag.prompt} from favorites`) : t(`收藏：${tag.labelZh}`, `Favorite ${tag.prompt}`)" @click="toggleFavorite(tag)">
                <template #icon><NIcon><Star :fill="favoriteIds.has(tag.id) ? 'currentColor' : 'none'" /></NIcon></template>
              </NButton>
            </div>
          </div>
        </section>
      </div>
    </div>

    <div class="market-selection">
      <div class="selection-heading">
        <strong aria-live="polite">{{ t(`已选 ${selectedTags.length} 个标签`, `${selectedTags.length} tags selected`) }}</strong>
        <div class="selection-tools">
          <NButton size="small" text :disabled="!selectedTags.length" @click="clearSelection">{{ t('清空选择', 'Clear selection') }}</NButton>
          <NButton size="small" text :disabled="!clearedTags.length" @click="restoreCleared">{{ t('复原清空', 'Restore cleared') }}</NButton>
        </div>
      </div>
      <div v-if="selectedTags.length" class="selected-tag-list">
        <NButton v-for="tag in selectedTags" :key="tag.id" size="small" secondary :aria-label="t(`取消选择：${tag.labelZh}`, `Deselect ${tag.prompt}`)" @click="toggleTag(tag)">{{ t(tag.labelZh, tag.prompt) }}<span aria-hidden="true"> ×</span></NButton>
      </div>
      <p v-else class="selection-empty">{{ t('点击上方标签开始选择', 'Choose tags from the list above') }}</p>
    </div>

    <footer class="market-actions">
      <NButton @click="emit('update:show', false)">{{ t('取消', 'Cancel') }}</NButton>
      <NButton :disabled="!selectedTags.length" secondary @click="applySelection('replace')">{{ t('覆盖提示词', 'Replace prompt') }}</NButton>
      <NButton :disabled="!selectedTags.length" type="primary" @click="applySelection('append')">{{ t('追加到提示词', 'Append to prompt') }}</NButton>
    </footer>
  </NModal>
</template>

<style scoped>
.market-search { flex-shrink: 0; padding: 0 18px 12px; }
.market-nsfw { display: flex; align-items: center; gap: 8px; margin-top: 8px; font-size: 12px; color: var(--cpa-text-muted); }
.market-subcategories { display: grid; gap: 8px; margin-bottom: 16px; justify-items: start; }
.market-subcategories.is-flat { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.market-subcategories.is-flat .n-button { max-width: 100%; height: auto; min-height: 28px; white-space: normal; }
.market-subcategory-row { display: flex; flex-wrap: wrap; gap: 6px; }
.market-subcategory-row .n-button { max-width: 100%; height: auto; min-height: 28px; white-space: normal; }
.market-search p, .selection-empty { margin: 8px 0 0; color: var(--cpa-text-muted); font-size: 12px; }
.favorite-editor { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; margin-bottom: 14px; }
.market-workspace { display: grid; flex: 1; min-height: 0; grid-template-columns: 170px minmax(0, 1fr); border-block: 1px solid var(--cpa-border); }
.market-categories { display: flex; flex-direction: column; gap: 6px; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; padding: 12px; border-right: 1px solid var(--cpa-border); background: var(--cpa-surface-muted); }
.market-categories .n-button { flex-shrink: 0; justify-content: flex-start; }
.market-categories :deep(.n-button__content) { display: flex; justify-content: space-between; gap: 10px; width: 100%; }
.market-categories span { font-size: 11px; opacity: 0.75; }
.market-catalog { min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; padding: 14px; }
.market-group + .market-group { margin-top: 20px; }
.market-group h3 { margin: 0 0 10px; color: var(--cpa-text-strong); font-size: 13px; font-weight: 650; }
.market-options { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 8px; }
.market-tag-item { display: grid; grid-template-columns: minmax(0, 1fr) 30px; align-items: center; gap: 4px; min-width: 0; }
.market-tag { width: 100%; height: auto; min-height: 58px; padding: 8px 10px; }
.market-tag :deep(.n-button__content) { display: flex; justify-content: space-between; gap: 8px; width: 100%; min-width: 0; }
.favorite-toggle { color: var(--cpa-text-muted); }
.favorite-toggle:hover, .favorite-toggle:focus-visible { color: var(--cpa-warning); }
.tag-copy { display: grid; gap: 2px; min-width: 0; text-align: left; white-space: normal; overflow-wrap: anywhere; }
.tag-copy code { color: var(--cpa-text-muted); font-family: inherit; font-size: 11px; line-height: 1.35; }
.tag-indicator { flex-shrink: 0; font-size: 16px; }
.market-selection { flex-shrink: 0; padding: 10px 18px; }
.selection-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.selection-tools { display: flex; flex-wrap: wrap; gap: 8px; }
.selection-heading strong { color: var(--cpa-text-strong); font-size: 13px; }
.selected-tag-list { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; max-height: min(96px, 18dvh); overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; }
.selected-tag-list .n-button { max-width: 100%; height: auto; min-height: 28px; white-space: normal; }
.market-actions { display: flex; flex-shrink: 0; flex-wrap: wrap; justify-content: flex-end; gap: 8px; padding: 12px 18px; border-top: 1px solid var(--cpa-border); }
.market-actions .n-button:first-child { margin-right: auto; }
@media (max-width: 600px) {
  .market-workspace { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); }
  .market-categories { flex-direction: row; overflow-x: auto; overflow-y: hidden; padding: 8px 12px; border-right: 0; border-bottom: 1px solid var(--cpa-border); }
  .market-options { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .market-search, .market-selection { padding-inline: 12px; }
  .market-catalog { padding: 12px; }
  .market-actions { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); padding: 10px 12px; }
  .market-actions .n-button:first-child { grid-column: 1 / -1; justify-self: start; }
}
@media (max-height: 650px) {
  .market-search p { display: none; }
  .market-selection { padding-block: 6px; }
  .selected-tag-list { max-height: 56px; }
}
</style>
