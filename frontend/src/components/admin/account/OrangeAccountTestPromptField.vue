<!--
  Orange 特有：账号连接测试的自定义提示词输入框。
  复用 components/common/TextArea，额外提供字符计数、「恢复默认」按钮与超长提示；
  提示词本身由父组件通过 useOrangeAccountTestPrompt 记忆到 localStorage。
-->
<template>
  <div class="space-y-1.5" data-test="orange-test-prompt-field">
    <div class="flex items-center justify-between gap-3">
      <label :for="inputId" class="text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('admin.accounts.textTestPromptLabel') }}
      </label>
      <div class="flex items-center gap-2">
        <span
          :class="[
            'text-xs tabular-nums',
            tooLong ? 'font-semibold text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-gray-400'
          ]"
          data-test="orange-test-prompt-counter"
        >
          {{ charCount }} / {{ ORANGE_ACCOUNT_TEST_PROMPT_LIMIT }}
        </span>
        <button
          type="button"
          class="text-xs font-medium text-primary-600 transition-colors hover:text-primary-700 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-400 dark:hover:text-primary-300"
          :disabled="disabled || charCount === 0"
          data-test="orange-test-prompt-reset"
          @click="restoreDefault"
        >
          {{ t('admin.accounts.textTestPromptRestoreDefault') }}
        </button>
      </div>
    </div>

    <TextArea
      :id="inputId"
      :model-value="modelValue"
      :placeholder="t('admin.accounts.textTestPromptPlaceholder')"
      :hint="tooLong ? '' : t('admin.accounts.textTestPromptHint', { max: ORANGE_ACCOUNT_TEST_PROMPT_LIMIT })"
      :error="tooLong ? t('admin.accounts.textTestPromptTooLong', { max: ORANGE_ACCOUNT_TEST_PROMPT_LIMIT }) : ''"
      :disabled="disabled"
      :rows="rows"
      @update:model-value="onInput"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TextArea from '@/components/common/TextArea.vue'
import {
  ORANGE_ACCOUNT_TEST_PROMPT_LIMIT,
  countOrangeAccountTestPromptChars
} from '@/composables/useOrangeAccountTestPrompt'

interface Props {
  modelValue: string
  disabled?: boolean
  rows?: number | string
}

const props = withDefaults(defineProps<Props>(), {
  disabled: false,
  rows: 3
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

const { t } = useI18n()

const inputId = 'account-test-text-prompt'
const charCount = computed(() => countOrangeAccountTestPromptChars(props.modelValue))
const tooLong = computed(() => charCount.value > ORANGE_ACCOUNT_TEST_PROMPT_LIMIT)

const onInput = (value: string) => {
  emit('update:modelValue', value)
}

/** 恢复默认：清空输入 → 后端回落到默认提示词 hi，同时清掉本机记忆。 */
const restoreDefault = () => {
  emit('update:modelValue', '')
}
</script>
