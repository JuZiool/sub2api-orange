/**
 * Orange 特有：密钥分组链接口与 keysAPI 包装。
 *
 * 端点（后端 Orange 自建表 orange_api_key_group_chain，对上游表只读）：
 * - GET  /keys/:id/group-chain       读取密钥的分组优先级链（v0.2.25-8 起为纯读，绝不再写库）
 * - PUT  /keys/:id/group-chain       保存链（group_ids[0] 成为新主分组，[] = 清除链）
 * - POST /keys/:id/group-chain/heal  显式自愈（只修剪 no_permission / missing 的候选）
 *
 * 上游 KeysView 里 `keysAPI` 改为引用本文件的包装对象：在官方 create / update 成功后，
 * 若本次提交已由 `prepareKeyGroupChainSubmit` 暂存了分组链，则补写一次链。
 * 未启用分组链（未暂存）时包装对象与原 keysAPI 行为完全一致。
 *
 * v0.2.25-8 的移除确认（契约 C1 / C3）：
 * PUT 请求体支持 expected_group_ids / confirm_prune 两个可选字段，后端按
 * removed = (expected_group_ids ?? 服务端当前集合) − group_ids 计算「本次保存会移除的分组」；
 * 只要 removed 非空且 confirm_prune != true 就返回 409（reason=ORANGE_GROUP_CHAIN_PRUNE_CONFIRM，
 * 并在 metadata.pending_removals 里带结构化清单）。因此**未提供 expected_group_ids 的旧调用方
 * 同样不能再静默裁剪**：列表侧的快捷写链必须显式传 confirm_prune: true（那是用户的主动操作）。
 * 这里不吞掉该错误：文案取后端 message（拦截器把它放在 error.message 上）后，再追加
 * 「请重新打开编辑窗口确认后再保存」的提示交给 UI —— 此时本地 serverMemberIds 已经过期。
 */
import { apiClient, keysAPI as upstreamKeysAPI } from '@/api'
import { useAppStore } from '@/stores/app'
import type { UpdateApiKeyRequest } from '@/types'
import type {
  ApiKeyGroupChain,
  KeyGroupChainIntent,
  TranslateKey
} from './types'

/** 后端错误码：保存会移除服务器链上仍存在的成员，需要用户显式确认后重试。 */
export const ORANGE_GROUP_CHAIN_PRUNE_CONFIRM = 'ORANGE_GROUP_CHAIN_PRUNE_CONFIRM'

/**
 * 响应 metadata.pending_removals / 本地 pendingChainRemovals 的共同结构：
 * 「本次保存将移除的分组」清单（group_id + 名称，名称为空时由调用方用 #id 兜底）。
 */
export interface GroupChainPendingRemoval {
  group_id: number
  name: string
}

/** PUT /keys/:id/group-chain 的可选参数（expected_* / confirm_* 为 v0.2.25-8 新增）。 */
export interface SetKeyGroupChainOptions {
  /** 链意图模式：fallback（兜底分组）或 aggregate（聚合密钥）。 */
  mode?: KeyGroupChainIntent
  /** 客户端当前看到的完整成员集合；不提供时后端用服务端当前集合计算移除项。 */
  expectedGroupIds?: number[]
  /** 用户已确认要移除这些分组（默认 false，即未确认 → 后端 409）。 */
  confirmPrune?: boolean
}

/** 待随 create / update 一起落库的分组链（由 prepareKeyGroupChainSubmit 暂存）。 */
export interface StagedKeyGroupChain {
  kind: 'create' | 'edit'
  keyId: number | null
  groupIds: number[]
  mode: KeyGroupChainIntent
  /** 见 SetKeyGroupChainOptions：客户端当前看到的完整成员集合（移除确认用）。 */
  expectedGroupIds?: number[]
  /** 见 SetKeyGroupChainOptions：用户是否已确认移除。 */
  confirmPrune?: boolean
  /** 暂存时的本地待移除清单：409 且服务端未给出结构化清单时用它渲染确认信息。 */
  pendingRemovals?: GroupChainPendingRemoval[]
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

/**
 * Orange 特有：从接口错误里取用户可读文案。
 *
 * axios 拦截器（@/api/client）把后端 `{ code, message }` 的 message 放到 `error.message` 上，
 * 因此优先取它，再退回 axios 原始结构；都取不到时用调用方给的兜底文案。
 */
export function groupChainErrorMessage(error: unknown, fallback: string): string {
  const detail = error as
    | { message?: unknown; response?: { data?: { message?: unknown; detail?: unknown } } }
    | null
    | undefined
  const candidates = [
    detail?.message,
    detail?.response?.data?.message,
    detail?.response?.data?.detail
  ]
  for (const candidate of candidates) {
    if (typeof candidate === 'string' && candidate.trim() !== '') return candidate
  }
  return fallback
}

/**
 * Orange 特有：是否为「保存会移除成员，需要确认」的 409。
 *
 * 契约 C1：后端错误体形如
 * `{ code: 409, message: "保存将移除 2 个分组：…；确认请重试",
 *    reason: "ORANGE_GROUP_CHAIN_PRUNE_CONFIRM",
 *    metadata: { pending_removals: "[{\"group_id\":1,\"name\":\"a\"}]" } }`。
 * 这里的 `code` 是 HTTP 状态码（数字），业务错误码只能按 `reason` 识别：
 * 优先看拦截器透传的 reason，其次看原始 axios 错误里的 response.data.reason；
 * 两者都没有时按「该端点唯一的 409 业务错误」兜底，避免旧拦截器下退化成通用失败提示。
 */
export function isGroupChainPruneConfirmError(error: unknown): boolean {
  const detail = error as
    | {
        reason?: unknown
        status?: unknown
        response?: { status?: unknown; data?: { reason?: unknown } }
      }
    | null
    | undefined
  if (!detail || typeof detail !== 'object') return false
  const reason = detail.reason ?? detail.response?.data?.reason
  // 有 reason 时以 reason 为唯一依据：该端点还有 409 ORANGE_GROUP_CHAIN_TOO_LONG，
  // 不能因为状态码是 409 就当成「需要确认删除」。
  if (typeof reason === 'string' && reason !== '') {
    return reason === ORANGE_GROUP_CHAIN_PRUNE_CONFIRM
  }
  const status = detail.status ?? detail.response?.status
  return status === 409
}

/** JSON 字符串解析失败时返回 null（不抛错，保证 409 提示本身不会把 UI 弄挂）。 */
function parseJsonSafely(raw: string): unknown {
  try {
    return JSON.parse(raw)
  } catch {
    return null
  }
}

/**
 * Orange 特有：解析 409 响应 metadata.pending_removals 里的结构化移除清单。
 *
 * 后端按契约 C1 以 **JSON 字符串**返回 `[{"group_id":1,"name":"a"}]`；这里同时兼容已解析的数组。
 * 任何一项结构不合法都认为整体解析失败（返回 null），由调用方回退本地 pendingChainRemovals，
 * 避免把半截清单渲染成错误信息。名称为空时用 #id 兜底（与后端文案口径一致）。
 */
export function parsePendingRemovalsFromError(
  error: unknown
): GroupChainPendingRemoval[] | null {
  const metadata = (error as { metadata?: unknown } | null | undefined)?.metadata
  if (!metadata || typeof metadata !== 'object') return null
  const raw = (metadata as { pending_removals?: unknown }).pending_removals
  const parsed = typeof raw === 'string' ? parseJsonSafely(raw) : raw
  if (!Array.isArray(parsed)) return null

  const removals: GroupChainPendingRemoval[] = []
  for (const item of parsed) {
    if (!item || typeof item !== 'object') return null
    const groupId = Number((item as { group_id?: unknown }).group_id)
    if (!Number.isFinite(groupId)) return null
    const name = (item as { name?: unknown }).name
    removals.push({
      group_id: groupId,
      name: typeof name === 'string' && name.trim() !== '' ? name : `#${groupId}`
    })
  }
  return removals
}

/** 移除清单 → 「a、b、#3」这样的名称串（与弹窗里的确认文案口径一致）。 */
function pendingRemovalNames(removals: GroupChainPendingRemoval[], t: TranslateKey): string {
  return removals.map((removal) => removal.name).join(t('orangeKeyChain.nameSeparator'))
}

/**
 * Orange 特有：409（ORANGE_GROUP_CHAIN_PRUNE_CONFIRM）时的提示文案。
 *
 * - 正文优先用服务端 message（契约 C1 的 message 已含「保存将移除 N 个分组：名1、名2」）；
 * - 服务端没给 message 时，用结构化清单（无则回退本地 pendingChainRemovals）自行拼一条；
 * - 末尾统一追加「请重新打开编辑窗口确认后再保存」：此刻本地 serverMemberIds 已经过期，
 *   只能重新读链（GET 纯读接口）后再确认一次。
 */
export function pruneConflictMessage(
  error: unknown,
  localRemovals: GroupChainPendingRemoval[] | undefined,
  t: TranslateKey
): string {
  // 先解析服务端清单：成功则优先用它渲染确认信息，失败再回退本地 pendingChainRemovals
  const serverRemovals = parsePendingRemovalsFromError(error)
  const removals = serverRemovals ?? localRemovals ?? []
  const serverMessage = groupChainErrorMessage(error, '')
  const base =
    serverMessage ||
    t('orangeKeyChain.pruneConfirmRequired', {
      n: removals.length,
      names: pendingRemovalNames(removals, t)
    })
  return `${base}${t('orangeKeyChain.pruneReopenHint')}`
}

/** Orange 特有：读取密钥的分组优先级链（纯读；members 含不可用成员与 state）。 */
export async function getKeyGroupChain(id: number): Promise<ApiKeyGroupChain> {
  const { data } = await apiClient.get<ApiKeyGroupChain>(`/keys/${id}/group-chain`)
  return data
}

/**
 * Orange 特有：保存密钥的分组优先级链（groupIds 有序，[0] = 新链首/主分组；[] = 清除链）。
 *
 * expectedGroupIds / confirmPrune 只在调用方提供时发送：
 * - 弹窗提交（prepareKeyGroupChainSubmit）会带上读链得到的完整成员集合，由用户勾选确认后才带
 *   confirm_prune: true；
 * - 列表侧快捷写链（改主分组 / 选兜底分组）不带 expectedGroupIds，但必须带 confirm_prune: true
 *   —— 契约 C3 下「未提供 expected_group_ids 的旧调用方」同样会被 409 拦，那是用户的主动操作，
 *   等于已经确认过本次裁剪。
 */
export async function setKeyGroupChain(
  id: number,
  groupIds: number[],
  opts: SetKeyGroupChainOptions = {}
): Promise<ApiKeyGroupChain> {
  const body: Record<string, unknown> = { group_ids: groupIds }
  if (opts.mode) body.mode = opts.mode
  if (opts.expectedGroupIds !== undefined) body.expected_group_ids = opts.expectedGroupIds
  if (opts.confirmPrune !== undefined) body.confirm_prune = opts.confirmPrune
  const { data } = await apiClient.put<ApiKeyGroupChain>(`/keys/${id}/group-chain`, body)
  return data
}

/**
 * Orange 特有：显式自愈分组链（只修剪 state ∈ {no_permission, missing} 的候选；
 * disabled / scope_mismatch 一律保留，与后端既有语义一致）。
 * 返回的 auto_pruned_groups 是本次真正被移除的分组（含名称）。
 */
export async function healKeyGroupChain(id: number): Promise<ApiKeyGroupChain> {
  const { data } = await apiClient.post<ApiKeyGroupChain>(`/keys/${id}/group-chain/heal`)
  return data
}

/**
 * 写入暂存链：失败只提示，不回滚已经保存成功的密钥字段（与历史行为一致）。
 *
 * 这里刻意不吞掉两类错误信息：
 * - 移除确认 409：按 reason 识别后，用服务端 message + 服务端结构化清单（metadata.pending_removals）
 *   渲染提示，并追加「请重新打开编辑窗口确认后再保存」（本地链成员集合已过期）；
 * - 其他业务错误都带具体 message，直接交给 UI 展示。
 */
async function flushStagedKeyGroupChain(keyId: number): Promise<void> {
  if (!stagedKeyGroupChain) return
  const { groupIds, mode, expectedGroupIds, confirmPrune, pendingRemovals, t } =
    stagedKeyGroupChain
  stagedKeyGroupChain = null
  const appStore = useAppStore()
  try {
    await setKeyGroupChain(keyId, groupIds, { mode, expectedGroupIds, confirmPrune })
  } catch (error: any) {
    if (isGroupChainPruneConfirmError(error)) {
      appStore.showError(pruneConflictMessage(error, pendingRemovals, t))
      return
    }
    appStore.showError(groupChainErrorMessage(error, t('keys.groupChain.saveFailed')))
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
