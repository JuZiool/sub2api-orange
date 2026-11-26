<template>
  <!-- 聚合密钥开关：开启后按分组链顺序依次尝试，兜底分组不再生效 -->
  <div class="flex items-start justify-between gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <div class="min-w-0">
      <div class="text-sm font-medium text-gray-900 dark:text-gray-100">
        {{ t('keys.aggregateModeLabel') }}
      </div>
      <p class="mt-0.5 text-xs leading-5 text-gray-500 dark:text-gray-400">
        {{ t('keys.aggregateModeHint') }}
      </p>
    </div>
    <button
      type="button"
      data-test="aggregate-mode-toggle"
      @click="toggleAggregateMode"
      :class="[
        'relative mt-0.5 inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none',
        form.aggregateMode ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
      ]"
    >
      <span
        :class="[
          'pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
          form.aggregateMode ? 'translate-x-4' : 'translate-x-0'
        ]"
      />
    </button>
  </div>

  <!-- 聚合分组链编辑器 -->
  <div v-if="form.aggregateMode" class="space-y-2" data-test="aggregate-group-chain">
    <label class="input-label">{{ t('keys.aggregateGroupChainLabel') }}</label>
    <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">
      {{ t('keys.aggregateGroupChainHint') }}
    </p>
    <div
      v-for="(groupId, index) in form.aggregateGroupIds"
      :key="`chain-${index}`"
      class="flex items-center gap-2"
    >
      <span
        :class="[
          'flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full text-xs font-semibold',
          memberStateLabel(groupId) === null
            ? 'bg-primary-50 text-primary-700 dark:bg-primary-500/15 dark:text-primary-300'
            : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
        ]"
      >
        {{ index + 1 }}
      </span>
      <Select
        class="min-w-0 flex-1"
        :model-value="groupId"
        :options="aggregateGroupOptions(index)"
        :placeholder="t('keys.selectGroup')"
        :searchable="true"
        :search-placeholder="t('keys.searchGroup')"
        @update:model-value="(value: string | number | boolean | null) => setAggregateGroup(index, value)"
      >
        <template #selected="{ option }">
          <GroupBadge
            v-if="option"
            :name="(option as unknown as GroupOptionView).label"
            :platform="(option as unknown as GroupOptionView).platform"
            :subscription-type="(option as unknown as GroupOptionView).subscriptionType"
            :rate-multiplier="(option as unknown as GroupOptionView).rate"
            :user-rate-multiplier="(option as unknown as GroupOptionView).userRate"
            :peak-rate-enabled="(option as unknown as GroupOptionView).peakRateEnabled"
            :peak-start="(option as unknown as GroupOptionView).peakStart"
            :peak-end="(option as unknown as GroupOptionView).peakEnd"
            :peak-rate-multiplier="(option as unknown as GroupOptionView).peakRateMultiplier"
          />
          <span v-else class="text-gray-400">{{ t('keys.selectGroup') }}</span>
        </template>
        <template #option="{ option, selected }">
          <GroupOptionItem
            :name="(option as unknown as GroupOptionView).label"
            :platform="(option as unknown as GroupOptionView).platform"
            :subscription-type="(option as unknown as GroupOptionView).subscriptionType"
            :rate-multiplier="(option as unknown as GroupOptionView).rate"
            :user-rate-multiplier="(option as unknown as GroupOptionView).userRate"
            :peak-rate-enabled="(option as unknown as GroupOptionView).peakRateEnabled"
            :peak-start="(option as unknown as GroupOptionView).peakStart"
            :peak-end="(option as unknown as GroupOptionView).peakEnd"
            :peak-rate-multiplier="(option as unknown as GroupOptionView).peakRateMultiplier"
            :description="(option as unknown as GroupOptionView).description"
            :selected="selected"
          />
        </template>
      </Select>
      <!-- 不可用且已不在下拉候选里的成员（无权限 / 已删除）：下拉框显示不出名字，用服务器返回的分组名兜底 -->
      <span
        v-if="orphanMemberLabel(groupId)"
        data-test="chain-member-orphan-name"
        class="max-w-[9rem] flex-shrink-0 truncate text-xs text-gray-400 dark:text-dark-400"
        :title="orphanMemberLabel(groupId) ?? undefined"
      >
        {{ orphanMemberLabel(groupId) }}
      </span>
      <!-- 不可用成员的状态标签（灰色/黄色）：只提示原因，不禁用下拉与删除按钮，方便用户替换掉它 -->
      <span
        v-if="memberStateLabel(groupId)"
        data-test="chain-member-state"
        :class="['flex-shrink-0 whitespace-nowrap text-xs', memberStateToneClass(groupId)]"
        :title="memberStateLabel(groupId) ?? undefined"
      >
        {{ memberStateLabel(groupId) }}
      </span>
      <button
        type="button"
        class="rounded p-1 text-gray-500 transition-colors hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700"
        :disabled="index === 0"
        :title="t('keys.aggregateMoveUp')"
        @click="moveAggregateGroup(index, -1)"
      >
        <Icon name="chevronUp" size="sm" />
      </button>
      <button
        type="button"
        class="rounded p-1 text-gray-500 transition-colors hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700"
        :disabled="index === form.aggregateGroupIds.length - 1"
        :title="t('keys.aggregateMoveDown')"
        @click="moveAggregateGroup(index, 1)"
      >
        <Icon name="chevronDown" size="sm" />
      </button>
      <button
        type="button"
        class="rounded p-1 text-red-500 transition-colors hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-red-900/20"
        :disabled="form.aggregateGroupIds.length <= 2"
        :title="t('keys.aggregateRemoveGroup')"
        @click="removeAggregateGroup(index)"
      >
        <Icon name="trash" size="sm" />
      </button>
    </div>
    <button
      type="button"
      class="btn btn-secondary btn-sm"
      data-test="aggregate-add-group"
      @click="addAggregateGroup"
    >
      <Icon name="plus" size="sm" class="mr-1" />
      {{ t('keys.aggregateAddGroup') }}
    </button>
  </div>

  <template v-else>
    <slot />

    <div>
      <label class="input-label">{{ t('keys.fallbackGroupLabel') }}</label>
      <Select
        :model-value="form.fallbackGroupId"
        :options="fallbackGroupOptions"
        :placeholder="t('keys.noFallbackGroup')"
        :searchable="true"
        :search-placeholder="t('keys.searchGroup')"
        data-test="fallback-group-select"
        @update:model-value="selectFallbackGroup"
      >
        <template #selected="{ option }">
          <GroupBadge
            v-if="option && (option as unknown as GroupOptionView).value !== null"
            :name="(option as unknown as GroupOptionView).label"
            :platform="(option as unknown as GroupOptionView).platform"
            :subscription-type="(option as unknown as GroupOptionView).subscriptionType"
            :rate-multiplier="(option as unknown as GroupOptionView).rate"
            :user-rate-multiplier="(option as unknown as GroupOptionView).userRate"
            :peak-rate-enabled="(option as unknown as GroupOptionView).peakRateEnabled"
            :peak-start="(option as unknown as GroupOptionView).peakStart"
            :peak-end="(option as unknown as GroupOptionView).peakEnd"
            :peak-rate-multiplier="(option as unknown as GroupOptionView).peakRateMultiplier"
          />
          <span v-else class="text-gray-400">{{ t('keys.noFallbackGroup') }}</span>
        </template>
        <template #option="{ option, selected }">
          <GroupOptionItem
            v-if="(option as unknown as GroupOptionView).value !== null"
            :name="(option as unknown as GroupOptionView).label"
            :platform="(option as unknown as GroupOptionView).platform"
            :subscription-type="(option as unknown as GroupOptionView).subscriptionType"
            :rate-multiplier="(option as unknown as GroupOptionView).rate"
            :user-rate-multiplier="(option as unknown as GroupOptionView).userRate"
            :peak-rate-enabled="(option as unknown as GroupOptionView).peakRateEnabled"
            :peak-start="(option as unknown as GroupOptionView).peakStart"
            :peak-end="(option as unknown as GroupOptionView).peakEnd"
            :peak-rate-multiplier="(option as unknown as GroupOptionView).peakRateMultiplier"
            :description="(option as unknown as GroupOptionView).description"
            :selected="selected"
          />
          <div v-else class="flex w-full items-center justify-between gap-3">
            <span class="text-gray-600 dark:text-gray-300">{{ t('keys.noFallbackGroup') }}</span>
            <Icon
              v-if="selected"
              name="check"
              size="sm"
              class="shrink-0 text-primary-500"
              :stroke-width="2"
            />
          </div>
        </template>
      </Select>
      <!-- 兜底分组不可用时的原因标签（与聚合链每行口径一致） -->
      <p
        v-if="memberStateLabel(form.fallbackGroupId)"
        data-test="fallback-group-state"
        :class="['mt-1 text-xs', memberStateToneClass(form.fallbackGroupId)]"
      >
        {{ memberStateLabel(form.fallbackGroupId) }}
      </p>
      <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
        {{ t('keys.fallbackGroupHint') }}
      </p>
    </div>
  </template>

  <!-- 移除确认：本次保存会删掉服务器链上仍存在的成员时，必须由用户显式勾选 -->
  <div
    v-if="pendingRemovals.length > 0"
    data-test="chain-prune-confirm"
    class="rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-500/30 dark:bg-amber-900/20"
  >
    <div class="text-sm font-medium text-amber-800 dark:text-amber-300">
      {{ t('orangeKeyChain.pruneTitle', { n: pendingRemovals.length }) }}
    </div>
    <p class="mt-0.5 text-xs leading-5 text-amber-700 dark:text-amber-200/90">
      {{ t('orangeKeyChain.pruneHint', { n: pendingRemovals.length, names: pendingRemovalNames }) }}
    </p>
    <label class="mt-2 flex items-start gap-2 text-xs text-amber-800 dark:text-amber-200">
      <input
        v-model="form.pruneConfirmed"
        type="checkbox"
        data-test="prune-confirm-checkbox"
        class="mt-0.5 h-3.5 w-3.5 flex-shrink-0 rounded border-amber-300 text-primary-600 focus:ring-primary-500"
      />
      <span>
        {{ t('orangeKeyChain.pruneConfirmLabel', { n: pendingRemovals.length, names: pendingRemovalNames }) }}
      </span>
    </label>
  </div>

  <!-- 自动修复：只修剪无权限 / 已删除的候选，停用与平台不一致的成员一律保留 -->
  <div v-if="healTargetKey && hasUnavailableChainMember(props.formData.group_id)" class="flex items-center">
    <button
      type="button"
      class="btn btn-secondary btn-sm"
      data-test="chain-heal"
      :disabled="healing"
      :title="t('orangeKeyChain.healHint')"
      @click="healChain"
    >
      <Icon name="refresh" size="sm" class="mr-1" />
      {{ healing ? t('orangeKeyChain.healing') : t('orangeKeyChain.healButton') }}
    </button>
  </div>
</template>

<script setup lang="ts">
/**
 * Orange 特有：新建 / 编辑密钥弹窗里的分组链表单区。
 *
 * 默认插槽由上游 KeysView 传入“主分组”选择行；本组件负责在其前后挂载：
 * - 聚合密钥开关（开启后隐藏主分组单选，改为分组优先级链编辑器）；
 * - 非聚合模式下的兜底分组下拉；
 * - 不可用成员的状态标签、保存前的移除确认、以及「自动修复不可用分组」按钮。
 *
 * 分组链表单状态放在 useKeyGroupChain 的 `keyGroupChainForm` 中，提交前由
 * prepareKeyGroupChainSubmit 统一校验；未开启聚合、未设置兜底分组时控件呈现为
 * “关闭 + 未设置”，对上游表单数据的唯一写入是把链首同步回 formData.group_id。
 *
 * v0.2.25-8 起读链是纯读接口：members 含链上全部成员与 state，弹窗按接口 1:1 回填，
 * 不可用成员只灰显 + 标注原因（不禁用下拉/删除按钮），保存会删掉服务器成员时强制勾选确认。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupOptionItem from '@/components/common/GroupOptionItem.vue'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import type { ApiKey } from '@/types'
import {
  chainMemberStateLabelKey,
  hasUnavailableChainMember,
  healKeyGroupChainAndRefresh,
  hydrateKeyGroupChain,
  initKeyGroupChainFormFromKey,
  keyGroupChainForm as form,
  pendingChainRemovals,
  resetKeyGroupChainForm
} from './useKeyGroupChain'
import type { FallbackGroupOption, GroupOptionView } from './types'

const props = defineProps<{
  /** 上游表单数据（只读；主分组变更通过 update:primary-group 事件回传，避免直接改 prop）。 */
  formData: { group_id: number | null }
  /** 全部分组选项（兜底分组下拉用）。 */
  groupOptions: GroupOptionView[]
  /** 当前模式的候选分组选项（编辑不限供应商 / 创建限定供应商，聚合链编辑器用）。 */
  formGroupOptions: GroupOptionView[]
  showEdit: boolean
  selectedKey: ApiKey | null
  createProvider: string
}>()

/**
 * Orange 特有：主分组（链首）变更统一走事件，由上游 KeysView 回写 formData。
 * `refresh` 供父级在分组链自愈后重新拉取列表（未监听时不影响弹窗内已回填的数据）。
 */
const emit = defineEmits<{
  (e: 'update:primary-group', value: number | null): void
  (e: 'refresh'): void
}>()
const setPrimaryGroup = (value: number | null) => emit('update:primary-group', value)

const { t } = useI18n()

const fallbackGroupOptions = computed<FallbackGroupOption[]>(() => {
  const primary = props.groupOptions.find((option) => option.value === props.formData.group_id)
  return [
    { value: null, label: t('keys.noFallbackGroup'), description: '' },
    ...props.groupOptions.filter(
      (option) =>
        option.value !== props.formData.group_id && (!primary || option.platform === primary.platform)
    )
  ]
})

const aggregateGroupOptions = (index: number) => {
  const taken = new Set(
    form.aggregateGroupIds.filter((id, i): id is number => i !== index && id !== null)
  )
  return props.formGroupOptions.filter((option) => !taken.has(option.value))
}

/** 服务器链上仍存在、但本次保存会移除的成员（非空时必须勾选确认才能保存）。 */
const pendingRemovals = computed(() => pendingChainRemovals(props.formData.group_id))

/** 移除成员名称串（用于确认文案）。 */
const pendingRemovalNames = computed(() =>
  pendingRemovals.value
    .map((removal) => removal.name)
    .join(t('orangeKeyChain.nameSeparator'))
)

/** 成员状态标签文案（available / 未知状态返回 null，不展示标签）。 */
const memberStateLabel = (groupId: number | null): string | null => {
  if (groupId === null) return null
  const key = chainMemberStateLabelKey(form.memberStates[groupId])
  return key ? t(key) : null
}

/**
 * 不可用且已不在下拉候选里的成员名（无权限 / 已删除的分组通常选不到）：
 * 下拉框无法显示它的名字，这里用读链接口返回的分组名兜底，便于用户判断要替换成哪个分组。
 */
const orphanMemberLabel = (groupId: number | null): string | null => {
  if (groupId === null) return null
  if (props.formGroupOptions.some((option) => option.value === groupId)) return null
  return form.serverMemberNames[groupId] ?? `#${groupId}`
}

/** 状态标签配色：停用 / 已删除用灰色，权限与平台类问题用黄色，避免和错误红混淆。 */
const memberStateToneClass = (groupId: number | null): string => {
  const state = groupId === null ? undefined : form.memberStates[groupId]
  return state === 'disabled' || state === 'missing'
    ? 'text-gray-500 dark:text-gray-400'
    : 'text-amber-600 dark:text-amber-400'
}

/** 打开自愈接口所需的密钥（仅编辑模式、且弹窗里正在编辑的那一个）。 */
const healTargetKey = computed<ApiKey | null>(() =>
  props.showEdit && props.selectedKey && form.editKeyId === props.selectedKey.id
    ? props.selectedKey
    : null
)

const healing = ref(false)
const healChain = async () => {
  const key = healTargetKey.value
  if (!key || healing.value) return
  healing.value = true
  try {
    await healKeyGroupChainAndRefresh(
      key,
      () => emit('refresh'),
      t,
      (groupId) => setPrimaryGroup(groupId)
    )
  } finally {
    healing.value = false
  }
}

const toggleAggregateMode = () => {
  const next = !form.aggregateMode
  form.aggregateMode = next
  if (next) {
    // 进入聚合模式：沿用当前主分组作为链首，并补一个空位；聚合密钥不使用单兜底分组。
    const current = props.formData.group_id
    form.aggregateGroupIds = current ? [current, null] : [null, null]
    form.fallbackGroupId = null
  } else {
    // 退出聚合模式：主分组保留链首，链清空。
    setPrimaryGroup(form.aggregateGroupIds[0] ?? null)
    form.aggregateGroupIds = []
  }
}

const setAggregateGroup = (index: number, value: string | number | boolean | null) => {
  const groupId = typeof value === 'number' ? value : null
  form.aggregateGroupIds[index] = groupId
  // 链首即主分组，保持同步，供非聚合路径与展示使用。
  if (index === 0) {
    setPrimaryGroup(groupId)
  }
}

const addAggregateGroup = () => {
  form.aggregateGroupIds.push(null)
}

const removeAggregateGroup = (index: number) => {
  if (form.aggregateGroupIds.length <= 2) return
  form.aggregateGroupIds.splice(index, 1)
  setPrimaryGroup(form.aggregateGroupIds[0] ?? null)
}

const moveAggregateGroup = (index: number, offset: number) => {
  const target = index + offset
  const list = form.aggregateGroupIds
  if (target < 0 || target >= list.length) return
  const [item] = list.splice(index, 1)
  list.splice(target, 0, item)
  setPrimaryGroup(list[0] ?? null)
}

const selectFallbackGroup = (value: string | number | boolean | null) => {
  form.fallbackGroupId = typeof value === 'number' ? value : null
}

watch(
  () => [props.showEdit, props.selectedKey?.id] as const,
  () => {
    if (props.showEdit && props.selectedKey) {
      const key = props.selectedKey
      initKeyGroupChainFormFromKey(key)
      void hydrateKeyGroupChain(
        key,
        () => props.showEdit && props.selectedKey?.id === key.id,
        (groupId) => {
          setPrimaryGroup(groupId)
        }
      )
    } else {
      resetKeyGroupChainForm()
    }
  },
  { immediate: true }
)

// 待移除成员集合一旦变化（用户改链 / 读到新链），旧的确认勾选就失效，必须重新确认。
watch(
  () => pendingRemovals.value.map((removal) => removal.groupId).join(','),
  () => {
    form.pruneConfirmed = false
  }
)

watch(
  () => props.createProvider,
  () => {
    // 聚合链与供应商绑定：切换供应商即清空链，避免残留跨平台选择。
    form.aggregateGroupIds = []
  }
)

onUnmounted(() => {
  resetKeyGroupChainForm()
})
</script>
