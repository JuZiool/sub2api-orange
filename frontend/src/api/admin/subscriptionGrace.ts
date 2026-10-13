import { apiClient } from '../client'

export interface OpenAISubscriptionGrace {
  active_until: string
  became_delinquent_timestamp: string
  grace_period_end_timestamp: string
}

// Orange 特有：列表自动刷新和虚拟滚动共享查询，成功缓存五分钟，失败缓存三十秒。
type CacheEntry = { until: number; pending: boolean; result: Promise<OpenAISubscriptionGrace> }
const cache = new Map<string, CacheEntry>()
let authScope: string | null = null

export function getSubscriptionGrace(id: number, credentialVersion: string, force = false): Promise<OpenAISubscriptionGrace> {
  const scope = localStorage.getItem('auth_token')
  if (scope !== authScope) {
    cache.clear()
    authScope = scope
  }
  const key = `${id}:${credentialVersion}`
  const now = Date.now()
  const cached = cache.get(key)
  if (cached && (cached.pending || (!force && cached.until > now))) return cached.result
  for (const [entryKey, entry] of cache) {
    if (!entry.pending && entry.until <= now) cache.delete(entryKey)
  }
  const entry: CacheEntry = {
    until: 0,
    pending: true,
    result: apiClient.get<OpenAISubscriptionGrace>(`/admin/accounts/${id}/subscription-grace`)
      .then(({ data }) => {
        entry.pending = false
        entry.until = (Math.floor(Date.now() / 300_000) + 1) * 300_000
        return data
      })
      .catch((error: unknown) => {
        entry.pending = false
        entry.until = Date.now() + 30_000
        throw error
      })
  }
  cache.set(key, entry)
  return entry.result
}
