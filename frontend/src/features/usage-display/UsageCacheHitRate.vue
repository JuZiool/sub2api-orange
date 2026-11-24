<!--
  Orange 特有：使用记录 TOKEN 列里的缓存命中率展示。

  挂在缓存行（缓存读取 / 缓存创建）下方，与上游 TOKEN 列其它行同一视觉节奏：
  标签取 `usage.cacheHitInline`，数值按档位着色，无法计算时回落「-」。
-->
<template>
  <div
    data-testid="token-cache-hit"
    class="flex items-center gap-1 text-xs leading-tight"
    :title="t('usage.cacheHitHint')"
  >
    <span class="text-gray-400 dark:text-gray-500">{{ t('usage.cacheHitInline') }}</span>
    <span
      v-if="rate !== null"
      data-testid="cache-hit-rate"
      class="font-medium tabular-nums"
      :class="cacheHitRateClass(rate)"
    >{{ rate.toFixed(1) }}%</span>
    <span v-else class="text-gray-400 dark:text-gray-500">-</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminUsageLog } from '@/types'
import { cacheHitRateClass, cacheHitRatePercent } from './cacheHitRate'

const props = defineProps<{
  row: AdminUsageLog
}>()

const { t } = useI18n()

const rate = computed(() => cacheHitRatePercent(props.row))
</script>
