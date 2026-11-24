/**
 * Orange 特有：Orange 页面与组件的英文文案命名空间。
 *
 * 顶层键与中文侧完全对称（`orangeNav` / `orangeTokenRanking` / `orangeSettings`），
 * 避免与上游常见顶层键（`nav` / `dashboard` / `admin` 等）碰撞后静默覆盖。
 */
export default {
  orangeNav: {
    tokenRanking: 'Token Ranking'
  },
  orangeTokenRanking: {
    title: 'Token Ranking',
    description: 'Site-wide token usage ranking. Emails are masked and costs are never shown.',
    dimensionLabel: 'Ranking dimension',
    dimensionUser: 'Users',
    dimensionModel: 'Models',
    rangeLabel: 'Time range',
    rangeToday: 'Today',
    rangeWeek: 'This week',
    rangeMonth: 'This month',
    period: '{start} to {end}',
    topThreeTitle: 'Top 3',
    topThreeHint: 'Ranked by total tokens',
    tableTitle: 'Full ranking',
    rank: 'Rank',
    user: 'User',
    model: 'Model',
    requests: 'Requests',
    input: 'Input tokens',
    output: 'Output tokens',
    cache: 'Cache tokens',
    totalTokens: 'Total tokens',
    rowsPerPage: 'Rows per page',
    prevPage: 'Previous',
    nextPage: 'Next',
    pageIndicator: 'Page {page}',
    cachedHint: 'Ranking results are cached for about 60 seconds; paging does not recompute them.',
    noData: 'No ranking data in this range yet',
    failedToLoad: 'Failed to load the token ranking. Please try again later.',
    featureDisabled: 'The token ranking has been disabled by an administrator.',
    backToDashboard: 'Back to dashboard'
  },
  orangeSettings: {
    title: 'Orange feature flags',
    description: 'These flags live in Orange-owned configuration. Toggling one saves immediately and does not use the Save Settings button at the bottom of the page.',
    instantHint: 'This flag takes effect immediately; there is no need to click the Save button at the bottom of the page.',
    loading: 'Loading Orange feature flags…',
    loadFailed: 'Failed to load Orange feature flags.',
    retry: 'Reload',
    saving: 'Saving…',
    saved: 'Saved',
    saveFailed: 'Save failed; the previous value has been restored.',
    updatedAt: 'Last updated: {time}',
    tokenRanking: {
      label: 'Enable token ranking',
      enabledHint: 'When disabled, the user-side navigation entry disappears, direct visits are blocked, and the ranking API returns 403.'
    }
  },
  orangeKeyGroupChain: {
    unavailableBadge: 'Unavailable (no access)',
    unavailableShort: 'Unavailable',
    unavailableChainHint: 'Groups marked unavailable are no longer accessible; they are skipped automatically when saving.',
    fallbackUnavailableHint: 'The current fallback group is no longer available; the fallback group will be cleared after saving.',
    primaryUnavailable: 'The primary group is no longer available. Please choose another primary group first.',
    fallbackCleared: 'The fallback group is no longer available; it will be cleared after saving.',
    aggregateChainCleared: 'Fewer than two available groups remain; the group chain will be cleared after saving.'
  }
}
