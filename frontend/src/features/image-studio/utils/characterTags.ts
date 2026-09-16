import type { PromptTag, PromptTagSection } from './promptTags'

function tags(entries: readonly (readonly [string, string])[]): PromptTag[] {
  return entries.map(([prompt, labelZh]) => ({ id: `character-${prompt.replace(/ /g, '-')}`, prompt, labelZh }))
}

export const subjectTags = tags([
  ['1girl', '一个女孩'], ['solo', '单人画面'], ['1boy', '一个男孩'], ['2girls', '两个女孩'],
  ['multiple girls', '多个女孩'], ['1other', '一个非男女角色'], ['2boys', '两个男孩'], ['multiple boys', '多个男孩'],
  ['solo focus', '只聚焦一人'], ['couple', '情侣两人'], ['3girls', '三个女孩'], ['1girl 1boy', '一女一男'],
  ['group', '一群人'], ['crowd', '人群围观'], ['no humans', '画面无人'], ['animal focus', '以动物为主'],
  ['mature female', '成熟女性'], ['younger sister', '妹妹角色'], ['4girls', '四个女孩'], ['3boys', '三个男孩'],
  ['6+girls', '六人以上女孩'], ['sisters', '姐妹'], ['twins', '双胞胎'], ['siblings', '兄弟姐妹'],
  ['male focus', '以男性为主'], ['androgynous', '中性外貌'], ['tomboy', '假小子'], ['mature male', '成熟男性'],
  ['adult', '成年角色'], ['furry', '兽人角色'], ['monster girl', '怪物娘'],
])

export const characterSections: readonly PromptTagSection[] = [
  {
    id: 'clothing-outfits', labelZh: '服装', labelEn: 'Clothes', parentId: 'clothing', parentZh: '服饰', parentEn: 'Clothing',
    tags: tags([
      ['school uniform', '校服'], ['serafuku', '日式水手服'], ['sailor collar', '水手领'], ['pleated skirt', '百褶裙'],
      ['maid', '女仆'], ['maid headdress', '女仆头饰'], ['apron', '围裙'], ['dress', '连衣裙'], ['china dress', '旗袍'],
      ['kimono', '和服'], ['yukata', '夏日浴衣'], ['hakama', '袴裙'], ['miko', '巫女服'], ['bikini', '比基尼'],
      ['swimsuit', '泳装'], ['one-piece swimsuit', '连体泳衣'], ['school swimsuit', '学校泳装'], ['hoodie', '连帽衫'],
      ['sweater', '毛衣'], ['shirt', '衬衫'], ['t-shirt', '短袖T恤'], ['crop top', '露脐短上衣'], ['jacket', '夹克外套'],
      ['coat', '大衣'], ['blazer', '西装外套'], ['shorts', '短裤'], ['denim shorts', '牛仔短裤'], ['miniskirt', '超短裙'],
      ['nurse', '护士装'], ['bunny suit', '兔女郎装'], ['gothic lolita', '哥特萝莉装'], ['wedding dress', '婚纱'],
      ['military uniform', '军装'], ['leotard', '紧身连体衣'], ['gym uniform', '体操服'], ['track suit', '运动服'],
      ['turtleneck', '高领衫'], ['cardigan', '开襟衫'], ['vest', '马甲'], ['blouse', '女式衬衫'], ['dress shirt', '衬衫'],
      ['tank top', '背心'], ['camisole', '吊带衫'], ['off-shoulder', '露肩装'], ['sundress', '太阳裙'],
      ['evening gown', '晚礼服'], ['pinafore dress', '背带裙'], ['sailor dress', '水手连衣裙'], ['hanbok', '韩服'],
      ['dirndl', '巴伐利亚裙'], ['sweet lolita', '甜美洛丽塔'], ['police uniform', '警服'], ['business suit', '西装'],
      ['lab coat', '白大衣'], ['overalls', '背带裤'], ['jumpsuit', '连体裤'], ['raincoat', '雨衣'],
      ['winter coat', '冬季外套'], ['trench coat', '风衣'], ['duffel coat', '牛角扣外套'], ['fur coat', '皮草外套'],
      ['leather jacket', '皮夹克'], ['denim jacket', '牛仔外套'], ['haori', '羽织'], ['cape', '斗篷'],
      ['cloak', '长斗篷'], ['poncho', '披风斗篷'], ['armor', '铠甲'], ['plate armor', '板甲'],
      ['shoulder armor', '护肩'], ['breastplate', '胸甲'], ['robe', '长袍'], ['pants', '长裤'], ['jeans', '牛仔裤'],
      ['long skirt', '长裙'], ['high-waist skirt', '高腰裙'], ['hakama skirt', '袴裙'], ['sarong', '沙笼'],
      ['casual clothes', '休闲服'], ['knit sweater', '针织毛衣'], ['uniform', '制服'], ['traditional clothes', '传统服饰'],
    ]),
  },
  {
    id: 'clothing-accessories', labelZh: '配饰与非人特征', labelEn: 'Accessories and non-human traits', parentId: 'clothing', parentZh: '服饰', parentEn: 'Clothing',
    tags: tags([
      ['gloves', '手套'], ['elbow gloves', '过肘长手套'], ['fingerless gloves', '露指手套'], ['choker', '颈圈项链'],
      ['collar', '项圈'], ['necklace', '项链'], ['earrings', '耳环'], ['sunglasses', '墨镜'], ['eyepatch', '眼罩'],
      ['cat ears', '猫耳'], ['cat tail', '猫尾巴'], ['fox ears', '狐耳'], ['fox tail', '狐狸尾巴'],
      ['animal ears', '兽耳'], ['rabbit ears', '兔耳朵'], ['angel wings', '天使翅膀'], ['demon horns', '恶魔角'],
      ['halo', '头顶光环'], ['tail', '尾巴'], ['wolf ears', '狼耳'], ['dog ears', '犬耳'], ['horse ears', '马耳'],
      ['cow horns', '牛角'], ['dragon horns', '龙角'], ['wolf tail', '狼尾'], ['rabbit tail', '兔尾'],
      ['dragon tail', '龙尾'], ['demon wings', '恶魔翼'], ['feathered wings', '羽翼'], ['dragon wings', '龙翼'],
      ['fairy wings', '妖精翼'], ['pointy ears', '尖耳'], ['claws', '利爪'], ['scales', '鳞片'],
      ['mechanical arms', '机械臂'], ['arm tattoo', '手臂纹身'], ['back tattoo', '背部纹身'], ['pendant', '吊坠'],
      ['bracelet', '手镯'], ['wristband', '护腕'], ['ring', '戒指'], ['belt', '腰带'], ['sash', '饰带'],
      ['scarf', '围巾'], ['tie', '领带'], ['bowtie', '领结'], ['brooch', '胸针'], ['badge', '徽章'],
      ['backpack', '背包'], ['shoulder bag', '单肩包'], ['handbag', '手提包'],
    ]),
  },
  {
    id: 'clothing-headwear', labelZh: '头部穿戴', labelEn: 'Headwear', parentId: 'clothing', parentZh: '服饰', parentEn: 'Clothing',
    tags: tags([
      ['cap', '鸭舌帽'], ['beret', '贝雷帽'], ['beanie', '毛线帽'], ['sun hat', '遮阳帽'], ['straw hat', '草帽'],
      ['wizard hat', '魔法帽'], ['top hat', '高顶礼帽'], ['cowboy hat', '牛仔帽'], ['military hat', '军帽'],
      ['police hat', '警帽'], ['nurse cap', '护士帽'], ['chef hat', '厨师帽'], ['helmet', '头盔'], ['crown', '王冠'],
      ['tiara', '头冠'], ['circlet', '头环'], ['hood', '兜帽'], ['hood up', '戴上兜帽'], ['headphones', '头戴耳机'],
      ['headset', '耳麦'], ['mask', '面具'], ['surgical mask', '口罩'], ['animal hood', '动物兜帽'], ['flower crown', '花冠'],
      ['hat', '帽子'], ['witch hat', '女巫尖帽'],
    ]),
  },
  {
    id: 'clothing-footwear', labelZh: '鞋袜与腿部', labelEn: 'Footwear and legs', parentId: 'clothing', parentZh: '服饰', parentEn: 'Clothing',
    tags: tags([
      ['striped thighhighs', '条纹过膝袜'], ['black pantyhose', '黑色连裤袜'], ['ankle socks', '短袜'],
      ['kneehighs', '及膝袜'], ['loose socks', '泡泡袜'], ['leg warmers', '腿套'], ['boots', '靴子'],
      ['knee boots', '及膝靴'], ['thigh boots', '过膝靴'], ['combat boots', '战斗靴'], ['ankle boots', '短靴'],
      ['high heels', '高跟鞋'], ['pumps', '浅口高跟'], ['sandals', '凉鞋'], ['sneakers', '运动鞋'], ['loafers', '乐福鞋'],
      ['mary janes', '玛丽珍鞋'], ['uwabaki', '室内鞋'], ['geta', '木屐'], ['slippers', '拖鞋'], ['barefoot', '赤脚'],
      ['thighhighs', '过膝长袜'], ['black thighhighs', '黑色过膝袜'], ['white thighhighs', '白色过膝袜'],
      ['pantyhose', '连裤袜'], ['garter belt', '吊袜带'], ['garter straps', '吊袜绑带'], ['socks', '短袜'],
    ]),
  },
  {
    id: 'actions', labelZh: '姿态动作', labelEn: 'Poses and actions', parentId: 'actions', parentZh: '姿态动作', parentEn: 'Poses and actions',
    tags: tags([
      ['standing', '站立'], ['sitting', '坐着'], ['lying', '躺着'], ['on back', '仰躺'], ['on stomach', '趴着'],
      ['on side', '侧躺'], ['kneeling', '跪着'], ['squatting', '蹲着'], ['walking', '走路'], ['running', '奔跑'],
      ['jumping', '跳跃'], ['arms up', '双手举高'], ['arms behind back', '双手背后'], ['arms behind head', '双手抱头'],
      ['hand on hip', '手叉腰'], ['hands on hips', '双手叉腰'], ['looking back', '回头看'], ['looking up', '抬头看'],
      ['looking down', '低头看'], ['crossed arms', '抱臂'], ['crossed legs', '翘二郎腿'], ['leaning forward', '身体前倾'],
      ['leaning back', '身体后仰'], ['stretching', '伸懒腰'], ['outstretched arms', '张开双臂'],
      ['hand on own face', '手扶脸颊'], ['hand up', '举起一只手'], ['waving', '挥手'], ['v', '比剪刀手'],
      ['peace symbol', '比 V 手势'], ['salute', '敬礼'], ['head tilt', '歪头'], ['sitting on chair', '坐在椅子上'],
      ['wariza', '鸭子坐'], ['indian style', '盘腿坐'], ['knees up', '抱膝屈腿'], ['all fours', '四肢着地'],
      ['dancing', '跳舞'], ['holding', '手里拿着东西'], ['seiza', '正坐'], ['sitting on bed', '坐在床上'],
      ['sitting on floor', '坐在地上'], ['against wall', '靠着墙'], ['flying', '飞行'], ['floating', '漂浮'],
      ['falling', '下落'], ['swimming', '游泳'], ['yawning', '打哈欠'], ['sleeping', '睡觉'], ['reading', '阅读'],
      ['writing', '书写'], ['drinking', '喝东西'], ['eating', '吃东西'], ['cooking', '做菜'], ['singing', '歌唱'],
      ['playing instrument', '演奏乐器'], ['holding sword', '持剑'], ['holding gun', '持枪'], ['holding umbrella', '撑伞'],
      ['holding book', '拿着书'], ['holding cup', '拿着杯子'], ['holding flower', '拿着花'], ['holding phone', '拿着手机'],
      ['hugging', '拥抱'], ['hug from behind', '背后拥抱'], ['looking over shoulder', '越肩回望'],
      ['tiptoes', '踮脚'], ['knees together', '双膝并拢'], ['holding a cup', '手持杯子'], ['drawing', '绘画'],
    ]),
  },
  {
    id: 'identity', labelZh: '身份', labelEn: 'Identity', parentId: 'identity', parentZh: '身份', parentEn: 'Identity',
    tags: tags([
      ['office lady', '职场女性'], ['college student', '大学生'], ['idol', '偶像'], ['singer', '歌手'], ['athlete', '运动员'],
      ['knight', '骑士'], ['witch', '女巫'], ['wizard', '魔法师'], ['nun', '修女'], ['samurai', '武士'], ['ninja', '忍者'],
      ['pirate', '海盗'], ['detective', '侦探'], ['scientist', '科学家'], ['doctor', '医生'], ['soldier', '士兵'],
      ['pilot', '飞行员'], ['chef', '厨师'], ['librarian', '图书管理员'], ['princess', '公主'], ['prince', '王子'],
      ['queen', '女王'], ['vampire', '吸血鬼'], ['angel', '天使'], ['demon', '恶魔'], ['elf', '精灵'], ['dark elf', '黑暗精灵'],
      ['android', '仿生人'], ['cyborg', '改造人'], ['mermaid', '人鱼'], ['fairy', '妖精'], ['goddess', '女神'],
    ]),
  },
]
