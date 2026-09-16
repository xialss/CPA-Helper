import type { PromptTagSection } from './promptTags'

function section(id: string, labelZh: string, labelEn: string, entries: readonly (readonly [string, string])[]): PromptTagSection {
  return {
    id, labelZh, labelEn, parentId: id, parentZh: labelZh, parentEn: labelEn,
    tags: entries.map(([prompt, label]) => ({ id: `scene-${prompt.replace(/ /g, '-')}`, prompt, labelZh: label })),
  }
}

// The four scene groups are transcribed from the user's reference screenshots.
export const sceneSections: readonly PromptTagSection[] = [
  section('scene-background', '场景与背景', 'Scenes and backgrounds', [
    ['classroom', '教室'], ['bedroom', '卧室'], ['beach', '沙滩海边'], ['forest', '森林'], ['cafe', '咖啡厅'],
    ['rooftop', '楼顶天台'], ['shrine', '日式神社'], ['library', '图书馆'], ['onsen', '温泉'], ['bathroom', '浴室'],
    ['kitchen', '厨房'], ['city', '城市远景'], ['cityscape', '城市远景'], ['street', '街道'], ['alley', '小巷'],
    ['cyberpunk', '赛博朋克风'], ['underwater', '水下'], ['night sky', '夜空'], ['starry sky', '星空'], ['field', '原野田野'],
    ['flower field', '花田'], ['cherry blossoms', '樱花树下'], ['mountain', '山'], ['lake', '湖泊'], ['waterfall', '瀑布'],
    ['ruins', '废墟遗迹'], ['church', '教堂'], ['castle', '城堡'], ['train interior', '电车车厢'],
    ['convenience store', '便利店'], ['swimming pool', '游泳池'], ['space', '宇宙太空'], ['desert', '沙漠'],
    ['snowy mountain', '雪山'], ['school hallway', '学校走廊'], ['school rooftop', '学校天台'],
    ['restaurant', '餐厅'], ['living room', '客厅'], ['office', '办公室'], ['laboratory', '实验室'], ['hospital', '医院'],
    ['temple', '寺庙'], ['shopping mall', '商场'], ['arcade', '游戏厅'], ['train station', '车站'],
    ['bus stop', '公交车站'], ['bridge', '桥'], ['park', '公园'], ['garden', '花园'], ['greenhouse', '温室'],
    ['bamboo forest', '竹林'], ['jungle', '丛林'], ['ocean', '海洋'], ['river', '河流'], ['island', '岛屿'], ['cave', '洞穴'],
    ['volcano', '火山'], ['canyon', '峡谷'], ['grassland', '草原'], ['wheat field', '麦田'], ['countryside', '乡村'],
    ['farm', '农场'], ['windmill', '风车'], ['lighthouse', '灯塔'], ['harbor', '港口'], ['airship', '飞艇'],
    ['space station', '空间站'], ['moon', '月球'], ['futuristic city', '未来都市'], ['post-apocalypse', '末世废土'],
    ['battlefield', '战场'], ['throne room', '王座厅'], ['ballroom', '舞厅'], ['tower', '塔楼'], ['fantasy', '幻想风景'],
  ]),
  section('scene-season', '季节与节日', 'Seasons and holidays', [
    ['spring', '春天'], ['summer', '夏天'], ['autumn', '秋天'], ['winter', '冬天'], ['rainy season', '雨季'],
    ['new year', '新年'], ['christmas', '圣诞节'], ['halloween', '万圣节'], ['valentine', '情人节'],
    ['birthday', '生日'], ['wedding', '婚礼'], ['festival', '祭典'], ['summer festival', '夏日祭'], ['fireworks', '烟火'],
    ['tanabata', '七夕'], ['hanami', '赏花'], ['picnic', '野餐'], ['camping', '露营'], ['skiing', '滑雪'],
    ['school festival', '校园祭'], ['sports festival', '运动会'], ['graduation', '毕业典礼'], ['tea ceremony', '茶道'],
  ]),
  section('scene-lighting', '光照氛围与天气', 'Lighting, atmosphere and weather', [
    ['sunset', '日落黄昏'], ['sunlight', '阳光照射'], ['night', '夜晚'], ['sunrise', '日出'], ['rain', '下雨'],
    ['snow', '下雪'], ['cloudy', '多云阴天'], ['fog', '雾气'], ['backlighting', '逆光'], ['cinematic lighting', '电影感打光'],
    ['dramatic lighting', '强烈戏剧光'], ['rim lighting', '轮廓边缘光'], ['god rays', '丁达尔光束'],
    ['light rays', '光线穿透'], ['dappled sunlight', '树叶碎光'], ['lens flare', '镜头光晕'], ['bloom', '光晕泛光'],
    ['soft lighting', '柔和光线'], ['moonlight', '月光'], ['neon lights', '霓虹灯光'], ['candlelight', '烛光'],
    ['firelight', '火光映照'], ['bioluminescence', '生物发光'], ['glowing', '发光'], ['colorful lighting', '彩色光照'],
    ['chromatic aberration', '色散边缘'], ['lightning', '闪电'], ['clear sky', '晴朗天空'], ['golden hour', '黄金时刻'],
    ['twilight', '暮色'], ['dawn', '黎明'], ['overcast', '阴云密布'], ['storm', '暴风雨'], ['blizzard', '暴风雪'],
    ['mist', '薄雾'], ['wind', '风起'], ['falling petals', '花瓣飘落'], ['falling leaves', '落叶飘零'],
    ['water drops', '水珠'], ['puddle', '水洼'], ['steam', '蒸汽'], ['smoke', '烟雾'], ['aurora', '极光'],
    ['milky way', '银河'], ['full moon', '满月'], ['crescent moon', '新月'], ['rainbow', '彩虹'],
    ['volumetric lighting', '体积光'], ['studio lighting', '影棚灯光'], ['spotlight', '聚光灯'],
    ['high key', '高调明亮'], ['low key', '低调暗部'], ['gloomy', '阴郁氛围'], ['serene', '静谧氛围'],
    ['dreamy', '梦幻氛围'], ['nostalgic', '怀旧氛围'], ['ethereal', '空灵氛围'],
  ]),
  section('scene-effects', '特效', 'Effects', [
    ['sparkle', '闪光点缀'], ['glitter', '亮片闪耀'], ['light particles', '光粒子'], ['floating particles', '漂浮微粒'],
    ['magic', '魔法特效'], ['aura', '魔力光环'], ['energy', '能量特效'], ['fire', '火焰'], ['ice', '冰霜'],
    ['water splash', '水花'], ['bubbles', '气泡'], ['feathers', '羽毛飘落'], ['confetti', '彩纸'], ['ripples', '水波纹'],
    ['glow', '发光效果'], ['vignette', '暗角'], ['glitch', '故障效果'], ['scanlines', '扫描线'],
    ['double exposure', '双重曝光'], ['soft focus', '柔焦'], ['heat haze', '热气扭曲'], ['afterimage', '残影'],
    ['emphasis lines', '强调线'], ['speech bubble', '对话框'], ['outline', '描边'],
  ]),
]
