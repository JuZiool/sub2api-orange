import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import CodexOverdraftPanel from '../CodexOverdraftPanel.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('CodexOverdraftPanel', () => {
  const fiveHourStats = { requests: 3, tokens: 3400, cost: 1.25 }
  const sevenDayStats = { requests: 8, tokens: 40000, cost: 2.5 }

  it('shows one compact row with a window prefix for each active period', () => {
    const wrapper = mount(CodexOverdraftPanel, {
      props: {
        fiveHourActive: true,
        fiveHourStats,
        sevenDayActive: true,
        sevenDayStats,
        status: '透支中',
        statusClass: 'text-amber-600 dark:text-amber-400'
      }
    })

    const rows = wrapper.findAll('[data-testid="overdraft-stats"]')
    expect(rows).toHaveLength(1)
    expect(rows[0].get('[data-window="5h"]').text()).toBe('5h透支中')
    expect(rows[0].get('[data-window="7d"]').text()).toBe('7d透支中')
    expect(rows[0].text()).toContain('3 req')
    expect(rows[0].text()).toContain('3.4K')
    expect(rows[0].text()).toContain('A $1.25')
    expect(rows[0].text()).toContain('8 req')
    expect(rows[0].text()).toContain('40.0K')
    expect(rows[0].text()).toContain('A $2.50')
    expect(rows[0].get('[data-window="5h"]').classes()).toContain('text-amber-600')
  })

  it('shows only the active window and keeps its own prefix', () => {
    const wrapper = mount(CodexOverdraftPanel, {
      props: {
        fiveHourActive: false,
        fiveHourStats,
        sevenDayActive: true,
        sevenDayStats,
        status: '透支中'
      }
    })

    expect(wrapper.get('[data-testid="overdraft-stats"] [data-window="7d"]').text()).toBe('7d透支中')
    expect(wrapper.find('[data-window="5h"]').exists()).toBe(false)
  })

  it('hides when an active window has no stats', () => {
    const wrapper = mount(CodexOverdraftPanel, {
      props: { fiveHourActive: true, fiveHourStats: null, sevenDayActive: false, sevenDayStats }
    })
    expect(wrapper.find('[data-testid="overdraft-stats"]').exists()).toBe(false)
  })
})
