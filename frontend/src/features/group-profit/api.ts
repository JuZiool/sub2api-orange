/**
 * Orange 特有：分组利润 / 上游成本倍率接口。
 *
 * 数据全部落在 Orange 自建的 `orange_group_upstream_rate` 表与利润聚合查询上，
 * 不读写上游 `groups` 表；接口实现统一收在 `@/api/orange`，这里只暴露本 feature
 * 需要的两个调用，便于后续替换或测试。
 */
import {
  getOrangeGroupProfits,
  updateOrangeGroupUpstreamRate
} from '@/api/orange'

/** Orange 特有：批量读取分组利润 / 上游成本倍率（空 ids 不查询）。 */
export const fetchGroupProfits = (ids: number[]) =>
  getOrangeGroupProfits({ ids })

/** Orange 特有：写入单个分组的上游成本倍率（独立于官方分组接口）。 */
export const saveGroupUpstreamRate = (
  groupId: number,
  upstreamRateMultiplier: number
) => updateOrangeGroupUpstreamRate(groupId, upstreamRateMultiplier)
