<template>
  <div>
    <label class="input-label">{{
      t("admin.groups.form.upstreamRateMultiplier")
    }}</label>
    <input
      v-model.number="rate"
      type="number"
      min="0"
      max="1000"
      step="0.0001"
      required
      class="input"
      :data-testid="testId"
      @input="handleInput"
    />
    <p class="input-hint">
      {{ t("admin.groups.form.upstreamRateMultiplierHint") }}
    </p>
  </div>
</template>

<script setup lang="ts">
/**
 * Orange 特有：分组创建 / 编辑弹窗里的「上游成本倍率」输入项。
 * 仅用于利润统计，不进官方分组接口，保存成功后单独写入 Orange 自建表。
 *
 * - create：挂载即重置为 1（等价于改造前关闭弹窗时重置表单）；
 * - edit：挂载即按利润缓存回填，缺失时异步补拉（不阻塞弹窗打开与保存）。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useGroupProfitState } from './useGroupProfit'

const props = defineProps<{
  mode: 'create' | 'edit'
  groupId?: number | null
}>()

const { t } = useI18n()
const state = useGroupProfitState()

// 输入框直接绑定到本 feature 的状态（创建 / 编辑各自独立），避免污染官方分组表单
const rate =
  props.mode === 'create' ? state.createUpstreamRate : state.editUpstreamRate

const testId = computed(() =>
  props.mode === 'create'
    ? 'create-upstream-rate-multiplier'
    : 'edit-upstream-rate-multiplier'
)

if (props.mode === 'create') {
  state.beginCreateUpstreamRate()
} else {
  state.beginEditUpstreamRate(props.groupId)
}

const handleInput = () => {
  if (props.mode === 'edit') state.markEditUpstreamRateDirty()
}
</script>
