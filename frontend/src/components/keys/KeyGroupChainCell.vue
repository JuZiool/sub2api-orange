<!--
  Orange 特有：密钥列表「分组」列的分组链徽章。
  - 链长 1：只显示主分组徽章（与官方渲染一致的观感，仅在无主分组时退化为占位文本）；
  - 链长 2：单兜底，第二行加「兜底」小标签；
  - 链长 ≥3：聚合分组，按 priority 升序显示 ①②③…，首行加「聚合」小标签。
  点击整块触发 edit 事件，由父页面打开 KeyGroupChainDialog。
-->
<template>
  <button
    type="button"
    class="-mx-2 -my-1 flex cursor-pointer flex-col items-start gap-1 rounded-lg px-2 py-1 text-left transition-colors hover:bg-gray-100 dark:hover:bg-dark-700"
    :title="t('keys.groupChain.editTitle')"
    data-test="key-group-chain-cell"
    @click="emit('edit')"
  >
    <template v-if="chainRows.length > 1">
      <span
        v-for="entry in chainRows"
        :key="entry.groupId"
        class="flex items-center gap-1.5"
        :data-test="`key-group-chain-row-${entry.index}`"
      >
        <span class="w-4 shrink-0 text-center text-xs font-semibold text-gray-500 dark:text-gray-400">
          {{ circledLabel(entry.index) }}
        </span>
        <GroupBadge
          v-if="entry.group"
          :name="entry.group.name"
          :platform="entry.group.platform"
          :subscription-type="entry.group.subscription_type"
          :rate-multiplier="entry.group.rate_multiplier"
          :user-rate-multiplier="userGroupRates?.[entry.group.id] ?? null"
          :peak-rate-enabled="entry.group.peak_rate_enabled"
          :peak-start="entry.group.peak_start"
          :peak-end="entry.group.peak_end"
          :peak-rate-multiplier="entry.group.peak_rate_multiplier"
        />
        <span v-else class="text-sm text-gray-400 dark:text-dark-500">#{{ entry.groupId }}</span>
        <span
          v-if="isFallbackChain && entry.index === 1"
          class="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
        >
          {{ t('keys.groupChain.fallbackBadge') }}
        </span>
        <span
          v-else-if="!isFallbackChain && entry.index === 0"
          class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-semibold text-gray-600 dark:bg-dark-700 dark:text-dark-300"
        >
          {{ t('keys.groupChain.aggregateBadge') }}
        </span>
      </span>
    </template>
    <template v-else>
      <GroupBadge
        v-if="primaryGroup"
        :name="primaryGroup.name"
        :platform="primaryGroup.platform"
        :subscription-type="primaryGroup.subscription_type"
        :rate-multiplier="primaryGroup.rate_multiplier"
        :user-rate-multiplier="userGroupRates?.[primaryGroup.id] ?? null"
        :peak-rate-enabled="primaryGroup.peak_rate_enabled"
        :peak-start="primaryGroup.peak_start"
        :peak-end="primaryGroup.peak_end"
        :peak-rate-multiplier="primaryGroup.peak_rate_multiplier"
      />
      <span v-else class="text-sm text-gray-400 dark:text-dark-500">{{ t('keys.noGroup') }}</span>
    </template>
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { ApiKey, Group } from '@/types'

interface Props {
  row: ApiKey
  userGroupRates?: Record<number, number>
}

const props = defineProps<Props>()

const emit = defineEmits<{
  (e: 'edit'): void
}>()

const { t } = useI18n()

const CIRCLED_LABELS = ['①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨']

const chainRows = computed(() =>
  [...(props.row.groups ?? [])]
    .sort((a, b) => a.priority - b.priority)
    .map((entry, index) => ({
      index,
      groupId: entry.group_id,
      group: entry.group ?? null
    }))
)

/** 链长 2 = 单兜底（第二行为兜底分组）；链长 ≥3 = 聚合分组。 */
const isFallbackChain = computed(() => chainRows.value.length === 2)

const primaryGroup = computed<Group | null>(() => props.row.group ?? chainRows.value[0]?.group ?? null)

const circledLabel = (index: number) => CIRCLED_LABELS[index] ?? `(${index + 1})`
</script>
