import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountCapacityCell from '../AccountCapacityCell.vue'
import CapacityBadge from '../CapacityBadge.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => `${key}:${params?.count ?? ''}` })
  }
})

const baseAccount = {
  id: 1,
  name: 'proxy-pool-account',
  platform: 'openai',
  type: 'oauth',
  status: 'active',
  concurrency: 10,
  proxy_ids: [11, 12],
  current_concurrency: 19
} as any

describe('AccountCapacityCell', () => {
  it('shows pool total and each proxy concurrency separately', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          ...baseAccount,
          proxy_pool: [
            { proxy_id: 11, proxy_name: 'proxy-a', current_concurrency: 10, max_concurrency: 10 },
            { proxy_id: 12, proxy_name: 'proxy-b', current_concurrency: 9, max_concurrency: 10 }
          ]
        }
      }
    })

    const badges = wrapper.findAllComponents(CapacityBadge)
    expect(badges).toHaveLength(3)
    expect(badges.map(badge => [badge.props('current'), badge.props('max')])).toEqual([
      [19, 20],
      [10, 10],
      [9, 10]
    ])
    expect(badges[0].props('tooltip')).toContain('admin.accounts.capacity.proxyPoolTotal:2')
    expect(badges[1].props('tooltip')).toBe('proxy-a')
    expect(badges[2].props('tooltip')).toBe('proxy-b')
  })

  it('keeps the account-level badge for a single proxy', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          ...baseAccount,
          proxy_ids: [11],
          current_concurrency: 3,
          proxy_pool: [{ proxy_id: 11, proxy_name: 'proxy-a', current_concurrency: 3, max_concurrency: 10 }]
        }
      }
    })

    const badges = wrapper.findAllComponents(CapacityBadge)
    expect(badges).toHaveLength(1)
    expect(badges[0].props('current')).toBe(3)
    expect(badges[0].props('max')).toBe(10)
  })
})
