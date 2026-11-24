/**
 * Orange 特有：分组「上游成本倍率 / 利润」feature 对外出口。
 *
 * 上游 GroupsView.vue 仅从这里取：列定义、两个单元格组件、表单输入组件与一次状态初始化。
 */
export { default as GroupProfitCell } from './GroupProfitCell.vue'
export { default as GroupUpstreamRateCell } from './GroupUpstreamRateCell.vue'
export { default as GroupUpstreamRateField } from './GroupUpstreamRateField.vue'
export { profitColumn, upstreamRateColumn } from './columns'
export {
  useGroupProfit,
  useGroupProfitState,
  type GroupProfitController,
  type GroupProfitInit
} from './useGroupProfit'
