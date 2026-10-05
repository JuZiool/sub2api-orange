import type { UsageLog } from '@/types'
import { BILLING_MODE_IMAGE, BILLING_MODE_VIDEO, getDisplayBillingMode, isImageUsage } from '@/utils/billingMode'
import { textOutputTokens } from '@/utils/imageUsage'
import { resolveUsageRequestType } from '@/utils/usageRequestType'

type UsageTpsRow = Pick<UsageLog, 'output_tokens' | 'duration_ms' | 'first_token_ms'> &
  Partial<Pick<UsageLog, 'request_type' | 'stream' | 'openai_ws_mode' | 'billing_mode' | 'image_count' | 'image_output_tokens'>>

/** Orange 特有：估算首字后的文字输出 TPS，不混用包含首字等待的全程口径。 */
export function formatUsageTps(row: UsageTpsRow): string {
  const requestType = resolveUsageRequestType(row)
  if (requestType === 'sync' || requestType === 'live' ||
      !(requestType === 'stream' || requestType === 'ws_v2' || row.stream || row.openai_ws_mode)) {
    return '-'
  }

  const mediaRow = { image_count: row.image_count ?? 0, billing_mode: row.billing_mode }
  const billingMode = getDisplayBillingMode(mediaRow)
  if (isImageUsage(mediaRow) || billingMode === BILLING_MODE_IMAGE || billingMode === BILLING_MODE_VIDEO) {
    return '-'
  }

  const { output_tokens: outputTokens, duration_ms: durationMs, first_token_ms: firstTokenMs } = row
  const imageTokens = row.image_output_tokens ?? 0
  if (durationMs == null || firstTokenMs == null ||
      ![outputTokens, imageTokens, durationMs, firstTokenMs].every(Number.isFinite) ||
      outputTokens <= 0 || imageTokens < 0 || firstTokenMs < 0 || durationMs <= firstTokenMs) {
    return '-'
  }

  const tokens = textOutputTokens({ output_tokens: outputTokens, image_output_tokens: imageTokens })
  const tps = tokens * 1000 / (durationMs - firstTokenMs)
  return Number.isFinite(tps) && tps > 0 ? `${tps.toFixed(1)} t/s` : '-'
}
