/**
 * Orange 特有：分组利润 / 上游成本倍率的展示格式化。
 *
 * 金额口径复用上游 GroupsView 的 `formatUsd`（由单元格以 prop 传入），
 * 这里不复制上游的金额格式化函数体，保证与上游展示完全一致。
 */

/** 上游金额格式化函数签名（GroupsView 的 formatUsd）。 */
export type UsdFormatter = (value: number | null | undefined) => string

/**
 * 利润展示：正数直接显示金额，负数带前置负号。
 * 金额部分沿用上游 formatUsd（如 -12 → "-$12.00"）。
 */
export const formatProfit = (profit: number, formatUsd: UsdFormatter): string =>
  `${profit < 0 ? '-' : ''}${formatUsd(Math.abs(profit))}`

/** 利润配色：正数绿色 / 负数红色 / 0 灰色（与改造前一致）。 */
export const getProfitClass = (profit: number): string =>
  profit > 0
    ? 'text-emerald-600 dark:text-emerald-400'
    : profit < 0
      ? 'text-red-600 dark:text-red-400'
      : 'text-gray-700 dark:text-gray-300'

/** 上游成本倍率展示：默认 1.0；最多 4 位小数并去掉末尾多余的 0（如 0.8 → "0.8x"）。 */
export const formatUpstreamRate = (rate: number | null | undefined): string => {
  const numeric = Number(rate)
  const value = Number.isFinite(numeric) ? numeric : 1
  const fixed = value.toFixed(4).replace(/0+$/, '')
  const trimmed = fixed.endsWith('.') ? `${fixed}0` : fixed
  return `${trimmed}x`
}
