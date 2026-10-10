import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import AccountDriverSelector from '../AccountDriverSelector.vue'
import AccountDriverBadge from '../AccountDriverBadge.vue'
import Select from '@/components/common/Select.vue'
import type { AccountDriver, AccountDriverBinding } from '../types'

const { listDrivers } = vi.hoisted(() => ({ listDrivers: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accountDrivers: { list: listDrivers } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: { version?: string }) => params?.version ? `${key}: ${params.version}` : key }) }))

const native: AccountDriver = {
  id: 'native', name: '原生管理', version: 'builtin', available: true,
  platforms: ['openai', 'anthropic'], account_types: ['oauth', 'apikey'],
  capabilities: { authorization: true, import: true, refresh: true, quota: true, test: true, models: true, settings: false, switch: false }
}
const enhanced: AccountDriver = {
  ...native, id: 'codex-rs-http', name: 'Codex HTTP/SSE', platforms: ['openai'], account_types: ['oauth'],
  lifecycle_driver_id: 'native', execution_driver_id: 'codex-rs-http', transport_protocols: ['http', 'sse']
}

function mountSelector(props: Partial<InstanceType<typeof AccountDriverSelector>['$props']> = {}) {
  return mount(AccountDriverSelector, {
    props: { modelValue: 'native', active: true, platform: 'openai', accountType: 'oauth', ...props },
    global: { stubs: { Teleport: true, Icon: true } }
  })
}

enableAutoUnmount(afterEach)
beforeEach(() => listDrivers.mockReset().mockResolvedValue({ drivers: [native], default_driver_id: 'native' }))

describe('account management methods', () => {
  it('shows the generic enhanced name and the live WebSocket capability and version over an older saved binding', async () => {
    const live = { ...enhanced, version: 'ws-version', transport_protocols: ['http', 'sse', 'websocket'] }
    listDrivers.mockResolvedValue({ drivers: [native, live], default_driver_id: 'native' })
    const binding: AccountDriverBinding = { ...enhanced, version: 'http-version', binding_revision: 1 }
    const wrapper = mountSelector({ modelValue: enhanced.id, readonly: true, binding })
    await flushPromises()
    expect(wrapper.getComponent(Select).props('options')[1].label).toBe('admin.accounts.accountDriver.enhanced')
    expect(wrapper.get('[data-testid="account-driver-transport"]').text()).toBe('admin.accounts.accountDriver.websocketTransportHint')
    expect(wrapper.get('[data-testid="account-driver-version"]').text()).toBe('admin.accounts.accountDriver.version: ws-version')
    expect(wrapper.emitted('update:driver')?.at(-1)?.[0]).toMatchObject({ available: true, transport_protocols: ['http', 'sse', 'websocket'] })
  })

  it('treats absent transport capabilities as HTTP-only rather than inferring WebSocket from a version', async () => {
    const oldRuntime = { ...enhanced, version: '999', transport_protocols: undefined }
    listDrivers.mockResolvedValue({ drivers: [native, oldRuntime], default_driver_id: 'native' })
    const wrapper = mountSelector({ modelValue: enhanced.id })
    await flushPromises()
    expect(wrapper.get('[data-testid="account-driver-transport"]').text()).toBe('admin.accounts.accountDriver.httpTransportHint')
  })

  it('keeps the saved transport capability visible but disables readiness when its runtime disappears from the catalogue', async () => {
    const binding: AccountDriverBinding = { ...enhanced, version: 'last-ws', binding_revision: 4, transport_protocols: ['http', 'sse', 'websocket'] }
    const wrapper = mountSelector({ modelValue: enhanced.id, readonly: true, binding })
    await flushPromises()
    expect(wrapper.emitted('update:driver')?.at(-1)?.[0]).toMatchObject({ id: enhanced.id, available: false, transport_protocols: ['http', 'sse', 'websocket'] })
    expect(wrapper.get('[role="status"]').text()).toBe('admin.accounts.accountDriver.unavailable')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('refreshes readiness when an open edit panel receives a newer unavailable binding', async () => {
    const live = { ...enhanced, version: 'ws-version', transport_protocols: ['http', 'sse', 'websocket'] }
    listDrivers.mockResolvedValue({ drivers: [native, live], default_driver_id: 'native' })
    const binding: AccountDriverBinding = { ...live, binding_revision: 1 }
    const wrapper = mountSelector({ modelValue: enhanced.id, readonly: true, binding })
    await flushPromises()
    expect(wrapper.emitted('update:driver')?.at(-1)?.[0]).toMatchObject({ available: true })
    listDrivers.mockResolvedValue({ drivers: [native], default_driver_id: 'native' })
    await wrapper.setProps({ binding: { ...binding, available: false, version: 'stopped-version' } })
    await flushPromises()
    expect(listDrivers).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('update:driver')?.at(-1)?.[0]).toMatchObject({ available: false, version: 'stopped-version' })
    expect(wrapper.get('[data-testid="account-driver-version"]').text()).toBe('admin.accounts.accountDriver.version: stopped-version')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('enables the known enhanced preset only when ready and compatible, and reports its actual scope', async () => {
    listDrivers.mockResolvedValue({ drivers: [native, enhanced], default_driver_id: 'native' })
    const wrapper = mountSelector({ modelValue: 'codex-rs-http' })
    await flushPromises()
    const select = wrapper.getComponent(Select)
    expect(select.props('options')[1].disabled).toBe(false)
    expect(wrapper.emitted('update:ready')?.at(-1)).toEqual([true])
    expect(wrapper.get('[data-testid="account-driver-transport"]').text()).toBe('admin.accounts.accountDriver.httpTransportHint')
    expect(wrapper.text()).toContain('admin.accounts.accountDriver.nativeLifecycleHint')
    await wrapper.setProps({ accountType: 'apikey' })
    expect(select.props('options')[1].disabled).toBe(true)
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['native'])
    expect(wrapper.get('[role="status"]').text()).toBe('admin.accounts.accountDriver.selectionReset')
  })

  it('keeps an unavailable enhanced selection visible and blocks submission instead of changing its binding', async () => {
    listDrivers.mockResolvedValue({ drivers: [native, { ...enhanced, available: false }], default_driver_id: 'native' })
    const wrapper = mountSelector({ modelValue: 'codex-rs-http' })
    await flushPromises()
    expect(wrapper.getComponent(Select).props('options')[1].disabled).toBe(true)
    expect(wrapper.emitted('update:ready')?.at(-1)).toEqual([false])
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('loads supported methods on opening and leaves an unavailable or unintegrated driver disabled', async () => {
    listDrivers.mockResolvedValue({
      drivers: [native, { ...native, id: 'codex-enhanced', name: 'Codex', available: false }, { ...native, id: 'unknown', name: 'Unknown' }],
      default_driver_id: 'unknown'
    })
    const wrapper = mountSelector({ active: false })
    expect(listDrivers).not.toHaveBeenCalled()
    await wrapper.setProps({ active: true })
    await flushPromises()
    const select = wrapper.getComponent(Select)
    expect(select.props('modelValue')).toBe('native')
    expect(select.props('options')).toEqual([
      { value: 'native', label: 'admin.accounts.accountDriver.native', disabled: false },
      { value: 'codex-enhanced', label: 'Codex (admin.accounts.accountDriver.unavailable)', disabled: true },
      { value: 'unknown', label: 'Unknown (admin.accounts.accountDriver.unavailable)', disabled: true }
    ])
    select.vm.$emit('update:modelValue', 'codex-enhanced')
    select.vm.$emit('update:modelValue', 'unknown')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    select.vm.$emit('update:modelValue', 'native')
    expect(wrapper.emitted('update:modelValue')).toEqual([['native']])
  })

  it('honors the returned platform and account type ranges', async () => {
    const wrapper = mountSelector({ platform: 'gemini' })
    await flushPromises()
    expect(wrapper.getComponent(Select).props('options')[0].disabled).toBe(true)
    await wrapper.setProps({ platform: 'openai', accountType: 'setup-token' })
    expect(wrapper.getComponent(Select).props('options')[0].disabled).toBe(true)
    await wrapper.setProps({ accountType: 'apikey' })
    expect(wrapper.getComponent(Select).props('options')[0].disabled).toBe(false)
  })

  it('retains native compatibility with older servers and reports other catalogue failures', async () => {
    listDrivers.mockRejectedValueOnce({ response: { status: 404 } })
    const wrapper = mountSelector()
    await flushPromises()
    expect(wrapper.getComponent(Select).props('options')).toEqual([
      { value: 'native', label: 'admin.accounts.accountDriver.native', disabled: false }
    ])
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    listDrivers.mockRejectedValueOnce({ response: { status: 503 } })
    await wrapper.setProps({ active: false })
    await wrapper.setProps({ active: true })
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('admin.accounts.accountDriver.loadFailed')
  })

  it('shows an existing missing driver without silently switching it to native in the edit form', async () => {
    const binding: AccountDriverBinding = { ...native, id: 'missing', name: 'Saved driver', available: false, binding_revision: 7 }
    const wrapper = mountSelector({ modelValue: 'missing', readonly: true, binding })
    await flushPromises()
    const select = wrapper.getComponent(Select)
    expect(select.props('disabled')).toBe(true)
    expect(select.props('modelValue')).toBe('missing')
    expect(select.props('options').at(-1)).toEqual({ value: 'missing', label: 'Saved driver', disabled: true })
    expect(wrapper.get('[role="status"]').text()).toBe('admin.accounts.accountDriver.unavailable')
    select.vm.$emit('update:modelValue', 'native')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('labels legacy accounts as native and displays the version and unavailable binding state', () => {
    const legacy = mount(AccountDriverBadge)
    expect(legacy.text()).toBe('admin.accounts.accountDriver.native')
    const binding: AccountDriverBinding = { ...native, id: 'enhanced', name: 'Codex', version: '1.0', available: false, binding_revision: 1 }
    const enhanced = mount(AccountDriverBadge, { props: { binding } })
    expect(enhanced.text()).toContain('Codex')
    expect(enhanced.text()).toContain('1.0')
    expect(enhanced.text()).toContain('admin.accounts.accountDriver.unavailable')
    const known = mount(AccountDriverBadge, { props: { binding: { ...binding, id: 'codex-rs-http', name: 'Old HTTP/SSE label' } } })
    expect(known.text()).toContain('admin.accounts.accountDriver.enhanced')
    expect(known.text()).not.toContain('Old HTTP/SSE label')
  })
})
