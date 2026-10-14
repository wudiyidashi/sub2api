import { beforeEach, describe, expect, it, vi } from 'vitest';
const transport = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }));
vi.mock('./client', () => ({ api: transport }));
import { endpoints } from './endpoints';

describe('native statistics API compatibility', () => {
  beforeEach(() => { transport.get.mockReset(); });
  it('adapts native trend and model billing fields', async () => {
    transport.get.mockResolvedValueOnce({ trend: [{ date: '2026-10-14', requests: 10, cost: 3.5, actual_cost: 2.5 }] });
    const trend = await endpoints.dashboardTrend({ start_date: '2026-10-14' });
    expect(trend.trend[0]).toMatchObject({ timestamp: '2026-10-14', requests: 10, total_cost: 3.5, actual_cost: 2.5 });
    transport.get.mockResolvedValueOnce({ models: [{ model: 'gpt-6', cost: 3.5, actual_cost: 2.5 }] });
    const models = await endpoints.dashboardModels({});
    expect(models.models[0]).toMatchObject({ total_cost: 3.5, actual_cost: 2.5 });
  });
  it('loads all pages when the server caps the key page size', async () => {
    const keys = Array.from({ length: 230 }, (_, i) => ({ id: i + 1, name: `Key ${i + 1}` }));
    transport.get.mockImplementation(async (_path, opts) => {
      const page = opts.query.page;
      return { total: 230, page_size: 100, page, items: keys.slice((page - 1) * 100, page * 100) };
    });
    expect((await endpoints.groupAllApiKeys(4)).map(k => k.id)).toEqual(keys.map(k => k.id));
    expect(transport.get).toHaveBeenCalledTimes(3);
  });
});
