/**
 * Orange 特有：Token 排行榜路由守卫。
 *
 * 只在目标路由声明 `meta.requiresOrangeTokenRanking === true` 时生效；
 * 其它路由立即 `next()`，既不触碰 store 也不发任何网络请求（避免影响
 * `router/__tests__/guards.spec.ts` 等导入真实 router 的测试）。
 *
 * 安装点在 `router/index.ts` 模块体（早于 `app.use(router)` 触发的首次导航）。
 */

import type { Router } from 'vue-router'
import { ORANGE_FEATURE_KEYS, useOrangeFeaturesStore } from '@/stores/orangeFeatures'
import { useAuthStore } from '@/stores/auth'

export function installOrangeTokenRankingGuard(router: Router): void {
  router.beforeEach(async (to, _from, next) => {
    if (!to.meta.requiresOrangeTokenRanking) {
      next()
      return
    }

    const features = useOrangeFeaturesStore()
    try {
      await features.ensureLoaded()
    } catch {
      // 拉取失败按默认值处理：放行，由后端 403 兜底。
    }

    if (features.isEnabled(ORANGE_FEATURE_KEYS.tokenRankingEnabled)) {
      next()
      return
    }

    const auth = useAuthStore()
    next(auth.isAdmin ? '/admin/dashboard' : '/dashboard')
  })
}
