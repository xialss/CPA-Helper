import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'

import { createServer } from 'vite'
import { createRenderer, nextTick, ref } from 'vue'
import { getOffset, getPlacementAndOffsetOfFollower } from 'vueuc/lib/binder/src/get-placement-style.js'
import { getPointRect, getRect } from 'vueuc/lib/binder/src/utils.js'

// Transform the real composable without starting HTTP, HMR or a browser.
const server = await createServer({
  root: fileURLToPath(new URL('..', import.meta.url)),
  logLevel: 'error',
  server: { middlewareMode: true, hmr: false, ws: false, watch: null },
})

let viewportWidth = 390
const viewportHeight = 844
const listeners = new Map()

function domRect(left, top, width, height) {
  return { left, right: left + width, top, bottom: top + height, width, height }
}

class TriggerElement {
  isConnected = true
  rect = domRect(68, 400, 174, 20)

  getBoundingClientRect() {
    return this.rect
  }
}

globalThis.HTMLElement = TriggerElement
globalThis.document = {
  documentElement: { get clientWidth() { return viewportWidth } },
  getElementById: () => ({ getBoundingClientRect: () => domRect(0, 0, viewportWidth, viewportHeight) }),
}
globalThis.window = {
  addEventListener(type, listener, options) {
    if (type === 'scroll') assert.equal(options.capture, true)
    if (!listeners.has(type)) listeners.set(type, new Set())
    listeners.get(type).add(listener)
  },
  removeEventListener(type, listener, capture) {
    if (type === 'scroll') assert.equal(capture, true)
    assert.ok(listeners.get(type)?.delete(listener), `Missing ${type} listener during cleanup`)
  },
}

function dispatch(type) {
  for (const listener of [...(listeners.get(type) ?? [])]) listener()
}

function listenerCount() {
  return [...listeners.values()].reduce((count, entries) => count + entries.size, 0)
}

const renderer = createRenderer({
  insert(node, parent) { node.parent = parent; parent.children.push(node) },
  remove(node) { node.parent.children.splice(node.parent.children.indexOf(node), 1) },
  createElement: (tag) => ({ tag, children: [] }),
  createText: (text) => ({ text }),
  createComment: (text) => ({ text }),
  setText(node, text) { node.text = text },
  setElementText(node, text) { node.text = text },
  parentNode: (node) => node.parent,
  nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  patchProp(node, key, _previous, value) { node[key] = value },
})

function positionedBox(targetRect, width, height, props) {
  const follower = { width, height }
  const proper = getPlacementAndOffsetOfFollower(props.placement, targetRect, follower, false, props.flip, false)
  const offset = getOffset(proper.placement, { left: 0, top: 0 }, targetRect, proper.top, proper.left, false)
  const translate = (axis, size) => Number(new RegExp(`translate${axis}\\((-?\\d+)%\\)`).exec(offset.transform)?.[1] ?? 0) * size / 100
  return {
    left: Number.parseFloat(offset.left) + translate('X', width),
    top: Number.parseFloat(offset.top) + translate('Y', height),
    placement: proper.placement,
  }
}

function geometrySnapshot(props) {
  const width = Number.parseFloat(props.style.maxWidth)
  const height = 80
  const gap = Number.parseFloat(props.themeOverrides.peers.Popover.space)
  const box = positionedBox(getPointRect(props.x, props.y), width, height + gap, props)
  const top = box.top + (box.placement.startsWith('bottom') ? gap : 0)
  return {
    inputs: {
      x: props.x,
      y: props.y,
      placement: props.placement,
      flip: props.flip,
      showArrow: props.showArrow,
      maxWidth: props.style.maxWidth,
      boxSizing: props.style.boxSizing,
      space: props.themeOverrides.peers.Popover.space,
    },
    box: { left: box.left, right: box.left + width, top, bottom: top + height, width, height, placement: box.placement },
  }
}

let app
try {
  const { useViewportTooltip } = await server.ssrLoadModule('/src/shared/composables/useViewportTooltip.ts')
  const hovered = ref(null)
  const focused = ref(null)
  let tooltips
  app = renderer.createApp({
    setup() {
      tooltips = useViewportTooltip(hovered, focused, { color: 'test-color', padding: '10px 12px' })
      return () => null
    },
  })
  app.mount({ children: [] })

  const first = new TriggerElement()
  const second = new TriggerElement()
  second.rect = domRect(220, 600, 116, 42)
  let firstTrigger = tooltips.triggerProps(1)
  let secondTrigger = tooltips.triggerProps('channel:Auth File.json')
  firstTrigger.ref(first)
  secondTrigger.ref(second)
  let positionSyncs = 0
  tooltips.tooltipProps(1).ref.value = { syncPosition: () => { positionSyncs += 1 } }
  assert.equal(tooltips.triggerProps(1).ref, firstTrigger.ref, 'Rerenders retain a stable trigger ref')
  assert.equal(listenerCount(), 0, 'Inactive tooltips must not listen for scrolling')

  // Reproduce the reviewed bug with the installed library and its real DOMRect conversion.
  const oldBox = positionedBox(getRect(first), 358, 80, { placement: 'top', flip: true })
  assert.equal(oldBox.left, 68)
  assert.equal(oldBox.left + 358, 426)

  firstTrigger.onMouseenter()
  await nextTick()
  await nextTick()
  assert.equal(listenerCount(), 2)
  assert.ok(positionSyncs > 0, 'Resync through the public instance after the DOM update')
  assert.equal(tooltips.tooltipProps(1).show, true)
  assert.equal(tooltips.tooltipProps(1).themeOverrides.color, 'test-color')
  assert.equal(tooltips.tooltipProps(1).themeOverrides.padding, '10px 12px')

  let geometryCases = 0
  const verticalPlacements = new Set()
  for (const screenWidth of [320, 390, 768, 1440]) {
    viewportWidth = screenWidth
    for (const triggerWidth of [28, 116, 144, 174]) {
      for (const left of [-triggerWidth + 1, -triggerWidth / 2, -1, 0, 16, 68, (screenWidth - triggerWidth) / 2, screenWidth - triggerWidth, screenWidth - 16, screenWidth - 1]) {
        for (const top of [4, 400, 800]) {
          first.rect = domRect(left, top, triggerWidth, 20)
          dispatch('scroll')
          const props = tooltips.tooltipProps(1)
          const maxWidth = Number.parseFloat(props.style.maxWidth)
          const gap = Number.parseFloat(props.themeOverrides.peers.Popover.space)
          for (const width of [Math.min(80, maxWidth), maxWidth]) {
            for (const contentHeight of [40, 80, 240]) {
              const box = positionedBox(getPointRect(props.x, props.y), width, contentHeight + gap, props)
              assert.ok(box.left >= 0 && box.left + width <= screenWidth, `Tooltip escaped ${screenWidth}px viewport at trigger left=${left}`)
              const contentTop = box.top + (box.placement === 'bottom' ? gap : 0)
              const contentBottom = contentTop + contentHeight
              assert.ok(contentBottom <= first.rect.top - 5 || contentTop >= first.rect.bottom + 5, 'A flipped tooltip must not overlap its hover target')
              verticalPlacements.add(box.placement)
              geometryCases += 1
            }
          }
        }
      }
    }
  }
  assert.deepEqual([...verticalPlacements].sort(), ['bottom', 'top'])
  viewportWidth = 390
  first.rect = domRect(68, 400, 174, 20)
  dispatch('resize')
  const syncsBeforeResize = positionSyncs
  await nextTick()
  await nextTick()
  assert.ok(positionSyncs > syncsBeforeResize, 'Viewport resize resyncs after the new width is applied')
  const fixedProps = tooltips.tooltipProps(1)
  const fixedBox = positionedBox(getPointRect(fixedProps.x, fixedProps.y), 358, 80, fixedProps)
  assert.equal(fixedBox.left, 16)
  assert.equal(fixedBox.left + 358, 374)

  const beforeMouseleave = geometrySnapshot(tooltips.tooltipProps(1))
  firstTrigger.onMouseleave()
  await nextTick()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, false)
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps(1)), beforeMouseleave, 'Mouseleave preserves positioning inputs and the closing tooltip box')
  assert.equal(listenerCount(), 0, 'A closing tooltip must not keep scroll or resize listeners')

  viewportWidth = 768
  first.rect = domRect(460, 120, 174, 32)
  dispatch('resize')
  dispatch('scroll')
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps(1)), beforeMouseleave, 'Inactive exit geometry is not changed by resize or scrolling')
  firstTrigger.onMouseenter()
  await nextTick()
  await nextTick()
  const reopenedProps = tooltips.tooltipProps(1)
  assert.equal(reopenedProps.show, true)
  assert.equal(reopenedProps.x, 547)
  assert.equal(reopenedProps.y, 136)
  assert.equal(reopenedProps.style.maxWidth, '360px')
  assert.equal(reopenedProps.themeOverrides.peers.Popover.space, '22px', 'Reopening measures the current trigger height')
  assert.notDeepEqual(geometrySnapshot(reopenedProps), beforeMouseleave, 'Quick re-entry uses the current viewport and trigger geometry')
  firstTrigger.onMouseleave()
  firstTrigger.onMouseenter()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, true, 'An immediate leave/re-entry keeps the current row open')
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps(1)), geometrySnapshot(reopenedProps))

  viewportWidth = 390
  first.rect = domRect(68, 400, 174, 20)
  dispatch('resize')
  firstTrigger.onMouseenter()
  firstTrigger.onFocus()
  firstTrigger.onMouseleave()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, true, 'Focus keeps the tooltip open after mouseleave')
  const beforeBlur = geometrySnapshot(tooltips.tooltipProps(1))
  firstTrigger.onBlur()
  await nextTick()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, false)
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps(1)), beforeBlur, 'Final blur preserves positioning inputs and the closing tooltip box')
  assert.equal(listenerCount(), 0, 'Ending keyboard focus stops listeners during the exit transition')

  firstTrigger.onFocus()
  secondTrigger.onMouseenter()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, true)
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, true, 'Hover and focus can show different rows')
  assert.notEqual(tooltips.tooltipProps(1).y, tooltips.tooltipProps('channel:Auth File.json').y)
  const beforeSwitch = geometrySnapshot(tooltips.tooltipProps(1))
  firstTrigger.onMouseleave()
  assert.equal(hovered.value, 'channel:Auth File.json', 'A late mouseleave must not clear another row')
  firstTrigger.onBlur()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, false)
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps(1)), beforeSwitch, 'Closing one row preserves its geometry while another stays open')
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, true)

  secondTrigger.onFocus()
  secondTrigger.onMouseleave()
  await nextTick()
  second.rect = domRect(-80, 200, 116, 42)
  dispatch('scroll')
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').y, 221)
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps(1)), beforeSwitch, 'Active-row scrolling does not overwrite a different closing row')

  const secondBeforeBlur = geometrySnapshot(tooltips.tooltipProps('channel:Auth File.json'))
  secondTrigger.onBlur()
  await nextTick()
  await nextTick()
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, false)
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps('channel:Auth File.json')), secondBeforeBlur, 'Channel-key tooltips retain their own exit geometry')
  assert.equal(listenerCount(), 0)
  const removedTriggerRef = secondTrigger.ref
  const removedTooltipRef = tooltips.tooltipProps('channel:Auth File.json').ref
  secondTrigger.ref(null)
  await nextTick()
  const removedProps = tooltips.tooltipProps('channel:Auth File.json')
  assert.equal(removedProps.x, 0)
  assert.equal(removedProps.y, 0, 'Removing an inactive row releases its retained exit geometry')
  assert.notEqual(removedProps.ref, removedTooltipRef, 'Removed rows release their tooltip instance refs')
  secondTrigger = tooltips.triggerProps('channel:Auth File.json')
  assert.notEqual(secondTrigger.ref, removedTriggerRef, 'Removed rows release their trigger callbacks')
  second.rect = domRect(80, 80, 116, 18)
  secondTrigger.ref(second)
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, false, 'Reusing a row key does not restore old focus')
  secondTrigger.onFocus()
  await nextTick()
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').y, 89, 'A reused row measures its replacement trigger')
  secondTrigger.ref(null)
  await nextTick()
  assert.equal(focused.value, null)
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, false)
  assert.equal(listenerCount(), 0, 'Removing the focused row clears its tooltip and listeners')
  secondTrigger = tooltips.triggerProps('channel:Auth File.json')
  secondTrigger.ref(second)

  firstTrigger.onMouseenter()
  await nextTick()
  const beforeDetail = geometrySnapshot(tooltips.tooltipProps(1))
  hovered.value = 'detail'
  await nextTick()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, false)
  assert.equal(tooltips.tooltipProps('detail').show, false, 'An unregistered drawer key cannot activate a table tooltip')
  assert.deepEqual(geometrySnapshot(tooltips.tooltipProps(1)), beforeDetail)
  assert.equal(listenerCount(), 0, 'Retained positions do not make an unregistered key listen for scrolling')

  firstTrigger.onMouseenter()
  await nextTick()
  first.isConnected = false
  dispatch('scroll')
  assert.equal(tooltips.tooltipProps(1).show, false, 'A disconnected active trigger cannot keep its tooltip shown')
  assert.equal(listenerCount(), 0, 'Only connected active targets need listeners')
  firstTrigger.ref(null)
  await nextTick()
  assert.equal(hovered.value, null)
  first.isConnected = true
  firstTrigger = tooltips.triggerProps(1)
  firstTrigger.ref(first)
  tooltips.tooltipProps(1).ref.value = { syncPosition: () => { positionSyncs += 1 } }

  firstTrigger.onMouseenter()
  await nextTick()
  assert.equal(listenerCount(), 2)
  secondTrigger.onFocus()
  app.unmount()
  app = undefined
  const syncsAtUnmount = positionSyncs
  await nextTick()
  assert.equal(listenerCount(), 0, 'Unmount cancels pending watchers and all listeners')
  assert.equal(positionSyncs, syncsAtUnmount, 'A queued position sync cannot call an unmounted tooltip')
  assert.equal(tooltips.tooltipProps(1).show, false)
  assert.equal(tooltips.tooltipProps(1).x, 0, 'Unmount releases retained geometry')
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').y, 0)
  globalThis.console.log(`Viewport tooltip smoke passed: ${geometryCases} installed-library geometry cases; exit geometry, re-entry, row switching, hover, focus, scroll, resize and cleanup passed`)
} finally {
  app?.unmount()
  await server.close()
}
