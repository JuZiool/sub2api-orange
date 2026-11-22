/**
 * Orange 特有：Orange 自有接口封装。
 *
 * 与上游 `api/usage.ts` / `api/admin/settings.ts` 完全隔离，不改动任何上游文件：
 * - GET  /orange/features            公开开关快照（无需登录，仅布尔值）
 * - GET  /usage/ranking              登录用户用量排行（用户榜 / 模型榜，分页）
 * - GET  /admin/orange/features      管理员读取开关真实值
 * - PUT  /admin/orange/features      管理员局部更新开关（白名单键）
 * - GET  /admin/orange/groups/profit 管理员读取分组利润与上游成本倍率（Orange 特有）
 * - PUT  /admin/orange/groups/:id/upstream-rate 管理员写分组上游成本倍率（Orange 特有）
 *
 * 开关数据落在 Orange 自建的 `orange_settings` 表，不写上游 `settings` 表；
 * 分组上游成本倍率落在 Orange 自建的 `orange_group_upstream_rate` 表，不写上游 `groups` 表。
 */

import { apiClient } from './client'

// ==================== 功能开关 ====================

/** 开关快照：键为开关键名（如 token_ranking_enabled），值为布尔。 */
export type OrangeFeatureValues = Record<string, boolean>

/** GET /admin/orange/features 响应（含最后一次更新时间）。 */
export interface OrangeAdminFeaturesResponse {
  items: OrangeFeatureValues
  updated_at?: string
}

/** 只保留布尔值，忽略后端将来可能追加的非布尔字段。 */
function toBooleanMap(value: unknown): OrangeFeatureValues {
  const out: OrangeFeatureValues = {}
  if (!value || typeof value !== 'object' || Array.isArray(value)) return out
  for (const [key, raw] of Object.entries(value as Record<string, unknown>)) {
    if (typeof raw === 'boolean') out[key] = raw
  }
  return out
}

/**
 * 兼容两种响应形状：扁平的 `{ key: boolean }` 与包一层的 `{ items: { key: boolean }, updated_at }`，
 * 避免后端字段包装方式微调后前端开关静默失效。
 */
function normalizeFeaturePayload(value: unknown): OrangeAdminFeaturesResponse {
  if (!value || typeof value !== 'object') return { items: {} }
  const record = value as Record<string, unknown>
  const updatedAt = typeof record.updated_at === 'string' ? record.updated_at : undefined
  if (record.items && typeof record.items === 'object' && !Array.isArray(record.items)) {
    return { items: toBooleanMap(record.items), updated_at: updatedAt }
  }
  return { items: toBooleanMap(record), updated_at: updatedAt }
}

/** 公开开关快照：GET /orange/features。 */
export async function getOrangeFeatures(): Promise<OrangeFeatureValues> {
  const { data } = await apiClient.get<OrangeFeatureValues>('/orange/features')
  return normalizeFeaturePayload(data).items
}

/** 管理端读取开关真实值：GET /admin/orange/features。 */
export async function getAdminOrangeFeatures(): Promise<OrangeAdminFeaturesResponse> {
  const { data } = await apiClient.get<OrangeAdminFeaturesResponse>('/admin/orange/features')
  return normalizeFeaturePayload(data)
}

/** 管理端局部更新开关：PUT /admin/orange/features（后端仅接受白名单键）。 */
export async function updateAdminOrangeFeatures(
  patch: OrangeFeatureValues
): Promise<OrangeAdminFeaturesResponse> {
  const { data } = await apiClient.put<OrangeAdminFeaturesResponse>('/admin/orange/features', patch)
  const normalized = normalizeFeaturePayload(data)
  // PUT 后端若只回显被更新的键，则用请求值兜底合并，保证界面状态与服务端一致。
  return Object.keys(normalized.items).length
    ? normalized
    : { items: { ...patch }, updated_at: normalized.updated_at }
}

// ==================== 用量排行 ====================

/** 排行维度：用户榜 / 模型榜。 */
export type OrangeRankingDimension = 'user' | 'model'

/** 排行时间范围：今日 / 本周（周一至今天） / 本月（1 号至今天）。 */
export type OrangeRankingRange = 'today' | 'week' | 'month'

/** GET /usage/ranking 查询参数（timezone 由 apiClient 拦截器按浏览器时区自动附加）。 */
export interface OrangeRankingParams {
  dimension?: OrangeRankingDimension
  range?: OrangeRankingRange
  limit?: number
  offset?: number
  timezone?: string
}

/**
 * 排行条目。
 * - dimension=user 时使用 user_id / email；
 * - dimension=model 时使用 model。
 */
export interface OrangeRankingItem {
  rank: number
  user_id?: number
  email?: string
  model?: string
  requests: number
  input_tokens: number
  output_tokens: number
  cache_tokens: number
  total_tokens: number
}

/** GET /usage/ranking 响应。 */
export interface OrangeRankingResponse {
  dimension: OrangeRankingDimension
  range: OrangeRankingRange
  timezone?: string
  /** 展示用自然区间（今日/本周/本月）起始日期 YYYY-MM-DD。 */
  period_start: string
  /** 展示用自然区间结束日期 YYYY-MM-DD（周榜为本周日、月榜为月末）。 */
  period_end: string
  /** 真实查询上界（排他）YYYY-MM-DD，与 period_end 可能不同。 */
  window_end?: string
  generated_at?: string
  cached?: boolean
  has_more: boolean
  items: OrangeRankingItem[]
}

/** 排行查询：GET /usage/ranking（需登录；开关关闭时后端返回 403）。 */
export async function getUsageRanking(params: OrangeRankingParams = {}): Promise<OrangeRankingResponse> {
  const { data } = await apiClient.get<OrangeRankingResponse>('/usage/ranking', {
    params: {
      dimension: params.dimension,
      range: params.range,
      limit: params.limit,
      offset: params.offset,
      ...(params.timezone ? { timezone: params.timezone } : {})
    }
  })
  return {
    dimension: data?.dimension ?? params.dimension ?? 'user',
    range: data?.range ?? params.range ?? 'today',
    timezone: data?.timezone,
    period_start: data?.period_start ?? '',
    period_end: data?.period_end ?? '',
    window_end: data?.window_end,
    generated_at: data?.generated_at,
    cached: data?.cached,
    has_more: data?.has_more === true,
    items: Array.isArray(data?.items) ? data.items : []
  }
}

// ==================== 分组利润与上游成本倍率（Orange 特有） ====================

/**
 * Orange 特有：分组利润条目（GET /admin/orange/groups/profit）。
 *
 * 沿用旧 Orange 统计口径：
 * - revenue 实付收入 = SUM(usage_logs.actual_cost)
 * - upstream_price 上游原价 = SUM(usage_logs.total_cost)
 * - upstream_cost 上游成本 = upstream_price * upstream_rate_multiplier
 * - profit 利润 = revenue - upstream_cost
 * 今天/昨天按用户时区自然日，累计取全部历史。
 */
export interface OrangeGroupProfitItem {
  group_id: number
  /** 上游成本倍率；无倍率记录时后端返回 1.0。 */
  upstream_rate_multiplier: number
  today_revenue: number
  today_upstream_price: number
  today_upstream_cost: number
  today_profit: number
  yesterday_revenue: number
  yesterday_upstream_price: number
  yesterday_upstream_cost: number
  yesterday_profit: number
  total_revenue: number
  total_upstream_price: number
  total_upstream_cost: number
  total_profit: number
}

/** Orange 特有：GET /admin/orange/groups/profit 响应（含进程内缓存命中标记）。 */
export interface OrangeGroupProfitResponse {
  items: OrangeGroupProfitItem[]
  generated_at: string
  cached: boolean
}

/** Orange 特有：PUT /admin/orange/groups/:id/upstream-rate 响应。 */
export interface OrangeGroupUpstreamRateResponse {
  group_id: number
  upstream_rate_multiplier: number
}

/** Orange 特有：利润条目中的数值字段（缺失/非数字统一按 0 处理，保证界面不出现 NaN）。 */
const ORANGE_GROUP_PROFIT_NUMERIC_FIELDS = [
  'today_revenue',
  'today_upstream_price',
  'today_upstream_cost',
  'today_profit',
  'yesterday_revenue',
  'yesterday_upstream_price',
  'yesterday_upstream_cost',
  'yesterday_profit',
  'total_revenue',
  'total_upstream_price',
  'total_upstream_cost',
  'total_profit'
] as const

type OrangeGroupProfitNumericField =
  (typeof ORANGE_GROUP_PROFIT_NUMERIC_FIELDS)[number]

/** Orange 特有：安全取数，非有限数字回退到指定值。 */
function toFiniteNumber(value: unknown, fallback = 0): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? numeric : fallback
}

/** Orange 特有：浏览器时区，取不到时回退 UTC（与 apiClient 拦截器口径一致）。 */
function getBrowserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** Orange 特有：容错解析单条利润数据，group_id 非法时丢弃该条。 */
function normalizeGroupProfitItem(raw: unknown): OrangeGroupProfitItem | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const record = raw as Record<string, unknown>
  const groupId = Number(record.group_id)
  if (!Number.isFinite(groupId)) return null

  const item: OrangeGroupProfitItem = {
    group_id: groupId,
    // 无倍率记录时按 1.0 展示，避免界面显示 0 造成误解
    upstream_rate_multiplier: toFiniteNumber(record.upstream_rate_multiplier, 1),
    today_revenue: 0,
    today_upstream_price: 0,
    today_upstream_cost: 0,
    today_profit: 0,
    yesterday_revenue: 0,
    yesterday_upstream_price: 0,
    yesterday_upstream_cost: 0,
    yesterday_profit: 0,
    total_revenue: 0,
    total_upstream_price: 0,
    total_upstream_cost: 0,
    total_profit: 0
  }
  for (const field of ORANGE_GROUP_PROFIT_NUMERIC_FIELDS) {
    item[field as OrangeGroupProfitNumericField] = toFiniteNumber(record[field])
  }
  return item
}

/**
 * Orange 特有：分组利润查询（管理员）：GET /admin/orange/groups/profit。
 *
 * - `ids` 省略时后端返回全部分组（最多 200 个）；为空数组时按省略处理。
 * - `ids` 会先排序再以逗号拼接，便于命中后端 60s 进程内缓存。
 * - `timezone` 缺省取浏览器时区（apiClient 拦截器也会补同一个值）。
 */
export async function getOrangeGroupProfits(
  params: { ids?: number[]; timezone?: string } = {}
): Promise<OrangeGroupProfitResponse> {
  const ids = (params.ids ?? []).filter((id) => Number.isFinite(id))
  // 按 unknown 接收响应后自行归一化：后端字段微调或接口未就绪时前端不会出现类型错误
  const { data } = await apiClient.get<unknown>('/admin/orange/groups/profit', {
    params: {
      ...(ids.length > 0
        ? { ids: [...ids].sort((a, b) => a - b).join(',') }
        : {}),
      timezone: params.timezone ?? getBrowserTimezone()
    }
  })
  const record = (
    data && typeof data === 'object' ? data : {}
  ) as Record<string, unknown>
  const rawItems = Array.isArray(record.items) ? record.items : []
  const items: OrangeGroupProfitItem[] = []
  for (const raw of rawItems) {
    const item = normalizeGroupProfitItem(raw)
    if (item) items.push(item)
  }
  return {
    items,
    generated_at: typeof record.generated_at === 'string' ? record.generated_at : '',
    cached: record.cached === true
  }
}

/**
 * Orange 特有：写分组上游成本倍率（管理员）：PUT /admin/orange/groups/:id/upstream-rate。
 *
 * 只写 Orange 自建的 `orange_group_upstream_rate` 表，不写上游 `groups` 表；
 * 后端写库后会失效 60s 利润缓存。取值为 0 ~ 1000，最多 4 位小数（后端校验）。
 */
export async function updateOrangeGroupUpstreamRate(
  groupId: number,
  upstreamRateMultiplier: number
): Promise<OrangeGroupUpstreamRateResponse> {
  const { data } = await apiClient.put<unknown>(
    `/admin/orange/groups/${groupId}/upstream-rate`,
    { upstream_rate_multiplier: upstreamRateMultiplier }
  )
  const record = (
    data && typeof data === 'object' ? data : {}
  ) as Record<string, unknown>
  return {
    group_id: toFiniteNumber(record.group_id, groupId),
    upstream_rate_multiplier: toFiniteNumber(
      record.upstream_rate_multiplier,
      upstreamRateMultiplier
    )
  }
}
