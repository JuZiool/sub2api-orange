/**
 * Orange 特有：Orange 页面与组件的中文文案命名空间。
 *
 * 顶层键统一加 `orange` 前缀（`orangeNav` / `orangeTokenRanking` / `orangeSettings`），
 * 避免与上游常见顶层键（`nav` / `dashboard` / `admin` 等）碰撞后静默覆盖。
 */
export default {
  orangeNav: {
    tokenRanking: 'Token 排行'
  },
  orangeTokenRanking: {
    title: 'Token 排行榜',
    description: '全站用户的 Token 用量排行，邮箱已脱敏，不展示费用。',
    dimensionLabel: '排行维度',
    dimensionUser: '用户榜',
    dimensionModel: '模型榜',
    rangeLabel: '统计范围',
    rangeToday: '今日',
    rangeWeek: '本周',
    rangeMonth: '本月',
    period: '{start} 至 {end}',
    topThreeTitle: 'Top 3',
    topThreeHint: '按总 Token 排名',
    tableTitle: '完整排行',
    rank: '排名',
    user: '用户',
    model: '模型',
    requests: '请求数',
    input: '输入 Token',
    output: '输出 Token',
    cache: '缓存 Token',
    totalTokens: '总 Token',
    rowsPerPage: '每页条数',
    prevPage: '上一页',
    nextPage: '下一页',
    pageIndicator: '第 {page} 页',
    cachedHint: '排行结果缓存约 60 秒，翻页不会触发实时统计。',
    noData: '该范围内暂无排行数据',
    failedToLoad: '加载 Token 排行榜失败，请稍后重试。',
    featureDisabled: 'Token 排行榜已被管理员关闭。',
    backToDashboard: '返回仪表盘'
  },
  orangeSettings: {
    title: 'Orange 功能开关',
    description: '这些开关来自 Orange 自有配置表，切换后立即保存生效，与页面底部「保存设置」无关。',
    instantHint: '此开关切换后立即生效，无需点击页面底部的保存按钮。',
    loading: '正在读取 Orange 功能开关…',
    loadFailed: '读取 Orange 功能开关失败。',
    retry: '重新读取',
    saving: '正在保存…',
    saved: '已保存',
    saveFailed: '保存失败，已恢复原值。',
    updatedAt: '最后更新时间：{time}',
    tokenRanking: {
      label: '启用 Token 排行榜',
      enabledHint: '关闭后用户侧导航入口消失、直接访问会被拦截，排行接口返回 403。'
    }
  },
  orangeKeyGroupChain: {
    unavailableBadge: '不可用（无权限）',
    unavailableShort: '不可用',
    unavailableChainHint: '标记为不可用的分组已无权使用，保存时会自动跳过这些分组。',
    fallbackUnavailableHint: '当前兜底分组已不可用，保存后将自动清空兜底分组。',
    primaryUnavailable: '主分组已不可用，请先更换主分组。',
    fallbackCleared: '兜底分组已不可用，保存后将清空兜底分组。',
    aggregateChainCleared: '可用分组不足两个，保存后将清空分组链。'
  }
}
