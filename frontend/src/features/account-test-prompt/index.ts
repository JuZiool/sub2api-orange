/**
 * Orange 特有：账号测试自定义提示词（features 挂载层）。
 *
 * 复用已有实现，不复制逻辑：
 * - 输入组件 `components/admin/account/OrangeAccountTestPromptField.vue`（字符计数 / 恢复默认 / 超长提示）
 * - 本地记忆 `composables/useOrangeAccountTestPrompt.ts`（localStorage，留空回落后端默认 `hi`）
 *
 * 新增的只是「弹窗侧装配」`useAccountTestPromptModal`：文本测试判定、超长拦截、
 * 发送文案与请求体 prompt 取值。上游 `AccountTestModal.vue` 只从这里一次导入。
 */
export { default as OrangeAccountTestPromptField } from '@/components/admin/account/OrangeAccountTestPromptField.vue'
export { useOrangeAccountTestPromptModal } from './useAccountTestPromptModal'
export type { OrangeAccountTestPromptEnv } from './useAccountTestPromptModal'
