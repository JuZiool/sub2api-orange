/**
 * Orange 特有：账号连接测试弹窗的自定义提示词装配。
 *
 * 上游 `components/admin/account/AccountTestModal.vue` 只保留接线（import / 一次调用 /
 * 一个组件挂载 / 三个单行替换），所有判定与文案都在这里维护：
 *
 * - `prompt`               —— 提示词状态（浏览器 localStorage 记忆，留空回落后端默认 `hi`）
 * - `supportsTextPrompt`   —— 只有「文本测试」才使用自定义提示词；与上游媒体提示词输入框
 *                             （`supportsPromptInput`）按构造互斥，因此两者都用 v-if 挂载
 * - `promptTooLong`        —— 超出 8192 字符时禁止开始测试（与后端校验一致）
 * - `sendingMessage`       —— 日志区「本次发送的提示词」文案
 * - `resolveRequestPrompt` —— 请求体 prompt：文本测试用自定义提示词，媒体测试沿用上游值
 *
 * 未配置自定义提示词（prompt 为空）时，发送的 prompt 与日志文案均与上游完全一致。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useOrangeAccountTestPrompt } from '@/composables/useOrangeAccountTestPrompt'

/** 判定文本测试所需的上游状态（只读访问，便于直接传 ref / computed）。 */
export interface OrangeAccountTestPromptEnv {
  /** OpenAI 测试模式：`default` / `compact`。 */
  testMode: { readonly value: string }
  /** 是否走生图/视频等媒体测试（上游 `supportsImageTest`）。 */
  imageTest: { readonly value: boolean }
  /** 是否 Grok 账号。 */
  isGrok: { readonly value: boolean }
  /** Grok 测试模式（`text` / `image` / `search` / ...）。 */
  grokMode: { readonly value: string }
}

/** 账号测试自定义提示词的可复用装配。必须在组件 setup 中调用一次。 */
export function useOrangeAccountTestPromptModal(env: OrangeAccountTestPromptEnv) {
  const { t } = useI18n()
  const { prompt, valid } = useOrangeAccountTestPrompt()

  // 仅在文本测试（非生图、非 OpenAI compact、Grok 仅 text 模式）时接收自定义提示词
  const supportsTextPrompt = computed(
    () =>
      env.testMode.value !== 'compact' &&
      !env.imageTest.value &&
      (!env.isGrok.value || env.grokMode.value === 'text')
  )

  // 自定义提示词超长时不允许开始测试（与后端 8192 字符校验一致）
  const promptTooLong = computed(() => supportsTextPrompt.value && !valid.value)

  // 日志区展示本次真正发送的提示词（自定义时带出内容，默认时沿用原文案）
  const sendingMessage = computed(() =>
    supportsTextPrompt.value && prompt.value.trim()
      ? t('admin.accounts.sendingCustomTestMessage', { prompt: prompt.value })
      : t('admin.accounts.sendingTestMessage')
  )

  // 请求体 prompt：文本测试原样传自定义提示词（不 trim，留空即后端默认）；媒体测试沿用上游取值
  const resolveRequestPrompt = (mediaPrompt: string): string =>
    supportsTextPrompt.value ? prompt.value : mediaPrompt

  return {
    prompt,
    supportsTextPrompt,
    promptTooLong,
    sendingMessage,
    resolveRequestPrompt
  }
}
