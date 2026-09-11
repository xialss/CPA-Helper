import type { ModelPrice, ModelPriceLibraryConflict } from '@/shared/types/api'

interface ModelGroupPriceRow {
  priceScope: ModelPrice['price_scope']
  price: ModelPrice | null
  templatePrice: ModelPrice | null
  migrationConflict: ModelPriceLibraryConflict | null
}

export function findModelGroupLibraryPrice(children: readonly ModelGroupPriceRow[]): ModelPrice | null {
  // The children already reflect the active filters. Keep their exact library
  // provider/model identity instead of matching a name against all prices.
  const library = children.find(child => child.priceScope === 'library' && child.migrationConflict === null && child.price)?.price
  if (library) return library
  return children.find(child => child.priceScope === 'channel' && child.templatePrice)?.templatePrice ?? null
}

export type ModelGroupLibraryPriceState = 'library' | 'unconfigured' | 'empty'

// A model group may contain independently configured channel prices without a
// reusable library price. In that case the group summary has no single price
// to show, but it is not an unconfigured model.
export function modelGroupLibraryPriceState(
  libraryPrice: Pick<ModelPrice, 'id'> | null,
  hasConfiguredChannelPrice: boolean,
): ModelGroupLibraryPriceState {
  if (libraryPrice) {
    return 'library'
  }
  return hasConfiguredChannelPrice ? 'empty' : 'unconfigured'
}
