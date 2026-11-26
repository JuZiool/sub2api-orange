<template>
  <div class="group relative">
    <button
      ref="triggerRef"
      type="button"
      data-test="fallback-group-trigger"
      :disabled="!row.group_id"
      @click="togglePicker"
      class="-mx-2 flex cursor-pointer items-center gap-2 rounded-lg px-2 py-1 transition-all duration-200 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-700"
      :title="t('keys.clickToChangeFallbackGroup')"
    >
      <span
        class="shrink-0 rounded bg-amber-50 px-1.5 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/25 dark:text-amber-300"
      >
        {{ t('keys.fallbackGroupShortLabel') }}
      </span>
      <!-- 兜底分组取自分组链第二项（group_chain_mode=fallback 且链长 2） -->
      <GroupBadge
        v-if="fallbackEntry?.group"
        :name="fallbackEntry.group.name"
        :platform="fallbackEntry.group.platform"
        :subscription-type="fallbackEntry.group.subscription_type"
        :rate-multiplier="fallbackEntry.group.rate_multiplier"
        :user-rate-multiplier="userGroupRates[fallbackEntry.group.id]"
        :peak-rate-enabled="fallbackEntry.group.peak_rate_enabled"
        :peak-start="fallbackEntry.group.peak_start"
        :peak-end="fallbackEntry.group.peak_end"
        :peak-rate-multiplier="fallbackEntry.group.peak_rate_multiplier"
      />
      <span v-else-if="fallbackEntry" class="text-sm text-gray-500 dark:text-gray-400">
        #{{ fallbackEntry.group_id }}
      </span>
      <span v-else class="text-sm text-gray-400 dark:text-dark-500">
        {{ t('keys.noFallbackGroup') }}
      </span>
      <!-- 兜底成员不可用（含契约 B 的 state=missing：无 group 详情，只有 #group_id）时的原因标签 -->
      <span
        v-if="fallbackStateLabel"
        data-test="fallback-group-state"
        class="flex-shrink-0 whitespace-nowrap text-[11px] text-gray-400 dark:text-dark-400"
        :title="fallbackStateLabel"
      >
        {{ fallbackStateLabel }}
      </span>
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.selectFallbackGroup') }}</span>
      <svg
        class="h-3.5 w-3.5 text-gray-400 opacity-60 transition-opacity group-hover:opacity-100"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
        stroke-width="2"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          d="M8.25 15L12 18.75 15.75 15m-7.5-6L12 5.25 15.75 9"
        />
      </svg>
    </button>

    <!-- 兜底分组选择浮层：Teleport 到 body，避免被表格 overflow 裁剪（与上游分组选择器一致） -->
    <Teleport to="body">
      <div
        v-if="pickerOpen && pickerPosition"
        ref="dropdownRef"
        class="animate-in fade-in slide-in-from-top-2 fixed z-[100000020] w-max max-w-[calc(100vw-16px)] overflow-hidden rounded-xl bg-white shadow-lg ring-1 ring-black/5 duration-200 sm:min-w-[380px] dark:bg-dark-800 dark:ring-white/10"
        style="pointer-events: auto !important"
        :style="{
          top: pickerPosition.top !== undefined ? pickerPosition.top + 'px' : undefined,
          bottom: pickerPosition.bottom !== undefined ? pickerPosition.bottom + 'px' : undefined,
          left: pickerPosition.left + 'px'
        }"
      >
        <!-- Search box -->
        <div class="border-b border-gray-100 p-2 dark:border-dark-700">
          <div class="relative">
            <svg class="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-gray-400" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
            </svg>
            <input
              v-model="searchQuery"
              type="text"
              class="w-full rounded-lg border border-gray-200 bg-gray-50 py-1.5 pl-8 pr-3 text-sm text-gray-900 placeholder-gray-400 outline-none focus:border-primary-300 focus:ring-1 focus:ring-primary-300 dark:border-dark-600 dark:bg-dark-700 dark:text-white dark:placeholder-gray-500 dark:focus:border-primary-600 dark:focus:ring-primary-600"
              :placeholder="t('keys.searchGroup')"
              @click.stop
            />
          </div>
        </div>
        <!-- Group list -->
        <div class="max-h-80 overflow-y-auto p-1.5">
          <button
            v-if="showNoFallbackOption"
            type="button"
            @click="chooseFallback(null)"
            :class="[
              'flex w-full items-center justify-between rounded-lg px-3 py-2.5 text-sm transition-colors',
              'border-b border-gray-100 last:border-0 dark:border-dark-700',
              isNoFallbackSelected
                ? 'bg-primary-50 dark:bg-primary-900/20'
                : 'hover:bg-gray-100 dark:hover:bg-dark-700'
            ]"
          >
            <div class="flex w-full items-center justify-between gap-3">
              <span class="text-gray-600 dark:text-gray-300">{{ t('keys.noFallbackGroup') }}</span>
              <Icon
                v-if="isNoFallbackSelected"
                name="check"
                size="sm"
                class="shrink-0 text-primary-500"
                :stroke-width="2"
              />
            </div>
          </button>
          <button
            v-for="option in visibleOptions"
            :key="option.value"
            type="button"
            @click="chooseFallback(option.value)"
            :class="[
              'flex w-full items-center justify-between rounded-lg px-3 py-2.5 text-sm transition-colors',
              'border-b border-gray-100 last:border-0 dark:border-dark-700',
              isSelected(option.value)
                ? 'bg-primary-50 dark:bg-primary-900/20'
                : 'hover:bg-gray-100 dark:hover:bg-dark-700'
            ]"
            :title="option.description || undefined"
          >
            <GroupOptionItem
              :name="option.label"
              :platform="option.platform"
              :subscription-type="option.subscriptionType"
              :rate-multiplier="option.rate"
              :user-rate-multiplier="option.userRate"
              :peak-rate-enabled="option.peakRateEnabled"
              :peak-start="option.peakStart"
              :peak-end="option.peakEnd"
              :peak-rate-multiplier="option.peakRateMultiplier"
              :description="option.description"
              :selected="isSelected(option.value)"
            />
          </button>
          <div v-if="showEmptyState" class="py-4 text-center text-sm text-gray-400 dark:text-dark-500">
            {{ t('keys.noGroupFound') }}
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
/**
 * Orange 特有：密钥列表“兜底分组”选择器（表格分组列的第二行）。
 *
 * 交互与改造前的分组选择浮层保持一致：
 * - 选项为“与主分组同平台、且不是主分组本身”的分组，首项是“不设置兜底分组”；
 * - 支持按分组名/描述搜索（首项也参与搜索）；
 * - 选中后写入分组链（PUT /keys/:id/group-chain，mode=fallback，confirm_prune=true），重新拉取列表；
 * - 与当前兜底相同则静默返回；主分组为空时不处理；失败只提示。
 *
 * 契约 B：兜底成员同样带 state，state=missing 时没有 group 详情，退回 #group_id + 「分组已删除」标签。
 *
 * 未配置兜底分组时只展示“未设置兜底分组”，不产生任何请求。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupOptionItem from '@/components/common/GroupOptionItem.vue'
import Icon from '@/components/icons/Icon.vue'
import type { ApiKey } from '@/types'
import { fallbackGroupOf } from './chain'
import { chainMemberStateLabelKey, saveFallbackGroupSelection } from './useKeyGroupChain'
import type { GroupOptionView, KeyGroupChainKey } from './types'

const props = defineProps<{
  row: ApiKey
  groupOptions: GroupOptionView[]
  userGroupRates: Record<number, number>
}>()

const emit = defineEmits<{
  (e: 'refresh'): void
}>()

const { t } = useI18n()

const triggerRef = ref<HTMLButtonElement | null>(null)
const dropdownRef = ref<HTMLElement | null>(null)
const pickerOpen = ref(false)
const searchQuery = ref('')
const pickerPosition = ref<{ top?: number; bottom?: number; left: number } | null>(null)

const chainRow = computed(() => props.row as KeyGroupChainKey)
const fallbackEntry = computed(() => fallbackGroupOf(chainRow.value))
const currentFallbackId = computed(() => fallbackEntry.value?.group_id ?? null)

/** 兜底成员不可用时的原因标签（available / 缺省 state 返回 null，不展示）。 */
const fallbackStateLabel = computed<string | null>(() => {
  const key = chainMemberStateLabelKey(fallbackEntry.value?.state)
  return key ? t(key) : null
})

/** 候选分组：与主分组同平台、且不是主分组本身（与改造前一致）。 */
const candidateOptions = computed(() => {
  const primary = props.groupOptions.find((option) => option.value === props.row.group_id)
  return props.groupOptions.filter(
    (option) =>
      option.value !== props.row.group_id && (!primary || option.platform === primary.platform)
  )
})

const normalizedQuery = computed(() => searchQuery.value.trim().toLowerCase())

const visibleOptions = computed(() => {
  const query = normalizedQuery.value
  if (!query) return candidateOptions.value
  return candidateOptions.value.filter(
    (option) =>
      option.label.toLowerCase().includes(query) ||
      (option.description ? option.description.toLowerCase().includes(query) : false)
  )
})

const showNoFallbackOption = computed(() => {
  const query = normalizedQuery.value
  if (!query) return true
  return t('keys.noFallbackGroup').toLowerCase().includes(query)
})

const showEmptyState = computed(
  () => visibleOptions.value.length === 0 && !showNoFallbackOption.value
)

const isSelected = (groupId: number): boolean => currentFallbackId.value === groupId
const isNoFallbackSelected = computed(() => currentFallbackId.value === null)

const openPicker = () => {
  const el = triggerRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const dropdownEstHeight = 400 // 与上游分组选择器一致：估算最大浮层高度
  const dropdownEstWidth = Math.min(380, window.innerWidth - 16)
  const spaceBelow = window.innerHeight - rect.bottom
  const spaceAbove = rect.top
  // 夹取 left，避免窄屏下浮层超出视口右缘
  const left = Math.max(8, Math.min(rect.left, window.innerWidth - dropdownEstWidth - 8))
  pickerPosition.value =
    spaceBelow < dropdownEstHeight && spaceAbove > spaceBelow
      ? { bottom: window.innerHeight - rect.top + 4, left }
      : { top: rect.bottom + 4, left }
  searchQuery.value = ''
  pickerOpen.value = true
}

const closePicker = () => {
  pickerOpen.value = false
  pickerPosition.value = null
}

const togglePicker = () => {
  if (pickerOpen.value) {
    closePicker()
    return
  }
  openPicker()
}

const chooseFallback = (groupId: number | null) => {
  closePicker()
  void saveFallbackGroupSelection(props.row, groupId, () => emit('refresh'), t)
}

const onDocumentClick = (event: MouseEvent) => {
  if (!pickerOpen.value) return
  const target = event.target as Node | null
  if (!target) {
    closePicker()
    return
  }
  if (triggerRef.value?.contains(target) || dropdownRef.value?.contains(target)) return
  closePicker()
}

onMounted(() => {
  document.addEventListener('click', onDocumentClick)
})

onUnmounted(() => {
  document.removeEventListener('click', onDocumentClick)
})
</script>
