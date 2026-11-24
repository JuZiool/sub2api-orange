<!--
  Orange 特有：系统设置 →「功能开关」Tab 内的 Orange 自有开关卡片。

  与上游设置表单完全解耦的硬性约束（避免影响父级 `<form @submit.prevent="saveSettings">`）：
  - 不渲染 `<form>`；所有按钮一律 `type="button"`（绝不 `type="submit"`）；
  - 不复用父级 `form.*` 字段、不参与父级 payload；
  - 切换即保存（PUT /admin/orange/features），并在卡片内提示保存状态。
-->
<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('orangeSettings.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('orangeSettings.description') }}
      </p>
    </div>

    <div class="space-y-5 p-6">
      <div
        v-if="loading"
        class="flex items-center gap-3 text-sm text-gray-500 dark:text-gray-400"
      >
        <LoadingSpinner size="sm" />
        <span>{{ t('orangeSettings.loading') }}</span>
      </div>

      <div
        v-else-if="loadError"
        role="alert"
        class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/30 dark:text-red-300"
      >
        <span>{{ t('orangeSettings.loadFailed') }}</span>
        <button type="button" class="btn btn-secondary btn-sm" @click="load">
          {{ t('orangeSettings.retry') }}
        </button>
      </div>

      <template v-else>
        <div v-for="row in featureRows" :key="row.key" class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t(row.labelKey) }}</span>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ t(row.enabledHintKey) }}</p>
            <p
              v-if="feedbackKey === row.key"
              role="status"
              class="mt-1 text-xs"
              :class="feedbackToneClass"
            >
              {{ feedbackText }}
            </p>
          </div>
          <Toggle
            :model-value="values[row.key] ?? true"
            @update:model-value="(next: boolean) => setValue(row.key, next)"
          />
        </div>

        <p class="text-xs text-gray-400 dark:text-gray-500">{{ t('orangeSettings.instantHint') }}</p>
        <p v-if="updatedAtText" class="text-xs text-gray-400 dark:text-gray-500">
          {{ updatedAtText }}
        </p>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import {
  getAdminOrangeFeatures,
  updateAdminOrangeFeatures,
  type OrangeFeatureValues
} from '@/api/orange'
import { ORANGE_FEATURE_KEYS, useOrangeFeaturesStore } from '@/stores/orangeFeatures'

interface FeatureRow {
  key: string
  labelKey: string
  enabledHintKey: string
}

const { t } = useI18n()
// Orange 特有：单元测试等无 pinia 的环境下不能因为取 store 抛错而打断父组件（SettingsView）渲染。
const orangeFeatures = (() => {
  try {
    return useOrangeFeaturesStore()
  } catch {
    return null
  }
})()

/** 新增 Orange 开关时只需在这里加一行（键名必须与后端白名单一致）。 */
const featureRows: FeatureRow[] = [
  {
    key: ORANGE_FEATURE_KEYS.tokenRankingEnabled,
    labelKey: 'orangeSettings.tokenRanking.label',
    enabledHintKey: 'orangeSettings.tokenRanking.enabledHint'
  }
]

const values = ref<OrangeFeatureValues>({})
const loading = ref(true)
const loadError = ref(false)
const savingKey = ref<string | null>(null)
const savedKey = ref<string | null>(null)
const saveErrorKey = ref<string | null>(null)
const updatedAt = ref('')

const feedbackKey = computed(() => savingKey.value ?? saveErrorKey.value ?? savedKey.value)
const feedbackToneClass = computed(() => {
  if (savingKey.value) return 'text-gray-500 dark:text-gray-400'
  if (saveErrorKey.value) return 'text-red-600 dark:text-red-400'
  return 'text-emerald-600 dark:text-emerald-400'
})
const feedbackText = computed(() => {
  if (savingKey.value) return t('orangeSettings.saving')
  if (saveErrorKey.value) return t('orangeSettings.saveFailed')
  return t('orangeSettings.saved')
})
const updatedAtText = computed(() =>
  updatedAt.value ? t('orangeSettings.updatedAt', { time: updatedAt.value }) : ''
)

function formatUpdatedAt(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  try {
    return date.toLocaleString()
  } catch {
    return value
  }
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = false
  try {
    const response = await getAdminOrangeFeatures()
    values.value = response.items
    updatedAt.value = response.updated_at ? formatUpdatedAt(response.updated_at) : ''
  } catch (error) {
    console.warn('[OrangeFeatureToggles] Failed to load Orange feature flags:', error)
    loadError.value = true
  } finally {
    loading.value = false
  }
}

async function setValue(key: string, next: boolean): Promise<void> {
  if (savingKey.value) return
  const previous = values.value[key]
  values.value = { ...values.value, [key]: next }
  savingKey.value = key
  savedKey.value = null
  saveErrorKey.value = null
  try {
    const response = await updateAdminOrangeFeatures({ [key]: next })
    values.value = { ...values.value, ...response.items }
    if (response.updated_at) updatedAt.value = formatUpdatedAt(response.updated_at)
    // 同步公开开关缓存，让侧栏入口与路由守卫立即跟随（无需刷新页面）。
    orangeFeatures?.apply(response.items)
    savedKey.value = key
  } catch (error) {
    console.warn('[OrangeFeatureToggles] Failed to save Orange feature flag:', error)
    values.value = { ...values.value, [key]: typeof previous === 'boolean' ? previous : true }
    saveErrorKey.value = key
  } finally {
    savingKey.value = null
  }
}

onMounted(() => {
  void load()
})
</script>
