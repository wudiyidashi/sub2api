import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
import { list } from '@/api/admin/accountDrivers'
import { exportData, list as listAccounts, listWithEtag } from '@/api/admin/accounts'

beforeEach(() => get.mockReset())

describe('admin account driver APIs', () => {
  it('reads the server catalogue from the admin endpoint', async () => {
    const catalog = { drivers: [], default_driver_id: 'native' }
    get.mockResolvedValue({ data: catalog })
    expect(await list()).toEqual(catalog)
    expect(get).toHaveBeenCalledWith('/admin/account-drivers')
  })

  it('keeps management method filters in paginated and automatic-refresh requests', async () => {
    get.mockResolvedValue({ status: 200, data: { items: [], total: 0 }, headers: {} })
    await listAccounts(2, 20, { account_driver_id: 'native' })
    expect(get).toHaveBeenLastCalledWith('/admin/accounts', expect.objectContaining({
      params: { page: 2, page_size: 20, account_driver_id: 'native' }
    }))
    await listWithEtag(2, 20, { account_driver_id: 'native' }, { etag: 'revision' })
    expect(get).toHaveBeenLastCalledWith('/admin/accounts', expect.objectContaining({
      params: { page: 2, page_size: 20, account_driver_id: 'native' }, headers: { 'If-None-Match': 'revision' }
    }))
  })

  it('preserves the management method filter when exporting the current filtered results', async () => {
    const payload = { accounts: [], proxies: [] }
    get.mockResolvedValue({ data: payload })
    expect(await exportData({
      filters: { platform: 'openai', account_driver_id: 'native', search: 'team' },
      includeProxies: false
    })).toEqual(payload)
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/data', {
      params: { platform: 'openai', account_driver_id: 'native', search: 'team', include_proxies: 'false' }
    })
  })
})
