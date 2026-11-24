/**
 * Orange 特有：使用记录（UsageTable）的 Orange 展示层。
 *
 * 上游 `components/admin/usage/UsageTable.vue` 只保留挂载点：
 * - `UsageCacheHitRate`        —— TOKEN 列缓存命中率（命中率文案 + 档位着色）
 * - `UsageLongContextMarker`   —— 费用列「长上下文」标记（改到 A$ 行下方独立一行）
 * - `ORANGE_INLINE_LONG_CONTEXT_MARKER` —— 关闭上游内联位（保持标记只有一处、位置唯一）
 */
export { default as UsageCacheHitRate } from './UsageCacheHitRate.vue'
export { default as UsageLongContextMarker } from './UsageLongContextMarker.vue'
export { ORANGE_INLINE_LONG_CONTEXT_MARKER } from './longContextMarker'
export { CACHE_HIT_RATE_CLASSES, cacheHitRateClass, cacheHitRatePercent } from './cacheHitRate'
