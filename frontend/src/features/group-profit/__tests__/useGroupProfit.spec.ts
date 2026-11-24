/**
 * Orange 特有：features/group-profit 的核心状态逻辑单测。
 *
 * 覆盖从 GroupsView 抽出的行为：按可见列加载利润、编辑回填（缓存 / 补拉 / 用户已改）、
 * 前端校验以及与「已知值 / 已改动」相关的保存判定。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminGroup } from '@/types'

const { getGroupProfits, updateGroupUpstreamRate, showError, authState } =
  vi.hoisted(() => ({
    getGroupProfits: vi.fn(),
    updateGroupUpstreamRate: vi.fn(),
    showError: vi.fn(),
    authState: { isSimpleMode: false }
  }))

vi.mock('@/api/orange', () => ({
  getOrangeGroupProfits: getGroupProfits,
  updateOrangeGroupUpstreamRate: updateGroupUpstreamRate
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import { useGroupProfit, type GroupProfitController } from '../useGroupProfit'

const profitItem = (overrides: Record<string, unknown> = {}) => ({
  group_id: 1,
  upstream_rate_multiplier: 0.8,
  today_revenue: 10,
  today_upstream_price: 5,
  today_upstream_cost: 4,
  today_profit: 6,
  yesterday_revenue: 4,
  yesterday_upstream_price: 2,
  yesterday_upstream_cost: 1.6,
  yesterday_profit: 2.4,
  total_revenue: 100,
  total_upstream_price: 50,
  total_upstream_cost: 40,
  total_profit: -12,
  ...overrides
})

const group = (id: number) => ({ id }) as unknown as AdminGroup

const mountProfit = (options: { hidden?: string[] } = {}) => {
  const groups = ref<AdminGroup[]>([])
  const hidden = ref(new Set(options.hidden ?? []))
  let controller!: GroupProfitController
  const Host = defineComponent({
    setup() {
      controller = useGroupProfit({
        groups,
        isColumnVisible: (key) => !hidden.value.has(key)
      })
      return () => null
    }
  })
  return { wrapper: mount(Host), groups, hidden, controller }
}

const loadGroups = async (
  groups: { value: AdminGroup[] },
  ids: number[] = [1]
) => {
  groups.value = ids.map(group)
  await flushPromises()
  await flushPromises()
}

describe('useGroupProfit', () => {
  beforeEach(() => {
    getGroupProfits.mockReset()
    updateGroupUpstreamRate.mockReset()
    showError.mockReset()
    authState.isSimpleMode = false
    getGroupProfits.mockResolvedValue({
      items: [profitItem()],
      generated_at: '',
      cached: false
    })
    updateGroupUpstreamRate.mockResolvedValue({
      group_id: 1,
      upstream_rate_multiplier: 0.8
    })
  })

  it('loads profits for the current page once the visible columns consume them', async () => {
    const { groups, controller } = mountProfit()

    await loadGroups(groups)

    expect(getGroupProfits).toHaveBeenCalledTimes(1)
    expect(getGroupProfits).toHaveBeenCalledWith({ ids: [1] })
    expect(controller.profitMap.value.get(1)?.upstream_rate_multiplier).toBe(0.8)
    expect(controller.profitLoading.value).toBe(false)
  })

  it('skips the profit request when both consuming columns are hidden', async () => {
    const { groups } = mountProfit({ hidden: ['profit', 'upstream_rate_multiplier'] })

    await loadGroups(groups)

    expect(getGroupProfits).not.toHaveBeenCalled()
  })

  it('skips the profit request in simple mode', async () => {
    authState.isSimpleMode = true
    const { groups } = mountProfit()

    await loadGroups(groups)

    expect(getGroupProfits).not.toHaveBeenCalled()
  })

  it('fills the edit rate from the loaded profit cache without an extra request', async () => {
    const { groups, controller } = mountProfit()
    await loadGroups(groups)
    getGroupProfits.mockClear()

    controller.beginEditUpstreamRate(1)

    expect(controller.editUpstreamRate.value).toBe(0.8)
    expect(getGroupProfits).not.toHaveBeenCalled()
  })

  it('fetches a single group to fill the edit rate when the cache is missing', async () => {
    const { controller } = mountProfit()

    controller.beginEditUpstreamRate(1)
    await flushPromises()

    expect(getGroupProfits).toHaveBeenCalledWith({ ids: [1] })
    expect(controller.editUpstreamRate.value).toBe(0.8)
  })

  it('does not overwrite a rate the user already edited', async () => {
    const { controller } = mountProfit()

    controller.beginEditUpstreamRate(1)
    controller.markEditUpstreamRateDirty()
    controller.editUpstreamRate.value = 2.5
    await flushPromises()

    expect(controller.editUpstreamRate.value).toBe(2.5)
  })

  it('validates the rate against the backend contract', () => {
    const { controller } = mountProfit()

    controller.beginCreateUpstreamRate()
    expect(controller.validateCreateUpstreamRate()).toBe(true)
    expect(showError).not.toHaveBeenCalled()

    controller.createUpstreamRate.value = 1001
    expect(controller.validateCreateUpstreamRate()).toBe(false)
    expect(showError).toHaveBeenCalledWith(
      'admin.groups.form.upstreamRateMultiplierInvalid'
    )

    controller.createUpstreamRate.value = 0.12345
    expect(controller.validateCreateUpstreamRate()).toBe(false)

    controller.createUpstreamRate.value = 0
    expect(controller.validateCreateUpstreamRate()).toBe(true)

    controller.editUpstreamRate.value = -1
    expect(controller.validateEditUpstreamRate()).toBe(false)
  })

  it('writes the edited rate only when it is known or dirty', async () => {
    const { groups, controller } = mountProfit()
    await loadGroups(groups)

    // 缓存命中 → 已知值，保存时写入
    controller.beginEditUpstreamRate(1)
    await controller.persistEditedUpstreamRate()
    expect(updateGroupUpstreamRate).toHaveBeenCalledWith(1, 0.8)

    updateGroupUpstreamRate.mockClear()
    // 缓存缺失且补拉失败 → 既非已知值也没改过，跳过写入，避免用默认 1.0 覆盖
    getGroupProfits.mockRejectedValueOnce(new Error('network'))
    controller.beginEditUpstreamRate(2)
    await flushPromises()
    await controller.persistEditedUpstreamRate()
    expect(updateGroupUpstreamRate).not.toHaveBeenCalled()

    // 用户手动改过 → 即使未取到已知值也写入
    controller.beginEditUpstreamRate(2)
    controller.markEditUpstreamRateDirty()
    controller.editUpstreamRate.value = 1.5
    await flushPromises()
    await controller.persistEditedUpstreamRate()
    expect(updateGroupUpstreamRate).toHaveBeenCalledWith(2, 1.5)
  })

  it('persists the created rate for the returned group id', async () => {
    const { controller } = mountProfit()

    controller.beginCreateUpstreamRate()
    controller.createUpstreamRate.value = 0.6
    await controller.persistCreatedUpstreamRate(9)

    expect(updateGroupUpstreamRate).toHaveBeenCalledWith(9, 0.6)

    updateGroupUpstreamRate.mockClear()
    await controller.persistCreatedUpstreamRate(undefined)
    expect(updateGroupUpstreamRate).not.toHaveBeenCalled()
  })
})
