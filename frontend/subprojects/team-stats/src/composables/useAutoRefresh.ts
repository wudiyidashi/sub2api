import { onBeforeUnmount, watch } from 'vue';
import { useConfigStore } from '@/stores/config';
import { useRangeStore } from '@/stores/range';
import { clearApiCache } from '@/api/client';

/**
 * 注册一个定时刷新回调。
 * - 间隔取自 config.refreshInterval（秒），0 = 关闭。
 * - 每次刷新前清空 API 内存缓存，确保拿到新数据。
 */
export function useAutoRefresh(refresh: () => void | Promise<void>) {
  const config = useConfigStore();
  const range = useRangeStore();
  let timer: ReturnType<typeof setInterval> | null = null;

  function start() {
    stop();
    const sec = config.refreshInterval;
    if (!sec || sec <= 0) return;
    timer = setInterval(() => {
      clearApiCache();
      range.requestRefresh();
    }, sec * 1000);
  }

  function stop() {
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }

  watch(() => config.refreshInterval, start, { immediate: true });
  watch(() => range.refreshVersion, () => {
    clearApiCache();
    void refresh();
  });

  onBeforeUnmount(stop);

  return { stop, restart: start };
}
