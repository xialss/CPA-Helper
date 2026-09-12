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
  server: { middlewareMode: true, hmr: false, watch: null },
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
  const firstTrigger = tooltips.triggerProps(1)
  const secondTrigger = tooltips.triggerProps('channel:Auth File.json')
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

  firstTrigger.onFocus()
  firstTrigger.onMouseleave()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, true, 'Focus keeps the tooltip open after mouseleave')
  secondTrigger.onMouseenter()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, true)
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, true, 'Hover and focus can show different rows')
  assert.notEqual(tooltips.tooltipProps(1).y, tooltips.tooltipProps('channel:Auth File.json').y)
  firstTrigger.onMouseleave()
  assert.equal(hovered.value, 'channel:Auth File.json', 'A late mouseleave must not clear another row')
  firstTrigger.onBlur()
  await nextTick()
  assert.equal(tooltips.tooltipProps(1).show, false)
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, true)

  secondTrigger.onFocus()
  secondTrigger.onMouseleave()
  await nextTick()
  second.rect = domRect(-80, 200, 116, 42)
  dispatch('scroll')
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').y, 221)
  secondTrigger.ref(null)
  await nextTick()
  assert.equal(focused.value, null)
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, false)
  assert.equal(listenerCount(), 0, 'Removing the focused row clears its tooltip and listeners')
  secondTrigger.ref(second)
  assert.equal(tooltips.tooltipProps('channel:Auth File.json').show, false, 'Reusing a row key does not restore old focus')

  firstTrigger.onMouseenter()
  await nextTick()
  assert.equal(listenerCount(), 2)
  secondTrigger.onFocus()
  app.unmount()
  app = undefined
  await nextTick()
  assert.equal(listenerCount(), 0, 'Unmount cancels pending watchers and all listeners')
  globalThis.console.log(`Viewport tooltip smoke passed: ${geometryCases} installed-library geometry cases; hover, focus, scroll, resize and cleanup passed`)
} finally {
  app?.unmount()
  await server.close()
}
