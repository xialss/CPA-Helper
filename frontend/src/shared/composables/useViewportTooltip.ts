import { nextTick, onBeforeUnmount, shallowRef, watch, type ComponentPublicInstance, type CSSProperties, type Ref } from 'vue'
import type { TooltipInst, TooltipProps } from 'naive-ui'

type TooltipTriggerRef = (element: Element | ComponentPublicInstance | null) => void

interface TooltipPosition {
  x: number
  y: number
  height: number
  maxWidth: number
}

const viewportMargin = 16
const maxTooltipWidth = 360
const triggerGap = 6

export function useViewportTooltip<Key extends string | number>(
  hoveredKey: Ref<Key | null>,
  focusedKey: Ref<Key | null>,
  themeOverrides?: TooltipProps['themeOverrides'],
) {
  const targets = new Map<Key, HTMLElement>()
  const triggerRefs = new Map<Key, TooltipTriggerRef>()
  const tooltipRefs = new Map<Key, Ref<TooltipInst | null>>()
  const positions = shallowRef(new Map<Key, TooltipPosition>())
  // Naive UI keeps the follower mounted through leave, so retain geometry until row removal.
  const lastPositions = new Map<Key, TooltipPosition>()
  let listening = false

  function setListening(active: boolean) {
    if (active === listening) return
    listening = active
    if (active) {
      window.addEventListener('scroll', syncPositions, { capture: true, passive: true })
      window.addEventListener('resize', syncPositions)
    } else {
      window.removeEventListener('scroll', syncPositions, true)
      window.removeEventListener('resize', syncPositions)
    }
  }

  function syncPositions() {
    const next = new Map<Key, TooltipPosition>()
    const viewportWidth = document.documentElement.clientWidth
    const maxWidth = Math.min(maxTooltipWidth, viewportWidth - viewportMargin * 2)
    for (const key of new Set([hoveredKey.value, focusedKey.value])) {
      if (key === null) continue
      const target = targets.get(key)
      if (!target?.isConnected) continue
      const rect = target.getBoundingClientRect()
      const halfWidth = maxWidth / 2
      // Reserve room for the widest allowed box while short text keeps its natural width.
      next.set(key, {
        x: Math.max(viewportMargin + halfWidth, Math.min(rect.left + rect.width / 2, viewportWidth - viewportMargin - halfWidth)),
        y: rect.top + rect.height / 2,
        height: rect.height,
        maxWidth,
      })
    }
    for (const [key, position] of next) lastPositions.set(key, position)
    positions.value = next
    setListening(next.size > 0)
  }

  function triggerProps(key: Key) {
    let triggerRef = triggerRefs.get(key)
    if (!triggerRef) {
      triggerRef = (element) => {
        if (element instanceof HTMLElement) {
          if (targets.get(key) === element) return
          targets.set(key, element)
          if (hoveredKey.value === key || focusedKey.value === key) syncPositions()
        } else {
          targets.delete(key)
          triggerRefs.delete(key)
          tooltipRefs.delete(key)
          lastPositions.delete(key)
          if (hoveredKey.value === key) hoveredKey.value = null
          if (focusedKey.value === key) focusedKey.value = null
        }
      }
      triggerRefs.set(key, triggerRef)
    }
    return {
      ref: triggerRef,
      onMouseenter: () => { hoveredKey.value = key },
      onMouseleave: () => { if (hoveredKey.value === key) hoveredKey.value = null },
      onFocus: () => { focusedKey.value = key },
      onBlur: () => { if (focusedKey.value === key) focusedKey.value = null },
    }
  }

  function tooltipProps(key: Key): TooltipProps & { style: CSSProperties; ref: Ref<TooltipInst | null> } {
    let tooltipRef = tooltipRefs.get(key)
    if (!tooltipRef) {
      tooltipRef = shallowRef<TooltipInst | null>(null)
      tooltipRefs.set(key, tooltipRef)
    }
    const activePosition = positions.value.get(key)
    const position = activePosition ?? lastPositions.get(key)
    return {
      ref: tooltipRef,
      trigger: 'manual',
      show: activePosition !== undefined && (hoveredKey.value === key || focusedKey.value === key),
      x: position?.x ?? 0,
      y: position?.y ?? 0,
      placement: 'top',
      flip: true,
      showArrow: false,
      // Point positioning removes the trigger slot. Keep the label outside the
      // tooltip and include half its height in the gap for either vertical flip.
      themeOverrides: {
        ...themeOverrides,
        peers: {
          ...themeOverrides?.peers,
          Popover: {
            ...themeOverrides?.peers?.Popover,
            space: `${(position?.height ?? 0) / 2 + triggerGap}px`,
          },
        },
      },
      style: { maxWidth: `${position?.maxWidth ?? maxTooltipWidth}px`, boxSizing: 'border-box' },
    }
  }

  const stopWatching = watch([hoveredKey, focusedKey], syncPositions, { flush: 'post' })
  const stopSynchronizing = watch(positions, () => {
    // The follower's x/y watchers may run before a resized max-width reaches the DOM.
    void nextTick(() => {
      for (const key of positions.value.keys()) tooltipRefs.get(key)?.value?.syncPosition()
    })
  }, { flush: 'post' })
  onBeforeUnmount(() => {
    stopWatching()
    stopSynchronizing()
    setListening(false)
    targets.clear()
    triggerRefs.clear()
    tooltipRefs.clear()
    lastPositions.clear()
    positions.value = new Map()
    hoveredKey.value = null
    focusedKey.value = null
  })

  return { triggerProps, tooltipProps }
}
