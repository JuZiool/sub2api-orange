/**
 * Orange 特有：侧栏 Orange 菜单项集中定义。
 *
 * 上游 `components/layout/AppSidebar.vue` 只保留三处接线：一次 `useOrangeSidebarMenu()`
 * 与三处展开/推入（用户端「使用记录」之后、管理端「系统设置」之后）。
 * 菜单顺序、图标、开关与文案都在本文件维护，因此上游合并时冲突面只有挂载行。
 *
 * - Token 排行榜：开关来自 Orange 自有接口 `GET /orange/features`，不在上游 PublicSettings 里，
 *   因此不走 `utils/featureFlags.ts` 注册表；`undefined/true` 视为显示（fail-open）。
 * - 费用计算器：纯前端页面，仅管理员菜单出现。
 * 未启用 Orange 开关时 `applyFeatureFlags` 会隐藏对应菜单项，其余上游菜单项行为完全一致。
 */
import { h } from 'vue'
import { Icon } from '@/components/icons'
import { ORANGE_FEATURE_KEYS, useOrangeFeaturesStore } from '@/stores/orangeFeatures'

/** 上游 AppSidebar 的 NavItem 未导出，这里按其结构声明 Orange 菜单项（可直接展开进 NavItem[]）。 */
export interface OrangeNavItem {
  path: string
  label: string
  icon: unknown
  hideInSimpleMode?: boolean
  featureFlag?: () => boolean | undefined
}

/** 与上游 `utils/imageUsage.ts` 等文件一致的翻译函数签名。 */
type Translate = (key: string) => string

/** 费用计算器入口图标（复用 Icon 组件内置的 calculator 字形）。 */
export const OrangeCalculatorIcon = { render: () => h(Icon, { name: 'calculator' }) }

/**
 * 侧栏 Orange 菜单装配。
 * 必须在组件 setup 中调用一次：内部会预取 Orange 开关（失败静默，默认开启）。
 */
export function useOrangeSidebarMenu() {
  const features = useOrangeFeaturesStore()
  // 首次渲染即预取；失败静默（默认开启），不阻塞侧栏渲染。
  void features.ensureLoaded()

  const tokenRankingVisible = (): boolean =>
    features.isEnabled(ORANGE_FEATURE_KEYS.tokenRankingEnabled)

  return {
    /**
     * 用户端菜单项（挂在「使用记录」之后、「可用渠道」之前）。
     * icon 由调用方传入，保持与上游「使用记录」同一图标实例。
     */
    selfNavItems: (t: Translate, icon: unknown): OrangeNavItem[] => [
      {
        path: '/token-ranking',
        label: t('orangeNav.tokenRanking'),
        icon,
        hideInSimpleMode: true,
        featureFlag: tokenRankingVisible
      }
    ],
    /** 管理员菜单项（挂在「系统设置」之后、自定义菜单之前）。 */
    adminNavItems: (t: Translate): OrangeNavItem[] => [
      {
        path: '/admin/fee-calculator',
        label: t('rateCalculator.navLabel'),
        icon: OrangeCalculatorIcon
      }
    ]
  }
}
