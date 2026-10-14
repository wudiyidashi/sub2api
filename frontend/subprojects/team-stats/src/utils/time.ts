/** 把 ISO 时间按指定时区折算到本地"日" YYYY-MM-DD。 */
export function localDate(iso: string, timezone?: string): string {
  const d = new Date(iso);
  const fmt = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  });
  return fmt.format(d); // YYYY-MM-DD
}

/** 取本地时间的 HH:MM:SS（按时区）。 */
export function localTimeOfDay(iso: string, timezone?: string): string {
  const d = new Date(iso);
  const fmt = new Intl.DateTimeFormat('en-GB', {
    timeZone: timezone,
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
  return fmt.format(d);
}

/** 把 HH:MM:SS 转回当日秒数，用于"最早最晚"比较。 */
export function timeOfDaySeconds(iso: string, timezone?: string): number {
  const [h, m, s] = localTimeOfDay(iso, timezone).split(':').map(Number);
  return h * 3600 + m * 60 + s;
}

/** 取该 ISO 时间在本地时区的小时 / 星期（0-6，0=星期日） */
export function hourAndDow(iso: string, timezone?: string): { hour: number; dow: number } {
  const d = new Date(iso);
  const fmt = new Intl.DateTimeFormat('en-US', {
    timeZone: timezone,
    hour12: false,
    hour: '2-digit',
    weekday: 'short',
  });
  const parts = fmt.formatToParts(d);
  const hourStr = parts.find((p) => p.type === 'hour')?.value || '0';
  const wd = parts.find((p) => p.type === 'weekday')?.value || 'Sun';
  const dowMap: Record<string, number> = {
    Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6,
  };
  return { hour: parseInt(hourStr, 10) % 24, dow: dowMap[wd] ?? 0 };
}

export function ymdToday(timezone?: string): string {
  return localDate(new Date().toISOString(), timezone);
}

/** trend 桶时间戳 → 坐标轴标签（按时区），day: MM-DD，hour: MM-DD HH:00。 */
export function formatBucketLabel(
  iso: string,
  granularity: 'hour' | 'day',
  timezone?: string,
): string {
  // The backend emits already-local calendar bucket labels without an offset.
  const calendar = iso.match(/^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):\d{2}(?::\d{2})?)?$/);
  if (calendar) return `${calendar[2]}-${calendar[3]}${granularity === 'hour' ? ` ${calendar[4] || '00'}:00` : ''}`;
  const d = new Date(iso);
  const fmt = new Intl.DateTimeFormat('en-US', {
    timeZone: timezone,
    hour12: false,
    month: '2-digit',
    day: '2-digit',
    ...(granularity === 'hour' ? { hour: '2-digit' } : {}),
  });
  const parts = fmt.formatToParts(d);
  const get = (t: string) => parts.find((p) => p.type === t)?.value || '00';
  const md = `${get('month')}-${get('day')}`;
  if (granularity === 'hour') {
    return `${md} ${String(parseInt(get('hour'), 10) % 24).padStart(2, '0')}:00`;
  }
  return md;
}
