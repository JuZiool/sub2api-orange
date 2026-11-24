/**
 * Orange 特有：GroupsView 的「上游成本倍率」表单接线回归测试。
 *
 * GroupsView 只保留挂载点，真正的输入与保存逻辑在 features/group-profit；
 * 这里通过真实挂载 GroupsView 验证创建 / 编辑两条链路：
 * - 表单里能渲染出上游成本倍率输入框，创建时默认 1、编辑时按利润缓存回填；
 * - 官方分组接口请求体不含 upstream_rate_multiplier；
 * - 保存成功后按「分组 id + 输入值」写入 Orange 自建表。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'

const {
  listGroups,
  getAllGroups,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getGroupProfits,
  updateGroupUpstreamRate,
  createGroup,
  updateGroup,
  getLiveCapability,
  listAccounts,
  showError,
  showSuccess,
  isCurrentStep,
  nextStep,
  authState
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getAllGroups: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getGroupProfits: vi.fn(),
  updateGroupUpstreamRate: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  getLiveCapability: vi.fn(),
  listAccounts: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  isCurrentStep: vi.fn(),
  nextStep: vi.fn(),
  authState: { isSimpleMode: false }
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: getAllGroups,
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      create: createGroup,
      update: updateGroup,
      delete: vi.fn(),
      updateSortOrder: vi.fn()
    },
    accounts: {
      list: listAccounts,
      getById: vi.fn()
    }
  }
}))

vi.mock('@/api/orange', () => ({
  getOrangeGroupProfits: getGroupProfits,
  updateOrangeGroupUpstreamRate: updateGroupUpstreamRate
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep, nextStep })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const createGroupRow = (overrides: Partial<AdminGroup> = {}): AdminGroup => ({
  id: 1,
  name: 'Core Anthropic',
  description: null,
  platform: 'anthropic',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  messages_dispatch_model_config: undefined,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 3,
  active_account_count: 2,
  rate_limited_account_count: 1,
  model_allowlist: undefined,
  sort_order: 10,
  ...overrides
})

const AppLayoutStub = { template: '<div><slot /></div>' }

const TablePageLayoutStub = {
  template: `
    <div>
      <slot name="filters" />
      <slot name="table" />
      <slot name="pagination" />
    </div>
  `
}

const DataTableStub = {
  props: ['columns', 'data'],
  emits: ['sort'],
  template: `
    <div>
      <div data-test="rows">{{ data.map((row) => row.name).join(',') }}</div>
      <div v-if="data.length" data-test="actions-cell">
        <slot name="cell-actions" :row="data[0]" />
      </div>
    </div>
  `
}

const SelectStub = {
  props: ['modelValue', 'options', 'placeholder'],
  emits: ['update:modelValue', 'change'],
  template: `
    <select
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value); $emit('change')"
    >
      <option v-for="option in options" :key="String(option.value)" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `
}

const BaseDialogStub = {
  props: ['show'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
}

const IconStub = {
  props: ['name'],
  template: '<span data-test="icon">{{ name }}</span>'
}

const mountView = async () => {
  const wrapper = mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        PlatformIcon: true,
        Icon: IconStub,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: { template: '<div><slot /></div>' }
      }
    }
  })
  await flushPromises()
  return wrapper
}

const inputValue = (element: Element) => (element as HTMLInputElement).value

describe('admin GroupsView × group-profit 接线', () => {
  beforeEach(() => {
    localStorage.clear()

    listGroups.mockReset()
    getAllGroups.mockReset()
    getModelAllowlistCandidates.mockReset()
    getUsageSummary.mockReset()
    getCapacitySummary.mockReset()
    getGroupProfits.mockReset()
    updateGroupUpstreamRate.mockReset()
    createGroup.mockReset()
    updateGroup.mockReset()
    getLiveCapability.mockReset()
    listAccounts.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    isCurrentStep.mockReset()
    nextStep.mockReset()
    authState.isSimpleMode = false

    listGroups.mockResolvedValue({
      items: [createGroupRow()],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getAllGroups.mockResolvedValue([])
    getModelAllowlistCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getGroupProfits.mockResolvedValue({
      items: [
        {
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
          total_profit: -12
        }
      ],
      generated_at: '',
      cached: false
    })
    updateGroupUpstreamRate.mockResolvedValue({
      group_id: 1,
      upstream_rate_multiplier: 1
    })
    getLiveCapability.mockResolvedValue({ supported: false })
    listAccounts.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })
    isCurrentStep.mockReturnValue(false)
  })

  it('创建分组时默认 1，并按输入值写入上游成本倍率（官方请求体不含该字段）', async () => {
    createGroup.mockResolvedValue(createGroupRow({ id: 42 }))
    const wrapper = await mountView()

    await wrapper.get('[data-tour="groups-create-btn"]').trigger('click')
    await flushPromises()

    await wrapper.get('#create-group-form input').setValue('Orange Group')
    const rateInput = wrapper.get(
      '[data-testid="create-upstream-rate-multiplier"]'
    )
    expect(inputValue(rateInput.element)).toBe('1')
    await rateInput.setValue('0.7')

    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledTimes(1)
    expect(createGroup.mock.calls[0][0]).not.toHaveProperty(
      'upstream_rate_multiplier'
    )
    expect(updateGroupUpstreamRate).toHaveBeenCalledWith(42, 0.7)
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.groupCreated')
  })

  it('编辑分组时按利润缓存回填，并在保存后写入上游成本倍率', async () => {
    updateGroup.mockResolvedValue(undefined)
    const wrapper = await mountView()

    const editButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('common.edit'))
    expect(editButton, 'edit action button').toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()

    const rateInput = wrapper.get('[data-testid="edit-upstream-rate-multiplier"]')
    expect(inputValue(rateInput.element)).toBe('0.8')
    await rateInput.setValue('1.2')

    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(updateGroup.mock.calls[0][1]).not.toHaveProperty(
      'upstream_rate_multiplier'
    )
    expect(updateGroupUpstreamRate).toHaveBeenCalledWith(1, 1.2)
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.groupUpdated')
  })
})
