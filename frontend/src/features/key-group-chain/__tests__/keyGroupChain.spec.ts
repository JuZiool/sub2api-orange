import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'

const {
  put,
  get,
  updateKey,
  showError,
  showSuccess,
  showWarning
} = vi.hoisted(() => ({
  put: vi.fn(),
  get: vi.fn(),
  updateKey: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api', () => ({
  apiClient: { get, put },
  keysAPI: { update: updateKey, create: vi.fn(), list: vi.fn() }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess, showWarning })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import FallbackGroupPicker from '../FallbackGroupPicker.vue'
import KeyGroupChainRowCell from '../KeyGroupChainRowCell.vue'
import KeyGroupChainFormFields from '../KeyGroupChainFormFields.vue'
import {
  keyGroupChainForm,
  hydrateKeyGroupChain,
  prepareKeyGroupChainSubmit,
  resetKeyGroupChainForm,
  handleKeyGroupChainGroupChange
} from '../useKeyGroupChain'
import { keysAPI } from '../api'
import type { ApiKey, Group } from '@/types'
import type { GroupOptionView } from '../types'

const groupOptions: GroupOptionView[] = [
  {
    value: 1,
    label: 'A',
    description: null,
    rate: 1,
    userRate: null,
    peakRateEnabled: false,
    peakStart: '',
    peakEnd: '',
    peakRateMultiplier: 0,
    subscriptionType: 'standard',
    platform: 'anthropic'
  },
  {
    value: 2,
    label: 'B',
    description: null,
    rate: 1,
    userRate: null,
    peakRateEnabled: false,
    peakStart: '',
    peakEnd: '',
    peakRateMultiplier: 0,
    subscriptionType: 'standard',
    platform: 'anthropic'
  },
  {
    value: 3,
    label: 'C',
    description: null,
    rate: 1,
    userRate: null,
    peakRateEnabled: false,
    peakStart: '',
    peakEnd: '',
    peakRateMultiplier: 0,
    subscriptionType: 'standard',
    platform: 'openai'
  }
]

const groups = groupOptions.map((o) => ({ id: o.value, platform: o.platform })) as unknown as Group[]

const row = (overrides: Partial<ApiKey> & Record<string, unknown> = {}): ApiKey =>
  ({
    id: 1,
    group_id: 1,
    group: { id: 1, name: 'A', platform: 'anthropic' },
    groups: [
      { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' } },
      { group_id: 2, priority: 2, group: { id: 2, name: 'B', platform: 'anthropic' } }
    ],
    group_chain_mode: 'fallback',
    ...overrides
  }) as unknown as ApiKey

const stubs = {
  GroupBadge: { props: ['name'], template: '<span data-test="badge">{{ name }}</span>' },
  GroupOptionItem: { props: ['name'], template: '<span data-test="option-item">{{ name }}</span>' },
  Icon: { template: '<i />' },
}

describe('fallback picker', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('renders current fallback and picks candidate', async () => {
    const wrapper = mount(FallbackGroupPicker, {
      props: { row: row(), groupOptions, userGroupRates: {} },
      global: { stubs }
    })
    expect(wrapper.get('[data-test="fallback-group-trigger"]').text()).toContain('B')
    await wrapper.get('[data-test="fallback-group-trigger"]').trigger('click')
    await nextTick()
    const items = document.body.querySelectorAll('[data-test="option-item"]')
    // 候选只剩同平台且非主分组的分组 B（当前兜底），再加“不设置兜底分组”行
    expect(Array.from(items).map((el) => el.textContent)).toEqual(['B'])
    const buttons = document.body.querySelectorAll('button')
    const optionButton = Array.from(buttons).find((el) => el.textContent?.includes('B'))
    expect(optionButton).toBeTruthy()
    optionButton!.click()
    await flushPromises()
    // 与当前兜底相同 → 不发请求
    expect(put).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('writes chain when selection changes', async () => {
    put.mockResolvedValue({ data: {} })
    const wrapper = mount(FallbackGroupPicker, {
      props: { row: row(), groupOptions, userGroupRates: {} },
      global: { stubs }
    })
    await wrapper.get('[data-test="fallback-group-trigger"]').trigger('click')
    await nextTick()
    const buttons = document.body.querySelectorAll('button')
    const nullRow = Array.from(buttons).find((el) => el.textContent?.includes('keys.noFallbackGroup'))
    expect(nullRow).toBeTruthy()
    nullRow!.click()
    await flushPromises()
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', { group_ids: [], mode: 'fallback' })
    expect(showSuccess).toHaveBeenCalledWith('keys.fallbackGroupChangedSuccess')
    wrapper.unmount()
  })
})

describe('row cell', () => {
  it('renders aggregate chain instead of slot', async () => {
    const wrapper = mount(KeyGroupChainRowCell, {
      props: {
        row: row({ group_chain_mode: 'aggregate', groups: [
          { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' } },
          { group_id: 2, priority: 2, group: { id: 2, name: 'B', platform: 'anthropic' } }
        ] }),
        groupOptions,
        userGroupRates: {}
      },
      slots: { default: '<div data-test="primary">primary</div>' },
      global: { stubs }
    })
    expect(wrapper.find('[data-test="aggregate-group-chain-cell"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="primary"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('1')
    expect(wrapper.text()).toContain('2')
  })

  it('renders upstream slot plus fallback picker for normal keys', () => {
    const wrapper = mount(KeyGroupChainRowCell, {
      props: { row: row(), groupOptions, userGroupRates: {} },
      slots: { default: '<div data-test="primary">primary</div>' },
      global: { stubs }
    })
    expect(wrapper.find('[data-test="primary"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="fallback-group-trigger"]').exists()).toBe(true)
  })
})

describe('form fields', () => {
  beforeEach(() => {
    resetKeyGroupChainForm()
    vi.clearAllMocks()
  })

  it('toggles aggregate mode and syncs primary group', async () => {
    const formData = reactive({ group_id: 1 as number | null })
    const wrapper = mount(KeyGroupChainFormFields, {
      props: {
        formData,
        groupOptions,
        formGroupOptions: groupOptions,
        showEdit: false,
        selectedKey: null,
        createProvider: 'anthropic'
      },
      slots: { default: '<div data-test="primary-field">primary field</div>' },
      global: {
        stubs: {
          ...stubs,
          Select: { props: ['modelValue', 'options'], template: '<select><slot /></select>' }
        }
      }
    })
    expect(wrapper.find('[data-test="primary-field"]').exists()).toBe(true)
    await wrapper.get('[data-test="aggregate-mode-toggle"]').trigger('click')
    expect(wrapper.find('[data-test="primary-field"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="aggregate-group-chain"]').exists()).toBe(true)
    expect(keyGroupChainForm.aggregateMode).toBe(true)
    expect(keyGroupChainForm.aggregateGroupIds).toEqual([1, null])
    await wrapper.get('[data-test="aggregate-mode-toggle"]').trigger('click')
    expect(keyGroupChainForm.aggregateMode).toBe(false)
    expect(formData.group_id).toBe(1)
  })
})

describe('submit preparation', () => {
  beforeEach(() => {
    resetKeyGroupChainForm()
    vi.clearAllMocks()
  })

  it('stages an empty chain for plain keys and flushes on update', async () => {
    const formData = { group_id: 1 as number | null }
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(updateKey).toHaveBeenCalledWith(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', { group_ids: [], mode: 'fallback' })
  })

  it('rejects duplicate aggregate groups', () => {
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = [1, 1]
    expect(prepareKeyGroupChainSubmit({ group_id: 1 }, groups, (key) => key)).toBe(false)
    expect(showError).toHaveBeenCalledWith('keys.aggregateGroupDuplicate')
  })

  it('clears incompatible fallback when primary group changes', async () => {
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    const refresh = vi.fn()
    const handled = await handleKeyGroupChainGroupChange(
      row({ groups: [
        { group_id: 1, priority: 1 },
        { group_id: 2, priority: 2, group: { id: 2, name: 'B', platform: 'anthropic' } }
      ] }),
      3,
      groups,
      refresh,
      (key) => key
    )
    expect(handled).toBe(true)
    expect(updateKey).toHaveBeenCalledWith(1, { group_id: 3 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', { group_ids: [], mode: 'fallback' })
    expect(showSuccess).toHaveBeenCalledWith('keys.groupChangedFallbackClearedSuccess')
    expect(refresh).toHaveBeenCalled()
  })
})

describe('unavailable chain members', () => {
  beforeEach(() => {
    resetKeyGroupChainForm()
    vi.clearAllMocks()
  })

  it('records unavailable members while hydrating the chain', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 1,
        members: [
          { group_id: 1, priority: 1 },
          { group_id: 99, priority: 2 },
          { group_id: 2, priority: 3 }
        ]
      }
    })
    const setPrimary = vi.fn()
    await hydrateKeyGroupChain(row(), () => true, setPrimary, groupOptions)
    expect(keyGroupChainForm.aggregateMode).toBe(true)
    expect(keyGroupChainForm.aggregateGroupIds).toEqual([1, 99, 2])
    expect(keyGroupChainForm.unavailableGroupIds).toEqual([99])
    expect(setPrimary).toHaveBeenCalledWith(1)
  })

  it('marks unavailable aggregate members in the editor and the row cell', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 1,
        members: [
          { group_id: 1, priority: 1 },
          { group_id: 99, priority: 2 }
        ]
      }
    })
    const fieldWrapper = mount(KeyGroupChainFormFields, {
      props: {
        formData: reactive({ group_id: 1 as number | null }),
        groupOptions,
        formGroupOptions: groupOptions,
        showEdit: true,
        selectedKey: row({ group_chain_mode: 'aggregate' }),
        createProvider: 'anthropic'
      },
      slots: { default: '<div data-test="primary-field">primary field</div>' },
      global: {
        stubs: {
          ...stubs,
          Select: { props: ['modelValue', 'options'], template: '<select><slot /></select>' }
        }
      }
    })
    await flushPromises()
    expect(fieldWrapper.findAll('[data-test="chain-group-unavailable"]')).toHaveLength(1)
    expect(fieldWrapper.get('[data-test="chain-unavailable-hint"]').text()).toBe(
      'orangeKeyGroupChain.unavailableChainHint'
    )
    fieldWrapper.unmount()

    const cellWrapper = mount(KeyGroupChainRowCell, {
      props: {
        row: row({
          group_chain_mode: 'aggregate',
          groups: [
            { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' } },
            { group_id: 99, priority: 2, group: { id: 99, name: 'Gone', platform: 'anthropic' } }
          ]
        }),
        groupOptions,
        userGroupRates: {}
      },
      global: { stubs }
    })
    expect(cellWrapper.findAll('[data-test="chain-member-unavailable"]')).toHaveLength(1)
    cellWrapper.unmount()
  })

  it('blocks submit when the chain head becomes unavailable', () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = [99, 1]
    keyGroupChainForm.unavailableGroupIds = [99]
    const formData = { group_id: 99 as number | null }
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(false)
    expect(showError).toHaveBeenCalledWith('orangeKeyGroupChain.primaryUnavailable')
    expect(put).not.toHaveBeenCalled()
  })

  it('skips unavailable non-head members and keeps the rest of the chain', async () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = [1, 99, 2]
    keyGroupChainForm.unavailableGroupIds = [99]
    const formData = { group_id: 1 as number | null }
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    expect(formData.group_id).toBe(1)
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [1, 2],
      mode: 'aggregate'
    })
    expect(showWarning).not.toHaveBeenCalled()
  })

  it('clears the chain when fewer than two available groups remain', async () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = [1, 99]
    keyGroupChainForm.unavailableGroupIds = [99]
    const formData = { group_id: 1 as number | null }
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    // 主分组保持链首，链退化为空（模式仍记录为 aggregate）
    expect(formData.group_id).toBe(1)
    expect(showWarning).toHaveBeenCalledWith('orangeKeyGroupChain.aggregateChainCleared')
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [],
      mode: 'aggregate'
    })
  })

  it('clears an unavailable fallback group before validating', async () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.fallbackGroupId = 99
    keyGroupChainForm.unavailableGroupIds = [99]
    const formData = { group_id: 1 as number | null }
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    expect(keyGroupChainForm.fallbackGroupId).toBeNull()
    expect(showWarning).toHaveBeenCalledWith('orangeKeyGroupChain.fallbackCleared')
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', { group_ids: [], mode: 'fallback' })
  })

  it('keeps the existing behaviour when nothing is unavailable', async () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.fallbackGroupId = 2
    keyGroupChainForm.unavailableGroupIds = []
    const formData = { group_id: 1 as number | null }
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    expect(showWarning).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [1, 2],
      mode: 'fallback'
    })
  })

  it('still rejects duplicate aggregate groups when nothing is unavailable', () => {
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = [1, 1]
    keyGroupChainForm.unavailableGroupIds = []
    expect(prepareKeyGroupChainSubmit({ group_id: 1 }, groups, (key) => key)).toBe(false)
    expect(showError).toHaveBeenCalledWith('keys.aggregateGroupDuplicate')
    expect(showWarning).not.toHaveBeenCalled()
  })

  it('treats the backend available flag as authoritative over the option list', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 1,
        members: [
          { group_id: 1, priority: 1, available: true },
          // 分组仍在可绑定列表里，但后端判定"用户已无权限"（同源判定）→ 以 false 为准
          { group_id: 2, priority: 2, available: false, unavailable_reason: 'not_entitled' }
        ]
      }
    })
    await hydrateKeyGroupChain(row(), () => true, vi.fn(), groupOptions)
    expect(keyGroupChainForm.backendAvailability).toEqual({ 1: true, 2: false })
    expect(keyGroupChainForm.unavailableGroupIds).toEqual([2])
  })

  it('does not drop members the backend reports as available even if missing from options', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 1,
        members: [
          { group_id: 1, priority: 1, available: true },
          // 订阅型等场景：选项列表里没有，但后端说可用 → 不能误剔除
          { group_id: 88, priority: 2, available: true }
        ]
      }
    })
    await hydrateKeyGroupChain(row(), () => true, vi.fn(), groupOptions)
    expect(keyGroupChainForm.unavailableGroupIds).toEqual([])
  })

  it('marks nothing when the option list is empty and the backend gives no flags', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 1,
        members: [
          { group_id: 1, priority: 1 },
          { group_id: 2, priority: 2 }
        ]
      }
    })
    await hydrateKeyGroupChain(row(), () => true, vi.fn(), [])
    expect(keyGroupChainForm.unavailableGroupIds).toEqual([])
  })
})



