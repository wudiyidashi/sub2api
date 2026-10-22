import { fileURLToPath, URL } from 'node:url'
import CodexProxyUI from '@codex-proxy/ui/vite'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'
export default defineConfig({
  base: '/cpr-console/',
  plugins: [vue(), tailwindcss(), CodexProxyUI()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  css: { postcss: { plugins: [] } },
  server: { host: '127.0.0.1', port: 3174, strictPort: true },
  build: {
    outDir: '../../../../../../backend/internal/web/dist/cpr-console',
    emptyOutDir: true,
    license: { fileName: 'THIRD_PARTY_LICENSES.md' },
    chunkSizeWarningLimit: 800,
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            { name: 'echarts', test: /node_modules\/echarts/ },
            { name: 'zrender', test: /node_modules\/zrender/ },
          ],
        },
      },
    },
  },
})
