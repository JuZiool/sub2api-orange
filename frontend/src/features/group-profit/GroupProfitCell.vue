<template>
  <div v-if="profitLoading" class="text-xs text-gray-400">—</div>
  <div v-else class="space-y-0.5 text-xs">
    <div
      v-for="entry in entries"
      :key="entry.key"
      class="text-gray-500 dark:text-gray-400"
    >
      <span class="text-gray-400 dark:text-gray-500">{{ entry.label }}</span>
      <span
        class="ml-1 font-semibold"
        :class="getProfitClass(entry.profit)"
        :title="entry.title"
        >{{ formatProfit(entry.profit, formatUsd) }}</span
      >
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * Orange 特有：分组表格「利润」单元格（今日 / 昨日 / 累计）。
 * 悬浮 title 显示「实付收入 / 上游成本」明细；金额格式化沿用上游 formatUsd（prop 传入）。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminGroup } from '@/types'
import { formatProfit, getProfitClass, type UsdFormatter } from './format'
import { useGroupProfitState } from './useGroupProfit'

const props = defineProps<{
  row: Pick<AdminGroup, 'id'>
  formatUsd: UsdFormatter
}>()

const { t } = useI18n()
const { profitMap, profitLoading } = useGroupProfitState()

const breakdownTitle = (
  income: number | null | undefined,
  cost: number | null | undefined
) => t('admin.groups.profitBreakdown', { income: props.formatUsd(income), cost: props.formatUsd(cost) })

const entries = computed(() => {
  const item = profitMap.value.get(props.row.id)
  return [
    {
      key: 'today',
      label: t('admin.groups.profitToday'),
      profit: item?.today_profit ?? 0,
      title: breakdownTitle(item?.today_revenue, item?.today_upstream_cost)
    },
    {
      key: 'yesterday',
      label: t('admin.groups.profitYesterday'),
      profit: item?.yesterday_profit ?? 0,
      title: breakdownTitle(item?.yesterday_revenue, item?.yesterday_upstream_cost)
    },
    {
      key: 'total',
      label: t('admin.groups.profitTotal'),
      profit: item?.total_profit ?? 0,
      title: breakdownTitle(item?.total_revenue, item?.total_upstream_cost)
    }
  ]
})
</script>
