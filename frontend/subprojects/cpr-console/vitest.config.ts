import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@codex-proxy/ui': fileURLToPath(new URL('./__tests__/ui-stubs.ts', import.meta.url)),
      '@lucide/vue': fileURLToPath(new URL('./__tests__/icons.ts', import.meta.url)),
      '@/utils/providers': fileURLToPath(new URL('./__tests__/providers.ts', import.meta.url)),
      '@': fileURLToPath(new URL('./.upstream/build/frontend/src', import.meta.url)),
    },
    dedupe: ['vue'],
  },
  test: {
    environment: 'jsdom',
    include: ['subprojects/cpr-console/__tests__/*.spec.ts'],
  },
  esbuild: { target: 'es2022', tsconfigRaw: { compilerOptions: { target: 'ES2022' } } },
})
