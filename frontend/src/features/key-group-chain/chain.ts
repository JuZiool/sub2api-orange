/**
 * Orange 特有：密钥分组链的纯函数工具。
 *
 * 与后端 dto.APIKey 约定（见 backend/internal/handler/dto/orange_api_key_group_chain_types.go）：
 * - `groups` 是链上成员（含链首），按 priority 升序，链首即 api_keys.group_id；
 * - `group_chain_mode` 区分普通 Key / 兜底分组 / 聚合密钥，链长 2 的兜底链与
 *   两组的聚合链只能靠该字段区分。
 */
import type { ApiKeyGroupEntry, KeyGroupChainKey } from './types'

/** 聚合密钥：意图为聚合且链上分组多于 1 个（兜底链同样是 2 个成员，只能靠 mode 区分）。 */
export const isAggregatedKey = (key: KeyGroupChainKey): boolean =>
  key.group_chain_mode === 'aggregate' && (key.groups?.length ?? 0) > 1

/** 链上分组按优先级升序，链首（主分组）恒为 priority 1。 */
export const sortedKeyGroups = (key: KeyGroupChainKey): ApiKeyGroupEntry[] =>
  [...(key.groups ?? [])].sort((a, b) => a.priority - b.priority)

/** 兜底分组：仅 group_chain_mode=fallback 且链长恰好 2 时存在，取非链首的那一项。 */
export const fallbackGroupOf = (key: KeyGroupChainKey): ApiKeyGroupEntry | null => {
  if (key.group_chain_mode !== 'fallback' || key.groups?.length !== 2) return null
  return key.groups.find((entry) => entry.group_id !== key.group_id) ?? null
}
