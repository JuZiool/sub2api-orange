<template>
  <div class="mt-3 border-t border-gray-200 pt-2 dark:border-dark-600" data-testid="codex-ticket-history">
    <div class="flex items-center gap-3">
      <button type="button" class="text-xs font-medium text-primary-600 hover:text-primary-500 dark:text-primary-400" :aria-expanded="expanded" data-testid="ticket-history-toggle" @click="toggleHistory">
        {{ t(`${key}.title`) }}
        <Icon :name="expanded ? 'chevronUp' : 'chevronDown'" size="xs" class="ml-1 inline" />
      </button>
      <button v-if="expanded" type="button" class="text-xs text-gray-500 hover:text-primary-600 disabled:opacity-50 dark:text-gray-400" :disabled="loading" data-testid="ticket-history-refresh" @click="loadHistory">
        {{ t('common.refresh') }}
      </button>
    </div>
    <div v-if="expanded" class="mt-2 space-y-2" aria-live="polite">
      <p class="text-[11px] text-gray-400 dark:text-gray-500">{{ t(`${key}.hint`, { limit }) }}</p>
      <p v-if="loading" class="text-xs text-gray-500 dark:text-gray-400">{{ t('common.loading') }}</p>
      <p v-else-if="failed" class="text-xs text-amber-600 dark:text-amber-400">{{ t(`${key}.loadFailed`) }}</p>
      <p v-else-if="events.length === 0" class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${key}.empty`) }}</p>
      <ol v-else class="max-h-80 space-y-2 overflow-y-auto pr-1">
        <li v-for="event in events" :key="event.id" class="rounded-md bg-white p-2.5 text-xs dark:bg-dark-800" :data-testid="`ticket-history-event-${event.id}`">
          <div class="flex flex-wrap items-center justify-between gap-1.5">
            <span :class="outcomeClass(event.outcome)" class="font-medium">{{ t(`${key}.${safeOutcome(event.outcome)}`) }}</span>
            <time class="text-gray-500 dark:text-gray-400">{{ formatDateTime(event.at) }}</time>
          </div>
          <p v-if="event.outcome !== 'success' && event.error_code" class="mt-1 break-words text-amber-600 dark:text-amber-400">{{ t(codexTicketErrorKey(event.error_code)) }}</p>
          <div class="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-gray-500 dark:text-gray-400">
            <span>{{ t(`${key}.length`, { actual: event.length, target: event.target_length }) }}</span>
            <span v-if="event.http_status">HTTP {{ event.http_status }}</span>
            <span v-if="event.proxy_index">{{ t(`${key}.proxy`, { index: event.proxy_index }) }}</span>
            <span>{{ t(`${key}.duration`, { duration: event.duration_ms }) }}</span>
          </div>
        </li>
      </ol>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import Icon from '@/components/icons/Icon.vue'
import type { CodexTicketHistoryEvent } from '@/types'
import { codexTicketErrorKey } from '@/utils/codexTicketDiagnostics'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ accountId: number; model: string }>()
const { t } = useI18n()
const key = 'admin.accounts.openai.codexTicketHistory'
const expanded = ref(false)
const loading = ref(false)
const failed = ref(false)
const events = ref<CodexTicketHistoryEvent[]>([])
const limit = ref(20)
let revision = 0
let controller: AbortController | undefined

function cancelRequest() {
  revision += 1
  controller?.abort()
  loading.value = false
}

function safeOutcome(outcome: string) {
  return outcome === 'success' || outcome === 'canceled' ? outcome : 'failure'
}

function outcomeClass(outcome: string) {
  if (outcome === 'success') return 'text-emerald-600 dark:text-emerald-400'
  if (outcome === 'canceled') return 'text-gray-500 dark:text-gray-400'
  return 'text-amber-600 dark:text-amber-400'
}

async function loadHistory() {
  controller?.abort()
  controller = new AbortController()
  const requestRevision = ++revision
  loading.value = true
  failed.value = false
  try {
    const result = await adminAPI.accounts.getCodexTicketHistory(props.accountId, props.model, controller.signal)
    if (requestRevision !== revision) return
    events.value = result.events
    limit.value = result.limit
  } catch {
    if (requestRevision === revision) {
      failed.value = true
      events.value = []
    }
  } finally {
    if (requestRevision === revision) loading.value = false
  }
}

function toggleHistory() {
  expanded.value = !expanded.value
  if (expanded.value) void loadHistory()
  else cancelRequest()
}

watch(() => [props.accountId, props.model], () => {
  cancelRequest()
  expanded.value = false
  loading.value = false
  failed.value = false
  events.value = []
})

onUnmounted(cancelRequest)
</script>
