/**
 * Orange 特有：分组利润 / 上游成本倍率状态与操作。
 *
 * 上游 GroupsView.vue 只保留接线（一次初始化、列定义、单元格/表单挂载、校验与保存调用），
 * 业务逻辑全部在本文件：
 * - 按可见列与当前页分组 id 批量拉取利润数据（两列共用同一接口）；
 * - 编辑弹窗回填上游成本倍率（缓存优先，缺失时异步补拉，不阻塞弹窗打开）；
 * - 上游成本倍率前端校验（0 ~ 1000，最多 4 位小数，与后端契约一致）；
 * - 创建 / 编辑成功后单独写入 Orange 自建表（失败只提示，不回滚分组本身）。
 *
 * 未启用（简单模式 / 两个消费列都隐藏）时不会发出任何 Orange 请求，行为与上游一致。
 */
import {
  computed,
  inject,
  provide,
  ref,
  watch,
  type InjectionKey,
  type Ref
} from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { OrangeGroupProfitItem } from '@/api/orange'
import type { AdminGroup } from '@/types'
import { fetchGroupProfits, saveGroupUpstreamRate } from './api'

const GROUP_PROFIT_KEY: InjectionKey<GroupProfitController> = Symbol(
  'orange-group-profit'
)

export interface GroupProfitInit {
  /** 当前页分组列表（用于收集利润查询的分组 id）。 */
  groups: Ref<AdminGroup[]>
  /** 上游列可见性判断（列设置开关）。 */
  isColumnVisible: (key: string) => boolean
}

export interface GroupProfitController {
  /** 分组 id → 利润 / 上游成本倍率。 */
  profitMap: Ref<Map<number, OrangeGroupProfitItem>>
  /** 利润数据加载中（单元格显示 "—"）。 */
  profitLoading: Ref<boolean>
  /** 创建表单里的上游成本倍率。 */
  createUpstreamRate: Ref<number>
  /** 编辑表单里的上游成本倍率。 */
  editUpstreamRate: Ref<number>
  /** 打开创建弹窗时重置上游成本倍率。 */
  beginCreateUpstreamRate: () => void
  /** 打开编辑弹窗时回填上游成本倍率（优先缓存，缺失时异步补拉）。 */
  beginEditUpstreamRate: (groupId: number | null | undefined) => void
  /** 用户手动改过编辑表单里的上游成本倍率。 */
  markEditUpstreamRateDirty: () => void
  /** 创建前校验上游成本倍率（非法时提示并返回 false）。 */
  validateCreateUpstreamRate: () => boolean
  /** 更新前校验上游成本倍率（非法时提示并返回 false）。 */
  validateEditUpstreamRate: () => boolean
  /** 创建成功后写入上游成本倍率。 */
  persistCreatedUpstreamRate: (groupId: number | null | undefined) => Promise<void>
  /** 更新成功后按需写入上游成本倍率。 */
  persistEditedUpstreamRate: () => Promise<void>
}

/** Orange 特有：在 GroupsView setup 中初始化，并向子树提供状态。 */
export function useGroupProfit(init: GroupProfitInit): GroupProfitController {
  const { t } = useI18n()
  const appStore = useAppStore()
  const authStore = useAuthStore()

  const profitMap = ref<Map<number, OrangeGroupProfitItem>>(new Map())
  const profitLoading = ref(false)
  const createUpstreamRate = ref(1)
  const editUpstreamRate = ref(1)
  // 编辑表单里的上游倍率是否为“已知值”（来自利润缓存或补拉结果）
  const editUpstreamRateKnown = ref(false)
  // 用户是否手动改过编辑表单里的上游倍率输入框
  const editUpstreamRateDirty = ref(false)
  // 当前编辑弹窗对应的分组 id（关闭弹窗时 editingGroup 会被清空，这里提前记住）
  const editingGroupId = ref<number | null>(null)
  // 补拉请求的世代号：弹窗关闭或换分组后丢弃过期结果
  let editFillToken = 0

  // 上游成本倍率列与利润列共用 Orange 利润接口，任一可见才请求
  const hasVisibleProfitConsumer = computed(
    () =>
      !authStore.isSimpleMode &&
      (init.isColumnVisible('profit') ||
        init.isColumnVisible('upstream_rate_multiplier'))
  )

  /** 加载当前页分组的利润与上游成本倍率（未显示相关列时不发请求）。 */
  const loadGroupProfits = async () => {
    if (!hasVisibleProfitConsumer.value) {
      profitLoading.value = false
      return
    }
    const ids = init.groups.value.map((group) => group.id)
    if (ids.length === 0) {
      profitMap.value = new Map()
      profitLoading.value = false
      return
    }
    profitLoading.value = true
    try {
      const data = await fetchGroupProfits(ids)
      const map = new Map<number, OrangeGroupProfitItem>()
      for (const item of data.items) {
        map.set(item.group_id, item)
      }
      profitMap.value = map
    } catch (error) {
      // 接口未就绪或失败时保留上一份数据，界面按 0 / 默认倍率展示，不打断分组列表
      console.error('Error loading group profit summary:', error)
    } finally {
      profitLoading.value = false
    }
  }

  /** 单独查询某个分组的利润数据（编辑弹窗回填上游成本倍率时兜底使用）。 */
  const loadGroupProfitForGroup = async (
    groupId: number
  ): Promise<OrangeGroupProfitItem | null> => {
    try {
      const data = await fetchGroupProfits([groupId])
      const item =
        data.items.find((entry) => entry.group_id === groupId) ?? null
      if (item) {
        const next = new Map(profitMap.value)
        next.set(groupId, item)
        profitMap.value = next
      }
      return item
    } catch (error) {
      console.error('Error loading group profit detail:', error)
      return null
    }
  }

  /** 保存上游成本倍率（独立于官方分组接口；失败只提示，不回滚分组本身）。 */
  const persistUpstreamRate = async (
    groupId: number | null | undefined,
    upstreamRateMultiplier: number
  ) => {
    if (!groupId || authStore.isSimpleMode) return
    try {
      await saveGroupUpstreamRate(groupId, upstreamRateMultiplier)
    } catch (error: unknown) {
      appStore.showError(
        extractApiErrorMessage(
          error,
          t('admin.groups.upstreamRateSaveFailed')
        )
      )
      console.error('Error saving group upstream rate:', error)
    }
  }

  const beginCreateUpstreamRate = () => {
    createUpstreamRate.value = 1
  }

  const beginEditUpstreamRate = (groupId: number | null | undefined) => {
    editFillToken += 1
    editingGroupId.value = typeof groupId === 'number' ? groupId : null
    editUpstreamRateDirty.value = false
    const cached =
      editingGroupId.value === null
        ? undefined
        : profitMap.value.get(editingGroupId.value)?.upstream_rate_multiplier
    editUpstreamRate.value = cached ?? 1
    editUpstreamRateKnown.value = cached !== undefined
    if (
      cached === undefined &&
      editingGroupId.value !== null &&
      !authStore.isSimpleMode
    ) {
      void fillUpstreamRateForEdit(editingGroupId.value, editFillToken)
    }
  }

  const fillUpstreamRateForEdit = async (groupId: number, token: number) => {
    const item = await loadGroupProfitForGroup(groupId)
    if (!item) return
    // 弹窗已关闭或已换成别的分组：丢弃过期结果，不写回输入框
    if (token !== editFillToken) return
    editUpstreamRateKnown.value = true
    // 用户已手动改过则不覆盖输入框，避免抢掉用户的输入
    if (!editUpstreamRateDirty.value) {
      editUpstreamRate.value = item.upstream_rate_multiplier
    }
  }

  /** 上游成本倍率前端校验，与后端契约一致（0 ~ 1000，最多 4 位小数）。 */
  const isValidUpstreamRateMultiplier = (value: unknown): boolean => {
    const rate = Number(value)
    if (!Number.isFinite(rate) || rate < 0 || rate > 1000) return false
    return Math.abs(rate * 10000 - Math.round(rate * 10000)) < 1e-6
  }

  const validateUpstreamRateMultiplier = (value: unknown): boolean => {
    if (isValidUpstreamRateMultiplier(value)) return true
    appStore.showError(t('admin.groups.form.upstreamRateMultiplierInvalid'))
    return false
  }

  const validateCreateUpstreamRate = () =>
    validateUpstreamRateMultiplier(createUpstreamRate.value)

  const validateEditUpstreamRate = () =>
    validateUpstreamRateMultiplier(editUpstreamRate.value)

  const persistCreatedUpstreamRate = async (
    groupId: number | null | undefined
  ) => {
    await persistUpstreamRate(groupId, createUpstreamRate.value)
  }

  const persistEditedUpstreamRate = async () => {
    // 补拉未完成且用户没改过时跳过写入，既不阻塞保存，也不会用默认 1.0 覆盖已配置值
    if (!editUpstreamRateKnown.value && !editUpstreamRateDirty.value) return
    await persistUpstreamRate(editingGroupId.value, editUpstreamRate.value)
  }

  // 上游列可见性来自 GroupsView 的列设置（hiddenColumns），这里跟随其变化补拉；
  // 分组列表刷新（翻页 / 保存后重载）也按当前页 id 重新加载，与改造前调用时机一致。
  watch(
    [
      () => init.groups.value,
      () => hasVisibleProfitConsumer.value
    ],
    () => {
      void loadGroupProfits()
    }
  )

  const controller: GroupProfitController = {
    profitMap,
    profitLoading,
    createUpstreamRate,
    editUpstreamRate,
    beginCreateUpstreamRate,
    beginEditUpstreamRate,
    markEditUpstreamRateDirty: () => {
      editUpstreamRateDirty.value = true
    },
    validateCreateUpstreamRate,
    validateEditUpstreamRate,
    persistCreatedUpstreamRate,
    persistEditedUpstreamRate
  }
  provide(GROUP_PROFIT_KEY, controller)
  return controller
}

/** Orange 特有：读取 useGroupProfit 提供的状态（单元格 / 表单组件使用）。 */
export function useGroupProfitState(): GroupProfitController {
  const state = inject(GROUP_PROFIT_KEY)
  if (!state) {
    throw new Error(
      'useGroupProfitState() 必须在 useGroupProfit() 的子树中调用'
    )
  }
  return state
}
