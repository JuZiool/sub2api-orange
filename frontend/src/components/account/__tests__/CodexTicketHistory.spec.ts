import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CodexTicketHistory from '../CodexTicketHistory.vue'

const getHistory = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getCodexTicketHistory: getHistory } } }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const event = {
  id: 2, at: '2026-09-19T11:00:00Z', model: 'gpt-6-astra', outcome: 'failure',
  error_code: 'proxy_auth', http_status: 407, length: 0, target_length: 332,
  proxy_index: 2, duration_ms: 300,
}

function mountHistory() {
  return mount(CodexTicketHistory, {
    props: { accountId: 9, model: 'gpt-6-astra' },
    global: { stubs: { Icon: true } },
  })
}

describe('CodexTicketHistory', () => {
  beforeEach(() => { getHistory.mockReset().mockResolvedValue({ events: [event], limit: 20 }) })

  it('loads only when expanded and refreshes only on demand', async () => {
    const wrapper = mountHistory()
    expect(getHistory).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    await flushPromises()
    expect(getHistory).toHaveBeenCalledWith(9, 'gpt-6-astra', expect.any(AbortSignal))
    expect(wrapper.find('[data-testid="ticket-history-event-2"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('errors.proxy_auth')
    expect(wrapper.text()).toContain('HTTP 407')
    await wrapper.get('[data-testid="ticket-history-refresh"]').trigger('click')
    await flushPromises()
    expect(getHistory).toHaveBeenCalledTimes(2)
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    expect(getHistory).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('discards old results when the account changes and never displays raw errors', async () => {
    let resolve!: (result: unknown) => void
    getHistory.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const wrapper = mountHistory()
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    const previousSignal = getHistory.mock.calls[0][2] as AbortSignal
    await wrapper.setProps({ accountId: 10 })
    expect(previousSignal.aborted).toBe(true)
    resolve({ events: [event], limit: 20 })
    await flushPromises()
    expect(wrapper.find('[data-testid="ticket-history-event-2"]').exists()).toBe(false)
    expect(getHistory).toHaveBeenCalledTimes(1)
    getHistory.mockResolvedValueOnce({ events: [{ ...event, error_code: 'https://secret:password@proxy.invalid', raw_error: 'private-token' }], limit: 20 })
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('errors.unknown')
    expect(wrapper.html()).not.toContain('password')
    expect(wrapper.html()).not.toContain('private-token')
    wrapper.unmount()
  })

  it('shows a fixed load failure and supports retry', async () => {
    getHistory.mockRejectedValueOnce(new Error('upstream private-token'))
    const wrapper = mountHistory()
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('codexTicketHistory.loadFailed')
    expect(wrapper.html()).not.toContain('private-token')
    await wrapper.get('[data-testid="ticket-history-refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="ticket-history-event-2"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('aborts history loading when collapsed and on unmount', async () => {
    getHistory.mockReturnValue(new Promise(() => {}))
    const wrapper = mountHistory()
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    const firstSignal = getHistory.mock.calls[0][2] as AbortSignal
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    expect(firstSignal.aborted).toBe(true)
    await wrapper.get('[data-testid="ticket-history-toggle"]').trigger('click')
    const secondSignal = getHistory.mock.calls[1][2] as AbortSignal
    wrapper.unmount()
    expect(secondSignal.aborted).toBe(true)
  })
})
