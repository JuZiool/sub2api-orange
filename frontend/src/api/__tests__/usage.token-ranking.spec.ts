import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import { getTokenRanking } from '../usage'

vi.mock('../client', () => ({
  apiClient: {
    get: vi.fn(),
  },
}))

describe('getTokenRanking', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('requests the ranking endpoint with timezone', async () => {
    vi.mocked(apiClient.get).mockResolvedValueOnce({ data: { weekly: { items: [] }, daily: { items: [] } } })

    await getTokenRanking({ timezone: 'Asia/Shanghai' })

    expect(apiClient.get).toHaveBeenCalledWith('/usage/ranking', {
      params: { timezone: 'Asia/Shanghai' },
    })
  })

  it('supports requests without parameters', async () => {
    vi.mocked(apiClient.get).mockResolvedValueOnce({ data: { weekly: { items: [] }, daily: { items: [] } } })

    await getTokenRanking()

    expect(apiClient.get).toHaveBeenCalledWith('/usage/ranking', { params: undefined })
  })
})
