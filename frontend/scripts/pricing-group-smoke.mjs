import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'

import { createServer } from 'vite'

const server = await createServer({
  root: fileURLToPath(new URL('..', import.meta.url)),
  logLevel: 'error',
  server: { middlewareMode: true },
})

function libraryPrice(id, provider, inputPrice, model = 'shared-model') {
  return {
    id,
    provider,
    model,
    price_scope: 'library',
    channel_auth_type: null,
    channel_brand: null,
    channel_key: null,
    billing_unit: 'token',
    input_usd_per_million: inputPrice,
    output_usd_per_million: inputPrice * 2,
    cache_read_usd_per_million: 0,
    cache_creation_usd_per_million: 0,
    request_usd: null,
    priority_multiplier: null,
    long_context: null,
    preserved_long_context: null,
    time_pricing: null,
    source: 'manual',
    source_model: null,
    auto_synced: false,
    last_synced_at: null,
    updated_at: '2026-09-09T12:00:00+08:00',
  }
}

function libraryRow(price) {
  return {
    priceScope: 'library',
    price,
    templatePrice: null,
    migrationConflict: null,
    channelFilterKey: `library:${price.provider}`,
  }
}

function channelRow(price, templatePrice = null) {
  return { priceScope: 'channel', price, templatePrice, migrationConflict: null }
}

try {
  const { findModelGroupLibraryPrice, modelGroupLibraryPriceState } = await server.ssrLoadModule(
    '/src/features/pricing/utils/modelPriceGroupSummary.ts',
  )
  const libraryA = libraryPrice(1, 'provider-a', 1)
  const libraryB = libraryPrice(2, 'provider-b', 2)
  const rows = [libraryRow(libraryA), libraryRow(libraryB)]
  const filteredB = rows.filter(row => row.channelFilterKey === 'library:provider-b')
  const summaryB = findModelGroupLibraryPrice(filteredB)
  assert.equal(summaryB, libraryB, 'A provider filter must also constrain the group price')
  assert.equal(summaryB.input_usd_per_million, 2)
  assert.equal(summaryB.provider, 'provider-b')
  assert.equal(summaryB.model, 'shared-model')
  assert.equal(findModelGroupLibraryPrice([rows[0]]), libraryA, 'A library-only row is its own summary')

  assert.equal(
    findModelGroupLibraryPrice([channelRow(null, libraryA), rows[1]]),
    libraryB,
    'An explicit library row takes precedence over another channel template',
  )
  assert.equal(findModelGroupLibraryPrice([channelRow(null, libraryB)]), libraryB)
  const prefixedLibrary = libraryPrice(3, 'gemini', 0, 'publishers/google/models/Gemini-2.5-Pro')
  assert.equal(findModelGroupLibraryPrice([libraryRow(prefixedLibrary)]), prefixedLibrary)
  assert.equal(modelGroupLibraryPriceState(prefixedLibrary, false), 'library', 'Zero is a configured price')

  const channelPrice = {
    ...libraryB,
    id: 4,
    price_scope: 'channel',
    channel_auth_type: 'apikey',
    channel_brand: 'openai_compatibility',
    channel_key: 'channel-b',
  }
  const noLibrary = findModelGroupLibraryPrice([channelRow(channelPrice)])
  assert.equal(noLibrary, null, 'A channel amount must never become the group library amount')
  assert.equal(modelGroupLibraryPriceState(noLibrary, true), 'empty')
  const noPrice = findModelGroupLibraryPrice([channelRow(null)])
  assert.equal(modelGroupLibraryPriceState(noPrice, false), 'unconfigured')

  const conflictRow = { ...libraryRow(libraryA), migrationConflict: { original_id: libraryA.id } }
  assert.equal(findModelGroupLibraryPrice([conflictRow]), null, 'Archived conflicts are not library templates')
  assert.equal(findModelGroupLibraryPrice([conflictRow, channelRow(null, libraryB)]), libraryB)
  assert.equal(findModelGroupLibraryPrice([]), null)

  const { multiplyModelPrice } = await server.ssrLoadModule('/src/features/pricing/utils/modelPriceMultiplier.ts')
  const template = libraryPrice(10, 'provider-a', 0.04)
  template.priority_multiplier = 1.00001
  template.long_context = {
    threshold_input_tokens: 123456,
    input_usd_per_million: 2,
    output_usd_per_million: 4,
    cache_read_usd_per_million: 0.2,
    cache_creation_usd_per_million: 0.4,
  }
  const originalTemplate = structuredClone(template)
  const converted = multiplyModelPrice(template, 0.11)
  assert.equal(converted.input_usd_per_million, 0.0044)
  assert.equal(converted.output_usd_per_million, 0.0088)
  assert.equal(converted.priority_multiplier, 1.00001)
  assert.deepEqual(converted.long_context, {
    threshold_input_tokens: 123456,
    input_usd_per_million: 0.22,
    output_usd_per_million: 0.44,
    cache_read_usd_per_million: 0.022,
    cache_creation_usd_per_million: 0.044,
  })
  assert.deepEqual(template, originalTemplate, 'Conversion must not mutate the library snapshot')
  const large = multiplyModelPrice({ ...template, input_usd_per_million: 1e300 }, 0.11)
  assert.ok(Number.isFinite(large.input_usd_per_million), 'Rounding must preserve a finite large amount')
  assert.ok(Math.abs(large.input_usd_per_million / 1.1e299 - 1) < 1e-15)
  assert.notEqual(JSON.parse(JSON.stringify(large)).input_usd_per_million, null)
  assert.equal(multiplyModelPrice({ ...template, input_usd_per_million: Number.MAX_VALUE }, 2), null)
  const invalidTier = { ...template, long_context: { ...template.long_context, output_usd_per_million: Number.MAX_VALUE } }
  const originalInvalidTier = structuredClone(invalidTier)
  assert.equal(multiplyModelPrice(invalidTier, 2), null, 'Any overflowing tier rejects the entire conversion')
  assert.deepEqual(invalidTier, originalInvalidTier)
  const requestTemplate = { ...template, billing_unit: 'request', request_usd: 0.04, long_context: null }
  assert.equal(multiplyModelPrice(requestTemplate, 0.11).request_usd, 0.0044)
  assert.equal(multiplyModelPrice(requestTemplate, 0).request_usd, 0)
  assert.equal(multiplyModelPrice({ ...requestTemplate, request_usd: Number.MAX_VALUE }, 2), null)
  assert.equal(multiplyModelPrice({ ...requestTemplate, request_usd: null }, 0.11), null)

  globalThis.console.log('Pricing group smoke tests passed')
} finally {
  await server.close()
}
