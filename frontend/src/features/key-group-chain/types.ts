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

/** 分组链成员（链首 = api_keys.group_id，priority 从 1 起升序）。 */
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

/** GET/PUT /keys/:id/group-chain 的响应体。members[0] 恒为链首（主分组）。 */
export interface ApiKeyGroupChain {
  api_key_id: number
  mode: ApiKeyGroupChainMode
  primary_group_id: number
  members: ApiKeyGroupEntry[]
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

/** 与 useI18n().t 兼容的最小翻译函数签名（用于非组件模块发提示文案）。 */
export type TranslateKey = (key: string) => string
