import { appearanceSections } from './appearanceTags'
import { characterSections, subjectTags } from './characterTags'
import { ipSections } from './ipTags'
import { sceneSections } from './sceneTags'
import { styleSections } from './styleTags'
import { nsfwSections } from './nsfwTags'

export interface PromptTag {
  id: string
  labelZh: string
  prompt: string
  aliases?: readonly string[]
  nsfw?: boolean
  custom?: boolean
}

export interface PromptTagSection {
  id: string
  labelZh: string
  labelEn: string
  parentId: string
  parentZh: string
  parentEn: string
  tags: readonly PromptTag[]
}

export interface PromptTagCategory {
  id: string
  labelZh: string
  labelEn: string
  tags: readonly PromptTag[]
  sections?: readonly PromptTagSection[]
  nsfw?: boolean
}

const allAppearanceSections = [...appearanceSections, ...characterSections]

const baseCategories: readonly PromptTagCategory[] = [
  {
    id: 'subjects', labelZh: '人数与主体', labelEn: 'People and subjects',
    tags: subjectTags,
  },
  {
    id: 'appearance', labelZh: '人物特征', labelEn: 'Appearance',
    tags: allAppearanceSections.flatMap((section) => section.tags),
    sections: allAppearanceSections,
  },
  {
    id: 'scenes', labelZh: '场景环境', labelEn: 'Scenes',
    tags: [
      { id: 'scene-forest', labelZh: '森林', prompt: 'forest' },
      { id: 'scene-flowers', labelZh: '花田', prompt: 'flower field' },
      { id: 'scene-mountain', labelZh: '山峦', prompt: 'mountain' },
      { id: 'scene-lake', labelZh: '湖泊', prompt: 'lake' },
      { id: 'scene-beach', labelZh: '海滩', prompt: 'beach' },
      { id: 'scene-garden', labelZh: '花园', prompt: 'garden' },
      { id: 'scene-library', labelZh: '图书馆', prompt: 'library' },
      { id: 'scene-cafe', labelZh: '咖啡馆', prompt: 'cafe' },
      { id: 'scene-night-street', labelZh: '夜晚街道', prompt: 'street at night' },
      { id: 'scene-starry-sky', labelZh: '星空', prompt: 'starry sky' },
    ],
  },
  {
    id: 'composition', labelZh: '构图视角', labelEn: 'Composition',
    tags: [
      { id: 'composition-full-body', labelZh: '全身', prompt: 'full body' },
      { id: 'composition-upper-body', labelZh: '半身', prompt: 'upper body' },
      { id: 'composition-close-up', labelZh: '特写', prompt: 'close-up' },
      { id: 'composition-wide-shot', labelZh: '远景', prompt: 'wide shot' },
      { id: 'composition-centered', labelZh: '居中构图', prompt: 'centered composition' },
      { id: 'composition-thirds', labelZh: '三分法', prompt: 'rule of thirds' },
      { id: 'composition-symmetry', labelZh: '对称构图', prompt: 'symmetrical composition' },
      { id: 'composition-above', labelZh: '俯视', prompt: 'from above' },
      { id: 'composition-below', labelZh: '仰视', prompt: 'from below' },
      { id: 'composition-side', labelZh: '侧面视角', prompt: 'side view' },
    ],
  },
  {
    id: 'lens', labelZh: '镜头景深', labelEn: 'Lens and focus',
    tags: [
      { id: 'lens-shallow', labelZh: '浅景深', prompt: 'shallow depth of field' },
      { id: 'lens-deep', labelZh: '深景深', prompt: 'deep depth of field' },
      { id: 'lens-bokeh', labelZh: '背景散景', prompt: 'bokeh' },
      { id: 'lens-wide-angle', labelZh: '广角镜头', prompt: 'wide-angle lens' },
      { id: 'lens-telephoto', labelZh: '长焦镜头', prompt: 'telephoto lens' },
      { id: 'lens-macro', labelZh: '微距摄影', prompt: 'macro photography' },
      { id: 'lens-fisheye', labelZh: '鱼眼镜头', prompt: 'fisheye lens' },
      { id: 'lens-soft-focus', labelZh: '柔焦', prompt: 'soft focus' },
    ],
  },
  {
    id: 'lighting', labelZh: '光照氛围', labelEn: 'Lighting',
    tags: [
      { id: 'lighting-natural', labelZh: '自然光', prompt: 'natural light' },
      { id: 'lighting-soft', labelZh: '柔和光照', prompt: 'soft lighting' },
      { id: 'lighting-golden', labelZh: '黄金时刻', prompt: 'golden hour' },
      { id: 'lighting-backlight', labelZh: '逆光', prompt: 'backlighting' },
      { id: 'lighting-rim', labelZh: '轮廓光', prompt: 'rim lighting' },
      { id: 'lighting-volumetric', labelZh: '体积光', prompt: 'volumetric lighting' },
      { id: 'lighting-moon', labelZh: '月光', prompt: 'moonlight' },
      { id: 'lighting-neon', labelZh: '霓虹灯光', prompt: 'neon lighting' },
      { id: 'lighting-overcast', labelZh: '阴天光照', prompt: 'overcast lighting' },
      { id: 'lighting-candle', labelZh: '烛光', prompt: 'candlelight' },
    ],
  },
]

function groupedCategory(id: string, labelZh: string, labelEn: string, ids: readonly string[]): PromptTagCategory {
  const sections = baseCategories.filter((category) => ids.includes(category.id)).map((category) => ({
    ...category, parentId: category.id, parentZh: category.labelZh, parentEn: category.labelEn,
    // Soft focus is now listed once under the reference catalog's Effects section.
    tags: category.tags.filter((tag) => tag.prompt !== 'soft focus'),
  }))
  return { id, labelZh, labelEn, sections, tags: sections.flatMap((section) => section.tags) }
}

const originalTags = new Map(baseCategories.flatMap((category) => category.tags.map((tag) => [tag.prompt, tag] as const)))
const referenceSceneTerms = new Set(sceneSections.flatMap((section) => section.tags.map((tag) => tag.prompt)))
const expandedSceneSections = sceneSections.map((section) => ({
  ...section,
  tags: [
    ...section.tags.map((tag) => {
      const original = originalTags.get(tag.prompt)
      return original ? { ...tag, id: original.id } : tag
    }),
    ...baseCategories.filter((category) => category.id === (section.id === 'scene-background' ? 'scenes' : section.id === 'scene-lighting' ? 'lighting' : ''))
      .flatMap((category) => category.tags.filter((tag) => !referenceSceneTerms.has(tag.prompt))),
  ],
}))
const safeAppearanceSections = allAppearanceSections.map((section) => ({ ...section, tags: section.tags.filter((tag) => !tag.nsfw) }))

export const promptTagCategories: readonly PromptTagCategory[] = [
  { id: 'subjects', labelZh: '人数与主体', labelEn: 'People and subjects', tags: subjectTags },
  { id: 'appearance', labelZh: '人物特征', labelEn: 'Appearance', sections: safeAppearanceSections, tags: safeAppearanceSections.flatMap((section) => section.tags) },
  { id: 'scenes', labelZh: '场景环境', labelEn: 'Scenes', sections: expandedSceneSections, tags: expandedSceneSections.flatMap((section) => section.tags) },
  groupedCategory('composition', '构图与镜头', 'Composition and lens', ['composition', 'lens']),
  { id: 'styles', labelZh: '风格与细节', labelEn: 'Style and details', sections: styleSections, tags: styleSections.flatMap((section) => section.tags) },
  { id: 'ip', labelZh: 'IP', labelEn: 'IP', sections: ipSections, tags: ipSections.flatMap((section) => section.tags) },
  { id: 'nsfw', labelZh: 'NSFW', labelEn: 'NSFW', nsfw: true, sections: nsfwSections, tags: nsfwSections.flatMap((section) => section.tags) },
]

export function appendPromptTags(prompt: string, tags: readonly string[]): string {
  // Only compare whole, simple comma/newline-separated terms; never rewrite weights.
  const existing = new Set(prompt.split(/[,，\r\n]/).map((tag) => tag.trim()))
  const additions = tags.filter((tag) => {
    if (existing.has(tag)) return false
    existing.add(tag)
    return true
  })
  if (!additions.length) return prompt
  if (!prompt.trim()) return prompt + additions.join(', ')
  const separator = /[,，]\s*$/.test(prompt) ? (/\s$/.test(prompt) ? '' : ' ') : ', '
  return prompt + separator + additions.join(', ')
}
