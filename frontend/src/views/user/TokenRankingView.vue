<template>
  <AppLayout>
    <div class="space-y-6">
      <header class="flex flex-wrap items-start gap-4">
        <span class="flex h-11 w-11 items-center justify-center rounded-xl bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400">
          <Icon name="trophy" size="lg" />
        </span>
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('tokenRanking.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('tokenRanking.description') }}</p>
        </div>
      </header>

      <div v-if="errorMessage" role="alert" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/30 dark:text-red-300">
        {{ errorMessage }}
      </div>

      <div v-if="loading" class="card flex min-h-[240px] items-center justify-center">
        <LoadingSpinner />
      </div>

      <template v-else>
        <section class="space-y-4">
          <div class="flex flex-wrap items-end justify-between gap-3">
            <div>
              <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('tokenRanking.weeklyTitle') }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ t('tokenRanking.dateRange', { start: data?.weekly.start_date || '-', end: data?.weekly.end_date || '-' }) }}
              </p>
            </div>
            <span class="text-sm text-gray-500 dark:text-gray-400">{{ t('tokenRanking.topThree') }}</span>
          </div>

          <div v-if="data?.weekly.items.length" class="grid grid-cols-1 gap-4 md:grid-cols-3">
            <article v-for="item in data.weekly.items" :key="`week-${item.user_id}`" class="card relative overflow-hidden p-5" :class="weeklyCardClass(item.rank)">
              <div class="flex items-center justify-between gap-3">
                <span class="flex h-9 w-9 items-center justify-center rounded-full text-sm font-semibold" :class="rankBadgeClass(item.rank)">{{ item.rank }}</span>
                <span class="text-xs font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">{{ t('tokenRanking.totalTokens') }}</span>
              </div>
              <p class="mt-5 truncate text-base font-semibold text-gray-900 dark:text-white" :title="item.email">{{ item.email }}</p>
              <div class="mt-2 flex items-end justify-between gap-3">
                <strong class="text-2xl font-semibold tabular-nums text-gray-950 dark:text-white">{{ formatTokens(item.total_tokens) }}</strong>
                <span class="text-xs tabular-nums text-gray-400 dark:text-gray-500">{{ formatRequests(item.requests) }} {{ t('tokenRanking.requests') }}</span>
              </div>
            </article>
          </div>
          <EmptyState v-else :title="t('tokenRanking.noData')" />
        </section>

        <section class="card overflow-hidden">
          <div class="flex flex-wrap items-end justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div>
              <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('tokenRanking.dailyTitle') }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ t('tokenRanking.dateRange', { start: data?.daily.start_date || '-', end: data?.daily.end_date || '-' }) }}
              </p>
            </div>
            <span class="text-sm text-gray-500 dark:text-gray-400">{{ t('tokenRanking.topTen') }}</span>
          </div>

          <div v-if="data?.daily.items.length" class="overflow-x-auto">
            <table class="w-full min-w-[780px] divide-y divide-gray-200 dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800">
                <tr>
                  <th class="w-16 px-5 py-3 text-left text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">#</th>
                  <th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">{{ t('tokenRanking.user') }}</th>
                  <th v-for="column in dailyColumns" :key="column.key" class="px-5 py-3 text-right text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">{{ t(column.label) }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
                <tr v-for="item in data.daily.items" :key="`day-${item.user_id}`" class="transition-colors hover:bg-gray-50 dark:hover:bg-dark-800/60">
                  <td class="px-5 py-4"><span class="flex h-7 w-7 items-center justify-center rounded-full text-xs font-semibold" :class="rankBadgeClass(item.rank)">{{ item.rank }}</span></td>
                  <td class="max-w-[260px] truncate px-5 py-4 text-sm font-medium text-gray-800 dark:text-gray-200" :title="item.email">{{ item.email }}</td>
                  <td v-for="column in dailyColumns" :key="column.key" class="whitespace-nowrap px-5 py-4 text-right text-sm tabular-nums text-gray-600 dark:text-gray-400" :class="column.key === 'total_tokens' ? 'font-semibold text-gray-950 dark:text-white' : ''">
                    {{ column.key === 'requests' ? formatRequests(item[column.key]) : formatTokens(item[column.key]) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <EmptyState v-else :title="t('tokenRanking.noData')" />
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { getTokenRanking, type TokenRankingResponse } from '@/api/usage'
import { formatCompactNumber } from '@/utils/format'

const { t } = useI18n()
const data = ref<TokenRankingResponse | null>(null)
const loading = ref(false)
const errorMessage = ref('')

const dailyColumns: Array<{ key: 'requests' | 'input_tokens' | 'output_tokens' | 'cache_tokens' | 'total_tokens'; label: string }> = [
  { key: 'requests', label: 'tokenRanking.requests' },
  { key: 'input_tokens', label: 'tokenRanking.input' },
  { key: 'output_tokens', label: 'tokenRanking.output' },
  { key: 'cache_tokens', label: 'tokenRanking.cache' },
  { key: 'total_tokens', label: 'tokenRanking.totalTokens' },
]

const formatTokens = (value: number) => formatCompactNumber(value)
const formatRequests = (value: number) => value.toLocaleString()

const rankBadgeClass = (rank: number) => {
  if (rank === 1) return 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-300'
  if (rank === 2) return 'bg-gray-200 text-gray-600 dark:bg-gray-500/20 dark:text-gray-300'
  if (rank === 3) return 'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-300'
  return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
}

const weeklyCardClass = (rank: number) => {
  if (rank === 1) return 'border-amber-200 dark:border-amber-800/60'
  if (rank === 3) return 'border-orange-200 dark:border-orange-800/60'
  return 'border-gray-200 dark:border-dark-700'
}

const normalizeResponse = (value: TokenRankingResponse): TokenRankingResponse => ({
  weekly: { ...value.weekly, items: value.weekly?.items ?? [] },
  daily: { ...value.daily, items: value.daily?.items ?? [] },
})

const getBrowserTimezone = (): string | undefined => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined
  } catch {
    return undefined
  }
}

const load = async () => {
  loading.value = true
  errorMessage.value = ''
  try {
    const timezone = getBrowserTimezone()
    data.value = normalizeResponse(await getTokenRanking(timezone ? { timezone } : undefined))
  } catch {
    data.value = null
    errorMessage.value = t('tokenRanking.failedToLoad')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>
