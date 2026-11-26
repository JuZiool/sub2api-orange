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
 * v0.2.25-8 起读链是**纯读**接口，members 会返回链上全部成员并用 state 标出可用性
 * （available / disabled / scope_mismatch / no_permission / missing）。因此这里不再“静默接受更小的链”：
 * - 弹窗按 members 1:1 回填（含不可用成员），由 KeyGroupChainFormFields 灰显并标注原因；
 * - 保存会移除服务器上仍存在的成员时，必须由用户显式勾选确认（`pruneConfirmed`）；
 * - 想清掉不可用成员时走显式自愈接口 `healKeyGroupChainAndRefresh`（只修剪 no_permission / missing）。
 */
import { reactive } from 'vue'
import { useAppStore } from '@/stores/app'
import { keysAPI as upstreamKeysAPI } from '@/api'
import type { ApiKey, Group } from '@/types'
import { fallbackGroupOf, isAggregatedKey, sortedKeyGroups } from './chain'
import {
  clearStagedKeyGroupChain,
  getKeyGroupChain,
  groupChainErrorMessage,
  healKeyGroupChain,
  setKeyGroupChain,
  stageKeyGroupChain
} from './api'
import type {
  ApiKeyGroupEntry,
  ApiKeyGroupChain,
  KeyGroupChainFormState,
  KeyGroupChainIntent,
  KeyGroupChainKey,
  KeyGroupChainMemberState,
  TranslateKey
} from './types'

/** 链成员可用状态常量（接口缺省 state 时按它处理，兼容旧后端）。 */
export const CHAIN_MEMBER_STATE_AVAILABLE: KeyGroupChainMemberState = 'available'

/** 分组链表单状态（由 KeyGroupChainFormFields 维护；提交前由 prepare 读取）。 */
export const keyGroupChainForm = reactive<KeyGroupChainFormState>({
  fallbackGroupId: null,
  aggregateMode: false,
  aggregateGroupIds: [],
  /** 当前弹窗是否为编辑模式（决定提交成功后按 update 还是 create 回写链）。 */
  isEdit: false,
  /** 当前编辑的密钥 id（关闭弹窗后上游会清空 selectedKey，这里提前记住）。 */
  editKeyId: null as number | null,
  /** 上次读链返回的完整成员 ID（含不可用成员）。 */
  serverMemberIds: [] as number[],
  /** group_id → 分组名（读链返回，用于移除确认文案）。 */
  serverMemberNames: {} as Record<number, string>,
  /** group_id → 成员状态。 */
  memberStates: {} as Record<number, string>,
  /** 用户是否已显式确认移除服务器上仍存在的成员。 */
  pruneConfirmed: false
})

/** 成员是否可用（缺省视为 available，兼容旧后端 / 旧客户端）。 */
export function isChainMemberAvailable(state: string | undefined | null): boolean {
  return !state || state === CHAIN_MEMBER_STATE_AVAILABLE
}

/** 成员状态 → i18n 文案键；available（或未知状态）不展示标签，返回 null。 */
export function chainMemberStateLabelKey(state: string | undefined | null): string | null {
  switch (state) {
    case 'disabled':
      return 'orangeKeyChain.stateDisabled'
    case 'scope_mismatch':
      return 'orangeKeyChain.stateScopeMismatch'
    case 'no_permission':
      return 'orangeKeyChain.stateNoPermission'
    case 'missing':
      return 'orangeKeyChain.stateMissing'
    default:
      return null
  }
}

/**
 * 本次表单将提交的链分组 ID（含链首，顺序与提交一致）。
 *
 * 聚合模式取链编辑器里的非空项；普通 / 兜底模式取「主分组 + 兜底分组」，
 * 未设兜底则为空（提交即清链，与历史行为一致）。
 */
export function plannedChainGroupIds(primaryGroupId: number | null): number[] {
  if (keyGroupChainForm.aggregateMode) {
    return keyGroupChainForm.aggregateGroupIds.filter((id): id is number => id !== null)
  }
  const fallbackGroupId = keyGroupChainForm.fallbackGroupId
  return fallbackGroupId !== null && primaryGroupId !== null
    ? [primaryGroupId, fallbackGroupId]
    : []
}

/** 保存将移除的服务器链成员（用于弹窗里的「我确认移除」二次确认）。 */
export interface PendingChainRemoval {
  groupId: number
  /** 分组名（来自读链接口；缺失时退化为 #id）。 */
  name: string
  /** 成员状态（available | disabled | scope_mismatch | no_permission | missing）。 */
  state: string
}

/**
 * Orange 特有：服务器上仍存在、但本次保存会从链里移除的成员。
 * 非空时说明保存会缩小链，必须由用户显式勾选确认（见 prepareKeyGroupChainSubmit）。
 *
 * 与后端剔除口径保持一致：清链（本次提交的 group_ids 为空）时不计「主分组本身」为移除项
 * —— 它只是退回 api_keys.group_id，并没有真的从服务器上消失。
 */
export function pendingChainRemovals(primaryGroupId: number | null): PendingChainRemoval[] {
  const planned = plannedChainGroupIds(primaryGroupId)
  const plannedSet = new Set(planned)
  const clearing = planned.length === 0
  return keyGroupChainForm.serverMemberIds
    .filter((groupId) => !plannedSet.has(groupId))
    .filter((groupId) => !(clearing && primaryGroupId !== null && groupId === primaryGroupId))
    .map((groupId) => ({
      groupId,
      name: keyGroupChainForm.serverMemberNames[groupId] ?? `#${groupId}`,
      state: keyGroupChainForm.memberStates[groupId] ?? CHAIN_MEMBER_STATE_AVAILABLE
    }))
}

/** 本次将提交的链里是否还有不可用成员（用于展示「自动修复不可用分组」入口）。 */
export function hasUnavailableChainMember(primaryGroupId: number | null): boolean {
  return plannedChainGroupIds(primaryGroupId).some(
    (groupId) => !isChainMemberAvailable(keyGroupChainForm.memberStates[groupId])
  )
}

/**
 * 计划链是否与服务器链完全一致（成员与顺序都一致）。
 *
 * 一致时直接 1:1 写回、跳过形态校验：契约 C2 放宽后链上「不可路由」的成员本身就是合法候选，
 * 若前端仍按「同平台 / 至少 2 组」校验，用户没有改动任何成员（例如自愈把链修剪到只剩 1 个成员后
 * 再想改个名字）也会被自己的校验挡住。
 */
function isSameChainAsServer(planned: number[]): boolean {
  const server = keyGroupChainForm.serverMemberIds
  return planned.length === server.length && planned.every((id, index) => id === server[index])
}

/**
 * 该成员是否「原样保留的服务器成员，且当前不可路由」。
 *
 * 契约 C2：PUT 的候选成员（group_ids[1:]）不再因 disabled / scope_mismatch / no_permission 被拒，
 * 只要分组存在或本来就在链上即可。前端因此不再对这些成员做平台一致性校验，
 * 保证「全部成员 1:1 提交」不会因为后端放宽的状态被前端提前判成非法。
 */
function isPreservedUnroutableMember(groupId: number): boolean {
  return (
    keyGroupChainForm.serverMemberIds.includes(groupId) &&
    !isChainMemberAvailable(keyGroupChainForm.memberStates[groupId])
  )
}

/** 记录服务器链成员（ID / 名称 / 状态），并清掉上一次的移除确认。 */
function applyServerChainMembers(entries: ApiKeyGroupEntry[]): void {
  const states: Record<number, string> = {}
  const names: Record<number, string> = {}
  for (const entry of entries) {
    states[entry.group_id] = entry.state ?? CHAIN_MEMBER_STATE_AVAILABLE
    const name = entry.group?.name
    if (name) names[entry.group_id] = name
  }
  keyGroupChainForm.memberStates = states
  keyGroupChainForm.serverMemberNames = names
  keyGroupChainForm.serverMemberIds = entries.map((entry) => entry.group_id)
  keyGroupChainForm.pruneConfirmed = false
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
  keyGroupChainForm.serverMemberIds = []
  keyGroupChainForm.serverMemberNames = {}
  keyGroupChainForm.memberStates = {}
  keyGroupChainForm.pruneConfirmed = false
}

/**
 * Orange 特有：按列表行初始化链状态。
 * 列表响应的 groups[]（v0.2.25-8 起同样返回全部成员并带 state）先用于占位，
 * 权威数据由 hydrateKeyGroupChain 按读链接口回填。
 */
export function initKeyGroupChainFormFromKey(key: ApiKey): void {
  const chainKey = key as KeyGroupChainKey
  const entries = sortedKeyGroups(chainKey)
  const aggregate = isAggregatedKey(chainKey)
  keyGroupChainForm.isEdit = true
  keyGroupChainForm.editKeyId = key.id
  keyGroupChainForm.aggregateMode = aggregate
  keyGroupChainForm.aggregateGroupIds = aggregate
    ? entries.map((entry) => entry.group_id)
    : key.group_id
      ? [key.group_id]
      : []
  keyGroupChainForm.fallbackGroupId = fallbackGroupOf(chainKey)?.group_id ?? null
  // mode=none 表示没有链（链首由 key 自身的 group_id 管理），不参与移除确认
  applyServerChainMembers(
    chainKey.group_chain_mode === 'fallback' || chainKey.group_chain_mode === 'aggregate'
      ? entries
      : []
  )
}

/**
 * 把读链 / 自愈接口返回的链（含不可用成员）1:1 回填到表单状态。
 *
 * - aggregate 模式不再要求成员数 > 1：一律用聚合编辑器并按 members 顺序回填；
 * - 记录服务器完整成员 / 名称 / 状态，供「移除确认」与状态标签使用。
 */
function applyKeyGroupChainView(
  chain: ApiKeyGroupChain,
  key: ApiKey,
  setPrimaryGroupId: (groupId: number | null) => void
): void {
  const members = [...(chain.members ?? [])].sort((a, b) => a.priority - b.priority)
  const primaryId = members[0]?.group_id ?? key.group_id ?? null
  // mode=none 表示该 Key 没有链，链首由 key 自身的 group_id 管理，不参与移除确认
  applyServerChainMembers(chain.mode === 'none' ? [] : members)

  if (chain.mode === 'aggregate') {
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = members.map((member) => member.group_id)
    keyGroupChainForm.fallbackGroupId = null
    setPrimaryGroupId(primaryId)
    return
  }

  // 兜底链：链首之后的第一个成员即兜底分组；再往后的成员（历史脏数据）交给移除确认处理
  const fallbackId =
    chain.mode === 'fallback'
      ? (members.find((member) => member.group_id !== primaryId)?.group_id ?? null)
      : null
  keyGroupChainForm.aggregateMode = false
  keyGroupChainForm.fallbackGroupId = fallbackId
  keyGroupChainForm.aggregateGroupIds = primaryId === null ? [] : [primaryId]
  setPrimaryGroupId(primaryId)
}

/**
 * Orange 特有：编辑弹窗打开后按 GET /keys/:id/group-chain 回填分组链。
 * 接口不可用时保留列表字段的回填结果，弹窗仍可正常使用。
 *
 * v0.2.25-8 起接口是纯读的：members 含链上全部成员（含不可用成员）与 state，
 * 这里 1:1 回填，不再静默接受更小的链；不可用成员由表单区灰显并标注原因。
 *
 * @param key 正在编辑的密钥
 * @param isStillEditing 用于丢弃过期响应（用户已关闭弹窗或改选其它密钥）
 * @param setPrimaryGroupId 回填主分组到上游 formData.group_id
 */
export async function hydrateKeyGroupChain(
  key: ApiKey,
  isStillEditing: () => boolean,
  setPrimaryGroupId: (groupId: number | null) => void
): Promise<void> {
  try {
    const chain = await getKeyGroupChain(key.id)
    if (!isStillEditing()) return
    applyKeyGroupChainView(chain, key, setPrimaryGroupId)
  } catch (error) {
    // 接口不可用时保留列表字段的回填结果，弹窗仍可正常使用
    console.error('Failed to load key group chain:', error)
  }
}

/**
 * Orange 特有：显式自愈分组链（POST /keys/:id/group-chain/heal）。
 *
 * - 后端只修剪 state ∈ {no_permission, missing} 的候选，disabled / scope_mismatch 一律保留；
 * - 用返回的 members 重新回填弹窗（含不可用成员），仅当本次真的移除了成员时才提示并刷新列表。
 *
 * @returns 本次真正被移除的分组数量（0 表示无需修复 / 后端未移除任何成员）
 */
export async function healKeyGroupChainAndRefresh(
  key: ApiKey,
  refresh: () => void,
  t: TranslateKey,
  setPrimaryGroupId: (groupId: number | null) => void = () => {}
): Promise<number> {
  const appStore = useAppStore()
  try {
    const chain = await healKeyGroupChain(key.id)
    const prunedGroups = chain.auto_pruned_groups ?? []
    const removedCount =
      prunedGroups.length > 0 ? prunedGroups.length : (chain.auto_pruned_group_ids?.length ?? 0)
    applyKeyGroupChainView(chain, key, setPrimaryGroupId)
    // 只有真的移除了分组才提示与刷新，避免点一次按钮就冒出“已移除 0 个”的噪音
    if (removedCount > 0) {
      appStore.showSuccess(t('orangeKeyChain.healSuccess', { n: removedCount }))
      refresh()
    }
    return removedCount
  } catch (error) {
    appStore.showError(groupChainErrorMessage(error, t('orangeKeyChain.healFailed')))
    return 0
  }
}

/**
 * Orange 特有：提交前校验分组链并暂存待写入的链。
 * 返回 false 表示校验失败（已弹出错误提示），调用方应中止提交。
 *
 * 校验顺序与历史行为一致：先兜底分组（非聚合模式），再聚合链（至少 2 组、同平台、无重复），
 * 最后做移除确认：本次保存会移除服务器上仍存在的成员时，必须由用户显式勾选确认。
 *
 * 契约 C2 放宽后新增两条豁免（保证「全部成员 1:1 提交」不会在前端被拦）：
 * - 计划链与服务器链完全一致时跳过形态校验（等价于原样写回）；
 * - 原样保留的不可路由候选（disabled / scope_mismatch / no_permission / missing）不参与平台一致性校验。
 */
export function prepareKeyGroupChainSubmit(
  formData: { group_id: number | null },
  groups: Group[],
  t: TranslateKey
): boolean {
  const appStore = useAppStore()
  const state = keyGroupChainForm
  // 计划链（未校验前）与服务器链一致 → 用户没有改动成员，直接写回
  const unchanged = isSameChainAsServer(plannedChainGroupIds(formData.group_id))

  // 校验兜底分组：必须与主分组不同且同平台（聚合模式下不校验兜底）
  if (!unchanged && !state.aggregateMode && state.fallbackGroupId !== null) {
    const primary = groups.find((group) => group.id === formData.group_id)
    const fallback = groups.find((group) => group.id === state.fallbackGroupId)
    const preserved = isPreservedUnroutableMember(state.fallbackGroupId)
    if (primary && fallback && primary.id === fallback.id) {
      appStore.showError(t('keys.fallbackGroupInvalid'))
      return false
    }
    if (!preserved && (!primary || !fallback || primary.platform !== fallback.platform)) {
      appStore.showError(t('keys.fallbackGroupInvalid'))
      return false
    }
  }

  // 聚合链校验：至少 2 组、同平台、无重复
  let aggregateGroupIds: number[] | undefined
  if (state.aggregateMode) {
    const ids = state.aggregateGroupIds.filter((id): id is number => id !== null)
    if (!unchanged) {
      if (new Set(ids).size !== ids.length) {
        appStore.showError(t('keys.aggregateGroupDuplicate'))
        return false
      }
      if (ids.length < 2) {
        appStore.showError(t('keys.aggregateGroupTooFew'))
        return false
      }
      // 只有可路由 / 新选的成员参与平台一致性校验（见 isPreservedUnroutableMember）
      const platforms = new Set(
        ids
          .filter((id) => !isPreservedUnroutableMember(id))
          .map((id) => groups.find((group) => group.id === id)?.platform)
          .filter(Boolean)
      )
      if (platforms.size > 1) {
        appStore.showError(t('keys.aggregateGroupPlatformMismatch'))
        return false
      }
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

  // 移除确认：会删掉服务器链上仍存在的成员时，必须由用户显式勾选（不可静默缩小链）
  const removals = pendingChainRemovals(formData.group_id)
  if (removals.length > 0 && !state.pruneConfirmed) {
    appStore.showError(
      t('orangeKeyChain.pruneConfirmRequired', {
        n: removals.length,
        names: removals.map((removal) => removal.name).join(t('orangeKeyChain.nameSeparator'))
      })
    )
    return false
  }

  const mode: KeyGroupChainIntent = aggregateGroupIds ? 'aggregate' : 'fallback'
  stageKeyGroupChain({
    kind: state.isEdit ? 'edit' : 'create',
    keyId: state.editKeyId,
    groupIds,
    mode,
    // 客户端当前看到的完整成员集合 + 确认标记：后端据此返回 409 或放行本次全量替换
    expectedGroupIds: [...state.serverMemberIds],
    confirmPrune: state.pruneConfirmed,
    // 409 兜底：服务端 message / metadata.pending_removals 缺失时用这份本地清单渲染确认信息
    pendingRemovals: removals.map((removal) => ({ group_id: removal.groupId, name: removal.name })),
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
  // 列表侧快捷操作是用户主动改分组：契约 C3 下不带 expected_group_ids 也会被「静默裁剪」拦成 409，
  // 这里显式传 confirm_prune: true，等于用户已确认本次可能裁剪掉的兜底成员。
  try {
    await setKeyGroupChain(
      key.id,
      fallbackCleared || newGroupId === null ? [] : [newGroupId, fallbackEntry.group_id],
      { mode: 'fallback', confirmPrune: true }
    )
  } catch (error) {
    appStore.showError(groupChainErrorMessage(error, t('keys.groupChain.saveFailed')))
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
 *
 * 契约 C3：不带 expected_group_ids 的旧调用方同样会被「静默裁剪」拦成 409，
 * 而这里是用户主动选的兜底分组（清除兜底 = 主动裁剪），因此显式传 confirm_prune: true。
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
    await setKeyGroupChain(key.id, groupId === null ? [] : [key.group_id, groupId], {
      mode: 'fallback',
      confirmPrune: true
    })
    appStore.showSuccess(t('keys.fallbackGroupChangedSuccess'))
    refresh()
  } catch (error) {
    appStore.showError(groupChainErrorMessage(error, t('keys.failedToChangeFallbackGroup')))
  }
}
