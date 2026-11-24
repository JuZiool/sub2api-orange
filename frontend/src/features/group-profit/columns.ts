/**
 * Orange 特有：GroupsView 分组表格的两列列定义。
 *
 * 数据来自 Orange 自建表（利润接口），上游服务端排序只认 `groups` 表列，
 * 因此两列均不可排序，避免把不存在的列名传给官方排序接口。
 */
import type { Column } from '@/components/common/types'

/** 列定义所需的最小翻译函数签名（与 useI18n().t 兼容）。 */
export type TranslateKey = (key: string) => string

/** Orange 特有：上游成本倍率列（不可排序）。 */
export const upstreamRateColumn = (t: TranslateKey): Column => ({
  key: 'upstream_rate_multiplier',
  label: t('admin.groups.columns.upstreamRateMultiplier'),
  sortable: false
})

/** Orange 特有：利润列（今日 / 昨日 / 累计，不可排序）。 */
export const profitColumn = (t: TranslateKey): Column => ({
  key: 'profit',
  label: t('admin.groups.columns.profit'),
  sortable: false
})
