import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get },
}))

import { getUsageSummary } from '@/api/admin/groups'

describe('admin group usage summary API', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({ data: [] })
  })

  it('does not send browser timezone parameters', async () => {
    const summary = [
      {
        group_id: 1,
        today_cost: 1.25,
        yesterday_cost: 2.5,
        total_cost: 9.75,
        today_upstream_cost: 0.75,
        yesterday_upstream_cost: 1.5,
        total_upstream_cost: 6.25,
        today_profit: 0.5,
        yesterday_profit: 1,
        total_profit: 3.5,
      },
    ]
    get.mockResolvedValue({ data: summary })

    await expect(getUsageSummary()).resolves.toEqual(summary)

    expect(get).toHaveBeenCalledWith('/admin/groups/usage-summary')
  })
})
