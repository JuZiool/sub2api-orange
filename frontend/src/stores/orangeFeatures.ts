/**
 * Orange 特有：Orange 功能开关的前端缓存 store。
 *
 * - 数据源是 Orange 自有接口 `GET /orange/features`（公开、仅布尔值）。
 * - 模块导入时**零网络请求**：router 守卫与侧栏都只在需要时调用 `ensureLoaded()`，
 *   这样 `router/__tests__/guards.spec.ts` 之类导入真实 router 的测试不会被触发出网。
 * - 拉取失败按默认值处理（fail-open，仅用于 UI 可见性；数据接口仍由后端 403 兜底）。
 */

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getOrangeFeatures, type OrangeFeatureValues } from '@/api/orange'

/** Orange 开关键名常量（与后端白名单键保持一致）。 */
export const ORANGE_FEATURE_KEYS = {
  tokenRankingEnabled: 'token_ranking_enabled'
} as const

export type OrangeFeatureKey = (typeof ORANGE_FEATURE_KEYS)[keyof typeof ORANGE_FEATURE_KEYS]

/** 默认值表：缺行 / 拉取失败 / 未知键一律视为开启（与旧 Orange「无开关=始终可见」一致）。 */
export const ORANGE_FEATURE_DEFAULTS: OrangeFeatureValues = {
  [ORANGE_FEATURE_KEYS.tokenRankingEnabled]: true
}

export const useOrangeFeaturesStore = defineStore('orangeFeatures', () => {
  const values = ref<OrangeFeatureValues>({ ...ORANGE_FEATURE_DEFAULTS })
  const loaded = ref(false)
  const loading = ref(false)

  // 并发合并：侧栏与路由守卫可能同时调用 ensureLoaded()，只发一次请求。
  let pending: Promise<void> | null = null

  function isEnabled(key: OrangeFeatureKey): boolean {
    const value = values.value[key]
    return typeof value === 'boolean' ? value : ORANGE_FEATURE_DEFAULTS[key] !== false
  }

  function apply(patch: OrangeFeatureValues): void {
    values.value = { ...values.value, ...patch }
    loaded.value = true
  }

  async function ensureLoaded(force = false): Promise<void> {
    if (loaded.value && !force) return
    if (pending) return pending

    loading.value = true
    pending = (async () => {
      try {
        const snapshot = await getOrangeFeatures()
        values.value = { ...ORANGE_FEATURE_DEFAULTS, ...snapshot }
        loaded.value = true
      } catch (error) {
        // fail-open：保留默认值，不把瞬时失败当成「功能已关闭」。
        console.warn('[orangeFeatures] Failed to load Orange feature flags:', error)
      } finally {
        loading.value = false
        pending = null
      }
    })()

    return pending
  }

  return {
    values,
    loaded,
    loading,
    isEnabled,
    ensureLoaded,
    apply
  }
})
