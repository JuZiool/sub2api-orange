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
          :class="[
            'flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-full text-[10px] font-semibold',
            memberStateLabel(entry) === null
              ? 'bg-primary-50 text-primary-700 dark:bg-primary-500/15 dark:text-primary-300'
              : 'bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400'
          ]"
        >
          {{ entry.priority }}
        </span>
        <!-- 不可用成员：徽标灰显 + 状态提示；仍然是链上一行，不改变行数 -->
        <div
          :class="[
            'flex min-w-0 items-center gap-1.5',
            memberStateLabel(entry) === null ? '' : 'opacity-60'
          ]"
          :title="memberStateLabel(entry) ?? undefined"
        >
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
          <!-- 没有 group 详情（契约 B 的 state=missing 成员：分组已删除，列表只回 { group_id, priority, state }）：
               退回 #group_id，并由下方的状态标签说明原因，不能报错也不能空白 -->
          <span
            v-else
            data-test="chain-member-id-fallback"
            class="text-sm text-gray-500 dark:text-gray-400"
          >
            #{{ entry.group_id }}
          </span>
        </div>
        <span
          v-if="memberStateLabel(entry)"
          data-test="chain-member-unavailable"
          :class="['flex-shrink-0 whitespace-nowrap text-[11px]', memberStateToneClass(entry)]"
          :title="memberStateLabel(entry) ?? undefined"
        >
          {{ memberStateLabel(entry) }}
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
 * v0.2.25-8 起列表接口的 groups[] 返回链上全部成员并带 state：不可用成员仍然占一行，
 * 只是徽标灰显并追加状态提示（title + 小标签），行数不变；网关仍只用 available 成员。
 * 其中 state=missing 的成员没有 group 详情（契约 B），退回 #group_id + 「分组已删除」标签，
 * 不允许报错或留白。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { ApiKey } from '@/types'
import { isAggregatedKey, sortedKeyGroups } from './chain'
import FallbackGroupPicker from './FallbackGroupPicker.vue'
import { chainMemberStateLabelKey } from './useKeyGroupChain'
import type { ApiKeyGroupEntry, GroupOptionView, KeyGroupChainKey } from './types'

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

/** 成员状态标签文案（available / 缺省返回 null，不展示标签）。 */
const memberStateLabel = (entry: ApiKeyGroupEntry): string | null => {
  const key = chainMemberStateLabelKey(entry.state)
  return key ? t(key) : null
}

/** 状态提示配色：停用 / 已删除用灰色，权限与平台类问题用黄色。 */
const memberStateToneClass = (entry: ApiKeyGroupEntry): string =>
  entry.state === 'disabled' || entry.state === 'missing'
    ? 'text-gray-400 dark:text-dark-400'
    : 'text-amber-600 dark:text-amber-400'
</script>
