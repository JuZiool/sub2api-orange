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
    <!-- 不可用链成员说明：保存时自动跳过，避免下拉框空白 / 保存报 ORANGE_GROUP_CHAIN_INVALID -->
    <p
      v-if="hasUnavailableChainGroup"
      data-test="chain-unavailable-hint"
      class="text-xs leading-5 text-red-600 dark:text-red-400"
    >
      {{ t('orangeKeyGroupChain.unavailableChainHint') }}
    </p>
    <div
      v-for="(groupId, index) in form.aggregateGroupIds"
      :key="`chain-${index}`"
      class="flex items-center gap-2"
    >
      <span
        class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary-50 text-xs font-semibold text-primary-700 dark:bg-primary-500/15 dark:text-primary-300"
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
      <!-- 不可用标记：该分组已不在「当前用户可绑定分组」列表中（例如被改为专属） -->
      <span
        v-if="isGroupUnavailable(groupId)"
        data-test="chain-group-unavailable"
        class="shrink-0 rounded bg-red-50 px-1.5 py-0.5 text-[11px] font-medium text-red-600 dark:bg-red-900/25 dark:text-red-300"
      >
        {{ t('orangeKeyGroupChain.unavailableBadge') }}
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
      <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
        {{ t('keys.fallbackGroupHint') }}
      </p>
      <!-- 兜底分组不可用：当前值已不在可绑定列表内，保存时自动清空（避免保存报错） -->
      <div
        v-if="fallbackGroupUnavailable"
        data-test="fallback-group-unavailable"
        class="mt-1.5 flex flex-wrap items-center gap-2 text-xs leading-5 text-red-600 dark:text-red-400"
      >
        <span class="rounded bg-red-50 px-1.5 py-0.5 text-[11px] font-medium text-red-600 dark:bg-red-900/25 dark:text-red-300">
          {{ t('orangeKeyGroupChain.unavailableBadge') }}
        </span>
        <span>{{ t('orangeKeyGroupChain.fallbackUnavailableHint') }}</span>
      </div>
    </div>
  </template>
</template>

<script setup lang="ts">
/**
 * Orange 特有：新建 / 编辑密钥弹窗里的分组链表单区。
 *
 * 默认插槽由上游 KeysView 传入“主分组”选择行；本组件负责在其前后挂载：
 * - 聚合密钥开关（开启后隐藏主分组单选，改为分组优先级链编辑器）；
 * - 非聚合模式下的兜底分组下拉。
 *
 * 分组链表单状态放在 useKeyGroupChain 的 `keyGroupChainForm` 中，提交前由
 * prepareKeyGroupChainSubmit 统一校验；未开启聚合、未设置兜底分组时控件呈现为
 * “关闭 + 未设置”，对上游表单数据的唯一写入是把链首同步回 formData.group_id。
 *
 * 不可用链成员（分组被改为专属后用户无权绑定）：可用性由 useKeyGroupChain 统一维护——优先用
 * 链详情接口返回的 `available`（与后端请求时的链成员过滤同源），接口未给出时才回退到「不在
 * formGroupOptions（当前用户可绑定分组）里即不可用」。本组件只负责把不可用成员标注成红色
 * 「不可用」标签，并把 group_id 同步到 keyGroupChainForm.unavailableGroupIds，由
 * prepareKeyGroupChainSubmit 在保存时自动跳过 / 清空，避免下拉框空白与保存报
 * ORANGE_GROUP_CHAIN_INVALID。
 */
import { computed, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupOptionItem from '@/components/common/GroupOptionItem.vue'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import type { ApiKey } from '@/types'
import {
  hydrateKeyGroupChain,
  initKeyGroupChainFormFromKey,
  keyGroupChainForm as form,
  refreshKeyGroupChainAvailability,
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

/** Orange 特有：主分组（链首）变更统一走事件，由上游 KeysView 回写 formData。 */
const emit = defineEmits<{ (e: 'update:primary-group', value: number | null): void }>()
const setPrimaryGroup = (value: number | null) => emit('update:primary-group', value)

const { t } = useI18n()

/** Orange 特有：不可用链成员集合（由表单状态统一维护：后端权威标记优先，其次为不在可绑定分组列表）。 */
const unavailableGroupIdSet = computed(() => new Set(form.unavailableGroupIds))

/** Orange 特有：该分组当前不可用（分组被改为专属后用户已无绑定权限）。 */
const isGroupUnavailable = (groupId: number | null): boolean =>
  groupId !== null && unavailableGroupIdSet.value.has(groupId)

/** 聚合链上存在不可用成员：顶部显示「保存时会自动跳过」说明。 */
const hasUnavailableChainGroup = computed(
  () => form.aggregateMode && form.aggregateGroupIds.some((id) => isGroupUnavailable(id))
)

/** 兜底分组当前值不可用：保存时会被自动清空。 */
const fallbackGroupUnavailable = computed(
  () => !form.aggregateMode && isGroupUnavailable(form.fallbackGroupId)
)

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

// 可用分组列表或链结构变化时刷新不可用标记：提交前校验读取的就是这份状态。
watch(
  () => [
    props.formGroupOptions,
    form.aggregateMode,
    form.aggregateGroupIds.slice(),
    form.fallbackGroupId
  ],
  () => refreshKeyGroupChainAvailability(props.formGroupOptions),
  { immediate: true }
)

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
        },
        props.formGroupOptions
      )
    } else {
      resetKeyGroupChainForm()
    }
  },
  { immediate: true }
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
