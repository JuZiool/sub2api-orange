<template>
  <AppLayout>
    <div class="space-y-5">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('rateCalculator.title') }}</h1>
        <button v-if="!auth.isSimpleMode" type="button" class="btn btn-secondary" :disabled="syncStatus === 'loading'" @click="syncGroups">
          {{ t('rateCalculator.refreshGroups') }}
        </button>
      </header>
      <p role="status" class="text-sm" :class="syncStatus === 'error' ? 'text-amber-700 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'">
        {{ syncMessage }}
      </p>

      <section class="card p-4 sm:p-5">
        <div class="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(260px,0.8fr)_minmax(0,1.2fr)]">
          <div class="space-y-3">
            <label for="rate-calculator-pay" class="block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('rateCalculator.payAmount') }}
            </label>
            <div class="flex items-center gap-2">
              <input
                id="rate-calculator-pay"
                v-model="state.pay"
                type="text"
                inputmode="decimal"
                autocomplete="off"
                :placeholder="t('rateCalculator.payPlaceholder')"
                :aria-invalid="calculation.payInvalid ? 'true' : undefined"
                class="input min-w-0 flex-1 tabular-nums"
              />
              <span class="text-sm text-gray-500 dark:text-gray-400">{{ t('rateCalculator.yuan') }}</span>
            </div>
            <div class="grid grid-cols-3 gap-1.5 sm:grid-cols-6 xl:grid-cols-3 2xl:grid-cols-6">
              <button
                v-for="amount in presets"
                :key="amount"
                type="button"
                class="btn btn-secondary justify-center px-2 tabular-nums"
                @click="state.pay = String(amount)"
              >
                {{ t('rateCalculator.preset', { amount }) }}
              </button>
            </div>
          </div>

          <div class="min-w-0 border-t border-gray-200 pt-4 dark:border-dark-700 xl:border-l xl:border-t-0 xl:pl-5 xl:pt-0">
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div class="min-w-0">
                <div class="mb-1 text-sm text-gray-500 dark:text-gray-400">{{ t('rateCalculator.safeUpstream') }}</div>
                <div class="break-words text-3xl font-semibold tabular-nums text-emerald-700 dark:text-emerald-400">
                  <template v-if="calculation.ready">{{ formatCents(calculation.suggestedCents!) }} <span class="text-base font-normal">{{ t('rateCalculator.yuan') }}</span></template>
                  <span v-else class="text-gray-400 dark:text-gray-500">—</span>
                </div>
              </div>
              <div class="min-w-0" :class="{ 'text-amber-700 dark:text-amber-400': isLoss }">
                <div class="mb-1 text-sm text-gray-500 dark:text-gray-400">{{ t(isLoss ? 'rateCalculator.expectedLoss' : 'rateCalculator.expectedProfit') }}</div>
                <div class="break-words text-3xl font-semibold tabular-nums">
                  <template v-if="calculation.ready">{{ formatCents(profitMagnitude!) }} <span class="text-base font-normal">{{ t('rateCalculator.yuan') }}</span></template>
                  <span v-else class="text-gray-400 dark:text-gray-500">—</span>
                </div>
              </div>
            </div>
            <p class="mt-3 text-sm leading-6 text-gray-500 dark:text-gray-400" aria-live="polite" aria-atomic="true">
              {{ summaryHint }}
            </p>
          </div>
        </div>

        <div class="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-gray-200 pt-3 dark:border-dark-700">
          <label class="inline-flex cursor-pointer items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <input v-model="state.remember" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
            {{ t(state.remember ? 'rateCalculator.remembered' : 'rateCalculator.remember') }}
          </label>
          <button type="button" class="btn btn-secondary" @click="resetAll">
            {{ t('rateCalculator.resetAll') }}
          </button>
        </div>
      </section>

      <section class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4" :aria-label="t('rateCalculator.rateGroups')">
        <article
          v-for="group in state.groups"
          :key="group.groupId === undefined ? `manual-${group.index}` : `synced-${group.groupId}`"
          class="min-w-0 rounded-lg border p-3"
          :class="groupIsWorst(group.index)
            ? 'border-emerald-500 dark:border-emerald-600'
            : 'border-gray-200 dark:border-dark-700'"
        >
          <div class="mb-3 flex min-w-0 items-center gap-2">
            <label :for="`rate-name-${group.index}`" class="sr-only">{{ t('rateCalculator.groupName', { number: group.index + 1 }) }}</label>
            <input
              :id="`rate-name-${group.index}`"
              v-model="group.name"
              type="text"
              maxlength="40"
              autocomplete="off"
              :readonly="group.groupId !== undefined"
              class="input min-w-0 flex-1 font-medium"
              :placeholder="t('rateCalculator.groupName', { number: group.index + 1 })"
              @input="cancelPendingDelete(group.index)"
            />
            <button
              v-if="group.groupId === undefined"
              type="button"
              class="btn shrink-0 px-2"
              :class="pendingDeleteIndex === group.index ? 'btn-danger' : 'btn-secondary'"
              @click="clearGroup(group.index)"
            >
              {{ t(pendingDeleteIndex === group.index ? 'rateCalculator.confirmDeleteGroup' : 'rateCalculator.clearGroup') }}
            </button>
            <span v-else class="shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ t('rateCalculator.autoSynced') }}</span>
          </div>

          <div class="space-y-3">
            <div>
              <label :for="`rate-cost-${group.index}`" class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
                {{ t('rateCalculator.upstreamRate') }}
              </label>
              <input
                :id="`rate-cost-${group.index}`"
                v-model="group.cost"
                type="text"
                inputmode="decimal"
                autocomplete="off"
                :readonly="group.groupId !== undefined"
                :placeholder="t('rateCalculator.upstreamPlaceholder')"
                :aria-invalid="calculation.groups[group.index].costInvalid ? 'true' : undefined"
                class="input w-full tabular-nums"
                @input="cancelPendingDelete(group.index)"
              />
            </div>
            <div>
              <label :for="`rate-sell-${group.index}`" class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
                {{ t('rateCalculator.userRate') }}
              </label>
              <input
                :id="`rate-sell-${group.index}`"
                v-model="group.sell"
                type="text"
                inputmode="decimal"
                autocomplete="off"
                :readonly="group.groupId !== undefined"
                :placeholder="t('rateCalculator.userPlaceholder')"
                :aria-invalid="calculation.groups[group.index].sellInvalid ? 'true' : undefined"
                class="input w-full tabular-nums"
                @input="cancelPendingDelete(group.index)"
              />
            </div>
          </div>

          <div class="mt-3 border-t border-gray-100 pt-3 dark:border-dark-700">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('rateCalculator.groupAmount') }}</div>
            <div class="mt-0.5 break-words text-xl font-semibold tabular-nums text-primary-600 dark:text-primary-400">
              <template v-if="(group.groupId === undefined || !syncBlocked) && calculation.groups[group.index].amountCents !== null">
                {{ formatCents(calculation.groups[group.index].amountCents!) }} {{ t('rateCalculator.yuan') }}
              </template>
              <span v-else class="text-gray-400 dark:text-gray-500">—</span>
            </div>
            <p
              class="mt-1 min-h-9 text-xs leading-5"
              :class="calculation.groups[group.index].status === 'incomplete' || calculation.groups[group.index].status === 'invalid'
                ? 'text-amber-700 dark:text-amber-400'
                : groupIsWorst(group.index)
                  ? 'text-emerald-700 dark:text-emerald-400'
                  : 'text-gray-500 dark:text-gray-400'"
            >
              {{ groupStatusText(group.index) }}
            </p>
          </div>
        </article>
        <button
          type="button"
          class="flex min-h-[260px] w-full flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-gray-300 p-4 text-sm font-medium text-gray-600 transition-colors hover:border-primary-500 hover:text-primary-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:border-dark-700 dark:text-gray-400 dark:hover:text-primary-400"
          @click="addGroup"
        >
          <Icon name="plus" size="md" />
          <span>{{ t('rateCalculator.addGroup') }}</span>
        </button>
      </section>

      <details class="text-sm text-gray-500 dark:text-gray-400">
        <summary class="cursor-pointer">{{ t('rateCalculator.calculationNotes') }}</summary>
        <p class="mt-2 leading-6">{{ t('rateCalculator.formula') }}</p>
        <p class="mt-1 leading-6">{{ t('rateCalculator.profitFormula') }}</p>
      </details>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getAll } from '@/api/admin/groups'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAuthStore } from '@/stores/auth'
import { calculateRates, formatCents, type RateInput } from './calculator'

interface CalculatorGroup extends RateInput {
  index: number
  groupId?: number
}

const STORAGE_KEY = 'rateCalc.shared.v2'
const presets = [10, 20, 50, 100, 500, 1000]
const { t } = useI18n()
const auth = useAuthStore()
const pendingDeleteIndex = ref<number | null>(null)
const syncStatus = ref<'loading' | 'synced' | 'error' | 'manual'>(auth.isSimpleMode ? 'manual' : 'loading')
const syncBlocked = computed(() => syncStatus.value === 'loading' || syncStatus.value === 'error')
const syncMessage = computed(() => {
  if (syncStatus.value === 'loading') return t('rateCalculator.syncLoading')
  if (syncStatus.value === 'error') return t('rateCalculator.syncError')
  if (syncStatus.value === 'manual') return t('rateCalculator.simpleModeManual')
  const count = state.groups.filter(group => group.groupId !== undefined).length
  return count > 0 ? t('rateCalculator.syncSuccess', { count }) : t('rateCalculator.syncEmpty')
})
let disposed = false
let requestId = 0
let pendingSync: Promise<void> | null = null
let keepBlankManualGroups = false

function createGroup(index: number): CalculatorGroup {
  return { index, name: '', cost: '', sell: '' }
}

function createGroups(count = 1) {
  return Array.from({ length: count }, (_, index) => createGroup(index))
}

const state = reactive({
  pay: '',
  remember: false,
  groups: createGroups(),
})

const calculation = computed(() => {
  const result = calculateRates(state.pay, state.groups)
  if (!syncBlocked.value) return result
  return { ...result, ready: false, suggestedCents: null, profitCents: null, highestGroupIndexes: [], highestGroupNames: [] }
})
const isLoss = computed(() => (calculation.value.profitCents ?? 0n) < 0n)
const profitMagnitude = computed(() => {
  const cents = calculation.value.profitCents
  return cents === null ? null : cents < 0n ? -cents : cents
})

const summaryHint = computed(() => {
  if (syncBlocked.value) return syncMessage.value
  const result = calculation.value
  if (result.invalidGroups.length) {
    return t('rateCalculator.invalidGroups', { groups: result.invalidGroups.join('、') })
  }
  if (result.payInvalid) return t('rateCalculator.invalidPay')
  if (!result.payEntered) return t('rateCalculator.enterPay')
  if (!result.ready) return t('rateCalculator.enterRates')
  return t('rateCalculator.coverage', {
    count: result.completeCount,
    groups: result.highestGroupNames.join('、'),
  })
})

function groupIsWorst(index: number): boolean {
  return calculation.value.ready && calculation.value.highestGroupIndexes.includes(index)
}

function groupStatusText(index: number): string {
  if (state.groups[index].groupId !== undefined && syncBlocked.value) return syncMessage.value
  if (groupIsWorst(index)) return t('rateCalculator.groupWorst')
  const group = calculation.value.groups[index]
  switch (group.status) {
    case 'blank': return t('rateCalculator.groupBlank')
    case 'incomplete': return t('rateCalculator.groupIncomplete')
    case 'invalid': return t('rateCalculator.groupInvalid')
    case 'awaitingPayment': return t('rateCalculator.groupAwaitingPay')
    case 'ready': return t('rateCalculator.groupReady')
  }
  return ''
}

function clearGroup(index: number) {
  if (state.groups[index].groupId !== undefined) return
  if (pendingDeleteIndex.value === index) {
    state.groups.splice(index, 1)
    state.groups.forEach((group, nextIndex) => {
      group.index = nextIndex
    })
    pendingDeleteIndex.value = null
    return
  }

  pendingDeleteIndex.value = null
  state.groups[index].cost = ''
  state.groups[index].sell = ''
  pendingDeleteIndex.value = index
}

function cancelPendingDelete(index: number) {
  if (pendingDeleteIndex.value === index) pendingDeleteIndex.value = null
}

function addGroup() {
  pendingDeleteIndex.value = null
  keepBlankManualGroups = true
  state.groups.push(createGroup(state.groups.length))
}

function clearSaved() {
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    // Storage may be disabled by the browser.
  }
}

function resetAll() {
  state.pay = ''
  state.remember = false
  keepBlankManualGroups = true
  state.groups = [
    ...state.groups.filter(group => group.groupId !== undefined),
    ...createGroups(),
  ]
  reindexGroups()
  clearSaved()
}

function restore() {
  try {
    const saved: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) || 'null')
    if (!saved || typeof saved !== 'object' || !('on' in saved) || saved.on !== true) {
      clearSaved()
      return
    }

    const data = saved as { pay?: unknown; rates?: unknown }
    state.remember = true
    if (typeof data.pay === 'string') state.pay = data.pay
    if (Array.isArray(data.rates)) {
      const savedRates = data.rates as unknown[]
      while (state.groups.length < savedRates.length) {
        state.groups.push(createGroup(state.groups.length))
      }
      state.groups.forEach((group, index) => {
        const rate = savedRates[index]
        if (!rate || typeof rate !== 'object') return
        const savedRate = rate as { name?: unknown; cost?: unknown; sell?: unknown; groupId?: unknown }
        if (typeof savedRate.groupId === 'number' && Number.isSafeInteger(savedRate.groupId) && savedRate.groupId > 0) {
          group.groupId = savedRate.groupId
        }
        if (typeof savedRate.name === 'string') group.name = savedRate.name
        if (typeof savedRate.cost === 'string') group.cost = savedRate.cost
        if (typeof savedRate.sell === 'string') group.sell = savedRate.sell
      })
      const manualGroups = state.groups.filter(group => group.groupId === undefined)
      if (manualGroups.length && manualGroups.every(group => !group.name.trim() && !group.cost.trim() && !group.sell.trim())) {
        state.groups = [
          ...state.groups.filter(group => group.groupId !== undefined),
          ...createGroups(),
        ]
        reindexGroups()
      }
    }
  } catch {
    clearSaved()
  }
}

watch(state, (value) => {
  if (!value.remember) {
    clearSaved()
    return
  }
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      on: true,
      pay: value.pay,
      rates: value.groups.map(({ name, cost, sell, groupId }) => ({ name, cost, sell, groupId })),
    }))
  } catch {
    // Storage may be disabled or full.
  }
}, { deep: true })

function reindexGroups() {
  pendingDeleteIndex.value = null
  state.groups.forEach((group, index) => { group.index = index })
}

async function syncGroups(): Promise<void> {
  if (disposed) return
  if (auth.isSimpleMode) {
    requestId += 1
    pendingSync = null
    state.groups = state.groups.filter(group => group.groupId === undefined)
    if (!state.groups.length) state.groups = createGroups()
    reindexGroups()
    syncStatus.value = 'manual'
    return
  }
  if (pendingSync) return pendingSync

  const currentRequest = ++requestId
  syncStatus.value = 'loading'
  pendingSync = (async () => {
    try {
      const response = await getAll()
      if (disposed || currentRequest !== requestId) return
      if (!Array.isArray(response)) throw new Error('Invalid group response')
      const groups = response.filter(group => group?.fee_calculator_sync_enabled === true)
      // Simple mode and incomplete responses must never silently become rate 1.
      const ids = new Set<number>()
      if (!groups.every(group => {
        if (!group || !Number.isSafeInteger(group.id) || group.id <= 0 || ids.has(group.id)
          || typeof group.name !== 'string'
          || typeof group.upstream_rate_multiplier !== 'number' || !Number.isFinite(group.upstream_rate_multiplier)
          || typeof group.rate_multiplier !== 'number' || !Number.isFinite(group.rate_multiplier)) return false
        ids.add(group.id)
        return true
      })) throw new Error('Incomplete group rates')

      const manualGroups = state.groups.filter(group => group.groupId === undefined
        && (keepBlankManualGroups || group.name.trim() || group.cost.trim() || group.sell.trim()))
      state.groups = [
        ...groups.map(group => ({
          index: 0,
          groupId: group.id,
          name: group.name,
          cost: String(group.upstream_rate_multiplier),
          sell: String(group.rate_multiplier),
        })),
        ...manualGroups,
      ]
      if (!state.groups.length) state.groups = createGroups()
      reindexGroups()
      keepBlankManualGroups = true
      syncStatus.value = 'synced'
    } catch {
      if (!disposed && currentRequest === requestId) syncStatus.value = 'error'
    } finally {
      if (currentRequest === requestId) pendingSync = null
    }
  })()
  return pendingSync
}

function refreshOnReturn() {
  if (document.visibilityState !== 'hidden') void syncGroups()
}

watch(() => auth.isSimpleMode, () => { void syncGroups() })

onMounted(() => {
  restore()
  void syncGroups()
  window.addEventListener('focus', refreshOnReturn)
  document.addEventListener('visibilitychange', refreshOnReturn)
})

onBeforeUnmount(() => {
  disposed = true
  requestId += 1
  window.removeEventListener('focus', refreshOnReturn)
  document.removeEventListener('visibilitychange', refreshOnReturn)
})
</script>
