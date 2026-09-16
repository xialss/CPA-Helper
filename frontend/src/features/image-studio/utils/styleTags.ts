import type { PromptTagSection } from './promptTags'

function section(id: string, labelZh: string, labelEn: string, entries: readonly (readonly [string, string, string?])[]): PromptTagSection {
  return {
    id, labelZh, labelEn, parentId: id, parentZh: labelZh, parentEn: labelEn,
    tags: entries.map(([prompt, label, originalId]) => ({ id: originalId ?? `style-${prompt.replace(/ /g, '-')}`, prompt, labelZh: label })),
  }
}

// Reference wording replaces equivalent old terms; their IDs remain stable.
// Distinct existing colors, printmaking and detail terms remain in these groups.
export const styleSections: readonly PromptTagSection[] = [
  section('style-quality', '质量', 'Quality', [
    ['masterpiece', '杰作级画质'], ['best quality', '最佳画质'], ['highly detailed', '高度精细', 'detail-high'],
    ['absurdres', '超高分辨率'], ['highres', '高分辨率'], ['very aesthetic', '极佳美感'], ['ultra-detailed', '极致细节'],
    ['crisp edges', '清晰边缘', 'detail-edges'], ['realistic texture', '真实质感', 'detail-texture'],
    ['detailed background', '细致背景', 'detail-background'], ['detailed reflections', '精细倒影', 'detail-reflections'],
  ]),
  section('style-art', '画风', 'Art style', [
    ['anime style', '日本动画风', 'style-anime'], ['realistic', '写实风格'], ['photorealistic', '照片级真实'], ['chibi', 'Q版/二头身'],
    ['monochrome', '单色画面', 'color-monochrome'], ['greyscale', '黑白灰阶'], ['retro artstyle', '复古画风'],
    ['1990s (style)', '九十年代动画风'], ['pixel art', '像素画', 'style-pixel'], ['official art', '官方设定图'], ['concept art', '概念设定稿'],
    ['scenery', '风景为主'], ['manga', '漫画风格'], ['4koma', '四格漫画'], ['game cg', '游戏CG风'], ['key visual', '主视觉图'],
    ['promotional art', '宣传图'], ['character sheet', '角色设定表'], ['high contrast', '高对比', 'color-contrast'],
    ['muted colors', '低饱和配色', 'color-muted'], ['pastel colors', '粉彩色调', 'color-pastel'],
    ['vibrant colors', '鲜艳色彩', 'color-vibrant'], ['limited palette', '有限配色', 'color-limited'], ['sepia', '棕褐色调'],
    ['ukiyo-e', '浮世绘'], ['art nouveau', '新艺术风格'], ['art deco', '装饰艺术风'], ['minimalism', '极简风格'], ['surreal', '超现实'],
    ['warm colors', '暖色调', 'color-warm'], ['cool colors', '冷色调', 'color-cool'],
    ['blue and gold', '蓝金配色', 'color-blue-gold'], ['earth tones', '大地色系', 'color-earth'],
  ]),
  section('style-medium', '媒介与技法', 'Media and techniques', [
    ['watercolor (medium)', '水彩画', 'style-watercolor'], ['sketch', '铅笔草稿', 'style-pencil'], ['lineart', '纯线稿', 'detail-lineart'],
    ['cel shading', '赛璐璐平涂'], ['flat color', '平涂上色', 'style-flat'], ['oil painting (medium)', '油画质感', 'style-oil'],
    ['traditional media', '手绘传统媒介'], ['impasto', '厚涂画法'], ['soft shading', '柔和阴影', 'detail-shading'],
    ['film grain', '胶片颗粒'], ['screentone', '漫画网点'], ['ink wash painting', '水墨画', 'style-ink'],
    ['thick outlines', '粗描边'], ['rough sketch', '粗略草稿'], ['painterly', '绘画笔触感'], ['digital painting', '数字绘画'],
    ['airbrush', '喷枪质感'], ['gouache', '水粉'], ['acrylic paint', '丙烯颜料'], ['colored pencil', '彩色铅笔'],
    ['marker', '马克笔'], ['crayon', '蜡笔'], ['charcoal', '炭笔'], ['ink', '墨线', 'style-ink-lines'], ['stained glass', '彩绘玻璃风'],
    ['mosaic', '马赛克拼贴'], ['papercraft', '剪纸风', 'style-paper'], ['collage', '拼贴画'], ['vector art', '矢量风'],
    ['3d', '三维渲染', 'style-3d'], ['toon shading', '卡通渲染'], ['realistic shading', '写实光影'],
    ['soft brush', '柔和笔刷', 'detail-brushstrokes'], ['texture overlay', '纹理叠加'], ['grainy', '颗粒质感'],
    ['woodcut', '木刻版画', 'style-woodcut'],
  ]),
]
