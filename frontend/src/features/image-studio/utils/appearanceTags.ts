import type { PromptTag, PromptTagSection } from './promptTags'

// Transcribed from the user's reference catalog; explicit expressions are opt-in.
function tags(entries: readonly (readonly [string, string, boolean?])[]): PromptTag[] {
  return entries.map(([prompt, labelZh, nsfw]) => ({
    id: `appearance-${prompt.replace(/ /g, '-')}`, labelZh, prompt, ...(nsfw ? { nsfw } : {}),
  }))
}

export const appearanceSections: readonly PromptTagSection[] = [
  {
    id: 'hair-color', labelZh: '发色', labelEn: 'Hair color', parentId: 'hair', parentZh: '头发', parentEn: 'Hair',
    tags: tags([
      ['blonde hair', '金发'], ['black hair', '黑发'], ['brown hair', '棕发'], ['white hair', '白发'],
      ['silver hair', '银发'], ['grey hair', '灰发'], ['pink hair', '粉发'], ['blue hair', '蓝发'],
      ['light blue hair', '浅蓝发'], ['red hair', '红发'], ['purple hair', '紫发'], ['green hair', '绿发'],
      ['orange hair', '橙发'], ['aqua hair', '水蓝发'], ['gradient hair', '渐变发色'], ['multicolored hair', '多色头发'],
      ['two-tone hair', '双色头发'], ['streaked hair', '挑染头发'], ['colored inner hair', '内层挑染'],
      ['platinum blonde hair', '铂金色头发'], ['dark blue hair', '深蓝发'], ['pink-tipped hair', '发梢粉色'],
      ['dark green hair', '深绿发'], ['light brown hair', '浅棕发'], ['light green hair', '浅绿发'],
      ['light purple hair', '浅紫发'], ['magenta hair', '洋红发'], ['teal hair', '蓝绿发'], ['rainbow hair', '彩虹发'],
      ['split-color hair', '左右异色发'], ['shiny hair', '亮泽头发'], ['roots', '发根异色'],
    ]),
  },
  {
    id: 'hair-style', labelZh: '发型', labelEn: 'Hairstyle', parentId: 'hair', parentZh: '头发', parentEn: 'Hair',
    tags: tags([
      ['long hair', '长发'], ['short hair', '短发'], ['medium hair', '中长发'], ['very long hair', '超长发'],
      ['twintails', '双马尾'], ['ponytail', '马尾辫'], ['side ponytail', '侧马尾'], ['twin braids', '双麻花辫'],
      ['braid', '辫子'], ['single braid', '单辫子'], ['french braid', '法式编发'], ['hair bun', '丸子头'],
      ['double bun', '双丸子头'], ['low twintails', '低双马尾'], ['drill hair', '卷筒发'], ['wavy hair', '波浪卷发'],
      ['curly hair', '卷发'], ['straight hair', '直发'], ['messy hair', '凌乱头发'], ['hime cut', '公主切'],
      ['bob cut', '波波头'], ['blunt bangs', '齐刘海'], ['swept bangs', '斜刘海'], ['parted bangs', '中分刘海'],
      ['hair between eyes', '眼间碎发'], ['side braid', '侧编发'], ['crown braid', '环形编发'],
      ['braided ponytail', '编发马尾'], ['high ponytail', '高马尾'], ['low ponytail', '低马尾'],
      ['half updo', '半扎发'], ['one side up', '单侧扎发'], ['two side up', '双侧扎发'], ['topknot', '顶部发髻'],
      ['undercut', '底部剃短'], ['pixie cut', '精灵短发'], ['asymmetrical hair', '不对称发型'],
      ['ringlets', '螺旋卷发'], ['flipped hair', '外翘发尾'], ['updo', '盘发'],
      ['absurdly long hair', '极长发'], ['dreadlocks', '脏辫'], ['afro', '爆炸头'], ['twin drills', '双钻头卷'],
      ['single hair bun', '单丸子头'], ['braided hair', '编发'],
    ]),
  },
  {
    id: 'hair-details', labelZh: '头发细节与发饰', labelEn: 'Hair details and accessories', parentId: 'hair', parentZh: '头发', parentEn: 'Hair',
    tags: tags([
      ['sidelocks', '鬓发'], ['ahoge', '呆毛'], ['hair over one eye', '遮住一只眼'], ['hair ribbon', '发带蝴蝶结'],
      ['hairband', '发箍'], ['hair bow', '头上蝴蝶结'], ['hair ornament', '发饰'], ['hair flower', '头戴花朵'],
      ['scrunchie', '发圈'], ['floating hair', '飘起的头发'], ['hair spread out', '头发散开'], ['wet hair', '湿发'],
      ['hairclip', '发夹'], ['hairpin', '发簪'], ['x hair ornament', 'X 形发饰'], ['star hair ornament', '星形发饰'],
      ['heart hair ornament', '心形发饰'], ['butterfly hair ornament', '蝴蝶发饰'], ['headband', '头带'],
      ['lolita hairband', '洛丽塔头饰'], ['frilled hairband', '蕾丝发箍'], ['hair bell', '发铃'], ['hair tie', '发绳'],
      ['flower wreath', '花环'], ['veil', '头纱'], ['hair over shoulder', '头发披在肩前'],
      ['braid over shoulder', '辫子搭肩'], ['antenna hair', '天线呆毛'], ['cowlick', '翘起的一撮'], ['large hair bow', '大蝴蝶结'],
    ]),
  },
  {
    id: 'face-eyes', labelZh: '眼睛与瞳', labelEn: 'Eyes and pupils', parentId: 'face', parentZh: '脸部', parentEn: 'Face',
    tags: tags([
      ['blue eyes', '蓝色眼睛'], ['red eyes', '红色眼睛'], ['green eyes', '绿色眼睛'], ['purple eyes', '紫色眼睛'],
      ['yellow eyes', '黄色眼睛'], ['brown eyes', '棕色眼睛'], ['pink eyes', '粉色眼睛'], ['golden eyes', '金色眼睛'],
      ['heterochromia', '异色瞳'], ['tsurime', '上挑吊眼'], ['tareme', '下垂眼'], ['jitome', '半眯白眼'],
      ['half-closed eyes', '半闭眼'], ['closed eyes', '闭着眼'], ['one eye closed', '眨一只眼'], ['glowing eyes', '发光眼睛'],
      ['sparkling eyes', '闪亮眼睛'], ['heart-shaped pupils', '爱心瞳孔'], ['star-shaped pupils', '星形瞳孔'],
      ['slit pupils', '竖瞳'], ['empty eyes', '空洞无神眼'], ['rolling eyes', '翻白眼'],
      ['eyes visible through hair', '眼睛透过刘海'], ['looking at viewer', '看向镜头'], ['upturned eyes', '向上看眼神'],
      ['aqua eyes', '水蓝眼'], ['orange eyes', '橙色眼睛'], ['grey eyes', '灰色眼睛'], ['black eyes', '黑色眼睛'],
      ['multicolored eyes', '多色瞳'], ['gradient eyes', '渐变瞳'], ['narrowed eyes', '眯起眼'],
      ['looking away', '视线偏移'], ['looking to the side', '看向侧面'], ['eye contact', '眼神交汇'],
      ['eyelashes', '长睫毛'], ['thick eyebrows', '浓眉'], ['tearing up', '泛起泪光'],
    ]),
  },
  {
    id: 'face-expression', labelZh: '表情与情绪', labelEn: 'Expressions and emotions', parentId: 'face', parentZh: '脸部', parentEn: 'Face',
    tags: tags([
      ['smile', '微笑'], ['blush', '脸红'], ['open mouth', '张开嘴'], ['closed mouth', '闭着嘴'], ['grin', '咧嘴笑'],
      ['light smile', '淡淡微笑'], ['laughing', '大笑'], ['embarrassed', '害羞尴尬'], ['shy', '羞涩'], ['crying', '哭泣'],
      ['tears', '流眼泪'], ['crying with eyes open', '睁眼流泪'], ['angry', '生气'], ['pout', '撅嘴不满'], ['sad', '伤心'],
      ['surprised', '惊讶'], ['scared', '害怕'], ['expressionless', '面无表情'], ['serious', '严肃'], ['smirk', '坏笑'],
      ['seductive smile', '诱惑微笑', true], ['sultry', '妖艳撩人', true], ['bedroom eyes', '媚眼含春', true],
      ['naughty face', '坏坏挑逗脸', true], ['heavy breathing', '喘息不止', true], ['nose blush', '鼻头泛红'],
      ['drooling', '流口水'], ['tongue out', '吐舌头'], ['ahegao', '高潮脸', true], ['orgasm', '高潮表情', true],
      ['smug', '得意表情'], ['frown', '皱眉'], ['annoyed', '不悦'], ['shocked', '震惊'], ['nervous', '紧张'],
      ['light blush', '淡淡脸红'], ['excited', '兴奋'], ['giggling', '窃笑'], ['bored', '无聊'], ['sleepy', '睡蒙蒙'],
      ['confused', '困惑'], ['thinking', '思考'], ['determined', '坚定'], ['confident', '自信'],
      ['gentle smile', '温柔微笑'], ['forced smile', '勉强笑容'], ['wry smile', '苦笑'],
      ['parted lips', '微张双唇'], ['licking lips', '舔嘴唇'], ['puffy cheeks', '鼓起腮'], ['sweatdrop', '汗滴'],
    ]),
  },
  {
    id: 'face-details', labelZh: '脸部细节与妆容', labelEn: 'Facial details and makeup', parentId: 'face', parentZh: '脸部', parentEn: 'Face',
    tags: tags([
      ['makeup', '妆容'], ['eyeshadow', '眼影'], ['eyeliner', '眼线'], ['lipstick', '口红'], ['red lips', '红唇'],
      ['mole under mouth', '嘴下痣'], ['scar on face', '脸上疤痕'], ['round eyewear', '圆框眼镜'],
      ['semi-rimless eyewear', '半框眼镜'], ['monocle', '单片眼镜'], ['bandaid on face', '脸上创可贴'],
      ['facial mark', '面部纹样'], ['forehead mark', '额头印记'], ['whisker markings', '胡须状纹'],
      ['fangs', '尖牙'], ['sharp teeth', '锐利牙齿'], ['stud earrings', '耳钉'], ['hoop earrings', '圆圈耳环'],
      ['face paint', '脸部彩绘'], ['freckles', '雀斑'], ['glasses', '眼镜'],
    ]),
  },
]
