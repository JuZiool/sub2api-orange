/**
 * Orange 特有：密钥分组链接口与 keysAPI 包装。
 *
 * 端点（后端 Orange 自建表 orange_api_key_group_chain，对上游表只读）：
 * - GET /keys/:id/group-chain  读取密钥的分组优先级链
 * - PUT /keys/:id/group-chain  保存链（group_ids[0] 成为新主分组，[] = 清除链）
 *
 * 上游 KeysView 里 `keysAPI` 改为引用本文件的包装对象：在官方 create / update 成功后，
 * 若本次提交已由 `prepareKeyGroupChainSubmit` 暂存了分组链，则补写一次链。
 * 未启用分组链（未暂存）时包装对象与原 keysAPI 行为完全一致。
 */
import { apiClient, keysAPI as upstreamKeysAPI } from '@/api'
import { useAppStore } from '@/stores/app'
import type { UpdateApiKeyRequest } from '@/types'
import type {
  ApiKeyGroupChain,
  KeyGroupChainIntent,
  TranslateKey
} from './types'

/** 待随 create / update 一起落库的分组链（由 prepareKeyGroupChainSubmit 暂存）。 */
export interface StagedKeyGroupChain {
  kind: 'create' | 'edit'
  keyId: number | null
  groupIds: number[]
  mode: KeyGroupChainIntent
  t: TranslateKey
}

let stagedKeyGroupChain: StagedKeyGroupChain | null = null

/** Orange 特有：暂存本次提交要写入的分组链（提交成功后由包装的 keysAPI 消费一次）。 */
export function stageKeyGroupChain(staged: StagedKeyGroupChain): void {
  stagedKeyGroupChain = staged
}

/** Orange 特有：丢弃暂存的分组链（弹窗关闭 / 表单重置时调用）。 */
export function clearStagedKeyGroupChain(): void {
  stagedKeyGroupChain = null
}

/** Orange 特有：读取密钥的分组优先级链。 */
export async function getKeyGroupChain(id: number): Promise<ApiKeyGroupChain> {
  const { data } = await apiClient.get<ApiKeyGroupChain>(`/keys/${id}/group-chain`)
  return data
}

/** Orange 特有：保存密钥的分组优先级链（groupIds 有序，[0] = 新链首/主分组；[] = 清除链）。 */
export async function setKeyGroupChain(
  id: number,
  groupIds: number[],
  mode?: KeyGroupChainIntent
): Promise<ApiKeyGroupChain> {
  const { data } = await apiClient.put<ApiKeyGroupChain>(`/keys/${id}/group-chain`, {
    group_ids: groupIds,
    ...(mode ? { mode } : {})
  })
  return data
}

/** 写入暂存链：失败只提示，不回滚已经保存成功的密钥字段（与历史行为一致）。 */
async function flushStagedKeyGroupChain(keyId: number): Promise<void> {
  if (!stagedKeyGroupChain) return
  const { groupIds, mode, t } = stagedKeyGroupChain
  stagedKeyGroupChain = null
  try {
    await setKeyGroupChain(keyId, groupIds, mode)
  } catch (error: any) {
    useAppStore().showError(error?.response?.data?.detail || t('keys.groupChain.saveFailed'))
  }
}

/**
 * 上游 keysAPI 的包装：其余方法原样透传，只覆盖 create / update 以补写分组链。
 * 无暂存链（未使用分组链配置）时，两个方法的请求与返回值与上游完全一致。
 */
export const keysAPI: typeof upstreamKeysAPI = {
  ...upstreamKeysAPI,
  create: async (...args: Parameters<typeof upstreamKeysAPI.create>) => {
    const created = await upstreamKeysAPI.create(...args)
    if (stagedKeyGroupChain?.kind === 'create') {
      await flushStagedKeyGroupChain(created.id)
    }
    return created
  },
  update: async (id: number, updates: UpdateApiKeyRequest) => {
    const updated = await upstreamKeysAPI.update(id, updates)
    if (
      stagedKeyGroupChain?.kind === 'edit' &&
      stagedKeyGroupChain.keyId === id &&
      updates &&
      'group_id' in updates
    ) {
      await flushStagedKeyGroupChain(id)
    }
    return updated
  }
}
