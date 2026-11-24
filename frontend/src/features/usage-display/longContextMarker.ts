/**
 * Orange 特有：长上下文计费标记的位置开关。
 *
 * Orange 把「长上下文」标记从上游的「费用首行内联」下移为「A$ 账号计费行下方独立一行」，
 * 因此上游内联位需要关闭。`UsageTable.vue` 只在该 span 的 v-if 上追加此常量：
 * 常量恒为 false 时内联位不渲染，标记由 `UsageLongContextMarker.vue` 在下方单独渲染。
 *
 * 若将来需要回到上游位置，把此常量改为 true 并移除下方挂载即可，无需理解其它逻辑。
 */
export const ORANGE_INLINE_LONG_CONTEXT_MARKER = false
