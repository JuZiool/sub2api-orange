import { ref, watch } from 'vue'
import { adminAPI } from '@/api/admin'

/** Refresh on each modal open; unknown/failed settings must not expose ticket controls. */
export function useCodexTicketGatewayGate(isVisible: () => boolean) {
  const gatewayEnabled = ref(false)
  watch(isVisible, async (visible, _previous, onCleanup) => {
    gatewayEnabled.value = false
    if (!visible) return
    let current = true
    onCleanup(() => { current = false })
    try {
      const settings = await adminAPI.settings.getSettings()
      if (current) gatewayEnabled.value = settings.openai_codex_ticket_enabled === true
    } catch {
      // Leave the controls hidden; existing account policy is preserved on save.
    }
  }, { immediate: true })
  return gatewayEnabled
}
