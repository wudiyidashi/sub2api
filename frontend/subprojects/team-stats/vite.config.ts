import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import * as vueCompiler from 'vue/compiler-sfc';
import { fileURLToPath, URL } from 'node:url';
import tailwindcss from 'tailwindcss';
import autoprefixer from 'autoprefixer';
// @ts-expect-error Tailwind's local JS configuration has no declaration file.
import tailwindConfig from './tailwind.config.js';

const root = fileURLToPath(new URL('.', import.meta.url));
export default defineConfig({
  root,
  base: '/team-stats/',
  plugins: [vue({ compiler: vueCompiler })],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  css: { postcss: { plugins: [tailwindcss({
    ...tailwindConfig,
    content: [`${root}/index.html`, `${root}/src/**/*.{vue,ts,tsx}`],
  }), autoprefixer()] } },
  build: {
    outDir: '../../../backend/internal/web/dist/team-stats',
    emptyOutDir: true,
    target: 'es2020',
    rollupOptions: { output: { manualChunks: {
      echarts: ['echarts', 'vue-echarts'],
      vue: ['vue', 'vue-router', 'pinia'],
    } } },
  },
});
