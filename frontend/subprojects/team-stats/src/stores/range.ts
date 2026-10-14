import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import { useConfigStore } from '@/stores/config';
import { ymdToday } from '@/utils/time';

export type RangePreset = 'today' | 'last_2d' | 'last_7d' | 'last_30d' | 'custom';

export interface ResolvedRange {
  /** ISO start, inclusive */
  start: Date;
  /** ISO end, exclusive */
  end: Date;
  /** 上一周期等长起 */
  prevStart: Date;
  prevEnd: Date;
  /** 推荐 granularity */
  granularity: 'hour' | 'day';
}

function startOfDay(d: Date): Date {
  const x = new Date(d);
  x.setHours(0, 0, 0, 0);
  return x;
}

function addDays(date: Date, days: number): Date {
  const result = new Date(date);
  result.setDate(result.getDate() + days);
  return result;
}

export const useRangeStore = defineStore('team-stats-range', () => {
  const config = useConfigStore();
  const preset = ref<RangePreset>('last_7d');
  const refreshVersion = ref(0);
  // 仅在 preset = custom 时生效，YYYY-MM-DD
  const customStart = ref<string>('');
  const customEnd = ref<string>('');

  const resolved = computed<ResolvedRange>(() => {
    void refreshVersion.value;
    const now = new Date(`${ymdToday(config.timezone)}T12:00:00`);
    let start: Date;
    let end: Date;
    let granularity: 'hour' | 'day' = 'day';

    switch (preset.value) {
      case 'today': {
        start = startOfDay(now);
        end = addDays(start, 1);
        granularity = 'hour';
        break;
      }
      case 'last_2d': {
        end = startOfDay(addDays(now, 1));
        start = addDays(end, -2);
        granularity = 'hour';
        break;
      }
      case 'last_7d': {
        end = startOfDay(addDays(now, 1));
        start = addDays(end, -7);
        granularity = 'day';
        break;
      }
      case 'last_30d': {
        end = startOfDay(addDays(now, 1));
        start = addDays(end, -30);
        granularity = 'day';
        break;
      }
      case 'custom':
      default: {
        const s = customStart.value || '';
        const e = customEnd.value || '';
        start = s ? new Date(`${s}T00:00:00`) : startOfDay(now);
        end = e ? addDays(new Date(`${e}T00:00:00`), 1) : startOfDay(addDays(now, 1));
        if (end.getTime() <= start.getTime()) end = addDays(start, 1);
        const days = Math.max(1, Math.round((end.getTime() - start.getTime()) / 86400_000));
        granularity = days <= 2 ? 'hour' : 'day';
        break;
      }
    }

    const days = Math.max(1, Math.round((end.getTime() - start.getTime()) / 86400_000));
    const prevEnd = new Date(start.getTime());
    const prevStart = addDays(start, -days);

    return { start, end, prevStart, prevEnd, granularity };
  });

  /** sub2api 接口接受 YYYY-MM-DD，end_date 是包含的最后一天 */
  const apiDates = computed(() => {
    const { start, end } = resolved.value;
    const fmt = (d: Date) => {
      const y = d.getFullYear();
      const m = String(d.getMonth() + 1).padStart(2, '0');
      const day = String(d.getDate()).padStart(2, '0');
      return `${y}-${m}-${day}`;
    };
    // end 是开区间上界，最后一天 = end - 1ms
    const lastDay = new Date(end.getTime() - 1);
    return { start_date: fmt(start), end_date: fmt(lastDay) };
  });

  function setPreset(p: RangePreset) {
    preset.value = p;
  }

  function setCustom(start: string, end: string) {
    preset.value = 'custom';
    customStart.value = start;
    customEnd.value = end;
  }

  function requestRefresh() {
    refreshVersion.value++;
  }

  return {
    preset,
    customStart,
    customEnd,
    refreshVersion,
    resolved,
    apiDates,
    setPreset,
    setCustom,
    requestRefresh,
  };
});
