<template>
  <div v-if="eligible" class="pl-0.5 text-[10px] font-medium leading-tight">
    <button
      v-if="failed"
      type="button"
      class="text-red-500 hover:underline dark:text-red-400"
      :title="failureTitle"
      :aria-label="`${t('admin.accounts.subscriptionGraceFailed')}: ${failureTitle}`"
      @click.stop="load(true)"
    >{{ t('admin.accounts.subscriptionGraceFailed') }}</button>
    <span v-else-if="loading" class="text-gray-400">{{ t('admin.accounts.subscriptionGraceLoading') }}</span>
    <span
      v-else-if="graceEnd"
      :class="ended ? 'text-gray-400 dark:text-gray-500' : 'text-amber-600 dark:text-amber-400'"
      :title="graceTitle"
    >{{ t(ended ? 'admin.accounts.subscriptionGraceEnded' : 'admin.accounts.subscriptionGraceUntil', { time: formatDate(graceEnd, dateOptions) }) }}</span>
    <span v-else class="text-gray-400 dark:text-gray-500" :title="upstreamExpires">{{ t('admin.accounts.subscriptionGraceMissing') }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useNow } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { getSubscriptionGrace, type OpenAISubscriptionGrace } from '@/api/admin/subscriptionGrace'
import { formatDate } from '@/utils/format'
import { normalizePlanType } from '@/utils/planType'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ account: Account }>()
const { t } = useI18n()
const now = useNow({ interval: 60_000 })
const data = ref<OpenAISubscriptionGrace | null>(null)
const loading = ref(false)
const failed = ref(false)
const failureDetails = ref('')
const failureTitle = computed(() =>
  [failureDetails.value, t('admin.accounts.subscriptionGraceRetry')].filter(Boolean).join('\n')
)
const dateOptions: Intl.DateTimeFormatOptions = {
  year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false
}
function credential(key: string): string {
  const value = props.account.credentials?.[key]
  return typeof value === 'string' ? value : ''
}
const eligible = computed(() => {
  const account = props.account
  const expiry = Date.parse(credential('subscription_expires_at'))
  return account.platform === 'openai' && account.type === 'oauth' && !account.parent_account_id &&
    normalizePlanType(credential('plan_type')) === 'plus' &&
    Number.isFinite(expiry) && expiry <= now.value.getTime()
})
const credentialVersion = computed(() =>
  `${credential('subscription_expires_at')}:${credential('expires_at')}`
)
const queryKey = computed(() => eligible.value
  ? `${props.account.id}:${credentialVersion.value}:${Math.floor(now.value.getTime() / 300_000)}` : ''
)
const upstreamExpires = computed(() => {
  const expiry = data.value?.active_until || ''
  return Number.isFinite(Date.parse(expiry))
    ? t('admin.accounts.subscriptionGraceUpstreamExpires', { time: formatDate(expiry, dateOptions) })
    : ''
})
const graceEnd = computed(() => {
  const end = data.value?.grace_period_end_timestamp || ''
  return Number.isFinite(Date.parse(end)) ? end : ''
})
const ended = computed(() => Date.parse(graceEnd.value) <= now.value.getTime())
const graceTitle = computed(() => {
  const start = data.value?.became_delinquent_timestamp
  const range = start
    ? `${formatDate(start)} — ${formatDate(graceEnd.value)}`
    : formatDate(graceEnd.value)
  return [range, upstreamExpires.value].filter(Boolean).join('\n')
})

let requestID = 0
async function load(force = false) {
  const current = ++requestID
  data.value = null
  failed.value = false
  failureDetails.value = ''
  loading.value = false
  if (!eligible.value) return
  loading.value = true
  try {
    const result = await getSubscriptionGrace(props.account.id, credentialVersion.value, force)
    if (current === requestID) data.value = result
  } catch (error: unknown) {
    if (current === requestID) {
      failed.value = true
      failureDetails.value = extractApiErrorMessage(error, t('admin.accounts.subscriptionGraceFailed'))
    }
  } finally {
    if (current === requestID) loading.value = false
  }
}

watch(queryKey, () => load(), { immediate: true })
onBeforeUnmount(() => { requestID++ })
</script>
