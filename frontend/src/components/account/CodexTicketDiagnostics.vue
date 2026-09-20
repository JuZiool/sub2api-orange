<template>
  <div
    v-if="lines.length"
    data-testid="codex-ticket-diagnostics"
    :title="tooltip"
    :class="compact ? 'min-w-0 text-[10px] leading-4 text-gray-500 dark:text-gray-400' : 'mt-2 space-y-1 text-xs text-gray-500 dark:text-gray-400'"
  >
    <p v-if="compact" class="max-w-64 truncate" :class="hasWarning ? 'text-amber-600 dark:text-amber-400' : ''">{{ summary }}</p>
    <template v-else>
      <p v-if="activity" :class="ticket.paused ? 'text-amber-600 dark:text-amber-400' : 'text-blue-600 dark:text-blue-400'">{{ activity }}</p>
      <p v-if="failure" class="break-words text-amber-600 dark:text-amber-400">{{ failure }}</p>
      <p v-if="unknownPlan" class="text-amber-600 dark:text-amber-400">{{ unknownPlan }}</p>
      <div class="grid gap-x-4 gap-y-1 sm:grid-cols-2">
        <p v-for="line in details" :key="line">{{ line }}</p>
      </div>
      <p v-if="ticket.attempts !== undefined" class="text-[11px] text-gray-400 dark:text-gray-500">{{ t(`${key}.counterHint`) }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexTurnTicketStatus } from '@/types'
import { formatDateTime } from '@/utils/format'
import { codexTicketErrorKey } from '@/utils/codexTicketDiagnostics'

const props = withDefaults(defineProps<{ ticket: CodexTurnTicketStatus; compact?: boolean }>(), { compact: false })
const { t } = useI18n()
const key = 'admin.accounts.openai.codexTicketDiagnostics'

const activity = computed(() => {
  if (props.ticket.in_progress) return t(`${key}.inProgress`)
  if (props.ticket.paused) return t(`${key}.paused`)
  return ''
})
const errorLabel = computed(() => {
  const code = props.ticket.last_error_code
  if (!code) return ''
  // Render fixed messages rather than upstream bodies, which may contain credentials.
  return t(codexTicketErrorKey(code))
})
const failure = computed(() => errorLabel.value ? t(`${key}.lastError`, { reason: errorLabel.value }) : '')
const unknownPlan = computed(() => props.ticket.plan_known === false
  ? t(`${key}.unknownPlan`, { length: props.ticket.target_length }) : '')
const details = computed(() => {
  const ticket = props.ticket
  const result: string[] = []
  if (ticket.attempts !== undefined) result.push(t(`${key}.attempts`, { count: ticket.attempts }))
  if (ticket.successes !== undefined) result.push(t(`${key}.successCount`, { count: ticket.successes }))
  if (ticket.failures !== undefined) result.push(t(`${key}.failureCount`, { count: ticket.failures }))
  if (ticket.inject_misses !== undefined) result.push(t(`${key}.injectMissCount`, { count: ticket.inject_misses }))
  if (ticket.last_inject_miss_at) result.push(t(`${key}.lastInjectMiss`, { time: formatDateTime(ticket.last_inject_miss_at) }))
  if (ticket.consecutive_failures) result.push(t(`${key}.failures`, { count: ticket.consecutive_failures }))
  if (ticket.last_length !== undefined && (ticket.attempts || ticket.last_attempt_at || ticket.last_error_code)) {
    result.push(t(`${key}.length`, { actual: ticket.last_length, target: ticket.target_length }))
  }
  if (ticket.last_http_status) result.push(t(`${key}.httpStatus`, { status: ticket.last_http_status }))
  if (ticket.last_proxy_index) result.push(t(`${key}.proxy`, { index: ticket.last_proxy_index }))
  if (ticket.last_attempt_at) result.push(t(`${key}.lastAttempt`, { time: formatDateTime(ticket.last_attempt_at) }))
  if (ticket.next_retry_at && !ticket.in_progress) {
    const retryLabel = ticket.paused ? 'nextCheck' : ticket.ready ? 'nextRefresh' : 'nextRetry'
    result.push(t(`${key}.${retryLabel}`, { time: formatDateTime(ticket.next_retry_at) }))
  }
  return result
})
const lines = computed(() => [activity.value, failure.value, unknownPlan.value, ...details.value].filter(Boolean))
const tooltip = computed(() => [
  props.ticket.model, ...lines.value,
  ...(props.ticket.attempts !== undefined ? [t(`${key}.counterHint`)] : []),
].join('\n'))
const hasWarning = computed(() => !!errorLabel.value || props.ticket.paused || props.ticket.plan_known === false)
const summary = computed(() => {
  const status = [activity.value, errorLabel.value].filter(Boolean).join(' · ') || unknownPlan.value
  const attempts = props.ticket.attempts ? t(`${key}.attempts`, { count: props.ticket.attempts }) : ''
  const counters = props.ticket.successes !== undefined || props.ticket.failures !== undefined || props.ticket.inject_misses !== undefined
    ? t(`${key}.compactCounters`, { success: props.ticket.successes ?? 0, failure: props.ticket.failures ?? 0, missed: props.ticket.inject_misses ?? 0 })
    : attempts
  return [status, counters].filter(Boolean).join(' · ') || details.value[0] || ''
})
</script>
