import { currentLanguage, localize } from '@/shared/i18n'

import type { GenerationTask, SaveStatus, TaskStatus } from '../types'
import { seedComparison } from './generationParameters'

export function taskStatusText(status: TaskStatus): string {
  switch (status) {
    case 'preparing': return localize('准备中', 'Preparing')
    case 'running': return localize('生成中', 'Generating')
    case 'succeeded': return localize('已生成', 'Generated')
    case 'failed': return localize('失败', 'Failed')
    case 'cancelled': return localize('已取消', 'Cancelled')
    case 'interrupted': return localize('结果待确认', 'Result unknown')
  }
}

export function saveStatusText(status: SaveStatus): string {
  switch (status) {
    case 'pending': return localize('未保存', 'Not saved')
    case 'saving': return localize('保存中', 'Saving')
    case 'saved': return localize('已保存', 'Saved')
    case 'failed': return localize('保存失败', 'Save failed')
  }
}

export function taskStatusType(status: TaskStatus): 'default' | 'info' | 'success' | 'error' | 'warning' {
  switch (status) {
    case 'preparing':
    case 'running': return 'info'
    case 'succeeded': return 'success'
    case 'failed': return 'error'
    case 'interrupted': return 'warning'
    case 'cancelled': return 'default'
  }
}

export function seedEvidenceText(task: GenerationTask): string {
  switch (seedComparison(task)) {
    case 'matches': return localize('PNG 种子与请求一致', 'PNG seed matches the request')
    case 'different': return localize('PNG 种子与请求不同', 'PNG seed differs from the request')
    case 'missing': return task.image
      ? localize('上游未回传可核实的种子', 'No verifiable seed returned by the upstream')
      : localize('尚无返回种子', 'No returned seed yet')
  }
}

export function studioTime(timestamp: number): string {
  return new Intl.DateTimeFormat(currentLanguage.value === 'zh' ? 'zh-CN' : 'en-US', {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'Asia/Shanghai',
  }).format(timestamp)
}
