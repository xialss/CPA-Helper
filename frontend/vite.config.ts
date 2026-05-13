import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'

import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

function readAppVersion(): string {
  return readFileSync(fileURLToPath(new URL('../VERSION', import.meta.url)), 'utf8').trim()
}

const appVersion = readAppVersion()
const logoUrl = `/logo.png?v=${encodeURIComponent(appVersion)}`

export default defineConfig({
  plugins: [
    vue(),
    {
      name: 'cpa-helper-html-assets',
      transformIndexHtml: {
        order: 'pre',
        handler(html) {
          return html.replaceAll('__CPA_HELPER_LOGO_URL__', logoUrl)
        },
      },
    },
  ],
  define: {
    'import.meta.env.VITE_APP_VERSION': JSON.stringify(appVersion),
  },
  build: {
    chunkSizeWarningLimit: 700,
    rollupOptions: {
      output: {
        manualChunks: {
          echarts: ['echarts/core', 'echarts/charts', 'echarts/components', 'echarts/renderers'],
        },
      },
    },
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:18317',
        changeOrigin: true,
      },
    },
  },
})
