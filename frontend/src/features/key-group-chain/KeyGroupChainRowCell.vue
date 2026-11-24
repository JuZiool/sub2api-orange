<template>
  <div class="flex flex-col gap-1">
    <!-- 聚合密钥：按优先级展示分组链，不展示兜底 -->
    <div v-if="isAggregated" class="flex flex-col gap-1" data-test="aggregate-group-chain-cell">
      <div
        v-for="entry in sortedGroups"
        :key="`agg-${row.id}-${entry.group_id}`"
        class="flex items-center gap-1.5"
      >
        <span
          class="flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-full bg-primary-50 text-[10px] font-semibold text-primary-700 dark:bg-primary-500/15 dark:text-primary-300"
        >
          {{ entry.priority }}
        </span>
        <GroupBadge
          v-if="entry.group"
          :name="entry.group.name"
          :platform="entry.group.platform"
          :subscription-type="entry.group.subscription_type"
          :rate-multiplier="entry.group.rate_multiplier"
          :user-rate-multiplier="userGroupRates[entry.group.id]"
          :peak-rate-enabled="entry.group.peak_rate_enabled"
          :peak-start="entry.group.peak_start"
          :peak-end="entry.group.peak_end"
          :peak-rate-multiplier="entry.group.peak_rate_multiplier"
        />
        <span v-else class="text-sm text-gray-500 dark:text-gray-400">#{{ entry.group_id }}</span>
      </div>
    </div>
    <template v-else>
      <slot />
      <FallbackGroupPicker
        :row="row"
        :group-options="groupOptions"
        :user-group-rates="userGroupRates"
        @refresh="emit('refresh')"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Orange 特有：密钥列表“分组”列的容器。
 *
 * - 聚合密钥：按链上优先级渲染分组徽标（不展示兜底行）；
 * - 普通 Key / 兜底密钥：原样渲染上游的“主分组”单元格（通过默认插槽传入），
 *   并在下方追加 Orange 的兜底分组选择行。
 *
 * 无分组链配置（group_chain_mode 缺省、groups 为空）时，仅渲染上游主分组单元格 +
 * 一行“未设置兜底分组”，不产生任何请求。
 */
import { computed } from 'vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { ApiKey } from '@/types'
import { isAggregatedKey, sortedKeyGroups } from './chain'
import FallbackGroupPicker from './FallbackGroupPicker.vue'
import type { GroupOptionView, KeyGroupChainKey } from './types'

const props = defineProps<{
  row: ApiKey
  groupOptions: GroupOptionView[]
  userGroupRates: Record<number, number>
}>()

const emit = defineEmits<{
  (e: 'refresh'): void
}>()

const chainRow = computed(() => props.row as KeyGroupChainKey)
const isAggregated = computed(() => isAggregatedKey(chainRow.value))
const sortedGroups = computed(() => sortedKeyGroups(chainRow.value))
</script>
