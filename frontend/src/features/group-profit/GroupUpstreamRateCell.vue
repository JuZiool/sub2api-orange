<template>
  <span v-if="profitLoading" class="text-sm text-gray-400 dark:text-gray-500"
    >—</span
  >
  <span v-else class="text-sm text-gray-700 dark:text-gray-300">{{
    rateText
  }}</span>
</template>

<script setup lang="ts">
/**
 * Orange 特有：分组表格「上游成本倍率」单元格。
 * 数据来自 Orange 自建表（利润接口）；未加载完成时显示 "—"，缺失记录时按 1.0x 展示。
 */
import { computed } from 'vue'
import type { AdminGroup } from '@/types'
import { formatUpstreamRate } from './format'
import { useGroupProfitState } from './useGroupProfit'

const props = defineProps<{ row: Pick<AdminGroup, 'id'> }>()
const { profitMap, profitLoading } = useGroupProfitState()

const rateText = computed(() =>
  formatUpstreamRate(profitMap.value.get(props.row.id)?.upstream_rate_multiplier)
)
</script>
