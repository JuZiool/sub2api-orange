<template>
  <div
    v-if="windows.length > 0"
    data-testid="overdraft-stats"
    class="mb-0.5 flex min-w-0 items-center text-[9px] text-gray-500 dark:text-gray-400"
    :title="detailsText"
    :aria-label="detailsText"
  >
    <div class="min-w-0 truncate whitespace-nowrap">
      <template v-for="(window, index) in windows" :key="window.label">
        <span v-if="index > 0" class="mx-1">|</span>
        <span
          :data-window="window.label"
          class="rounded bg-orange-50 px-1.5 py-0.5 dark:bg-orange-900/30"
          :class="statusClass || 'text-orange-600 dark:text-orange-400'"
        >
          {{ window.label }}{{ status || t('usage.overdraft') }}
        </span>
        <span class="ml-1">
          {{ formatRequests(window.stats) }} req · {{ formatTokens(window.stats) }} ·
          <span :title="t('usage.accountBilled')">A ${{ formatCost(window.stats) }}</span>
        </span>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatCompactNumber } from '@/utils/format'

interface OverdraftWindow {
  label: '5h' | '7d'
  stats: WindowStats
}

const props = defineProps<{
  fiveHourActive?: boolean
  fiveHourStats?: WindowStats | null
  sevenDayActive?: boolean
  sevenDayStats?: WindowStats | null
  status?: string
  statusClass?: string
}>()

const { t } = useI18n()

const windows = computed<OverdraftWindow[]>(() => {
  const result: OverdraftWindow[] = []
  if (props.fiveHourActive && props.fiveHourStats) {
    result.push({ label: '5h', stats: props.fiveHourStats })
  }
  if (props.sevenDayActive && props.sevenDayStats) {
    result.push({ label: '7d', stats: props.sevenDayStats })
  }
  return result
})

const formatRequests = (stats: WindowStats) => formatCompactNumber(stats.requests, { allowBillions: false })
const formatTokens = (stats: WindowStats) => formatCompactNumber(stats.tokens)
const formatCost = (stats: WindowStats) => stats.cost.toFixed(2)

const detailsText = computed(() => {
  const status = props.status || t('usage.overdraft')
  return windows.value
    .map(({ label, stats }) => `${label}${status} · ${formatRequests(stats)} req · ${formatTokens(stats)} · A $${formatCost(stats)}`)
    .join(' | ')
})
</script>
