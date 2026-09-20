const knownErrors = new Set([
  'auth', 'forbidden', 'model_unsupported', 'quota', 'rate_limited', 'proxy_auth',
  'network', 'timeout', 'upstream_5xx', 'invalid_response', 'length_mismatch',
  'invalid_ticket', 'no_proxy', 'proxy_cooldown', 'canceled',
])

/** Only fixed translations reach the UI; never render upstream error bodies. */
export function codexTicketErrorKey(code: string | undefined): string {
  return `admin.accounts.openai.codexTicketDiagnostics.errors.${code && knownErrors.has(code) ? code : 'unknown'}`
}
