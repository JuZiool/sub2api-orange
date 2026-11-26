import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'

const {
  put,
  get,
  post,
  updateKey,
  showError,
  showSuccess,
  showWarning
} = vi.hoisted(() => ({
  put: vi.fn(),
  get: vi.fn(),
  post: vi.fn(),
  updateKey: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api', () => ({
  apiClient: { get, put, post },
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
  pendingChainRemovals,
  healKeyGroupChainAndRefresh,
  resetKeyGroupChainForm,
  handleKeyGroupChainGroupChange,
  saveFallbackGroupSelection
} from '../useKeyGroupChain'
import {
  isGroupChainPruneConfirmError,
  keysAPI,
  parsePendingRemovalsFromError,
  pruneConflictMessage
} from '../api'
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

/** 测试用的 t：把命名参数也拼进结果，便于断言「确认信息里用了哪份移除清单」。 */
const tNamed = (key: string, named?: Record<string, unknown>): string => {
  if (!named) return key
  const args = Object.entries(named)
    .map(([name, value]) => `${name}=${String(value)}`)
    .join(',')
  return `${key}(${args})`
}

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

/**
 * v0.2.25-8 的链视图：链上 5 个成员全部返回，其中 3 个不可用（无权限 / 停用 / 已删除）。
 * 旧实现会在读链时静默剔除它们，前端于是只看到更小的链——这正是本轮修复的缺陷。
 */
const fiveMemberChain = () => ({
  api_key_id: 1,
  mode: 'aggregate',
  primary_group_id: 2,
  members: [
    { group_id: 2, priority: 1, group: { id: 2, name: '自用主分组', platform: 'anthropic' }, state: 'available' },
    { group_id: 15, priority: 2, group: { id: 15, name: '自用生图', platform: 'anthropic' }, state: 'no_permission' },
    { group_id: 16, priority: 3, group: { id: 16, name: '自用圆模', platform: 'anthropic' }, state: 'disabled' },
    { group_id: 17, priority: 4, group: { id: 17, name: '已删分组', platform: 'anthropic' }, state: 'missing' },
    { group_id: 18, priority: 5, group: { id: 18, name: '自用备用', platform: 'anthropic' }, state: 'available' }
  ],
  auto_pruned_group_ids: [],
  auto_pruned_groups: [],
  auto_promoted_group_id: 0
})

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
    // 契约 C3：列表侧的快捷写链是用户主动操作，必须显式带上 confirm_prune: true，
    // 否则「清除兜底」会被服务端当成静默裁剪拦成 409。
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [],
      mode: 'fallback',
      confirm_prune: true
    })
    expect(showSuccess).toHaveBeenCalledWith('keys.fallbackGroupChangedSuccess')
    wrapper.unmount()
  })

  it('renders a deleted fallback member as #id plus the deleted label', () => {
    const wrapper = mount(FallbackGroupPicker, {
      props: {
        row: row({
          // 契约 B：state=missing 的成员只有 { group_id, priority, state }，没有 group 详情
          groups: [
            { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' }, state: 'available' },
            { group_id: 42, priority: 2, state: 'missing' }
          ]
        }),
        groupOptions,
        userGroupRates: {}
      },
      global: { stubs }
    })
    expect(wrapper.get('[data-test="fallback-group-trigger"]').text()).toContain('#42')
    expect(wrapper.get('[data-test="fallback-group-state"]').text()).toBe('orangeKeyChain.stateMissing')
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

  it('renders members without a state (old backend) with no availability marker', () => {
    const wrapper = mount(KeyGroupChainRowCell, {
      props: {
        row: row({
          group_chain_mode: 'aggregate',
          groups: [
            { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' } },
            // 缺省 state 视为可用（兼容旧后端）
            { group_id: 99, priority: 2, group: { id: 99, name: 'Gone', platform: 'anthropic' } }
          ]
        }),
        groupOptions,
        userGroupRates: {}
      },
      global: { stubs }
    })
    expect(wrapper.text()).toContain('Gone')
    expect(wrapper.find('[data-test="chain-member-unavailable"]').exists()).toBe(false)
  })

  it('keeps every chain member and marks the unavailable ones', () => {
    const wrapper = mount(KeyGroupChainRowCell, {
      props: {
        row: row({
          group_chain_mode: 'aggregate',
          groups: [
            { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' } },
            {
              group_id: 99,
              priority: 2,
              group: { id: 99, name: 'Gone', platform: 'anthropic' },
              state: 'no_permission'
            },
            { group_id: 2, priority: 3, group: { id: 2, name: 'B', platform: 'anthropic' }, state: 'available' }
          ]
        }),
        groupOptions,
        userGroupRates: {}
      },
      global: { stubs }
    })
    // 行数不变：3 个成员仍然 3 行
    expect(wrapper.findAll('[data-test="badge"]')).toHaveLength(3)
    // 只有不可用成员带状态提示
    const markers = wrapper.findAll('[data-test="chain-member-unavailable"]')
    expect(markers).toHaveLength(1)
    expect(markers[0].text()).toBe('orangeKeyChain.stateNoPermission')
  })

  it('renders a missing member without group details as #id plus the deleted label', () => {
    const wrapper = mount(KeyGroupChainRowCell, {
      props: {
        row: row({
          group_chain_mode: 'aggregate',
          groups: [
            { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' }, state: 'available' },
            // 契约 B：列表接口同样返回 state=missing 的成员，且此时只有 { group_id, priority, state }
            { group_id: 17, priority: 2, state: 'missing' },
            { group_id: 2, priority: 3, group: { id: 2, name: 'B', platform: 'anthropic' }, state: 'available' }
          ]
        }),
        groupOptions,
        userGroupRates: {}
      },
      global: { stubs }
    })
    // 没有 group 详情 → 退回 #group_id，不能报错也不能空白
    expect(wrapper.get('[data-test="chain-member-id-fallback"]').text()).toBe('#17')
    // 并用状态标签说明「分组已删除」
    expect(wrapper.get('[data-test="chain-member-unavailable"]').text()).toBe(
      'orangeKeyChain.stateMissing'
    )
    // 其余成员照常渲染徽标，行数不变
    expect(wrapper.findAll('[data-test="badge"]')).toHaveLength(2)
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

  it('renders every member from the chain view, marks unavailable ones and asks before pruning', async () => {
    get.mockResolvedValue({ data: fiveMemberChain() })
    const formData = reactive({ group_id: 2 as number | null })
    const wrapper = mount(KeyGroupChainFormFields, {
      props: {
        formData,
        groupOptions,
        formGroupOptions: groupOptions,
        showEdit: true,
        selectedKey: row(),
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
    // 5 个成员全部渲染（旧实现只会渲染可用成员）
    expect(keyGroupChainForm.aggregateGroupIds).toHaveLength(5)
    expect(wrapper.findAll('[data-test="chain-member-state"]')).toHaveLength(3)
    // 有不可用成员 → 出现自动修复入口
    expect(wrapper.find('[data-test="chain-heal"]').exists()).toBe(true)
    // 还没动链 → 不需要确认
    expect(wrapper.find('[data-test="chain-prune-confirm"]').exists()).toBe(false)

    // 用户删掉一个仍存在于服务器链上的成员 → 必须确认
    keyGroupChainForm.aggregateGroupIds.splice(1, 1)
    await nextTick()
    expect(wrapper.find('[data-test="chain-prune-confirm"]').exists()).toBe(true)
    await wrapper.get('[data-test="prune-confirm-checkbox"]').setValue(true)
    expect(keyGroupChainForm.pruneConfirmed).toBe(true)
    wrapper.unmount()
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
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [],
      mode: 'fallback',
      expected_group_ids: [],
      confirm_prune: false
    })
  })

  it('stages the fallback chain without any extra hint', async () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.fallbackGroupId = 2
    const formData = { group_id: 1 as number | null }
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    expect(showWarning).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [1, 2],
      mode: 'fallback',
      expected_group_ids: [],
      confirm_prune: false
    })
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
    // 列表侧快捷操作不带 expected_group_ids，但必须显式 confirm_prune: true（用户主动裁剪）
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [],
      mode: 'fallback',
      confirm_prune: true
    })
    expect(showSuccess).toHaveBeenCalledWith('keys.groupChangedFallbackClearedSuccess')
    expect(refresh).toHaveBeenCalled()
  })

  it('confirms the prune explicitly when the list changes the primary group and keeps the fallback', async () => {
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    const refresh = vi.fn()
    const handled = await handleKeyGroupChainGroupChange(
      row(),
      5,
      // 新主分组与兜底分组同平台 → 保留兜底，链首随主分组变更
      [{ id: 5, platform: 'anthropic' }] as unknown as Group[],
      refresh,
      (key) => key
    )
    expect(handled).toBe(true)
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [5, 2],
      mode: 'fallback',
      confirm_prune: true
    })
    expect(showSuccess).toHaveBeenCalledWith('keys.groupChangedSuccess')
    expect(refresh).toHaveBeenCalled()
  })

  it('confirms the prune explicitly when the list clears or replaces a fallback group', async () => {
    put.mockResolvedValue({ data: {} })
    const refresh = vi.fn()
    // 清除兜底（[]）与改选新兜底都算用户主动裁剪，必须带 confirm_prune: true
    await saveFallbackGroupSelection(row(), null, refresh, (key) => key)
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [],
      mode: 'fallback',
      confirm_prune: true
    })
    put.mockClear()
    await saveFallbackGroupSelection(row(), 3, refresh, (key) => key)
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [1, 3],
      mode: 'fallback',
      confirm_prune: true
    })
    // 列表侧不发送 expected_group_ids（服务端按当前集合计算移除项）
    for (const call of put.mock.calls) {
      expect((call[1] as Record<string, unknown>).expected_group_ids).toBeUndefined()
    }
    expect(showSuccess).toHaveBeenCalledWith('keys.fallbackGroupChangedSuccess')
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('surfaces the prune-confirm 409 message plus the reopen hint', async () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    expect(prepareKeyGroupChainSubmit({ group_id: 1 }, groups, (key) => key)).toBe(true)
    updateKey.mockResolvedValue({ id: 1 })
    // 后端错误体：业务码放在 reason（code 是 HTTP 状态码 409），拦截器原样透传 reason / metadata
    const conflict = {
      status: 409,
      code: 409,
      reason: 'ORANGE_GROUP_CHAIN_PRUNE_CONFIRM',
      message: '保存将移除 2 个分组：自用生图、自用圆模；确认请重试',
      metadata: {
        pending_removals: JSON.stringify([
          { group_id: 15, name: '自用生图' },
          { group_id: 16, name: '自用圆模' }
        ])
      }
    }
    expect(isGroupChainPruneConfirmError(conflict)).toBe(true)
    put.mockRejectedValue(conflict)
    await keysAPI.update(1, { group_id: 1 })
    // 后端 message 必须冒泡到 UI，并追加「请重新打开编辑窗口确认后再保存」
    expect(showError).toHaveBeenCalledWith(`${conflict.message}orangeKeyChain.pruneReopenHint`)
  })

  it('parses the structured removal list out of metadata.pending_removals', () => {
    const error = {
      status: 409,
      reason: 'ORANGE_GROUP_CHAIN_PRUNE_CONFIRM',
      metadata: {
        pending_removals: JSON.stringify([
          { group_id: 15, name: '自用生图' },
          { group_id: 16, name: '' }
        ])
      }
    }
    // 名称为空时用 #id 兜底（与后端文案口径一致）
    expect(parsePendingRemovalsFromError(error)).toEqual([
      { group_id: 15, name: '自用生图' },
      { group_id: 16, name: '#16' }
    ])
    // 解析失败一律返回 null，由调用方回退本地 pendingChainRemovals
    expect(
      parsePendingRemovalsFromError({ metadata: { pending_removals: 'not-json' } })
    ).toBeNull()
    expect(
      parsePendingRemovalsFromError({
        metadata: { pending_removals: JSON.stringify([{ name: '缺 group_id' }]) }
      })
    ).toBeNull()
    expect(parsePendingRemovalsFromError({ metadata: {} })).toBeNull()
    expect(parsePendingRemovalsFromError({})).toBeNull()
  })

  it('renders the 409 confirmation from the server list first and falls back to the local one', () => {
    const fromServer = {
      status: 409,
      message: '',
      metadata: { pending_removals: JSON.stringify([{ group_id: 15, name: '自用生图' }]) }
    }
    // 服务端清单可解析 → 优先用它渲染确认信息
    expect(
      pruneConflictMessage(fromServer, [{ group_id: 99, name: '本地清单' }], tNamed)
    ).toBe('orangeKeyChain.pruneConfirmRequired(n=1,names=自用生图)orangeKeyChain.pruneReopenHint')

    // metadata 缺失 / 解析失败 → 回退本地 pendingChainRemovals
    expect(
      pruneConflictMessage({ status: 409, message: '' }, [{ group_id: 99, name: '本地清单' }], tNamed)
    ).toBe('orangeKeyChain.pruneConfirmRequired(n=1,names=本地清单)orangeKeyChain.pruneReopenHint')

    // 服务端给了 message → 直接用服务端文案
    expect(
      pruneConflictMessage({ status: 409, message: '服务端文案' }, undefined, tNamed)
    ).toBe('服务端文案orangeKeyChain.pruneReopenHint')
  })
})

describe('chain hydration (full member list)', () => {
  beforeEach(() => {
    resetKeyGroupChainForm()
    vi.clearAllMocks()
  })

  it('hydrates all five members including the unavailable ones', async () => {
    get.mockResolvedValue({ data: fiveMemberChain() })
    const setPrimary = vi.fn()
    await hydrateKeyGroupChain(row(), () => true, setPrimary)
    // 不再静默接受更小的链：5 个成员 1:1 回填
    expect(keyGroupChainForm.aggregateMode).toBe(true)
    expect(keyGroupChainForm.aggregateGroupIds).toEqual([2, 15, 16, 17, 18])
    expect(keyGroupChainForm.serverMemberIds).toEqual([2, 15, 16, 17, 18])
    expect(keyGroupChainForm.memberStates).toEqual({
      2: 'available',
      15: 'no_permission',
      16: 'disabled',
      17: 'missing',
      18: 'available'
    })
    expect(keyGroupChainForm.pruneConfirmed).toBe(false)
    expect(setPrimary).toHaveBeenCalledWith(2)
    // 读链是纯读：不提示任何“已自动更新”文案
    expect(showWarning).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it('uses the aggregate editor even when the aggregate chain has a single member', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 9,
        members: [{ group_id: 9, priority: 1, state: 'available' }],
        auto_pruned_group_ids: [],
        auto_pruned_groups: [],
        auto_promoted_group_id: 0
      }
    })
    const setPrimary = vi.fn()
    await hydrateKeyGroupChain(row(), () => true, setPrimary)
    expect(keyGroupChainForm.aggregateMode).toBe(true)
    expect(keyGroupChainForm.aggregateGroupIds).toEqual([9])
    expect(setPrimary).toHaveBeenCalledWith(9)
  })

  it('keeps the fallback editor for a fallback chain', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'fallback',
        primary_group_id: 1,
        members: [
          { group_id: 1, priority: 1, state: 'available' },
          { group_id: 2, priority: 2, state: 'scope_mismatch' }
        ],
        auto_pruned_group_ids: [],
        auto_pruned_groups: [],
        auto_promoted_group_id: 0
      }
    })
    await hydrateKeyGroupChain(row(), () => true, vi.fn())
    expect(keyGroupChainForm.aggregateMode).toBe(false)
    expect(keyGroupChainForm.fallbackGroupId).toBe(2)
    expect(keyGroupChainForm.serverMemberIds).toEqual([1, 2])
    expect(keyGroupChainForm.memberStates).toEqual({ 1: 'available', 2: 'scope_mismatch' })
  })
})

describe('removal confirmation', () => {
  beforeEach(() => {
    resetKeyGroupChainForm()
    vi.clearAllMocks()
  })

  it('blocks the submit while the user has not confirmed the removals', async () => {
    get.mockResolvedValue({ data: fiveMemberChain() })
    await hydrateKeyGroupChain(row(), () => true, vi.fn())
    // 用户删掉了「自用生图」这一行（服务器链上仍然存在）
    keyGroupChainForm.aggregateGroupIds.splice(1, 1)
    const formData = { group_id: 2 as number | null }
    expect(pendingChainRemovals(formData.group_id)).toEqual([
      { groupId: 15, name: '自用生图', state: 'no_permission' }
    ])
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(false)
    expect(showError).toHaveBeenCalledWith('orangeKeyChain.pruneConfirmRequired')
    // 校验失败不暂存链：密钥更新成功后不会补写分组链
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 2 })
    expect(put).not.toHaveBeenCalled()
  })

  it('stages expected_group_ids and confirm_prune once the user confirms', async () => {
    get.mockResolvedValue({ data: fiveMemberChain() })
    await hydrateKeyGroupChain(row(), () => true, vi.fn())
    // 编辑弹窗打开时会先按列表行初始化（isEdit / editKeyId），这里补上同样的状态
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.aggregateGroupIds.splice(1, 1)
    keyGroupChainForm.pruneConfirmed = true
    const formData = { group_id: 2 as number | null }
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    // stage 的 expected_group_ids 必须等于读链接口返回的完整成员集合
    expect(keyGroupChainForm.serverMemberIds).toEqual([2, 15, 16, 17, 18])
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 2 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [2, 16, 17, 18],
      mode: 'aggregate',
      // 客户端当前看到的完整集合 = 服务器成员
      expected_group_ids: [2, 15, 16, 17, 18],
      confirm_prune: true
    })
  })

  it('requires confirmation when a fallback key clears its chain', async () => {
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    keyGroupChainForm.serverMemberIds = [1, 2]
    keyGroupChainForm.serverMemberNames = { 1: 'A', 2: 'B' }
    keyGroupChainForm.memberStates = { 1: 'available', 2: 'available' }
    // 用户把兜底分组改成「不设置兜底」，本次提交的链为空 = 清链
    keyGroupChainForm.fallbackGroupId = null
    const formData = { group_id: 1 as number | null }
    expect(pendingChainRemovals(formData.group_id).map((removal) => removal.groupId)).toEqual([2])
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(false)
    expect(showError).toHaveBeenCalledWith('orangeKeyChain.pruneConfirmRequired')
    keyGroupChainForm.pruneConfirmed = true
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [],
      mode: 'fallback',
      expected_group_ids: [1, 2],
      confirm_prune: true
    })
  })

  it('reports removals by name and keeps the clearing rule of the backend', () => {
    keyGroupChainForm.serverMemberIds = [1, 2, 3]
    keyGroupChainForm.serverMemberNames = { 2: 'B' }
    keyGroupChainForm.memberStates = { 1: 'available', 2: 'no_permission', 3: 'missing' }
    keyGroupChainForm.aggregateMode = false
    keyGroupChainForm.fallbackGroupId = null
    // 清链时主分组本身不算移除项（它只是退回 api_keys.group_id），与后端口径一致
    expect(pendingChainRemovals(1).map((removal) => removal.name)).toEqual(['B', '#3'])
    // 非清链时，被替换掉的旧链首也是真正的移除项
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = [2, 3]
    expect(pendingChainRemovals(2).map((removal) => removal.groupId)).toEqual([1])
  })
})

describe('relaxed candidate validation (contract C2)', () => {
  beforeEach(() => {
    resetKeyGroupChainForm()
    vi.clearAllMocks()
  })

  /** 链首/候选可用 + 一个 scope_mismatch 候选（C 分组是 openai，但同样出现在 groups 里）。 */
  const chainWithScopeMismatch = () => ({
    api_key_id: 1,
    mode: 'aggregate',
    primary_group_id: 1,
    members: [
      { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' }, state: 'available' },
      { group_id: 2, priority: 2, group: { id: 2, name: 'B', platform: 'anthropic' }, state: 'available' },
      { group_id: 3, priority: 3, group: { id: 3, name: 'C', platform: 'openai' }, state: 'scope_mismatch' }
    ],
    auto_pruned_group_ids: [],
    auto_pruned_groups: [],
    auto_promoted_group_id: 0
  })

  it('submits an unchanged chain with unrouteable candidates 1:1', async () => {
    get.mockResolvedValue({ data: chainWithScopeMismatch() })
    await hydrateKeyGroupChain(row(), () => true, vi.fn())
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    const formData = { group_id: 1 as number | null }
    // 平台不一致的候选（C 是 openai）原样保留，保存不被前端拦下
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    // 不剔除任何成员：链上 3 个成员 1:1 提交
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [1, 2, 3],
      mode: 'aggregate',
      expected_group_ids: [1, 2, 3],
      confirm_prune: false
    })
    expect(showError).not.toHaveBeenCalled()
  })

  it('keeps a preserved scope_mismatch candidate while the chain is being edited', async () => {
    get.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 1,
        members: [
          { group_id: 1, priority: 1, group: { id: 1, name: 'A', platform: 'anthropic' }, state: 'available' },
          { group_id: 2, priority: 2, group: { id: 2, name: 'B', platform: 'anthropic' }, state: 'available' },
          { group_id: 3, priority: 3, group: { id: 3, name: 'C', platform: 'openai' }, state: 'scope_mismatch' },
          { group_id: 99, priority: 4, group: { id: 99, name: 'D', platform: 'anthropic' }, state: 'available' }
        ],
        auto_pruned_group_ids: [],
        auto_pruned_groups: [],
        auto_promoted_group_id: 0
      }
    })
    await hydrateKeyGroupChain(row(), () => true, vi.fn())
    keyGroupChainForm.isEdit = true
    keyGroupChainForm.editKeyId = 1
    // 用户删掉最后一个成员（仍需确认移除），保留那个 scope_mismatch 候选
    keyGroupChainForm.aggregateGroupIds.splice(3, 1)
    const formData = { group_id: 1 as number | null }
    expect(pendingChainRemovals(formData.group_id).map((removal) => removal.groupId)).toEqual([99])
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(false)
    expect(showError).toHaveBeenLastCalledWith('orangeKeyChain.pruneConfirmRequired')
    keyGroupChainForm.pruneConfirmed = true
    // 保留下来的 scope_mismatch 候选不参与平台一致性校验，不会被 'keys.aggregateGroupPlatformMismatch' 拦下
    expect(prepareKeyGroupChainSubmit(formData, groups, (key) => key)).toBe(true)
    updateKey.mockResolvedValue({ id: 1 })
    put.mockResolvedValue({ data: {} })
    await keysAPI.update(1, { group_id: 1 })
    expect(put).toHaveBeenCalledWith('/keys/1/group-chain', {
      group_ids: [1, 2, 3],
      mode: 'aggregate',
      expected_group_ids: [1, 2, 3, 99],
      confirm_prune: true
    })
  })

  it('still rejects a newly picked cross-platform candidate', () => {
    // 3 = C(openai) 不在服务器链上 → 不是「原样保留的候选」，仍按同平台校验拦下
    keyGroupChainForm.aggregateMode = true
    keyGroupChainForm.aggregateGroupIds = [1, 3]
    expect(prepareKeyGroupChainSubmit({ group_id: 1 }, groups, (key) => key)).toBe(false)
    expect(showError).toHaveBeenCalledWith('keys.aggregateGroupPlatformMismatch')
  })
})

describe('explicit heal', () => {
  beforeEach(() => {
    resetKeyGroupChainForm()
    vi.clearAllMocks()
  })

  it('heals the chain and only prompts when groups were really removed', async () => {
    post.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 2,
        members: [
          { group_id: 2, priority: 1, group: { id: 2, name: '自用主分组', platform: 'anthropic' }, state: 'available' },
          { group_id: 18, priority: 2, group: { id: 18, name: '自用备用', platform: 'anthropic' }, state: 'available' }
        ],
        auto_pruned_group_ids: [15, 17],
        auto_pruned_groups: [
          { group_id: 15, name: '自用生图' },
          { group_id: 17, name: '已删分组' }
        ],
        auto_promoted_group_id: 0
      }
    })
    const refresh = vi.fn()
    const setPrimary = vi.fn()
    const removed = await healKeyGroupChainAndRefresh(row(), refresh, (key) => key, setPrimary)
    expect(post).toHaveBeenCalledWith('/keys/1/group-chain/heal')
    expect(removed).toBe(2)
    // 用 heal 返回的 members 重新回填（不可用成员已被移除）
    expect(keyGroupChainForm.aggregateGroupIds).toEqual([2, 18])
    expect(keyGroupChainForm.serverMemberIds).toEqual([2, 18])
    expect(setPrimary).toHaveBeenCalledWith(2)
    expect(showSuccess).toHaveBeenCalledWith('orangeKeyChain.healSuccess')
    expect(showWarning).not.toHaveBeenCalled()
    expect(refresh).toHaveBeenCalled()
  })

  it('stays silent when the heal removed nothing', async () => {
    post.mockResolvedValue({
      data: {
        api_key_id: 1,
        mode: 'aggregate',
        primary_group_id: 2,
        members: [
          { group_id: 2, priority: 1, state: 'available' },
          { group_id: 16, priority: 2, state: 'disabled' }
        ],
        auto_pruned_group_ids: [],
        auto_pruned_groups: [],
        auto_promoted_group_id: 0
      }
    })
    const refresh = vi.fn()
    const removed = await healKeyGroupChainAndRefresh(row(), refresh, (key) => key)
    expect(removed).toBe(0)
    // 停用 / 平台不一致的分组不会被自动修复：不提示、不刷新
    expect(keyGroupChainForm.aggregateGroupIds).toEqual([2, 16])
    expect(showSuccess).not.toHaveBeenCalled()
    expect(refresh).not.toHaveBeenCalled()
  })

  it('surfaces the heal failure message', async () => {
    post.mockRejectedValue({ status: 500, message: 'heal failed: boom' })
    const removed = await healKeyGroupChainAndRefresh(row(), vi.fn(), (key) => key)
    expect(removed).toBe(0)
    expect(showError).toHaveBeenCalledWith('heal failed: boom')
  })
})
