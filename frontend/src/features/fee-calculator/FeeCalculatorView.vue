<!--
  Orange 特有：管理员费用计算器（纯前端模拟，不落库；上游倍率从 Orange 自有接口只读同步）。

  数据来源与记忆：
  - 分组列表来自上游既有的只读接口 `GET /api/v1/admin/groups/all`（admin-only）；
  - 「哪些分组参与计算」改为页内本地多选，记忆在 `localStorage['orange.rateCalc.groups.v1']`；
  - 「上游倍率」自动同步自 Orange 自有接口 `GET /api/v1/admin/orange/groups/profit`，
    即「分组管理 → 上游倍率」（未配置时后端返回 1.0）；接口失败时保留本地手工输入，只给轻量提示；
  - 手动改过的上游倍率记忆在 `localStorage['orange.rateCalc.costMultipliers.v1']`，
    与接口值不同时视为「手动覆盖」，仅本地模拟、不写回数据库，可点「恢复同步值」清除覆盖；
  - 「给用户的倍率」取分组的 `rate_multiplier`（只读）；
  - 手动添加的分组（非分组列表带出）上游倍率仍是纯手填，不参与同步；
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
        :class="syncStatus === 'error' || upstreamSyncFailed
          ? 'text-amber-700 dark:text-amber-400'
          : 'text-gray-500 dark:text-gray-400'"
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
            <!-- Orange 特有：分组勾选面板可折叠（默认收起，状态记忆在本浏览器）。 -->
            <button
              type="button"
              class="btn btn-secondary btn-sm inline-flex items-center gap-1"
              :aria-expanded="!groupsCollapsed"
              aria-controls="rate-calculator-groups"
              :title="groupsToggleLabel"
              @click="toggleGroupsPanel"
            >
              <Icon :name="groupsCollapsed ? 'chevronDown' : 'chevronUp'" size="sm" />
              <span>{{ groupsToggleLabel }}</span>
            </button>
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

        <div v-show="!groupsCollapsed" id="rate-calculator-groups">
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
        </div>
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
            <!-- Orange 特有：上游倍率手动覆盖时不再显示「自动同步」，改在倍率输入框旁提示 -->
            <span
              v-else-if="!groupHasManualOverride(group)"
              class="shrink-0 text-xs text-gray-500 dark:text-gray-400"
            >{{ t('rateCalculator.autoSynced') }}</span>
          </div>

          <div class="space-y-3">
            <div>
              <div class="mb-1 flex min-w-0 flex-wrap items-center justify-between gap-x-2 gap-y-1">
                <label :for="`rate-cost-${group.index}`" class="block text-xs font-medium text-gray-600 dark:text-gray-400">
                  {{ t('rateCalculator.upstreamRate') }}
                </label>
                <!-- Orange 特有：手动覆盖提示 + 一键恢复同步值（仅分组列表带出的卡片可能出现） -->
                <span
                  v-if="groupHasManualOverride(group)"
                  class="flex min-w-0 items-center gap-1.5 text-xs text-amber-700 dark:text-amber-400"
                >
                  <span :title="t('rateCalculator.manualOverrideHint')">
                    {{ t('rateCalculator.manualOverride') }}
                  </span>
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm px-2 py-0.5 text-xs"
                    :title="t('rateCalculator.manualOverrideHint')"
                    @click="restoreSyncedRate(group)"
                  >
                    {{ t('rateCalculator.restoreSynced') }}
                  </button>
                </span>
              </div>
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
import { getOrangeGroupProfits } from '@/api/orange'
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
/** Orange 特有：分组勾选面板是否收起（默认收起，本浏览器记忆）。 */
const GROUPS_COLLAPSED_KEY = 'orange.rateCalc.groupsCollapsed.v1'

const presets = [10, 20, 50, 100, 500, 1000]
const { t } = useI18n()
const auth = useAuthStore()
const pendingDeleteIndex = ref<number | null>(null)
const syncStatus = ref<'loading' | 'synced' | 'error' | 'manual'>(auth.isSimpleMode ? 'manual' : 'loading')
const syncBlocked = computed(() => syncStatus.value === 'loading' || syncStatus.value === 'error')

const availableGroups = ref<AdminGroup[]>([])
const selectedIds = ref<number[]>([])
/** Orange 特有：分组勾选面板折叠状态（默认收起，避免一屏铺满分组）。 */
const groupsCollapsed = ref(readGroupsCollapsed())
const groupsToggleLabel = computed(() =>
  groupsCollapsed.value ? t('rateCalculator.expandGroups') : t('rateCalculator.collapseGroups')
)
const costMultipliers = ref<Record<string, string>>({})
/** Orange 特有：接口返回的上游倍率（按分组 ID 缓存，仅内存；每次进入页面重新同步）。 */
const upstreamRates = ref<Record<string, number>>({})
/** Orange 特有：上游倍率同步失败标记（页内一次轻量提示，不弹窗、不阻塞页面）。 */
const upstreamSyncFailed = ref(false)

const syncMessage = computed(() => {
  if (syncStatus.value === 'loading') return t('rateCalculator.syncLoading')
  if (syncStatus.value === 'error') return t('rateCalculator.syncError')
  if (syncStatus.value === 'manual') return t('rateCalculator.simpleModeManual')
  const count = state.groups.filter(group => group.groupId !== undefined).length
  if (count <= 0) return t('rateCalculator.syncEmpty')
  // Orange 特有：上游倍率同步失败只降级成一条轻量提示，不阻塞分组卡片与手工输入
  return upstreamSyncFailed.value
    ? t('rateCalculator.syncUpstreamFailed', { count })
    : t('rateCalculator.syncSuccess', { count })
})
let disposed = false
let requestId = 0
/** Orange 特有：上游倍率请求序号，避免快速勾选时旧响应覆盖新状态。 */
let upstreamRequestId = 0
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

/** Orange 特有：读取分组勾选面板的折叠状态；没有记录时默认收起。 */
function readGroupsCollapsed(): boolean {
  try {
    const raw = localStorage.getItem(GROUPS_COLLAPSED_KEY)
    return raw === null ? true : raw === '1'
  } catch {
    return true
  }
}

function writeGroupsCollapsed(): void {
  try {
    localStorage.setItem(GROUPS_COLLAPSED_KEY, groupsCollapsed.value ? '1' : '0')
  } catch {
    // Storage may be disabled by the browser.
  }
}

function toggleGroupsPanel(): void {
  groupsCollapsed.value = !groupsCollapsed.value
  writeGroupsCollapsed()
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

// ==================== Orange 特有：上游倍率自动同步 ====================
// 「分组管理 → 上游倍率」已经落库（orange_group_upstream_rate 表），这里只读同步：
// 卡片的值优先取接口值，用户手改过的值（localStorage）与接口值不同时保留并标记「手动覆盖」。

/** Orange 特有：与 api 层口径一致地取浏览器时区，取不到时回退 UTC。 */
function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** Orange 特有：接口数值 → 输入框文本（最多 4 位小数，顺手抹掉浮点噪声）。 */
function formatUpstreamRate(value: number): string {
  if (!Number.isFinite(value)) return ''
  return String(Math.round(value * 10000) / 10000)
}

/**
 * Orange 特有：本地记忆是否构成「手动覆盖」。
 * 只有「已拿到接口值」且「本地值与接口值不同」才算覆盖；接口值未知（未同步/同步失败）
 * 时按本地手工值照旧处理，避免误报覆盖或给出无法恢复的按钮。
 */
function isManualOverride(groupId: number): boolean {
  const apiRate = upstreamRates.value[String(groupId)]
  if (typeof apiRate !== 'number') return false
  const local = (costMultipliers.value[String(groupId)] ?? '').trim()
  if (!local) return false
  const parsed = Number(local)
  return !Number.isFinite(parsed) || Math.abs(parsed - apiRate) > 1e-9
}

/** Orange 特有：卡片上游倍率取值优先级：手动覆盖 > 接口值 > 本地手工记忆 > 空。 */
function resolveUpstreamCost(groupId: number): string {
  if (isManualOverride(groupId)) return costMultipliers.value[String(groupId)] ?? ''
  const apiRate = upstreamRates.value[String(groupId)]
  if (typeof apiRate === 'number') return formatUpstreamRate(apiRate)
  return costMultipliers.value[String(groupId)] ?? ''
}

/** Orange 特有：模板判定——只有分组列表带出的卡片才可能出现手动覆盖。 */
function groupHasManualOverride(group: CalculatorGroup): boolean {
  return group.groupId !== undefined && isManualOverride(group.groupId)
}

/** Orange 特有：把已缓存的上游倍率回填到同步卡片（手动分组与手动覆盖的值都不动）。 */
function applySyncedUpstreamRates(): void {
  for (const group of state.groups) {
    if (group.groupId === undefined) continue
    group.cost = resolveUpstreamCost(group.groupId)
  }
}

/**
 * Orange 特有：按分组 ID 拉取上游倍率（GET /admin/orange/groups/profit）。
 * 失败时不抛错、不动已有输入，只置一个页内轻量提示，返回 false 由调用方决定是否回填。
 */
async function fetchUpstreamRates(ids: number[]): Promise<boolean> {
  const unique = [...new Set(ids)].filter(id => Number.isSafeInteger(id) && id > 0)
  if (!unique.length || auth.isSimpleMode || disposed) return true

  const currentRequest = ++upstreamRequestId
  try {
    const response = await getOrangeGroupProfits({ ids: unique, timezone: browserTimezone() })
    if (disposed || currentRequest !== upstreamRequestId) return false
    const next: Record<string, number> = { ...upstreamRates.value }
    let received = 0
    for (const item of response.items) {
      const groupId = Number(item.group_id)
      if (!Number.isSafeInteger(groupId) || !unique.includes(groupId)) continue
      const rate = Number(item.upstream_rate_multiplier)
      if (!Number.isFinite(rate)) continue
      next[String(groupId)] = rate
      received += 1
    }
    if (received > 0) upstreamRates.value = next
    upstreamSyncFailed.value = false
    return true
  } catch {
    // 接口未就绪 / 网络失败：保留手工输入与本地记忆，仅提示一次
    if (!disposed && currentRequest === upstreamRequestId) upstreamSyncFailed.value = true
    return false
  }
}

/** Orange 特有：同步当前参与计算的分组的上游倍率，成功后回填卡片。 */
async function syncUpstreamRates(): Promise<void> {
  if (auth.isSimpleMode) return
  const ids = selectedIds.value.filter(id => Number.isSafeInteger(id) && id > 0)
  if (!ids.length) return
  const ok = await fetchUpstreamRates(ids)
  if (!ok || disposed) return
  applySyncedUpstreamRates()
}

/** Orange 特有：清除某个分组的本地覆盖，恢复成接口值（接口值缺失时再同步一次）。 */
function restoreSyncedRate(group: CalculatorGroup): void {
  if (group.groupId === undefined) return
  const key = String(group.groupId)
  if (Object.prototype.hasOwnProperty.call(costMultipliers.value, key)) {
    const next = { ...costMultipliers.value }
    delete next[key]
    costMultipliers.value = next
    writeCostMultipliers()
  }
  const apiRate = upstreamRates.value[key]
  if (typeof apiRate === 'number') {
    group.cost = formatUpstreamRate(apiRate)
    return
  }
  group.cost = ''
  void syncUpstreamRates()
}

function reindexGroups() {
  pendingDeleteIndex.value = null
  state.groups.forEach((group, index) => { group.index = index })
}

/**
 * 用「已选分组 + 上游倍率（接口值 / 本地覆盖）」重建带出的倍率组，并保留手动分组。
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
      // Orange 特有：上游倍率优先接口值，手动覆盖保留本地值
      cost: resolveUpstreamCost(group.id),
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
  // Orange 特有：重新勾选分组后同步一次上游倍率（失败只提示，不阻塞）
  void syncUpstreamRates()
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
  // Orange 特有：全部重置时清掉本地手动覆盖，重新同步接口值（不是清空成空）
  costMultipliers.value = {}
  writeCostMultipliers()
  applySyncedUpstreamRates()
  void syncUpstreamRates()
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
    // Orange 特有：简易模式没有分组倍率接口，清空上游倍率缓存并停止同步
    upstreamRequestId += 1
    upstreamRates.value = {}
    upstreamSyncFailed.value = false
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
      // Orange 特有：先用本地记忆/已缓存的接口值渲染，避免卡片空白闪烁
      rebuildSyncedGroups()
      syncStatus.value = 'synced'
      // Orange 特有：分组就绪后同步「分组管理 → 上游倍率」；失败只给轻量提示，不影响汇总可用性
      await syncUpstreamRates()
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
  upstreamRequestId += 1
  window.removeEventListener('focus', refreshOnReturn)
  document.removeEventListener('visibilitychange', refreshOnReturn)
})
</script>
