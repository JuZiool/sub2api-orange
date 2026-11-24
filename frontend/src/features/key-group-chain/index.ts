/**
 * Orange 特有：密钥分组链（密钥级兜底分组 / 聚合分组）功能入口。
 *
 * KeysView.vue 只从这里取：
 * - `keysAPI`：上游 keysAPI 的包装（create / update 成功后补写分组链）；
 * - `KeyGroupChainRowCell` / `KeyGroupChainFormFields`：列表分组列与弹窗表单区的挂载组件；
 * - `prepareKeyGroupChainSubmit` / `handleKeyGroupChainGroupChange`：提交前校验与列表改主分组钩子。
 *
 * 未启用分组链配置时，以上接线对上游行为零影响。
 */
export { keysAPI } from './api'
export { default as KeyGroupChainRowCell } from './KeyGroupChainRowCell.vue'
export { default as KeyGroupChainFormFields } from './KeyGroupChainFormFields.vue'
export {
  prepareKeyGroupChainSubmit,
  handleKeyGroupChainGroupChange
} from './useKeyGroupChain'
