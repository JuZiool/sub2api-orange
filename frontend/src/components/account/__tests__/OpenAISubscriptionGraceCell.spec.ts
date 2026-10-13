import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { Account } from '@/types'
import OpenAISubscriptionGraceCell from '../OpenAISubscriptionGraceCell.vue'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, values?: { time: string }) => `${key}${values ? ` ${values.time}` : ''}` })
}))

const response = {
  active_until: '2026-10-12T00:00:00Z',
  became_delinquent_timestamp: '2026-10-12T00:00:00Z',
  grace_period_end_timestamp: '2026-10-18T00:30:00Z'
}
let scope = 0
let wrappers: VueWrapper[] = []
function account(overrides: Partial<Account> = {}): Account {
  return {
    id: 42, platform: 'openai', type: 'oauth',
    credentials: { plan_type: 'plus', subscription_expires_at: '2026-10-12T00:00:00Z', expires_at: '2026-10-14T00:00:00Z' },
    ...overrides
  } as Account
}
function render(row = account()) {
  const wrapper = mount(OpenAISubscriptionGraceCell, { props: { account: row } })
  wrappers.push(wrapper)
  return wrapper
}

describe('OpenAI Plus subscription grace in the account column', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    vi.setSystemTime(new Date('2026-10-13T12:00:00Z'))
    localStorage.setItem('auth_token', `grace-test-${++scope}`)
    get.mockReset().mockResolvedValue({ data: response })
  })
  afterEach(() => {
    wrappers.forEach(wrapper => wrapper.unmount())
    wrappers = []
    vi.useRealTimers()
  })

  it.each([
    { credentials: { plan_type: 'plus', subscription_expires_at: '2026-10-26T00:00:00Z' } },
    { credentials: { plan_type: 'plus', subscription_expires_at: 'bad-date' } },
    { credentials: { plan_type: 'free', subscription_expires_at: '2026-10-12T00:00:00Z' } },
    { credentials: {} },
    { platform: 'anthropic' },
    { type: 'apikey' },
    { parent_account_id: 1 }
  ] as Partial<Account>[])('hides and never queries an ineligible account: %j', async (overrides) => {
    const wrapper = render(account(overrides))
    await flushPromises()
    expect(wrapper.text()).toBe('')
    expect(get).not.toHaveBeenCalled()
  })

  it('automatically displays the returned grace end and keeps the start in the tooltip', async () => {
    const wrapper = render()
    expect(wrapper.text()).toContain('subscriptionGraceLoading')
    await flushPromises()
    expect(get).toHaveBeenCalledWith('/admin/accounts/42/subscription-grace')
    expect(wrapper.text()).toContain('subscriptionGraceUntil')
    expect(wrapper.text()).toContain('2026')
    expect(wrapper.text()).toContain('18')
    expect(wrapper.get('span').attributes('title')).toContain('12')
    expect(wrapper.get('span').attributes('title')).toContain('18')
  })

  it('shares in-flight requests and cached results across list refreshes and remounts', async () => {
    const first = render()
    render()
    await flushPromises()
    await first.setProps({ account: account({ name: 'refreshed row' }) })
    render()
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('rechecks after the five-minute cache period', async () => {
    render()
    await flushPromises()
    vi.advanceTimersByTime(300_000)
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('requeries when refreshed credential expiration changes', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.setProps({ account: account({ credentials: { ...account().credentials, expires_at: '2026-10-15T00:00:00Z' } }) })
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('rechecks on the next five-minute boundary when mounted between boundaries', async () => {
    vi.setSystemTime(new Date('2026-10-13T12:04:59Z'))
    render()
    await flushPromises()
    vi.advanceTimersByTime(60_000)
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('starts querying when a previously unexpired subscription expires', async () => {
    const wrapper = render(account({ credentials: { plan_type: 'plus', subscription_expires_at: '2026-10-13T12:00:30Z' } }))
    expect(get).not.toHaveBeenCalled()
    vi.advanceTimersByTime(60_000)
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('subscriptionGraceUntil')
  })

  it('marks a past grace end as ended', async () => {
    get.mockResolvedValue({ data: { ...response, grace_period_end_timestamp: '2026-10-13T00:00:00Z' } })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('subscriptionGraceEnded')
  })

  it('reports missing grace data without inventing a deadline', async () => {
    get.mockResolvedValue({ data: { ...response, grace_period_end_timestamp: '' } })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('subscriptionGraceMissing')
    expect(wrapper.text()).not.toContain('subscriptionGraceUntil')
  })

  it('shows failure and lets the user retry immediately', async () => {
    get.mockRejectedValueOnce(new Error('upstream unavailable'))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('subscriptionGraceFailed')
    render()
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(1)
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('subscriptionGraceUntil')
  })

  it('hides when the upstream reports a renewed subscription', async () => {
    get.mockResolvedValue({ data: { ...response, active_until: '2026-11-12T00:00:00Z' } })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toBe('')
  })

  it('discards a late response after the row becomes unexpired', async () => {
    let resolve!: (value: { data: typeof response }) => void
    get.mockReturnValue(new Promise<{ data: typeof response }>(done => { resolve = done }))
    const wrapper = render()
    await wrapper.setProps({ account: account({ credentials: { plan_type: 'plus', subscription_expires_at: '2026-11-12T00:00:00Z' } }) })
    resolve({ data: response })
    await flushPromises()
    expect(wrapper.text()).toBe('')
  })
})
