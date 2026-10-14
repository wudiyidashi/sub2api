import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { normalizeConfig } from './config';
import { useRangeStore } from './range';
import { formatBucketLabel } from '@/utils/time';

describe('team dashboard configuration and dates', () => {
  beforeEach(() => { setActivePinia(createPinia()); vi.useRealTimers(); });
  it('imports legacy groups without retaining server credentials', () => {
    const cfg = normalizeConfig({ baseURL: 'https://example.com', adminKey: 'secret', groups: [{ id: 'alice', name: 'Alice', color: '#123abc', apiKeyIds: [10] }] });
    expect(cfg.groups[0].apiKeyIds).toEqual([10]);
    expect(cfg).not.toHaveProperty('baseURL');
    expect(cfg).not.toHaveProperty('adminKey');
  });
  it('rejects duplicated keys, bad refresh intervals and malformed imports', () => {
    expect(() => normalizeConfig({ refreshInterval: 1 })).toThrow();
    expect(() => normalizeConfig({ groups: 'oops' })).toThrow();
    expect(() => normalizeConfig({ groups: [{ id: 'alice', name: 'Alice', color: '#123abc', apiKeyIds: [10, 10] }] })).toThrow();
  });
  it('includes the selected end date even for a one-day custom range', () => {
    const range = useRangeStore();
    range.setCustom('2026-10-14', '2026-10-14');
    expect(range.apiDates).toEqual({ start_date: '2026-10-14', end_date: '2026-10-14' });
  });
  it('uses the configured timezone across the date boundary and refreshes dates', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-10-14T23:30:00Z'));
    const range = useRangeStore();
    range.setPreset('today');
    expect(range.apiDates.start_date).toBe('2026-10-15');
    vi.setSystemTime(new Date('2026-10-15T23:30:00Z'));
    range.requestRefresh();
    expect(range.apiDates.start_date).toBe('2026-10-16');
  });
  it('does not shift backend calendar bucket labels a second time', () => {
    expect(formatBucketLabel('2026-10-14', 'day', 'America/Los_Angeles')).toBe('10-14');
    expect(formatBucketLabel('2026-10-14 13:00', 'hour', 'Asia/Shanghai')).toBe('10-14 13:00');
  });
});
