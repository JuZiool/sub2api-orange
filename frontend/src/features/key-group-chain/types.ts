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
  /**
   * Orange 特有：链详情接口（GET /keys/:id/group-chain）返回的权威可用性。
   * false 表示当前用户已无该分组的绑定权限（例如分组被改为专属），请求时会被跳过。
   * 列表接口的 groups 不返回该字段（缺省）。
   */
  available?: boolean
  /** 不可用原因（available=false 时给出），当前取值：not_entitled。 */
  unavailable_reason?: string
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

/**
 * Orange 特有：判定分组“可用性”所需的选项最小形状。
 *
 * 「当前用户可绑定分组」列表就是上游分组下拉的选项：编辑弹窗用 `formGroupOptions`，
 * 列表页用 `groupOptions`。链成员的 group_id 不在该列表中（典型场景：分组被改为专属）
 * 即视为不可用，前端需要显式标注并在保存时自愈，否则下拉框会空白、保存会报
 * ORANGE_GROUP_CHAIN_INVALID。
 */
export interface GroupOptionLike {
  value: number
}

/** 分组链表单状态（useKeyGroupChain 的 `keyGroupChainForm`）。 */
export interface KeyGroupChainFormState {
  fallbackGroupId: number | null
  aggregateMode: boolean
  aggregateGroupIds: Array<number | null>
  /**
   * 链上有、但不在「当前用户可绑定分组」列表中的 group_id。
   * 由 hydrateKeyGroupChain / KeyGroupChainFormFields 按「后端权威标记 > 选项缺失」的顺序实时计算，
   * 供界面标注与提交前校验自愈。
   */
  unavailableGroupIds: number[]
  /**
   * 链详情接口返回的权威可用性（group_id → available）。
   * 存在该标记时以它为准（与后端请求时的过滤同源），缺失时才回退到「不在可绑定分组列表」的启发式。
   */
  backendAvailability: Record<number, boolean>
  /** 当前弹窗是否为编辑模式（决定提交成功后按 update 还是 create 回写链）。 */
  isEdit: boolean
  /** 当前编辑的密钥 id（关闭弹窗后上游会清空 selectedKey，这里提前记住）。 */
  editKeyId: number | null
}

/** 与 useI18n().t 兼容的最小翻译函数签名（用于非组件模块发提示文案）。 */
export type TranslateKey = (key: string) => string
