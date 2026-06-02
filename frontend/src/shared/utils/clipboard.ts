import { localize } from '@/shared/i18n'

function copyWithSelection(text: string): boolean {
  const textArea = document.createElement('textarea')
  const activeElement = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const selection = document.getSelection()
  const ranges = selection
    ? Array.from({ length: selection.rangeCount }, (_, index) => selection.getRangeAt(index))
    : []

  textArea.value = text
  textArea.setAttribute('readonly', '')
  textArea.style.position = 'fixed'
  textArea.style.top = '0'
  textArea.style.left = '-9999px'
  textArea.style.width = '1px'
  textArea.style.height = '1px'
  textArea.style.opacity = '0'
  textArea.style.pointerEvents = 'none'

  document.body.appendChild(textArea)
  textArea.focus()
  textArea.select()
  textArea.setSelectionRange(0, textArea.value.length)

  try {
    return document.execCommand('copy')
  } finally {
    document.body.removeChild(textArea)
    if (selection) {
      selection.removeAllRanges()
      ranges.forEach((range) => selection.addRange(range))
    }
    activeElement?.focus({ preventScroll: true })
  }
}

export async function copyToClipboard(text: string): Promise<void> {
  if (!text) {
    throw new Error(localize('没有可复制的内容', 'Nothing to copy'))
  }

  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return
    }
  } catch {
    // Fall through to the selection-based copy path for HTTP/LAN/WebView contexts.
  }

  if (copyWithSelection(text)) {
    return
  }

  throw new Error(
    localize('复制失败，请手动选中后复制', 'Copy failed. Select the text and copy it manually.'),
  )
}
