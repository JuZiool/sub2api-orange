<!--
  Orange 特有：管理员费用计算器（纯前端，不落库、不调后端新接口）。

  数据来源与记忆：
  - 分组列表来自上游既有的只读接口 `GET /api/v1/admin/groups/all`（admin-only）；
  - 「哪些分组参与计算」改为页内本地多选，记忆在 `localStorage['orange.rateCalc.groups.v1']`；
  - 「上游倍率」在上游 `groups` 表没有该字段，改为可编辑并记忆在
    `localStorage['orange.rateCalc.costMultipliers.v1']`；「给用户的倍率」取分组的 `rate_multiplier`（只读）；
  - 简易模式（`auth.isSimpleMode`）下分组接口不返回倍率，直接降级为手动填写。
-->
<template>
  <AppLayout>
    <div class="space-y-5">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <div class="min-w-0">
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('rateCalculator.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('rateCalculator.description') }}</p>
        </div>
        <button
          v-if="!auth.isSimpleMode"
          type="button"
          class="btn btn-secondary"
          :disabled="syncStatus === 'loading'"
          @click="syncGroups"
        >
          {{ t('rateCalculator.refreshGroups') }}
        </button>
      </header>

      <p
        role="status"
        class="text-sm"
        :class="syncStatus === 'error' ? 'text-amber-700 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'"
      >
        {{ syncMessage }}
      </p>

      <section v-if="!auth.isSimpleMode" class="card p-4 sm:p-5">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('rateCalculator.groupsPanelTitle') }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('rateCalculator.groupsPanelDescription') }}
            </p>
          </div>
          <div class="flex flex-wrap items-center gap-3">
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('rateCalculator.groupsSelectedCount', { count: selectedIds.length }) }}
            </span>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="!selectedIds.length"
              @click="clearSelection"
            >
              {{ t('rateCalculator.clearSelection') }}
            </button>
          </div>
        </div>

        <div v-if="syncStatus === 'loading'" class="mt-4 flex items-center gap-3 text-sm text-gray-500 dark:text-gray-400">
          <LoadingSpinner size="sm" />
          <span>{{ t('rateCalculator.groupsPanelLoading') }}</span>
        </div>
        <p v-else-if="syncStatus === 'error'" class="mt-4 text-sm text-amber-700 dark:text-amber-400">
          {{ t('rateCalculator.groupsPanelError') }}
        </p>
        <p v-else-if="!availableGroups.length" class="mt-4 text-sm text-gray-500 dark:text-gray-400">
          {{ t('rateCalculator.groupsPanelEmpty') }}
        </p>
        <div v-else class="mt-4 grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-3">
          <label
            v-for="group in availableGroups"
            :key="`pick-${group.id}`"
            class="flex min-w-0 cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-colors"
            :class="isSelected(group.id)
              ? 'border-primary-500 bg-primary-50/60 dark:border-primary-500 dark:bg-primary-900/20'
              : 'border-gray-200 hover:border-gray-300 dark:border-dark-700 dark:hover:border-dark-600'"
          >
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              :checked="isSelected(group.id)"
              @change="toggleSelection(group.id, ($event.target as HTMLInputElement).checked)"
            />
            <span class="min-w-0 flex-1 truncate text-gray-800 dark:text-gray-200" :title="group.name">
              {{ group.name }}
            </span>
            <span class="shrink-0 text-xs tabular-nums text-gray-500 dark:text-gray-400">
              {{ t('rateCalculator.userRate') }} × {{ group.rate_multiplier }}
            </span>
          </label>
        </div>
        <p class="mt-3 text-xs text-gray-400 dark:text-gray-500">{{ t('rateCalculator.upstreamRateHint') }}</p>
      </section>

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
                  <template v-if="calculation.ready">
                    {{ formatCents(calculation.suggestedCents!) }}
                    <span class="text-base font-normal">{{ t('rateCalculator.yuan') }}</span>
                  </template>
                  <span v-else class="text-gray-400 dark:text-gray-500">—</span>
                </div>
              </div>
              <div class="min-w-0" :class="{ 'text-amber-700 dark:text-amber-400': isLoss }">
                <div class="mb-1 text-sm text-gray-500 dark:text-gray-400">
                  {{ t(isLoss ? 'rateCalculator.expectedLoss' : 'rateCalculator.expectedProfit') }}
                </div>
                <div class="break-words text-3xl font-semibold tabular-nums">
                  <template v-if="calculation.ready">
                    {{ formatCents(profitMagnitude!) }}
                    <span class="text-base font-normal">{{ t('rateCalculator.yuan') }}</span>
                  </template>
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
              @input="onGroupInput(group)"
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
                :placeholder="t('rateCalculator.upstreamPlaceholder')"
                :aria-invalid="calculation.groups[group.index].costInvalid ? 'true' : undefined"
                class="input w-full tabular-nums"
                @input="onGroupInput(group)"
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
                @input="onGroupInput(group)"
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
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { useAuthStore } from '@/stores/auth'
import type { AdminGroup } from '@/types'
import { calculateRates, formatCents, type RateInput } from './calculator'

interface CalculatorGroup extends RateInput {
  index: number
  groupId?: number
}

const STORAGE_KEY = 'rateCalc.shared.v2'
/** Orange 特有：参与计算的分组 ID 列表（本浏览器记忆，不落库）。 */
const SELECTED_GROUPS_KEY = 'orange.rateCalc.groups.v1'
/** Orange 特有：人工填写的上游倍率（按分组 ID 记忆，不落库）。 */
const COST_MULTIPLIERS_KEY = 'orange.rateCalc.costMultipliers.v1'

const presets = [10, 20, 50, 100, 500, 1000]
const { t } = useI18n()
const auth = useAuthStore()
const pendingDeleteIndex = ref<number | null>(null)
const syncStatus = ref<'loading' | 'synced' | 'error' | 'manual'>(auth.isSimpleMode ? 'manual' : 'loading')
const syncBlocked = computed(() => syncStatus.value === 'loading' || syncStatus.value === 'error')

const availableGroups = ref<AdminGroup[]>([])
const selectedIds = ref<number[]>([])
const costMultipliers = ref<Record<string, string>>({})

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

const fallbackLabel = (index: number) => t('rateCalculator.groupName', { number: index + 1 })
const joinList = (values: string[]) => values.join(t('rateCalculator.joinSeparator'))

const calculation = computed(() => {
  const result = calculateRates(state.pay, state.groups, fallbackLabel)
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
    return t('rateCalculator.invalidGroups', { groups: joinList(result.invalidGroups) })
  }
  if (result.payInvalid) return t('rateCalculator.invalidPay')
  if (!result.payEntered) return t('rateCalculator.enterPay')
  if (!result.ready) return t('rateCalculator.enterRates')
  return t('rateCalculator.coverage', {
    count: result.completeCount,
    groups: joinList(result.highestGroupNames),
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

function isSelected(groupId: number): boolean {
  return selectedIds.value.includes(groupId)
}

function readSelectedIds(): number[] {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(SELECTED_GROUPS_KEY) || 'null')
    if (!Array.isArray(raw)) return []
    return raw
      .filter((value): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value > 0)
      .filter((value, index, list) => list.indexOf(value) === index)
  } catch {
    return []
  }
}

function writeSelectedIds(): void {
  try {
    localStorage.setItem(SELECTED_GROUPS_KEY, JSON.stringify(selectedIds.value))
  } catch {
    // Storage may be disabled by the browser.
  }
}

function readCostMultipliers(): Record<string, string> {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(COST_MULTIPLIERS_KEY) || 'null')
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
    const out: Record<string, string> = {}
    for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
      if (typeof value === 'string') out[key] = value
    }
    return out
  } catch {
    return {}
  }
}

function writeCostMultipliers(): void {
  try {
    localStorage.setItem(COST_MULTIPLIERS_KEY, JSON.stringify(costMultipliers.value))
  } catch {
    // Storage may be disabled by the browser.
  }
}

function reindexGroups() {
  pendingDeleteIndex.value = null
  state.groups.forEach((group, index) => { group.index = index })
}

/**
 * 用「已选分组 + 人工上游倍率」重建带出的倍率组，并保留手动分组。
 * 分组数据不完整时抛错（由调用方转成 syncError），绝不静默按倍率 1 计算。
 */
function rebuildSyncedGroups(): void {
  const byId = new Map<number, AdminGroup>()
  for (const group of availableGroups.value) byId.set(group.id, group)

  const synced: CalculatorGroup[] = []
  for (const groupId of selectedIds.value) {
    const group = byId.get(groupId)
    if (!group) continue
    if (typeof group.name !== 'string' || typeof group.rate_multiplier !== 'number' || !Number.isFinite(group.rate_multiplier)) {
      throw new Error('Incomplete group rates')
    }
    synced.push({
      index: 0,
      groupId: group.id,
      name: group.name,
      cost: costMultipliers.value[String(group.id)] ?? '',
      sell: String(group.rate_multiplier),
    })
  }

  const manualGroups = state.groups.filter(group => group.groupId === undefined
    && (keepBlankManualGroups || group.name.trim() || group.cost.trim() || group.sell.trim()))
  state.groups = [...synced, ...manualGroups]
  if (!state.groups.length) state.groups = createGroups()
  reindexGroups()
  keepBlankManualGroups = true
}

function toggleSelection(groupId: number, checked: boolean): void {
  if (checked) {
    if (!selectedIds.value.includes(groupId)) selectedIds.value = [...selectedIds.value, groupId]
  } else {
    selectedIds.value = selectedIds.value.filter(id => id !== groupId)
  }
  writeSelectedIds()
  try {
    rebuildSyncedGroups()
    if (syncStatus.value !== 'loading') syncStatus.value = 'synced'
  } catch {
    syncStatus.value = 'error'
  }
}

function clearSelection(): void {
  selectedIds.value = []
  writeSelectedIds()
  try {
    rebuildSyncedGroups()
    if (syncStatus.value !== 'loading') syncStatus.value = 'synced'
  } catch {
    syncStatus.value = 'error'
  }
}

function onGroupInput(group: CalculatorGroup): void {
  cancelPendingDelete(group.index)
  if (group.groupId !== undefined) {
    costMultipliers.value = { ...costMultipliers.value, [String(group.groupId)]: group.cost }
    writeCostMultipliers()
  }
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
        const savedRate = rate as { name?: unknown; cost?: unknown; sell?: unknown }
        if (typeof savedRate.name === 'string') group.name = savedRate.name
        if (typeof savedRate.cost === 'string') group.cost = savedRate.cost
        if (typeof savedRate.sell === 'string') group.sell = savedRate.sell
      })
      const manualGroups = state.groups.filter(group => group.groupId === undefined)
      if (manualGroups.every(group => !group.name.trim() && !group.cost.trim() && !group.sell.trim())) {
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

// 只记忆「用户充值金额 + 手动分组」；带出的分组由勾选状态与上游倍率记忆决定。
watch(state, (value) => {
  if (!value.remember) {
    clearSaved()
    return
  }
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      on: true,
      pay: value.pay,
      rates: value.groups
        .filter(group => group.groupId === undefined)
        .map(({ name, cost, sell }) => ({ name, cost, sell })),
    }))
  } catch {
    // Storage may be disabled or full.
  }
}, { deep: true })

async function syncGroups(): Promise<void> {
  if (disposed) return
  if (auth.isSimpleMode) {
    requestId += 1
    pendingSync = null
    availableGroups.value = []
    state.groups = state.groups.filter(group => group.groupId === undefined)
    if (!state.groups.length) state.groups = createGroups()
    reindexGroups()
    syncStatus.value = 'manual'
    return
  }
  if (pendingSync) return pendingSync

  const currentRequest = ++requestId
  syncStatus.value = 'loading'
  const run = (async () => {
    try {
      const response = await getAll()
      if (disposed || currentRequest !== requestId) return
      if (!Array.isArray(response)) throw new Error('Invalid group response')
      // 只展示启用分组；简易模式返回体不含 rate_multiplier，交由下方完整性校验处理。
      availableGroups.value = response.filter(group => group
        && Number.isSafeInteger(group.id)
        && group.id > 0
        && group.status !== 'inactive')
      selectedIds.value = selectedIds.value.filter(id => availableGroups.value.some(group => group.id === id))
      writeSelectedIds()
      rebuildSyncedGroups()
      syncStatus.value = 'synced'
    } catch {
      if (!disposed && currentRequest === requestId) syncStatus.value = 'error'
    } finally {
      if (currentRequest === requestId) pendingSync = null
    }
  })()
  pendingSync = run
  return run
}

function refreshOnReturn() {
  if (document.visibilityState !== 'hidden') void syncGroups()
}

watch(() => auth.isSimpleMode, () => { void syncGroups() })

onMounted(() => {
  selectedIds.value = readSelectedIds()
  costMultipliers.value = readCostMultipliers()
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
