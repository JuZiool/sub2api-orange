/**
 * Orange 特有：Orange 路由的集中挂载点。
 *
 * 上游 `router/index.ts` 只保留两处接线：
 * 1. `...orangeRoutes` 展开（放在 404 兜底路由之前）；
 * 2. `installOrangeRouting(router)` 安装 Orange 守卫。
 *
 * 新增 Orange 页面时只改本文件，不再动上游路由表，因此与上游合并时的冲突面
 * 压缩到 2 行以内。未配置 Orange 功能（或后端返回关闭）时，这些路由与上游无关：
 * 不展开时上游路由表行为完全一致，守卫只对声明了 meta 的路由生效。
 */
import type { RouteRecordRaw, Router } from 'vue-router'
import { installOrangeTokenRankingGuard } from './orangeTokenRankingGuard'

/** Orange 自有路由（按需懒加载，不影响上游 route chunk 划分）。 */
export const orangeRoutes: RouteRecordRaw[] = [
  // Token 排行榜：受 orange_settings.token_ranking_enabled 展示开关控制
  {
    path: '/token-ranking',
    name: 'TokenRanking',
    component: () => import('@/views/user/TokenRankingView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
      requiresOrangeTokenRanking: true,
      title: 'Token Ranking',
      titleKey: 'orangeTokenRanking.title',
      descriptionKey: 'orangeTokenRanking.description'
    }
  },
  // 管理员费用计算器（纯前端计算，仅管理员可见）
  {
    path: '/admin/fee-calculator',
    name: 'AdminFeeCalculator',
    component: () => import('@/features/fee-calculator/FeeCalculatorView.vue'),
    meta: {
      requiresAuth: true,
      requiresAdmin: true,
      title: 'Fee Calculator',
      titleKey: 'rateCalculator.title',
      descriptionKey: 'rateCalculator.description'
    }
  }
]

/**
 * 安装 Orange 路由守卫。
 * 必须在 router/index.ts 模块体（早于上游全局守卫注册）调用：
 * Token 排行榜守卫只对声明了 `meta.requiresOrangeTokenRanking` 的路由做判断，其余路由直接放行。
 */
export function installOrangeRouting(router: Router): void {
  installOrangeTokenRankingGuard(router)
}
