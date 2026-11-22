/**
 * Orange 特有：账号连接测试自定义提示词的浏览器本地记忆。
 *
 * - 只存在当前浏览器的 localStorage（不做跨设备同步、不落库、不依赖 pinia/auth store，
 *   便于单元测试直接使用）；
 * - 键名 `orange.account.testPrompt.v1`；留空表示使用后端默认提示词（`hi`）；
 * - 上限 8192 个 Unicode 字符（与后端 `OrangeValidateAccountTestPrompt` 一致，按 rune 计数）；
 * - localStorage 不可用（隐私模式/配额超限）时静默降级为空字符串，不影响测试流程。
 */
import { computed, ref, watch } from 'vue'

/** 与后端 `OrangeAccountTestPromptMaxCharacters` 保持一致。 */
export const ORANGE_ACCOUNT_TEST_PROMPT_LIMIT = 8192
/** localStorage 键（v1：结构变更时递增）。 */
export const ORANGE_ACCOUNT_TEST_PROMPT_STORAGE_KEY = 'orange.account.testPrompt.v1'

function getLocalStorage(): Storage | null {
  if (typeof window === 'undefined') return null
  try {
    return window.localStorage ?? null
  } catch {
    return null
  }
}

/** 读取本机记住的提示词；存储不可用时返回空字符串。 */
export function readOrangeAccountTestPrompt(): string {
  const storage = getLocalStorage()
  if (!storage) return ''
  try {
    return storage.getItem(ORANGE_ACCOUNT_TEST_PROMPT_STORAGE_KEY) ?? ''
  } catch {
    return ''
  }
}

/** 写入/清除本机提示词；写入失败不影响页面功能。 */
export function writeOrangeAccountTestPrompt(value: string): void {
  const storage = getLocalStorage()
  if (!storage) return
  try {
    if (value) {
      storage.setItem(ORANGE_ACCOUNT_TEST_PROMPT_STORAGE_KEY, value)
    } else {
      storage.removeItem(ORANGE_ACCOUNT_TEST_PROMPT_STORAGE_KEY)
    }
  } catch {
    // 忽略：提示词记忆是可选增强，任何存储错误都不应阻断测试。
  }
}

/** 按 Unicode 字符（rune）计数，与后端校验口径一致。 */
export function countOrangeAccountTestPromptChars(value: string): number {
  return Array.from(value ?? '').length
}

/**
 * 账号测试自定义提示词状态。
 * 返回 `prompt`（可 v-model 绑定，改动自动写回 localStorage）、`length`、`valid`（未超长）、
 * `isCustom`（是否非空，即本次是否使用自定义提示词）与 `reset()`（恢复默认，清空记忆）。
 */
export function useOrangeAccountTestPrompt() {
  const prompt = ref(readOrangeAccountTestPrompt())
  const length = computed(() => countOrangeAccountTestPromptChars(prompt.value))
  const valid = computed(() => length.value <= ORANGE_ACCOUNT_TEST_PROMPT_LIMIT)
  const isCustom = computed(() => prompt.value.trim().length > 0)

  watch(prompt, (value) => {
    writeOrangeAccountTestPrompt(value)
  })

  const reset = () => {
    prompt.value = ''
  }

  return {
    prompt,
    length,
    valid,
    isCustom,
    reset,
    limit: ORANGE_ACCOUNT_TEST_PROMPT_LIMIT
  }
}
