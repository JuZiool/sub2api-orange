<!--
  Orange 特有：使用记录费用列的长上下文计费标记（独立一行，位于 A$ 账号计费行下方）。

  与上游内联位的区别只在位置：文案 `admin.usage.longContext`、tooltip
  `admin.usage.longContextPricingTooltip`、data-testid 与配色均与上游保持一致。
  无 `long_context_billing_applied` 的行不渲染任何内容。
-->
<template>
  <div v-if="row.long_context_billing_applied" class="mt-0.5 flex items-center">
    <span
      data-testid="long-context-billing-marker"
      :title="t('admin.usage.longContextPricingTooltip')"
      class="inline-flex items-center rounded px-1 py-px text-[10px] font-semibold leading-tight bg-amber-100 text-amber-700 ring-1 ring-inset ring-amber-200 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/30"
    >{{ t('admin.usage.longContext') }}</span>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AdminUsageLog } from '@/types'

defineProps<{
  row: AdminUsageLog
}>()

const { t } = useI18n()
</script>
