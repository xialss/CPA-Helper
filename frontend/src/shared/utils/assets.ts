const assetCacheKey = import.meta.env.VITE_APP_VERSION || 'dev'

export const logoUrl = `/logo.png?v=${encodeURIComponent(assetCacheKey)}`
