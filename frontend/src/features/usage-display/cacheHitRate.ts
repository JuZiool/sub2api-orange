/**
 * Orange 特有：使用记录「缓存命中率」的计算与着色。
 *
 * 命中率 = 缓存读取 ÷（输入 + 缓存创建 + 缓存读取）；分母为 0（历史行、图片行）时返回 null，
 * 展示层回落为「-」，避免出现 NaN。
 *
 * 着色档位：<50% 红 / 50–80% 黄 / 80–90% 浅绿 / ≥90% 深绿，命中越高越绿，便于纵向扫视。
 */
import type { AdminUsageLog } from '@/types'

/** 命中率档位对应的 Tailwind 文本色（深浅色主题各一套）。 */
export const CACHE_HIT_RATE_CLASSES = {
  low: 'text-rose-600 dark:text-rose-400',
  medium: 'text-amber-600 dark:text-amber-400',
  good: 'text-emerald-500 dark:text-emerald-400',
  high: 'text-green-600 dark:text-green-400',
} as const

/** 计算缓存命中率百分比；分母为 0 时返回 null（展示为 -）。 */
export const cacheHitRatePercent = (row: AdminUsageLog): number | null => {
  const cacheRead = Number(row.cache_read_tokens ?? 0) || 0
  const cacheCreation = Number(row.cache_creation_tokens ?? 0) || 0
  const input = Number(row.input_tokens ?? 0) || 0
  const total = input + cacheCreation + cacheRead
  if (total <= 0) return null
  return (cacheRead / total) * 100
}

/** 按档位返回命中率文本色。 */
export const cacheHitRateClass = (rate: number): string => {
  if (rate < 50) return CACHE_HIT_RATE_CLASSES.low
  if (rate < 80) return CACHE_HIT_RATE_CLASSES.medium
  if (rate < 90) return CACHE_HIT_RATE_CLASSES.good
  return CACHE_HIT_RATE_CLASSES.high
}
