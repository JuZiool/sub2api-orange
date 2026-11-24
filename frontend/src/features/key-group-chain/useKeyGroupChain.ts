/**
 * Orange 特有：密钥分组链（兜底分组 / 聚合密钥）的表单状态、校验与提交钩子。
 *
 * 调用关系（KeysView.vue 只保留接线）：
 * - KeyGroupChainFormFields 维护本文件的 `keyGroupChainForm` 状态；
 * - 提交前 KeyGroupChainFormFields 的父级调用 `prepareKeyGroupChainSubmit` 做校验并暂存链，
 *   暂存链由本 feature 包装过的 keysAPI 在 create / update 成功后补写；
 * - 主分组变更（列表分组列）调用 `handleKeyGroupChainGroupChange`，仅在“该 Key 带兜底链”时接管，
 *   其余情况返回 false 交回上游 changeGroup 原逻辑。
 *
 * 未配置分组链（aggregateMode=false 且 fallbackGroupId=null）时：校验通过、暂存空链，
 * 行为与上游一致（提交后仅清空可能残留的链，与历史实现一致）。
 *
 * 用户已无权限的链成员（分组从「公开」改为「专属」等）由后端负责：读取链详情接口时后端会剔除
 * 这些成员、必要时把第一个可用成员提升为主分组，并把结果写回数据库；列表接口返回的链同样已剔除。
 * 前端因此不做任何可用性判定，只在 `hydrateKeyGroupChain` 里按响应体的
 * `auto_pruned_group_ids` / `auto_promoted_group_id` 给一句“已自动更新密钥”的提示。
 */
import { reactive } from 'vue'
import { useAppStore } from '@/stores/app'
import { keysAPI as upstreamKeysAPI } from '@/api'
import type { ApiKey, Group } from '@/types'
import { fallbackGroupOf, isAggregatedKey, sortedKeyGroups } from './chain'
import {
  clearStagedKeyGroupChain,
  getKeyGroupChain,
  setKeyGroupChain,
  stageKeyGroupChain
} from './api'
import type {
  ApiKeyGroupChain,
  GroupOptionLike,
  KeyGroupChainFormState,
  KeyGroupChainIntent,
  KeyGroupChainKey,
  TranslateKey
} from './types'

/** 分组链表单状态（由 KeyGroupChainFormFields 维护；提交前由 prepare 读取）。 */
export const keyGroupChainForm = reactive<KeyGroupChainFormState>({
  fallbackGroupId: null,
  aggregateMode: false,
  aggregateGroupIds: [],
  /** 当前弹窗是否为编辑模式（决定提交成功后按 update 还是 create 回写链）。 */
  isEdit: false,
  /** 当前编辑的密钥 id（关闭弹窗后上游会清空 selectedKey，这里提前记住）。 */
  editKeyId: null as number | null
})

/** Orange 特有：把分组 id 解析成界面上的分组名（选项里找不到时回退成 `#id`）。 */
function resolveGroupLabel(groupId: number, options?: readonly GroupOptionLike[]): string {
  return options?.find((option) => option.value === groupId)?.label || `#${groupId}`
}

/**
 * Orange 特有：提示后端在读取链详情时自动改动过的链。
 *
 * 后端把用户已无权限的成员从链里剔除、必要时把第一个可用成员提升为主分组并已写回数据库，
 * 前端只提示一句，避免用户误以为配置被吞掉（界面不再显示这些成员）。
 */
function notifyAutoChainUpdate(
  chain: ApiKeyGroupChain,
  t: TranslateKey,
  availableGroupOptions?: readonly GroupOptionLike[]
): void {
  const appStore = useAppStore()
  const prunedIds = chain.auto_pruned_group_ids ?? []
  if (prunedIds.length > 0) {
    appStore.showWarning(
      t('orangeKeyGroupChain.autoPruned', {
        count: prunedIds.length,
        groups: prunedIds.map((id) => resolveGroupLabel(id, availableGroupOptions)).join(', ')
      })
    )
  }
  const promotedGroupId = chain.auto_promoted_group_id ?? 0
  if (promotedGroupId > 0) {
    appStore.showWarning(
      t('orangeKeyGroupChain.autoPromoted', {
        group: resolveGroupLabel(promotedGroupId, availableGroupOptions)
      })
    )
  }
}

/**
 * Orange 特有：重置表单状态（弹窗关闭 / 打开创建弹窗时调用）。
 *
 * 这里只清表单状态，不清“暂存链”：提交成功后暂存链由包装过的 keysAPI 消费；
 * 若用户提交后立刻关闭弹窗，请求仍会完成后补写链（与历史行为一致）。
 * 暂存链在写入成功/失败、下次 prepare 提交、或列表发起链写入时被覆盖/清空。
 */
export function resetKeyGroupChainForm(): void {
  keyGroupChainForm.fallbackGroupId = null
  keyGroupChainForm.aggregateMode = false
  keyGroupChainForm.aggregateGroupIds = []
  keyGroupChainForm.isEdit = false
  keyGroupChainForm.editKeyId = null
}

/**
 * Orange 特有：按列表行初始化链状态。
 * 列表响应里的链字段只用于首屏占位，权威数据由 hydrateKeyGroupChain 按接口回填。
 */
export function initKeyGroupChainFormFromKey(key: ApiKey): void {
  const chainKey = key as KeyGroupChainKey
  const aggregate = isAggregatedKey(chainKey)
  keyGroupChainForm.isEdit = true
  keyGroupChainForm.editKeyId = key.id
  keyGroupChainForm.aggregateMode = aggregate
  keyGroupChainForm.aggregateGroupIds = aggregate
    ? sortedKeyGroups(chainKey).map((entry) => entry.group_id)
    : key.group_id
      ? [key.group_id]
      : []
  keyGroupChainForm.fallbackGroupId = fallbackGroupOf(chainKey)?.group_id ?? null
}

/**
 * Orange 特有：编辑弹窗打开后按 GET /keys/:id/group-chain 回填分组链。
 * 接口不可用时保留列表字段的回填结果，弹窗仍可正常使用。
 *
 * @param key 正在编辑的密钥
 * @param isStillEditing 用于丢弃过期响应（用户已关闭弹窗或改选其它密钥）
 * @param setPrimaryGroupId 回填主分组到上游 formData.group_id
 * @param t 翻译函数（用于“后端已自动更新密钥”的提示文案）
 * @param availableGroupOptions 当前用户可绑定的分组选项（编辑弹窗为 formGroupOptions）；
 *        仅用于把提示里的 group_id 解析成分组名
 */
export async function hydrateKeyGroupChain(
  key: ApiKey,
  isStillEditing: () => boolean,
  setPrimaryGroupId: (groupId: number | null) => void,
  t: TranslateKey,
  availableGroupOptions?: readonly GroupOptionLike[]
): Promise<void> {
  try {
    const chain = await getKeyGroupChain(key.id)
    if (!isStillEditing()) return
    // 后端已在读链时剔除无权限成员 / 自动提升链首并写回库：界面只提示，不再做任何前端判定。
    notifyAutoChainUpdate(chain, t, availableGroupOptions)
    const members = [...(chain.members ?? [])].sort((a, b) => a.priority - b.priority)
    const primaryId = members[0]?.group_id ?? key.group_id ?? null
    if (chain.mode === 'aggregate' && members.length > 1) {
      keyGroupChainForm.aggregateMode = true
      keyGroupChainForm.aggregateGroupIds = members.map((member) => member.group_id)
      keyGroupChainForm.fallbackGroupId = null
      setPrimaryGroupId(primaryId)
      return
    }
    const fallbackId =
      chain.mode === 'fallback' && members.length === 2
        ? (members.find((member) => member.group_id !== primaryId)?.group_id ?? null)
        : null
    keyGroupChainForm.aggregateMode = false
    keyGroupChainForm.fallbackGroupId = fallbackId
    keyGroupChainForm.aggregateGroupIds = primaryId === null ? [] : [primaryId]
    setPrimaryGroupId(primaryId)
  } catch (error) {
    // 接口不可用时保留列表字段的回填结果，弹窗仍可正常使用
    console.error('Failed to load key group chain:', error)
  }
}

/**
 * Orange 特有：提交前校验分组链并暂存待写入的链。
 * 返回 false 表示校验失败（已弹出错误提示），调用方应中止提交。
 *
 * 校验顺序与历史行为一致：先兜底分组（非聚合模式），再聚合链（至少 2 组、同平台、无重复）。
 */
export function prepareKeyGroupChainSubmit(
  formData: { group_id: number | null },
  groups: Group[],
  t: TranslateKey
): boolean {
  const appStore = useAppStore()
  const state = keyGroupChainForm

  // 校验兜底分组：必须与主分组不同且同平台（聚合模式下不校验兜底）
  if (!state.aggregateMode && state.fallbackGroupId !== null) {
    const primary = groups.find((group) => group.id === formData.group_id)
    const fallback = groups.find((group) => group.id === state.fallbackGroupId)
    if (
      !primary ||
      !fallback ||
      primary.id === fallback.id ||
      primary.platform !== fallback.platform
    ) {
      appStore.showError(t('keys.fallbackGroupInvalid'))
      return false
    }
  }

  // 聚合链校验：至少 2 组、同平台、无重复
  let aggregateGroupIds: number[] | undefined
  if (state.aggregateMode) {
    const ids = state.aggregateGroupIds.filter((id): id is number => id !== null)
    if (new Set(ids).size !== ids.length) {
      appStore.showError(t('keys.aggregateGroupDuplicate'))
      return false
    }
    if (ids.length < 2) {
      appStore.showError(t('keys.aggregateGroupTooFew'))
      return false
    }
    const platforms = new Set(
      ids.map((id) => groups.find((group) => group.id === id)?.platform).filter(Boolean)
    )
    if (platforms.size > 1) {
      appStore.showError(t('keys.aggregateGroupPlatformMismatch'))
      return false
    }
    aggregateGroupIds = ids
  }

  // 分组链：聚合密钥提交完整优先级链；普通密钥提交 [主分组, 兜底分组]，未设兜底则清链
  const primaryGroupId = aggregateGroupIds ? aggregateGroupIds[0] : (formData.group_id as number)
  // 链首即密钥主分组：确保上游 create / update 使用链首作为 group_id
  if (aggregateGroupIds) formData.group_id = aggregateGroupIds[0]
  const groupIds: number[] =
    aggregateGroupIds ??
    (state.fallbackGroupId !== null ? [primaryGroupId, state.fallbackGroupId] : [])
  const mode: KeyGroupChainIntent = aggregateGroupIds ? 'aggregate' : 'fallback'
  stageKeyGroupChain({
    kind: state.isEdit ? 'edit' : 'create',
    keyId: state.editKeyId,
    groupIds,
    mode,
    t
  })
  return true
}

/**
 * Orange 特有：列表“分组”列切换主分组时的钩子（在 KeysView.changeGroup 内最前面调用）。
 *
 * 仅当该 Key 带兜底链（group_chain_mode=fallback 且链长 2）时接管：更新主分组后同步链，
 * 与新主分组不兼容则清链，并按历史文案提示。其余情况返回 false，由上游 changeGroup 原逻辑处理，
 * 未配置兜底链时行为与上游完全一致。
 */
export async function handleKeyGroupChainGroupChange(
  key: ApiKey,
  newGroupId: number | null,
  groups: Group[],
  refresh: () => void,
  t: TranslateKey
): Promise<boolean> {
  const appStore = useAppStore()
  // 列表操作不属于任何一次弹窗提交：先丢弃可能残留的暂存链，避免误写到本 Key。
  clearStagedKeyGroupChain()
  const fallbackEntry = fallbackGroupOf(key as KeyGroupChainKey)
  if (!fallbackEntry || key.group_id === newGroupId) return false

  const primary = groups.find((group) => group.id === newGroupId)
  const fallback =
    fallbackEntry.group || groups.find((group) => group.id === fallbackEntry.group_id)
  const fallbackCleared =
    !fallback ||
    !primary ||
    primary.id === fallback.id ||
    primary.platform !== fallback.platform

  try {
    await upstreamKeysAPI.update(key.id, { group_id: newGroupId })
  } catch (error) {
    appStore.showError(t('keys.failedToChangeGroup'))
    return true
  }

  // 兼容则保留兜底（链首随主分组变更），不兼容或主分组被清空则清链；
  // 链写入失败只提示，主分组本身已经改成功。
  try {
    await setKeyGroupChain(
      key.id,
      fallbackCleared || newGroupId === null ? [] : [newGroupId, fallbackEntry.group_id],
      'fallback'
    )
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || t('keys.groupChain.saveFailed'))
  }
  appStore.showSuccess(
    t(fallbackCleared ? 'keys.groupChangedFallbackClearedSuccess' : 'keys.groupChangedSuccess')
  )
  refresh()
  return true
}

/**
 * Orange 特有：兜底分组下拉选中后的保存动作（列表分组列的兜底行）。
 * 与历史行为一致：与当前兜底相同则静默返回；主分组为空时不处理；失败只提示。
 */
export async function saveFallbackGroupSelection(
  key: ApiKey,
  groupId: number | null,
  refresh: () => void,
  t: TranslateKey
): Promise<void> {
  const appStore = useAppStore()
  // 列表操作不属于任何一次弹窗提交：先丢弃可能残留的暂存链，避免误写到本 Key。
  clearStagedKeyGroupChain()
  const currentFallbackId = fallbackGroupOf(key as KeyGroupChainKey)?.group_id ?? null
  if (currentFallbackId === groupId) return
  if (key.group_id === null) return
  try {
    await setKeyGroupChain(key.id, groupId === null ? [] : [key.group_id, groupId], 'fallback')
    appStore.showSuccess(t('keys.fallbackGroupChangedSuccess'))
    refresh()
  } catch (error) {
    appStore.showError(t('keys.failedToChangeFallbackGroup'))
  }
}
