<template>
  <div v-if="active && stats" data-testid="overdraft-stats" class="mb-0.5 flex items-center">
    <div class="flex items-center gap-1.5 text-[9px] text-orange-600 dark:text-orange-400">
      <span
        :class="[
          'rounded bg-orange-50 px-1.5 py-0.5 dark:bg-orange-900/30',
          statusClass || 'text-orange-600 dark:text-orange-400',
        ]"
      >
        {{ status || t('usage.overdraft') }}
      </span>
      <span class="rounded bg-orange-50 px-1.5 py-0.5 dark:bg-orange-900/30">
        {{ formatRequests }} req
      </span>
      <span class="rounded bg-orange-50 px-1.5 py-0.5 dark:bg-orange-900/30">
        {{ formatTokens }}
      </span>
      <span class="rounded bg-orange-50 px-1.5 py-0.5 dark:bg-orange-900/30" :title="t('usage.accountBilled')">
        A ${{ formatCost }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatCompactNumber } from '@/utils/format'

const props = defineProps<{
  active?: boolean
  stats?: WindowStats | null
  status?: string
  statusClass?: string
}>()

const { t } = useI18n()

const formatRequests = computed(() => formatCompactNumber(props.stats?.requests ?? 0, { allowBillions: false }))
const formatTokens = computed(() => formatCompactNumber(props.stats?.tokens ?? 0))
const formatCost = computed(() => (props.stats?.cost ?? 0).toFixed(2))
</script>
