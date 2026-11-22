<!--
  Orange 特有：Token 排行榜（用户侧）。
  与上游 `views/user/UsageView.vue` 完全独立的新页面，不改动任何上游页面：
  - 维度：用户榜 / 模型榜；范围：今日 / 本周 / 本月；支持 limit 与分页（has_more）；
  - 展示 Top3 卡片 + 明细表格，含加载 / 空 / 错误 / 已关闭四种状态；
  - 开关关闭时（路由守卫之外的页内兜底）直接跳转仪表盘。
-->
<template>
  <AppLayout>
    <div class="space-y-6">
      <header class="flex flex-wrap items-start gap-4">
        <span
          class="flex h-11 w-11 items-center justify-center rounded-xl bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400"
        >
          <Icon name="trophy" size="lg" />
        </span>
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
            {{ t('orangeTokenRanking.title') }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('orangeTokenRanking.description') }}
          </p>
        </div>
      </header>

      <div
        v-if="featureDisabled"
        role="alert"
        class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-300"
      >
        <span>{{ t('orangeTokenRanking.featureDisabled') }}</span>
        <button type="button" class="btn btn-secondary btn-sm" @click="goDashboard">
          {{ t('orangeTokenRanking.backToDashboard') }}
        </button>
      </div>

      <template v-else>
        <section class="card p-4 sm:p-5">
          <div class="flex flex-wrap items-end gap-x-8 gap-y-4">
            <div>
              <span class="mb-1.5 block text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                {{ t('orangeTokenRanking.dimensionLabel') }}
              </span>
              <div class="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-1 dark:border-dark-600 dark:bg-dark-900/40">
                <button
                  v-for="option in dimensionOptions"
                  :key="option.value"
                  type="button"
                  class="inline-flex items-center justify-center rounded-md px-3 py-1.5 text-sm font-medium transition"
                  :class="
                    dimension === option.value
                      ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-800 dark:text-primary-300'
                      : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
                  "
                  :aria-pressed="dimension === option.value"
                  @click="dimension = option.value"
                >
                  {{ t(option.labelKey) }}
                </button>
              </div>
            </div>

            <div>
              <span class="mb-1.5 block text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                {{ t('orangeTokenRanking.rangeLabel') }}
              </span>
              <div class="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-1 dark:border-dark-600 dark:bg-dark-900/40">
                <button
                  v-for="option in rangeOptions"
                  :key="option.value"
                  type="button"
                  class="inline-flex items-center justify-center rounded-md px-3 py-1.5 text-sm font-medium transition"
                  :class="
                    range === option.value
                      ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-800 dark:text-primary-300'
                      : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
                  "
                  :aria-pressed="range === option.value"
                  @click="range = option.value"
                >
                  {{ t(option.labelKey) }}
                </button>
              </div>
            </div>

            <div>
              <label
                for="orange-ranking-page-size"
                class="mb-1.5 block text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400"
              >
                {{ t('orangeTokenRanking.rowsPerPage') }}
              </label>
              <select id="orange-ranking-page-size" v-model.number="limit" class="input w-28">
                <option v-for="size in limitOptions" :key="size" :value="size">{{ size }}</option>
              </select>
            </div>

            <div class="ml-auto flex items-center gap-3">
              <span v-if="periodLabel" class="text-sm text-gray-500 dark:text-gray-400">{{ periodLabel }}</span>
              <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">
                <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
                <span class="ml-1">{{ t('common.refresh') }}</span>
              </button>
            </div>
          </div>
        </section>

        <div
          v-if="errorMessage"
          role="alert"
          class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/30 dark:text-red-300"
        >
          {{ errorMessage }}
        </div>

        <div v-if="loading" class="card flex min-h-[240px] items-center justify-center">
          <LoadingSpinner />
        </div>

        <template v-else>
          <section class="space-y-4">
            <div class="flex flex-wrap items-end justify-between gap-3">
              <h2 class="text-xl font-semibold text-gray-900 dark:text-white">
                {{ t('orangeTokenRanking.topThreeTitle') }}
              </h2>
              <span class="text-sm text-gray-500 dark:text-gray-400">{{ t('orangeTokenRanking.topThreeHint') }}</span>
            </div>

            <div v-if="topThree.length" class="grid grid-cols-1 gap-4 md:grid-cols-3">
              <article
                v-for="item in topThree"
                :key="`orange-top-${rowKey(item)}`"
                class="card relative overflow-hidden p-5"
                :class="topCardClass(item.rank)"
              >
                <div class="flex items-center justify-between gap-3">
                  <span
                    class="flex h-9 w-9 items-center justify-center rounded-full text-sm font-semibold"
                    :class="rankBadgeClass(item.rank)"
                  >
                    {{ item.rank }}
                  </span>
                  <span class="text-xs font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">
                    {{ t('orangeTokenRanking.totalTokens') }}
                  </span>
                </div>
                <p
                  class="mt-5 truncate text-base font-semibold text-gray-900 dark:text-white"
                  :title="primaryLabel(item)"
                >
                  {{ primaryLabel(item) }}
                </p>
                <div class="mt-2 flex items-end justify-between gap-3">
                  <strong class="text-2xl font-semibold tabular-nums text-gray-950 dark:text-white">
                    {{ formatTokens(item.total_tokens) }}
                  </strong>
                  <span class="text-xs tabular-nums text-gray-400 dark:text-gray-500">
                    {{ formatRequests(item.requests) }} {{ t('orangeTokenRanking.requests') }}
                  </span>
                </div>
              </article>
            </div>
            <EmptyState v-else :title="t('orangeTokenRanking.noData')" />
          </section>

          <section class="card overflow-hidden">
            <div
              class="flex flex-wrap items-end justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700"
            >
              <h2 class="text-xl font-semibold text-gray-900 dark:text-white">
                {{ t('orangeTokenRanking.tableTitle') }}
              </h2>
              <span class="text-sm text-gray-500 dark:text-gray-400">{{ pageIndicator }}</span>
            </div>

            <div v-if="items.length" class="overflow-x-auto">
              <table class="w-full min-w-[860px] divide-y divide-gray-200 dark:divide-dark-700">
                <thead class="bg-gray-50 dark:bg-dark-800">
                  <tr>
                    <th
                      class="w-20 px-5 py-3 text-left text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400"
                    >
                      {{ t('orangeTokenRanking.rank') }}
                    </th>
                    <th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                      {{ t(isUserDimension ? 'orangeTokenRanking.user' : 'orangeTokenRanking.model') }}
                    </th>
                    <th
                      v-for="column in valueColumns"
                      :key="column.key"
                      class="px-5 py-3 text-right text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400"
                    >
                      {{ t(column.labelKey) }}
                    </th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
                  <tr
                    v-for="item in items"
                    :key="`orange-row-${rowKey(item)}`"
                    class="transition-colors hover:bg-gray-50 dark:hover:bg-dark-800/60"
                  >
                    <td class="px-5 py-4">
                      <span
                        class="flex h-7 w-7 items-center justify-center rounded-full text-xs font-semibold"
                        :class="rankBadgeClass(item.rank)"
                      >
                        {{ item.rank }}
                      </span>
                    </td>
                    <td
                      class="max-w-[280px] truncate px-5 py-4 text-sm font-medium text-gray-800 dark:text-gray-200"
                      :title="primaryLabel(item)"
                    >
                      {{ primaryLabel(item) }}
                    </td>
                    <td
                      v-for="column in valueColumns"
                      :key="column.key"
                      class="whitespace-nowrap px-5 py-4 text-right text-sm tabular-nums text-gray-600 dark:text-gray-400"
                      :class="column.key === 'total_tokens' ? 'font-semibold text-gray-950 dark:text-white' : ''"
                    >
                      {{ column.key === 'requests' ? formatRequests(item.requests) : formatTokens(item[column.key]) }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <EmptyState v-else :title="t('orangeTokenRanking.noData')" />

            <div
              class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-5 py-4 dark:border-dark-700"
            >
              <span class="text-xs text-gray-400 dark:text-gray-500">{{ t('orangeTokenRanking.cachedHint') }}</span>
              <div class="flex items-center gap-2">
                <button type="button" class="btn btn-secondary btn-sm" :disabled="page === 0" @click="goPrev">
                  {{ t('orangeTokenRanking.prevPage') }}
                </button>
                <span class="text-sm tabular-nums text-gray-600 dark:text-gray-300">{{ pageIndicator }}</span>
                <button type="button" class="btn btn-secondary btn-sm" :disabled="!hasMore" @click="goNext">
                  {{ t('orangeTokenRanking.nextPage') }}
                </button>
              </div>
            </div>
          </section>
        </template>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import {
  getUsageRanking,
  type OrangeRankingDimension,
  type OrangeRankingItem,
  type OrangeRankingRange
} from '@/api/orange'
import { ORANGE_FEATURE_KEYS, useOrangeFeaturesStore } from '@/stores/orangeFeatures'
import { useAuthStore } from '@/stores/auth'
import { formatCompactNumber } from '@/utils/format'

type ValueColumnKey = 'requests' | 'input_tokens' | 'output_tokens' | 'cache_tokens' | 'total_tokens'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const orangeFeatures = useOrangeFeaturesStore()

const dimensionOptions: Array<{ value: OrangeRankingDimension; labelKey: string }> = [
  { value: 'user', labelKey: 'orangeTokenRanking.dimensionUser' },
  { value: 'model', labelKey: 'orangeTokenRanking.dimensionModel' }
]

const rangeOptions: Array<{ value: OrangeRankingRange; labelKey: string }> = [
  { value: 'today', labelKey: 'orangeTokenRanking.rangeToday' },
  { value: 'week', labelKey: 'orangeTokenRanking.rangeWeek' },
  { value: 'month', labelKey: 'orangeTokenRanking.rangeMonth' }
]

const valueColumns: Array<{ key: ValueColumnKey; labelKey: string }> = [
  { key: 'requests', labelKey: 'orangeTokenRanking.requests' },
  { key: 'input_tokens', labelKey: 'orangeTokenRanking.input' },
  { key: 'output_tokens', labelKey: 'orangeTokenRanking.output' },
  { key: 'cache_tokens', labelKey: 'orangeTokenRanking.cache' },
  { key: 'total_tokens', labelKey: 'orangeTokenRanking.totalTokens' }
]

const limitOptions = [10, 20, 50]

const dimension = ref<OrangeRankingDimension>('user')
const range = ref<OrangeRankingRange>('today')
const limit = ref(10)
const page = ref(0)

const items = ref<OrangeRankingItem[]>([])
const topThree = ref<OrangeRankingItem[]>([])
const hasMore = ref(false)
const periodStart = ref('')
const periodEnd = ref('')

const loading = ref(false)
const errorMessage = ref('')
const featureDisabled = ref(false)

const isUserDimension = computed(() => dimension.value === 'user')
const dashboardPath = computed(() => (auth.isAdmin ? '/admin/dashboard' : '/dashboard'))
const offset = computed(() => page.value * limit.value)
const tokenRankingEnabled = computed(() => orangeFeatures.isEnabled(ORANGE_FEATURE_KEYS.tokenRankingEnabled))

const pageIndicator = computed(() => t('orangeTokenRanking.pageIndicator', { page: page.value + 1 }))

const periodLabel = computed(() => {
  if (!periodStart.value) return ''
  return t('orangeTokenRanking.period', {
    start: periodStart.value,
    end: periodEnd.value || periodStart.value
  })
})

const formatTokens = (value: number | undefined) => formatCompactNumber(value ?? 0)
const formatRequests = (value: number | undefined) => (value ?? 0).toLocaleString()

const primaryLabel = (item: OrangeRankingItem): string =>
  isUserDimension.value ? item.email || '***' : item.model || '-'

const rowKey = (item: OrangeRankingItem): string =>
  `${dimension.value}-${item.user_id ?? item.model ?? item.rank}`

const rankBadgeClass = (rank: number) => {
  if (rank === 1) return 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-300'
  if (rank === 2) return 'bg-gray-200 text-gray-600 dark:bg-gray-500/20 dark:text-gray-300'
  if (rank === 3) return 'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-300'
  return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
}

const topCardClass = (rank: number) => {
  if (rank === 1) return 'border-amber-200 dark:border-amber-800/60'
  if (rank === 3) return 'border-orange-200 dark:border-orange-800/60'
  return 'border-gray-200 dark:border-dark-700'
}

const goDashboard = () => {
  void router.replace(dashboardPath.value)
}

const isForbidden = (error: unknown): boolean => {
  const status = (error as { status?: number } | null)?.status
  return status === 403
}

const load = async () => {
  if (!tokenRankingEnabled.value) return

  loading.value = true
  errorMessage.value = ''
  featureDisabled.value = false
  try {
    const response = await getUsageRanking({
      dimension: dimension.value,
      range: range.value,
      limit: limit.value,
      offset: offset.value
    })
    items.value = response.items
    hasMore.value = response.has_more
    periodStart.value = response.period_start
    periodEnd.value = response.period_end
    // Top3 只取决于「维度 + 范围」，因此仅在首页刷新时重算，翻页时保持稳定。
    if (page.value === 0) topThree.value = response.items.slice(0, 3)
  } catch (error) {
    items.value = []
    hasMore.value = false
    if (page.value === 0) {
      topThree.value = []
      periodStart.value = ''
      periodEnd.value = ''
    }
    if (isForbidden(error)) {
      // 后端兜底：开关关闭时排行接口返回 403。
      featureDisabled.value = true
      errorMessage.value = ''
    } else {
      errorMessage.value = t('orangeTokenRanking.failedToLoad')
    }
  } finally {
    loading.value = false
  }
}

const ensureFeatureEnabled = async (): Promise<boolean> => {
  try {
    await orangeFeatures.ensureLoaded()
  } catch {
    // 拉取失败按默认值（开启）处理，由后端 403 兜底。
  }
  if (tokenRankingEnabled.value) return true
  goDashboard()
  return false
}

const goPrev = () => {
  if (page.value === 0) return
  page.value -= 1
  void load()
}

const goNext = () => {
  if (!hasMore.value) return
  page.value += 1
  void load()
}

watch([dimension, range, limit], () => {
  page.value = 0
  void load()
})

// 管理员在其它标签页关闭开关后，本页在开关状态变化时立即跳走。
watch(tokenRankingEnabled, (enabled) => {
  if (!enabled) {
    featureDisabled.value = true
    goDashboard()
  }
})

onMounted(async () => {
  if (!(await ensureFeatureEnabled())) return
  await load()
})
</script>
