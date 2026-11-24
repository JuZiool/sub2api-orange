/**
 * Orange 特有：密钥分组链（密钥级兜底分组 / 聚合分组）的类型定义。
 *
 * 这些字段由 Orange 后端在 `dto.APIKey` 上追加（groups / group_chain_mode），
 * 上游 `@/types` 的 ApiKey 不包含它们；为保持上游类型文件零改动，
 * 这里用交叉类型描述，业务代码只在 features/key-group-chain 内使用。
 */
import type { ApiKey, Group, GroupPlatform, SubscriptionType } from '@/types'

/** 分组链模式：none = 普通 Key，fallback = 单兜底（链长 2），aggregate = 聚合（链长 ≥3）。 */
export type ApiKeyGroupChainMode = 'none' | 'fallback' | 'aggregate'

/** Orange 特有：写链时的交互意图（链长为 2 时无法从链本身区分兜底与聚合）。 */
export type KeyGroupChainIntent = 'fallback' | 'aggregate'

/**
 * 分组链成员（链首 = api_keys.group_id，priority 从 1 起升序）。
 *
 * 后端在读取链（GET /keys/:id/group-chain）与列表接口里就已剔除用户无权绑定的成员，
 * 因此前端不需要任何可用性字段。
 */
export interface ApiKeyGroupEntry {
  group_id: number
  priority: number
  group?: Group
}

/** Orange 特有：带分组链字段的 API Key 视图。 */
export type KeyGroupChainKey = ApiKey & {
  /** 分组优先级链（含链首，按 priority 升序）。普通 Key 为空数组或缺省。 */
  groups?: ApiKeyGroupEntry[]
  /** 分组链意图模式（none=普通 Key，fallback=兜底分组，aggregate=聚合密钥）。 */
  group_chain_mode?: ApiKeyGroupChainMode
}

/**
 * GET/PUT /keys/:id/group-chain 的响应体。members[0] 恒为链首（主分组）。
 *
 * 打开编辑弹窗读取链时，后端会把用户已无权限的成员剔除、必要时把第一个可用成员提升为主分组，
 * 并把结果写回数据库；受影响的分组通过下面两个字段回传，前端只负责给一句提示。
 */
export interface ApiKeyGroupChain {
  api_key_id: number
  mode: ApiKeyGroupChainMode
  primary_group_id: number
  /** 已剔除无权限成员后的链成员，按 priority 升序。 */
  members: ApiKeyGroupEntry[]
  /** 本次自动移除的分组 id（无则为空数组）。 */
  auto_pruned_group_ids?: number[]
  /** 链首被自动替换后的新主分组 id（0 表示未变）。 */
  auto_promoted_group_id?: number
}

/**
 * 分组下拉选项（与上游 KeysView 内部 GroupOption 结构保持一致）。
 * 上游把该结构内联在视图里，这里复制一份类型定义便于组件间传递，不涉及运行逻辑。
 */
export type GroupOptionView = {
  value: number
  label: string
  description: string | null
  rate: number
  userRate: number | null
  peakRateEnabled: boolean
  peakStart: string
  peakEnd: string
  peakRateMultiplier: number
  subscriptionType: SubscriptionType
  platform: GroupPlatform
}

/** 兜底分组下拉的选项：首项为“不设置兜底分组”（value = null）。 */
export type FallbackGroupOption =
  | GroupOptionView
  | { value: null; label: string; description?: string }

/**
 * Orange 特有：分组下拉选项的最小形状。
 *
 * 只用于把后端回传的 group_id（自动移除 / 自动提升）解析成界面上的分组名，
 * 解析不到时前端回退成 `#id`。
 */
export interface GroupOptionLike {
  value: number
  label?: string
}

/** 分组链表单状态（useKeyGroupChain 的 `keyGroupChainForm`）。 */
export interface KeyGroupChainFormState {
  fallbackGroupId: number | null
  aggregateMode: boolean
  aggregateGroupIds: Array<number | null>
  /** 当前弹窗是否为编辑模式（决定提交成功后按 update 还是 create 回写链）。 */
  isEdit: boolean
  /** 当前编辑的密钥 id（关闭弹窗后上游会清空 selectedKey，这里提前记住）。 */
  editKeyId: number | null
}

/** 与 useI18n().t 兼容的最小翻译函数签名（用于非组件模块发提示文案）。 */
export type TranslateKey = (key: string, named?: Record<string, unknown>) => string
