import type { PromptTagSection } from './promptTags'
import { appearanceSections } from './appearanceTags'

function section(id: string, labelZh: string, labelEn: string, entries: readonly (readonly [string, string])[]): PromptTagSection {
  return {
    id, labelZh, labelEn, parentId: id, parentZh: labelZh, parentEn: labelEn,
    tags: entries.map(([prompt, label]) => ({ id: `nsfw-${prompt.replace(/ /g, '-')}`, prompt, labelZh: label, nsfw: true })),
  }
}

// Catalog terms transcribed from the supplied reference, without generated additions.
const referenceSections: readonly PromptTagSection[] = [
  section('nsfw-nudity', '裸露状态', 'Nudity', [
    ['nude', '裸体'], ['completely nude', '全裸'], ['topless', '上身赤裸'], ['bottomless', '下身赤裸'],
    ['partially undressed', '衣衫半褪'], ['nipples', '露出乳头'], ['cleavage', '乳沟'], ['see-through', '衣物透视'],
    ['clothes lift', '掀起衣服'], ['shirt lift', '掀起上衣'], ['skirt lift', '掀起裙子'], ['dress lift', '掀起连衣裙'],
    ['clothing aside', '衣物拨到一边'], ['panties aside', '内裤拨到一边'], ['torn clothes', '衣服破损'],
    ['undressing', '正在脱衣'], ['open shirt', '敞开上衣'], ['open clothes', '敞开衣襟'], ['no bra', '没穿胸罩'],
    ['no panties', '没穿内裤'], ['pussy', '露出下体'], ['navel', '露出肚脐'], ['underboob', '乳房下缘外露'],
    ['nipple slip', '乳头走光'], ['bare shoulders', '裸露肩膀'], ['exposed breasts', '胸部外露'],
  ]),
  section('nsfw-poses', '姿势', 'Poses', [
    ['spread legs', '双腿张开'], ['m legs', 'M 字开腿'], ['legs up', '双腿抬高'], ['arched back', '腰背弓起'],
    ['bent over', '弯腰前倾'], ['straddling', '跨坐'], ['spread pussy', '掰开下体'], ['top-down bottom-up', '翘臀趴伏'],
    ['leg lift', '单腿抬起'], ['presenting', '展示私处'], ['spread ass', '掰开臀部'], ['legs apart', '两腿分开'],
    ['upright straddle', '直立跨坐'],
  ]),
  section('nsfw-actions', '行为', 'Actions', [
    ['sex', '性行为'], ['vaginal', '阴道交合'], ['anal', '肛交'], ['oral', '口交'], ['fellatio', '为男性口交'],
    ['cunnilingus', '为女性口交'], ['paizuri', '乳交'], ['handjob', '用手抚慰'], ['footjob', '足交'],
    ['masturbation', '自慰'], ['fingering', '手指插入'], ['cowgirl position', '女上位'],
    ['reverse cowgirl position', '背对女上位'], ['missionary', '正常位'], ['doggystyle', '后入式'],
    ['sex from behind', '从背后进入'], ['suspended congress', '抱起交合'], ['spooning', '侧躺后入'],
    ['kissing', '接吻'], ['french kiss', '深吻'], ['groping', '抚摸身体'], ['breast grab', '抓握胸部'],
    ['licking', '舔舐'], ['deepthroat', '深喉'],
  ]),
  section('nsfw-fluids-state', '体液与状态', 'Fluids and states', [
    ['cum', '精液'], ['cum on body', '精液在身上'], ['cum on breasts', '精液在胸部'], ['cum on face', '精液在脸上'],
    ['cum in pussy', '体内射精'], ['cum in mouth', '口中含精'], ['overflow', '精液满溢'], ['pussy juice', '爱液'],
    ['wet', '身体湿润'], ['saliva', '唾液'], ['saliva trail', '唾液拉丝'], ['trembling', '身体颤抖'],
    ['aroused', '情动兴奋'], ['after sex', '事后余韵'],
  ]),
]

export const nsfwSections: readonly PromptTagSection[] = referenceSections.map((group) => ({
  ...group,
  tags: group.id === 'nsfw-fluids-state'
    ? [...group.tags, ...appearanceSections.flatMap((entry) => entry.tags.filter((tag) => tag.nsfw))]
    : group.tags,
}))
