<!--
  Orange 特有：密钥分组优先级链（单兜底 / 聚合分组）配置弹窗。
  - 链首 = 该密钥的主分组（api_keys.group_id），这里只读展示；改主分组仍在列表的分组列完成；
  - 候选项为链首之后的候选分组，有序（按 priority 升序），最多 8 项，可上移/下移/删除；
  - 本地校验：同平台、同订阅类型、分组可用、无重复；
  - 保存调用 PUT /api/v1/keys/:id/group-chain（body: { group_ids: [主分组, ...候选] }）。
-->
<template>
  <BaseDialog
    :show="show"
    :title="t('keys.groupChain.title')"
    width="wide"
    :close-on-escape="!submitting"
    :show-close-button="!submitting"
    @close="close"
  >
    <div class="space-y-5">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('keys.groupChain.description') }}</p>

      <!-- 链首：只读展示当前主分组 -->
      <section class="space-y-2">
        <div class="flex items-center gap-2">
          <span class="w-5 shrink-0 text-center text-xs font-semibold text-gray-500 dark:text-gray-400">①</span>
          <span class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('keys.groupChain.primaryLabel') }}
          </span>
        </div>
        <div
          class="rounded-xl border border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-500 dark:bg-dark-700"
        >
          <GroupBadge
            v-if="primaryGroup"
            :name="primaryGroup.name"
            :platform="primaryGroup.platform"
            :subscription-type="primaryGroup.subscription_type"
            :rate-multiplier="primaryGroup.rate_multiplier"
            :user-rate-multiplier="userGroupRates?.[primaryGroup.id] ?? null"
            :peak-rate-enabled="primaryGroup.peak_rate_enabled"
            :peak-start="primaryGroup.peak_start"
            :peak-end="primaryGroup.peak_end"
            :peak-rate-multiplier="primaryGroup.peak_rate_multiplier"
          />
          <span v-else-if="primaryGroupId === null" class="text-sm text-red-600 dark:text-red-400">
            {{ t('keys.groupChain.noPrimary') }}
          </span>
          <span v-else class="text-sm text-gray-500 dark:text-gray-400">
            #{{ primaryGroupId }} · {{ t('keys.groupChain.groupUnavailable') }}
          </span>
        </div>
        <!-- Orange 特有：链首（主分组）可就地更换，保存时一并写回 api_keys.group_id -->
        <Select
          v-model="selectedPrimaryId"
          :options="primaryOptions"
          :placeholder="t('keys.selectGroup')"
          :disabled="loading || submitting"
          searchable
          :aria-label="t('keys.groupChain.primaryLabel')"
          data-test="group-chain-primary"
        />
        <p class="input-hint">{{ t('keys.groupChain.primaryHint') }}</p>
      </section>

      <!-- 候选分组有序列表 -->
      <section class="space-y-2">
        <div class="flex items-center justify-between gap-3">
          <span class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('keys.groupChain.candidates') }}
          </span>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="!canAddCandidate || loading || submitting"
            data-test="add-group-chain-candidate"
            @click="addCandidate"
          >
            <Icon name="plus" size="sm" class="mr-1" />
            {{ t('keys.groupChain.addCandidate') }}
          </button>
        </div>

        <p v-if="loading" class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('common.loading') }}
        </p>
        <p
          v-else-if="!candidates.length"
          class="rounded-lg border border-dashed border-gray-300 px-3 py-2 text-sm text-gray-500 dark:border-dark-500 dark:text-gray-400"
        >
          {{ t('keys.groupChain.empty') }}
        </p>

        <div
          v-for="(candidate, index) in candidates"
          :key="candidate.key"
          class="flex items-center gap-2"
          :data-test="`group-chain-candidate-${index}`"
        >
          <span class="w-5 shrink-0 text-center text-xs font-semibold text-gray-500 dark:text-gray-400">
            {{ circledLabel(index + 1) }}
          </span>
          <div class="min-w-0 flex-1">
            <Select
              v-model="candidate.groupId"
              :options="optionsFor(index)"
              :placeholder="t('keys.selectGroup')"
              :disabled="submitting"
              searchable
              :aria-label="t('keys.groupChain.candidates')"
            />
          </div>
          <div class="flex shrink-0 items-center gap-1">
            <button
              type="button"
              class="btn btn-secondary btn-sm px-2"
              :disabled="index === 0 || submitting"
              :title="t('keys.groupChain.moveUp')"
              :aria-label="t('keys.groupChain.moveUp')"
              @click="moveCandidate(index, -1)"
            >
              <Icon name="arrowUp" size="sm" />
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm px-2"
              :disabled="index === candidates.length - 1 || submitting"
              :title="t('keys.groupChain.moveDown')"
              :aria-label="t('keys.groupChain.moveDown')"
              @click="moveCandidate(index, 1)"
            >
              <Icon name="arrowDown" size="sm" />
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm px-2"
              :disabled="submitting"
              :title="t('keys.groupChain.remove')"
              :aria-label="t('keys.groupChain.remove')"
              @click="removeCandidate(index)"
            >
              <Icon name="trash" size="sm" />
            </button>
          </div>
        </div>

        <p class="input-hint">{{ t('keys.groupChain.hint', { max: MAX_CANDIDATES }) }}</p>
      </section>

      <p v-if="loadError" role="alert" class="text-sm text-red-600 dark:text-red-400">
        {{ loadError }}
      </p>
      <p v-else-if="validationError" role="alert" class="text-sm text-red-600 dark:text-red-400">
        {{ validationError }}
      </p>
      <p v-if="saveError" role="alert" class="text-sm text-red-600 dark:text-red-400">
        {{ saveError }}
      </p>
    </div>

    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="submitting" @click="close">
        {{ t('common.cancel') }}
      </button>
      <button
        type="button"
        class="btn btn-primary"
        :disabled="!canSubmit"
        data-test="save-group-chain"
        @click="save"
      >
        {{ submitting ? t('keys.saving') : t('common.save') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { keysAPI } from '@/api'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import type { ApiKey, ApiKeyGroupEntry, Group } from '@/types'

interface Props {
  show: boolean
  apiKey: ApiKey | null
  groups: Group[]
  userGroupRates?: Record<number, number>
}

interface Candidate {
  /** v-for 的稳定 key（分组可换、序号可移，不能用 groupId 或索引） */
  key: number
  groupId: number | null
}

/** 候选分组上限（链首不计入）；与后端 `ORANGE_GROUP_CHAIN_MAX_MEMBERS` 口径一致。 */
const MAX_CANDIDATES = 8
const CIRCLED_LABELS = ['②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨']

const props = defineProps<Props>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const submitting = ref(false)
const loadError = ref('')
const saveError = ref('')
const candidates = ref<Candidate[]>([])
const chainMembers = ref<ApiKeyGroupEntry[]>([])
// Orange 特有：链首（主分组）允许就地更换；null 表示尚未确定
const selectedPrimaryId = ref<number | null>(null)
let candidateSeq = 0

const primaryGroupId = computed(() => selectedPrimaryId.value)

/** 分组详情查找表：可用分组 + 链上已加载的分组（后者用于旧数据的准确展示）。 */
const groupById = computed(() => {
  const map = new Map<number, Group>()
  for (const group of props.groups) map.set(group.id, group)
  for (const member of chainMembers.value) {
    if (member.group && !map.has(member.group_id)) map.set(member.group_id, member.group)
  }
  return map
})

const primaryGroup = computed<Group | null>(() => {
  if (props.apiKey?.group) return props.apiKey.group
  const primaryId = primaryGroupId.value
  if (primaryId === null) return null
  return groupById.value.get(primaryId) ?? null
})

const optionGroups = computed(() => [...groupById.value.values()])

/** 链首候选：全部可用分组（停用分组不可作为链首）。 */
const primaryOptions = computed(() =>
  optionGroups.value
    .filter((group) => group.status === 'active')
    .map((group) => ({ value: group.id, label: group.name }))
)

const canAddCandidate = computed(
  () => candidates.value.length < MAX_CANDIDATES && optionGroups.value.length > 0
)

const isGroupIncompatible = (group: Group): boolean => {
  const primary = primaryGroup.value
  if (!primary) return false
  return group.platform !== primary.platform || group.subscription_type !== primary.subscription_type
}

const groupLabel = (group: Group): string => {
  if (group.status !== 'active') {
    return `${group.name} · ${t('keys.groupChain.incompatibleInactiveTag')}`
  }
  const primary = primaryGroup.value
  if (primary && group.platform !== primary.platform) {
    return `${group.name} · ${t('keys.groupChain.incompatiblePlatformTag')}`
  }
  if (primary && group.subscription_type !== primary.subscription_type) {
    return `${group.name} · ${t('keys.groupChain.incompatibleSubscriptionTag')}`
  }
  return group.name
}

/** 每一行的候选项：已选（其它行）、链首、停用、跨平台/跨订阅类型的一律禁用。 */
const optionsFor = (index: number) => {
  const taken = new Set<number>()
  candidates.value.forEach((candidate, candidateIndex) => {
    if (candidateIndex === index) return
    if (candidate.groupId !== null) taken.add(candidate.groupId)
  })
  const primaryId = primaryGroupId.value
  if (primaryId !== null) taken.add(primaryId)

  return optionGroups.value.map((group) => ({
    value: group.id,
    label: groupLabel(group),
    disabled: taken.has(group.id) || group.status !== 'active' || isGroupIncompatible(group)
  }))
}

const validationError = computed(() => {
  if (primaryGroupId.value === null) return t('keys.groupChain.noPrimary')
  const primary = primaryGroup.value
  if (!primary) return t('keys.groupChain.noPrimary')
  if (primary.status !== 'active') {
    return t('keys.groupChain.invalidInactive', { name: primary.name })
  }
  if (candidates.value.length > MAX_CANDIDATES) {
    return t('keys.groupChain.tooMany', { max: MAX_CANDIDATES })
  }
  const chosen: number[] = []
  for (const candidate of candidates.value) {
    if (candidate.groupId === null) return t('keys.groupChain.incomplete')
    chosen.push(candidate.groupId)
  }
  if (new Set(chosen).size !== chosen.length) return t('keys.groupChain.duplicate')

  for (const groupId of chosen) {
    if (groupId === primaryGroupId.value) return t('keys.groupChain.duplicate')
    const group = groupById.value.get(groupId)
    if (!group) return t('keys.groupChain.groupUnavailable')
    if (group.status !== 'active') {
      return t('keys.groupChain.invalidInactive', { name: group.name })
    }
    if (group.platform !== primary.platform) {
      return t('keys.groupChain.invalidPlatform', { platform: primary.platform })
    }
    if (group.subscription_type !== primary.subscription_type) {
      return t('keys.groupChain.invalidSubscriptionType')
    }
  }
  return ''
})

const canSubmit = computed(
  () => !submitting.value && !loading.value && !loadError.value && !validationError.value
)

const circledLabel = (index: number) => CIRCLED_LABELS[index] ?? `(${index + 2})`

const resolveErrorMessage = (error: unknown): string => {
  if (error && typeof error === 'object') {
    const message = (error as { message?: unknown }).message
    if (typeof message === 'string' && message.trim()) return message
  }
  return ''
}

/** 服务端 members 或列表里的 groups 归一化为候选列表（跳过链首，去重，按 priority 升序）。 */
const applyMembers = (members: ApiKeyGroupEntry[]) => {
  const sorted = [...members].sort((a, b) => a.priority - b.priority)
  const primaryId = primaryGroupId.value
  const seen = new Set<number>()
  const tail: number[] = []
  for (const member of sorted) {
    if (primaryId !== null && member.group_id === primaryId) continue
    if (seen.has(member.group_id)) continue
    seen.add(member.group_id)
    tail.push(member.group_id)
  }
  candidates.value = tail
    .slice(0, MAX_CANDIDATES)
    .map((groupId) => ({ key: ++candidateSeq, groupId }))
}

const resetState = () => {
  candidates.value = []
  chainMembers.value = []
  selectedPrimaryId.value = props.apiKey?.group_id ?? null
  loadError.value = ''
  saveError.value = ''
}

const loadChain = async () => {
  const apiKey = props.apiKey
  if (!apiKey) return
  loading.value = true
  try {
    const chain = await keysAPI.getKeyGroupChain(apiKey.id)
    selectedPrimaryId.value = chain?.primary_group_id ?? apiKey.group_id ?? null
    chainMembers.value = chain?.members ?? []
    applyMembers(chainMembers.value)
  } catch (error) {
    // 端点不可用（灰度/后端未部署）时退化为列表响应里的 groups，避免弹窗不可用。
    chainMembers.value = apiKey.groups ?? []
    if (chainMembers.value.length > 1) {
      applyMembers(chainMembers.value)
    } else {
      loadError.value = resolveErrorMessage(error) || t('keys.groupChain.loadFailed')
    }
  } finally {
    loading.value = false
  }
}

const addCandidate = () => {
  if (!canAddCandidate.value) return
  candidates.value.push({ key: ++candidateSeq, groupId: null })
}

const moveCandidate = (index: number, offset: number) => {
  const target = index + offset
  if (target < 0 || target >= candidates.value.length) return
  const list = candidates.value
  const [moved] = list.splice(index, 1)
  list.splice(target, 0, moved)
}

const removeCandidate = (index: number) => {
  candidates.value.splice(index, 1)
}

const close = () => {
  if (submitting.value) return
  emit('close')
}

const save = async () => {
  const apiKey = props.apiKey
  const primaryId = primaryGroupId.value
  if (!apiKey || primaryId === null || !canSubmit.value) return

  submitting.value = true
  saveError.value = ''
  try {
    const groupIds = [primaryId, ...candidates.value.map((candidate) => candidate.groupId as number)]
    await keysAPI.setKeyGroupChain(apiKey.id, groupIds)
    appStore.showSuccess(t('keys.groupChain.saved'))
    emit('saved')
    emit('close')
  } catch (error) {
    const message = resolveErrorMessage(error) || t('keys.groupChain.saveFailed')
    saveError.value = message
    appStore.showError(message)
  } finally {
    submitting.value = false
  }
}

watch(
  () => props.show,
  (show) => {
    if (!show) return
    resetState()
    loadChain()
  },
  { immediate: true }
)

// Orange 特有：链首变更后，把与之重复的候选项从列表里移除，避免保存时校验冲突。
watch(selectedPrimaryId, (primaryId) => {
  if (primaryId === null) return
  const filtered = candidates.value.filter((candidate) => candidate.groupId !== primaryId)
  if (filtered.length !== candidates.value.length) {
    candidates.value = filtered
  }
})
</script>
