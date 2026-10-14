import { watchEffect } from 'vue';
import { useConfigStore } from '@/stores/config';

/** 把 config.theme 应用到 <html data-theme>。 */
export function useTheme() {
  const config = useConfigStore();
  watchEffect(() => {
    const want = config.theme;
    let resolved: 'light' | 'dark' = 'dark';
    if (want === 'auto') {
      resolved = window.matchMedia('(prefers-color-scheme: dark)').matches
        ? 'dark'
        : 'light';
    } else {
      resolved = want;
    }
    document.documentElement.setAttribute('data-theme', resolved);
  });
}
