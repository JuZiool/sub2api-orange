<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-400">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.groups.modelRateMultipliers.title') }}</h4>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.modelRateMultipliers.description') }}</p>
      </div>
      <button type="button" class="btn btn-secondary shrink-0" @click="addRule">
        <Icon name="plus" size="sm" class="mr-1" />{{ t('admin.groups.modelRateMultipliers.add') }}
      </button>
    </div>
    <div v-for="(rule, index) in rules" :key="index" class="mt-3 grid grid-cols-[minmax(0,1fr)_8rem_auto] items-end gap-2">
      <div>
        <label class="input-label text-xs">{{ t('admin.groups.modelRateMultipliers.model') }}</label>
        <input v-model="rule.model" class="input" :placeholder="t('admin.groups.modelRateMultipliers.modelPlaceholder')" />
      </div>
      <div>
        <label class="input-label text-xs">{{ t('admin.groups.modelRateMultipliers.multiplier') }}</label>
        <input v-model.number="rule.multiplier" type="number" min="0.0001" step="0.0001" class="input" />
      </div>
      <button type="button" class="p-2 text-gray-400 hover:text-red-500" :title="t('admin.groups.modelRateMultipliers.remove')" @click="removeRule(index)">
        <Icon name="trash" size="sm" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModelRateMultiplierRule } from '@/types'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ modelValue: ModelRateMultiplierRule[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: ModelRateMultiplierRule[]] }>()

const { t } = useI18n()

const rules = computed(() => props.modelValue)

function addRule() {
  emit('update:modelValue', [...props.modelValue, { model: '', multiplier: 1 }])
}

function removeRule(index: number) {
  emit('update:modelValue', props.modelValue.filter((_, i) => i !== index))
}
</script>
