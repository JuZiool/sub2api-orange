/**
 * Orange 特有：Vue Router meta 声明合并。
 *
 * 刻意新增独立声明文件，不改上游 `router/meta.d.ts`（避免与上游合并冲突）。
 */

import 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    /**
     * 是否要求 Orange「Token 排行榜」展示开关处于开启状态
     * （由 `router/orangeTokenRankingGuard.ts` 校验，关闭时重定向到仪表盘）
     * @default false
     */
    requiresOrangeTokenRanking?: boolean
  }
}
