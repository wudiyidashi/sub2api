import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import TeamStatsView from '../TeamStatsView.vue'

const state = vi.hoisted(() => ({ auth: { isAdmin: true }, request: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { request: state.request } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))

describe('administrator team dashboard host', () => {
  beforeEach(() => {
    localStorage.clear()
    state.auth.isAdmin = true
    state.request.mockReset()
    state.request.mockResolvedValue({ data: { requests: 42 } })
  })
  it('uses the main API client and removes transport on navigation', async () => {
    const wrapper = mount(TeamStatsView)
    expect(wrapper.get('iframe').attributes('src')).toBe('/team-stats/index.html')
    const bridge = window.__SUB2API_TEAM_STATS__!
    const result = await bridge.request({ path: '/admin/usage', method: 'GET', query: { timezone: 'Asia/Shanghai' } })
    expect(result).toEqual({ requests: 42 })
    expect(state.request).toHaveBeenCalledWith(expect.objectContaining({ params: { timezone: 'Asia/Shanghai' } }))
    await expect(bridge.request({ path: '/admin/settings/admin-api-key', method: 'GET' })).rejects.toMatchObject({ status: 403 })
    wrapper.unmount()
    expect(window.__SUB2API_TEAM_STATS__).toBeUndefined()
  })
  it('refuses transport and hides the frame for ordinary users', async () => {
    state.auth.isAdmin = false
    const wrapper = mount(TeamStatsView)
    expect(wrapper.find('iframe').exists()).toBe(false)
    await expect(window.__SUB2API_TEAM_STATS__!.request({ path: '/admin/usage', method: 'GET' })).rejects.toMatchObject({ status: 403 })
    expect(state.request).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('discards requests that finish after an account switch', async () => {
    localStorage.setItem('auth_user', '{"id":1}')
    const wrapper = mount(TeamStatsView)
    state.request.mockImplementation(async () => {
      localStorage.setItem('auth_user', '{"id":2}')
      return { data: { private: true } }
    })
    await expect(window.__SUB2API_TEAM_STATS__!.request({ path: '/admin/usage', method: 'GET' })).rejects.toMatchObject({ status: 401 })
    wrapper.unmount()
  })
})
