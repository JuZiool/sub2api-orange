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
 * Orange 特有：链成员的可用性状态（v0.2.25-8 起见 GET/PUT/heal 与列表接口 groups[]）。
 *
 * 判定优先级与后端一致：missing → disabled → no_permission → scope_mismatch → available：
 * - available       可用；
 * - disabled        分组停用 / 分组上下文不合法；
 * - scope_mismatch  平台或订阅类型与链首不一致；
 * - no_permission   用户无绑定权限（专属未授权）；
 * - missing         分组已删除 / 读不到。
 *
 * 只有 available 的成员会被网关用于选号；其余成员仍会出现在接口返回里（不再静默丢弃）。
 */
export type KeyGroupChainMemberState =
  | 'available'
  | 'disabled'
  | 'scope_mismatch'
  | 'no_permission'
  | 'missing'

/**
 * 分组链成员（链首 = api_keys.group_id，priority 从 1 起升序）。
 *
 * v0.2.25-8 起接口返回链上**全部**成员（链首 + 全部候选），用 `state` 标出可用性；
 * 旧后端 / 旧数据缺省该字段，按 available 处理。
 */
export interface ApiKeyGroupEntry {
  group_id: number
  priority: number
  group?: Group
  /** 成员可用性状态（字符串，取值见 KeyGroupChainMemberState）；缺省视为 available。 */
  state?: string
}

/** Orange 特有：带分组链字段的 API Key 视图。 */
export type KeyGroupChainKey = ApiKey & {
  /** 分组优先级链（含链首，按 priority 升序）。普通 Key 为空数组或缺省。 */
  groups?: ApiKeyGroupEntry[]
  /** 分组链意图模式（none=普通 Key，fallback=兜底分组，aggregate=聚合密钥）。 */
  group_chain_mode?: ApiKeyGroupChainMode
}

/**
 * GET/PUT /keys/:id/group-chain 与 POST /keys/:id/group-chain/heal 的响应体。
 * members[0] 恒为链首（主分组）。
 *
 * v0.2.25-8 起 GET 是**纯读**接口：members 返回链上全部成员（含不可用成员）并带上 state，
 * 不再静默剔除、也不再自动重置链首；显式自愈改走 /heal。
 * `auto_pruned_*` / `auto_promoted_*` 是自愈接口留下的诊断信息（GET 恒为空数组 / 0）。
 */
export interface ApiKeyGroupChain {
  api_key_id: number
  mode: ApiKeyGroupChainMode
  primary_group_id: number
  /** 链上全部成员（含不可用成员），按 priority 升序。 */
  members: ApiKeyGroupEntry[]
  /** 本次自动移除的分组 id（heal 之外恒为空数组）。 */
  auto_pruned_group_ids?: number[]
  /** 本次自动移除的分组（含名称）。 */
  auto_pruned_groups?: ApiKeyPrunedGroup[]
  /** 链首被自动替换后的新主分组 id（0 表示未变）。 */
  auto_promoted_group_id?: number
}

/** 链自愈时被移除的分组（含名称，用于提示「已自动移除 N 个不可用分组」）。 */
export interface ApiKeyPrunedGroup {
  group_id: number
  name: string
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

/** 分组链表单状态（useKeyGroupChain 的 `keyGroupChainForm`）。 */
export interface KeyGroupChainFormState {
  fallbackGroupId: number | null
  aggregateMode: boolean
  aggregateGroupIds: Array<number | null>
  /** 当前弹窗是否为编辑模式（决定提交成功后按 update 还是 create 回写链）。 */
  isEdit: boolean
  /** 当前编辑的密钥 id（关闭弹窗后上游会清空 selectedKey，这里提前记住）。 */
  editKeyId: number | null
  /** 上次读链（GET / heal）返回的完整成员 ID（含不可用成员），用于保存前的移除确认。 */
  serverMemberIds: number[]
  /** group_id → 分组名（读链返回；用于「确认移除 N 个分组：a、b」这类提示）。 */
  serverMemberNames: Record<number, string>
  /** group_id → 成员状态（available | disabled | scope_mismatch | no_permission | missing）。 */
  memberStates: Record<number, string>
  /** 用户是否已显式勾选确认「保存会移除服务器上仍存在的成员」。 */
  pruneConfirmed: boolean
}

/** 与 useI18n().t 兼容的最小翻译函数签名（用于非组件模块发提示文案）。 */
export type TranslateKey = (key: string, named?: Record<string, unknown>) => string
