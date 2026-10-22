import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CPRAccountPoolView from '../CPRAccountPoolView.vue'

const mocks = vi.hoisted(() => ({ auth: { isAdmin: true }, get: vi.fn(), request: vi.fn(), onCancel: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/api/client', () => ({ apiClient: { get: mocks.get, request: mocks.request } }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: (action: () => unknown) => action(), onCancel: mocks.onCancel }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/auth/TotpStepUpDialog.vue', () => ({ default: { template: '<div />' } }))

describe('CPR account pool host', () => {
  beforeEach(() => {
    mocks.auth.isAdmin = true
    mocks.get.mockReset()
    mocks.request.mockReset()
    mocks.onCancel.mockReset()
    localStorage.setItem('auth_user', 'admin-one')
  })
  afterEach(() => { delete window.__SUB2API_CPR_BRIDGE__; localStorage.clear() })
  it('shows the real console when enabled and releases its bridge on exit', async () => {
    mocks.get.mockResolvedValue({ data: { enabled: true, connected: true } })
    const wrapper = mount(CPRAccountPoolView)
    await flushPromises()
    expect(wrapper.get('iframe').attributes('src')).toBe('/cpr-console/index.html#/accounts')
    expect(window.__SUB2API_CPR_BRIDGE__).toBeDefined()
    wrapper.unmount()
    expect(window.__SUB2API_CPR_BRIDGE__).toBeUndefined()
  })
  it('does not expose an iframe or make backend requests for a non-admin', async () => {
    mocks.auth.isAdmin = false
    const wrapper = mount(CPRAccountPoolView)
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(false)
    await expect(window.__SUB2API_CPR_BRIDGE__?.request({ path: '/api/admin/accounts', method: 'GET' })).rejects.toMatchObject({ status: 403 })
    expect(mocks.get).not.toHaveBeenCalled()
    expect(mocks.request).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('rejects out-of-scope requests before forwarding', async () => {
    mocks.get.mockResolvedValue({ data: { enabled: true, connected: true } })
    const wrapper = mount(CPRAccountPoolView)
    await flushPromises()
    await expect(window.__SUB2API_CPR_BRIDGE__?.request({ path: '/api/admin/client-keys/reveal', method: 'GET' })).rejects.toMatchObject({ status: 403 })
    expect(mocks.request).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('discards a result if another user signs in while the request is running', async () => {
    mocks.get.mockResolvedValue({ data: { enabled: true, connected: true } })
    let finish!: (value: unknown) => void
    mocks.request.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = mount(CPRAccountPoolView)
    await flushPromises()
    const pending = window.__SUB2API_CPR_BRIDGE__!.request({ path: '/api/admin/accounts', method: 'GET' })
    localStorage.setItem('auth_user', 'admin-two')
    finish({ data: { items: [{ id: 'private-account' }] }, headers: {}, status: 200 })
    await expect(pending).rejects.toMatchObject({ status: 401 })
    wrapper.unmount()
  })
})
