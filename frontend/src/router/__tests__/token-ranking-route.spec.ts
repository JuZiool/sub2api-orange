import { describe, expect, it } from 'vitest'
import router from '@/router'


describe('token ranking route', () => {
  it('is an authenticated non-admin user route', () => {
    const route = router.getRoutes().find((record) => record.name === 'TokenRanking')

    expect(route?.path).toBe('/token-ranking')
    expect(route?.meta.requiresAuth).toBe(true)
    expect(route?.meta.requiresAdmin).toBe(false)
  })
})
