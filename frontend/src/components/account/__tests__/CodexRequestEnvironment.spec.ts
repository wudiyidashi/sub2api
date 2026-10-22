import { describe, it, vi, expect, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CodexRequestEnvironment from '../CodexRequestEnvironment.vue'
const api = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: api }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const environment = { ip: '1.2.3.4', country: 'United States', country_code: 'US', region: 'California', city: 'LA', timezone: 'America/Los_Angeles', checked_at: '2026-10-22T01:00:00Z' }
describe('account egress environment preview', () => {
 beforeEach(() => api.get.mockReset())
 it('previews the selected proxy and supports a fresh check', async () => {
  api.get.mockResolvedValue({ data: environment })
  const wrapper = mount(CodexRequestEnvironment, { props: { accountId: 7, proxyId: 4 } })
  await flushPromises()
  expect(wrapper.text()).toContain('America/Los_Angeles')
  expect(api.get).toHaveBeenCalledWith('/admin/accounts/7/request-environment', expect.objectContaining({ params: { proxy_id: 4, refresh: undefined } }))
  await wrapper.get('button').trigger('click'); await flushPromises()
  expect(api.get).toHaveBeenLastCalledWith('/admin/accounts/7/request-environment', expect.objectContaining({ params: { proxy_id: 4, refresh: 1 } }))
  wrapper.unmount()
 })
 it('discards a stale result after switching proxy', async () => {
  let resolve!: (v: unknown) => void
  api.get.mockImplementationOnce(() => new Promise(r => { resolve = r }))
  api.get.mockResolvedValueOnce({ data: { ...environment, timezone: 'Asia/Tokyo' } })
  const wrapper = mount(CodexRequestEnvironment, { props: { accountId: 7, proxyId: 4 } })
  await wrapper.setProps({ proxyId: 5 }); await flushPromises()
  resolve({ data: environment }); await flushPromises()
  expect(wrapper.text()).toContain('Asia/Tokyo')
  expect(wrapper.text()).not.toContain('America/Los_Angeles')
  wrapper.unmount()
 })
 it('clears previous location when detection fails', async () => {
  api.get.mockResolvedValueOnce({ data: environment }).mockRejectedValueOnce(new Error('Detection unavailable'))
  const wrapper = mount(CodexRequestEnvironment, { props: { accountId: 7, proxyId: null } })
  await flushPromises(); await wrapper.get('button').trigger('click'); await flushPromises()
  expect(wrapper.text()).toContain('Detection unavailable')
  expect(wrapper.text()).not.toContain(environment.ip)
  wrapper.unmount()
 })
})
