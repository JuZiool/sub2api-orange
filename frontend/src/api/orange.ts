/**
 * Orange 特有：Orange 自有接口封装。
 *
 * 与上游 `api/usage.ts` / `api/admin/settings.ts` 完全隔离，不改动任何上游文件：
 * - GET  /orange/features            公开开关快照（无需登录，仅布尔值）
 * - GET  /usage/ranking              登录用户用量排行（用户榜 / 模型榜，分页）
 * - GET  /admin/orange/features      管理员读取开关真实值
 * - PUT  /admin/orange/features      管理员局部更新开关（白名单键）
 *
 * 开关数据落在 Orange 自建的 `orange_settings` 表，不写上游 `settings` 表。
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
