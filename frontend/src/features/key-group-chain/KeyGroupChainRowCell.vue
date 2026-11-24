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
        <!-- 不可用标记：该分组已不在用户可绑定分组列表中（例如被改为专属），保存时会被自动跳过 -->
        <span
          v-if="!isGroupAvailable(entry.group_id)"
          data-test="chain-member-unavailable"
          class="shrink-0 rounded bg-red-50 px-1.5 py-0.5 text-[11px] font-medium text-red-600 dark:bg-red-900/25 dark:text-red-300"
          :title="t('orangeKeyGroupChain.unavailableBadge')"
        >
          {{ t('orangeKeyGroupChain.unavailableShort') }}
        </span>
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
 *
 * 不可用链成员（分组被改为专属后用户无权绑定）：按 `groupOptions`（当前用户可绑定分组）
 * 在徽标后追加红色「不可用」标记，与编辑弹窗的标注保持一致。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { ApiKey } from '@/types'
import { isAggregatedKey, sortedKeyGroups } from './chain'
import FallbackGroupPicker from './FallbackGroupPicker.vue'
import { isGroupIdAvailable, groupOptionIdSet } from './useKeyGroupChain'
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

const chainRow = computed(() => props.row as KeyGroupChainKey)
const isAggregated = computed(() => isAggregatedKey(chainRow.value))
const sortedGroups = computed(() => sortedKeyGroups(chainRow.value))
/**
 * 当前用户可绑定分组的 id 集合：链成员不在其中即视为不可用（分组被改为专属等）。
 *
 * 选项列表为空（尚未加载 / 加载失败）时不做判定，避免把整条链误标成不可用；
 * 列表数据没有 `available` 字段，因此这里只能用「是否在可绑定列表中」这一启发式。
 */
const availableGroupIds = computed(() => groupOptionIdSet(props.groupOptions))
const isGroupAvailable = (groupId: number): boolean =>
  props.groupOptions.length === 0 || isGroupIdAvailable(groupId, availableGroupIds.value)
</script>
