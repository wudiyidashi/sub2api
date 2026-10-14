/** 千分位 + 简化（>1e6 → M、>1e9 → B）。 */
export function formatNumber(n: number | undefined | null, opts: { short?: boolean } = {}): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  const abs = Math.abs(n);
  if (opts.short) {
    if (abs >= 1e9) return (n / 1e9).toFixed(2) + 'B';
    if (abs >= 1e6) return (n / 1e6).toFixed(2) + 'M';
    if (abs >= 1e4) return (n / 1e3).toFixed(1) + 'K';
  }
  return n.toLocaleString(undefined, { maximumFractionDigits: 0 });
}

export function formatTokens(n: number | undefined | null): string {
  return formatNumber(n, { short: true });
}

export function formatCost(n: number | undefined | null, currency: 'USD' | 'CNY' = 'USD'): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  const symbol = currency === 'USD' ? '$' : '¥';
  if (Math.abs(n) >= 100) {
    return `${symbol}${n.toLocaleString(undefined, { maximumFractionDigits: 2 })}`;
  }
  return `${symbol}${n.toFixed(2)}`;
}

export function formatDuration(ms: number | undefined | null): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '—';
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(2)} s`;
  const m = Math.floor(s / 60);
  const rs = Math.round(s - m * 60);
  if (m < 60) return `${m}m ${rs}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m - h * 60}m`;
}

export function formatPercent(n: number | undefined | null, fixed = 1): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return `${(n * 100).toFixed(fixed)}%`;
}

export function formatTime(iso: string | undefined | null, timezone?: string): string {
  if (!iso) return '—';
  try {
    const d = new Date(iso);
    // sv-SE 固定输出 "YYYY-MM-DD HH:mm:ss"，避免浏览器 locale 影响 slice 取段
    return d.toLocaleString('sv-SE', {
      timeZone: timezone,
      hour12: false,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  } catch {
    return iso;
  }
}

export function formatHHmm(iso: string | undefined | null, timezone?: string): string {
  if (!iso) return '—';
  try {
    const d = new Date(iso);
    return d.toLocaleTimeString('en-GB', {
      timeZone: timezone,
      hour12: false,
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  } catch {
    return iso;
  }
}
