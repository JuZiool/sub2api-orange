import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import TokenRankingView from '../TokenRankingView.vue'

const { getTokenRanking } = vi.hoisted(() => ({
  getTokenRanking: vi.fn(),
}))

vi.mock('@/api/usage', () => ({
  getTokenRanking,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values?: Record<string, string>) => {
        if (values) return `${key}:${values.start}-${values.end}`
        return key
      },
    }),
  }
})

const response = () => ({
  weekly: {
    start_date: '2026-09-07',
    end_date: '2026-09-10',
    items: [
      { rank: 1, user_id: 7, email: 'abc***ef@example.com', requests: 3, input_tokens: 100, output_tokens: 200, cache_tokens: 30, total_tokens: 330 },
      { rank: 2, user_id: 8, email: 'x***y@example.com', requests: 2, input_tokens: 80, output_tokens: 100, cache_tokens: 20, total_tokens: 200 },
    ],
  },
  daily: {
    date: '2026-09-10',
    start_date: '2026-09-10',
    end_date: '2026-09-10',
    items: [
      { rank: 4, user_id: 7, email: 'abc***ef@example.com', requests: 1, input_tokens: 50, output_tokens: 100, cache_tokens: 10, total_tokens: 160 },
    ],
  },
})

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  Icon: true,
  LoadingSpinner: { template: '<div data-testid="loading" />' },
  EmptyState: { props: ['title'], template: '<div data-testid="empty">{{ title }}</div>' },
}

describe('TokenRankingView', () => {
  beforeEach(() => {
    getTokenRanking.mockReset()
    vi.stubGlobal('Intl', {
      DateTimeFormat: () => ({ resolvedOptions: () => ({ timeZone: 'Asia/Shanghai' }) }),
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('loads and renders weekly and daily rankings', async () => {
    getTokenRanking.mockResolvedValueOnce(response())
    const wrapper = mount(TokenRankingView, { global: { stubs } })
    await flushPromises()

    expect(getTokenRanking).toHaveBeenCalledWith({ timezone: 'Asia/Shanghai' })
    expect(wrapper.text()).toContain('abc***ef@example.com')
    expect(wrapper.text()).toContain('330')
    expect(wrapper.text()).toContain('160')
    expect(wrapper.text()).not.toContain('user_id')
    expect(wrapper.text()).toContain('4')
  })

  it('renders empty states for both rankings', async () => {
    getTokenRanking.mockResolvedValueOnce({
      weekly: { start_date: '', end_date: '', items: [] },
      daily: { date: '', start_date: '', end_date: '', items: [] },
    })
    const wrapper = mount(TokenRankingView, { global: { stubs } })
    await flushPromises()

    expect(wrapper.findAll('[data-testid="empty"]')).toHaveLength(2)
  })

  it('renders an error when the request fails', async () => {
    getTokenRanking.mockRejectedValueOnce(new Error('network'))
    const wrapper = mount(TokenRankingView, { global: { stubs } })
    await flushPromises()

    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('tokenRanking.failedToLoad')
  })

  it('shows loading state while the request is pending', async () => {
    getTokenRanking.mockReturnValueOnce(new Promise(() => undefined))
    const wrapper = mount(TokenRankingView, { global: { stubs } })
    await Promise.resolve()

    expect(wrapper.find('[data-testid="loading"]').exists()).toBe(true)
  })
})
