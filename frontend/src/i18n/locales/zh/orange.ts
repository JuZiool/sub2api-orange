/**
 * Orange 特有：Orange 页面与组件的中文文案命名空间。
 *
 * 顶层键统一加 `orange` 前缀（`orangeNav` / `orangeTokenRanking` / `orangeSettings` / `orangeKeyChain`），
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
  // Orange 特有：密钥分组链成员可用性标注 / 移除确认 / 自动修复（v0.2.25-8）
  orangeKeyChain: {
    nameSeparator: '、',
    stateDisabled: '分组已停用',
    stateScopeMismatch: '平台或订阅类型不一致',
    stateNoPermission: '无权限',
    stateMissing: '分组已删除',
    pruneTitle: '保存将移除 {n} 个分组',
    pruneHint: '这些分组仍存在于服务器上的分组链中：{names}。保存后它们会从链里移除，请确认后勾选下方复选框，否则无法保存。',
    pruneConfirmLabel: '我确认移除 {n} 个分组：{names}',
    pruneConfirmRequired: '保存将移除 {n} 个分组：{names}；请先勾选「我确认移除」再保存',
    // 服务端 409 拦截后追加的提示：此刻本地读到的链成员集合已过期，只能重新打开编辑窗口再确认。
    pruneReopenHint: '；请重新打开编辑窗口确认后再保存。',
    healButton: '自动修复不可用分组',
    healing: '正在修复…',
    healHint: '只移除「无权限」「分组已删除」的分组；已停用、平台或订阅类型不一致的分组会保留，需要手动替换。',
    healSuccess: '已自动移除 {n} 个不可用分组',
    healFailed: '自动修复分组失败，请稍后重试。'
  }
}
