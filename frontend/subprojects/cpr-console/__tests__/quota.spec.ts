import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import Summary from '../.upstream/build/frontend/src/views/accounts/components/account-quota-summary-cell/index.vue'
import Actions from '../.upstream/build/frontend/src/views/accounts/components/AccountTableActions.vue'
import type { Account } from '../.upstream/build/frontend/src/api'

function account(percent: number | null = 0, kind = 'oauth') {
  return {
    id: 'oauth-fixture', name: 'OAuth fixture', provider: 'openai', authenticationKind: kind,
    enabled: true, capacity: { usedSlots: 0, totalSlots: 4 },
    capabilities: { quota: true, quotaRefresh: true },
    usage: { requestCount: 0, totalTokensDisplay: '0' },
    quota: { refreshedAtDisplay: '刚刚', windows: percent === null ? [] : [{
      key: 'weekly', group: 'shortTerm', limitId: 'codex', limitName: null, role: 'primary',
      labelDisplay: '周额度', windowLabelDisplay: '周额度', windowSeconds: 604800,
      usedPercent: percent, usedPercentDisplay: `${percent}.0%`, resetAtDisplay: '2026-10-30 08:00:00',
    }] },
  } as Account
}

describe('embedded CPR quota visibility', () => {
  it('shows upstream zero usage without any local requests, including reset and refresh time', async () => {
    const wrapper = mount(Summary, { props: { account: account() } })
    expect(wrapper.text()).toContain('0.0%')
    expect(wrapper.text()).toContain('2026-10-30 08:00:00')
    expect(wrapper.text()).toContain('刷新 刚刚')
    await wrapper.get('button[aria-label="查看OAuth fixture的额度详情"]').trigger('click')
    expect(wrapper.emitted('showQuota')).toHaveLength(1)
    await wrapper.setProps({ account: account(90) })
    expect(wrapper.text()).toContain('90.0%')
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('90')
  })

  it('distinguishes missing quota and API key usage from a zero quota value', () => {
    const unknown = mount(Summary, { props: { account: account(null) } })
    expect(unknown.text()).toContain('额度待观测')
    expect(unknown.text()).not.toContain('0.0%')
    const apiKey = mount(Summary, { props: { account: account(null, 'api_key') } })
    expect(apiKey.text()).toContain('上游额度未提供')
    expect(apiKey.find('[role="progressbar"]').exists()).toBe(false)
  })

  it('refreshes only the selected account and disables its refresh action during the request', async () => {
    const wrapper = mount(Actions, { props: {
      account: account(), deleting: false, downloadingCatalog: false, recovering: false,
      refreshing: false, refreshingQuota: false, testing: false, togglingScheduling: false,
    } })
    await wrapper.get('button[aria-label="刷新额度"]').trigger('click')
    expect(wrapper.emitted('refreshQuota')).toEqual([['oauth-fixture']])
    await wrapper.setProps({ refreshingQuota: true })
    expect(wrapper.get('button[aria-label="刷新额度"]').attributes()).toHaveProperty('disabled')
    expect(wrapper.get('button[aria-label="编辑账号"]').attributes()).not.toHaveProperty('disabled')
  })
})
