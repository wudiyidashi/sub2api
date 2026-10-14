import { defineConfig } from 'vitest/config';
import { fileURLToPath, URL } from 'node:url';
export default defineConfig({
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  test: {
    environment: 'jsdom',
    include: ['subprojects/team-stats/src/**/*.spec.ts', 'src/features/team-stats/**/*.spec.ts'],
  },
});
