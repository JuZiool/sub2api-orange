import { describe, expect, it } from 'vitest'
import { formatUsageTps } from '@/utils/usageTps'

const row = { request_type: 'stream' as const, stream: true, output_tokens: 131, first_token_ms: 3380, duration_ms: 4890 }

describe('formatUsageTps', () => {
  it('uses raw milliseconds and excludes first-token wait', () => {
    expect(formatUsageTps(row)).toBe('86.8 t/s')
    expect(formatUsageTps({ ...row, first_token_ms: 3384, duration_ms: 4886 })).toBe('87.2 t/s')
  })
  it('accepts a zero first-token time', () => {
    expect(formatUsageTps({ ...row, first_token_ms: 0, duration_ms: 1000 })).toBe('131.0 t/s')
  })
  it.each([
    { request_type: 'ws_v2' as const, stream: false },
    { request_type: undefined, stream: true },
    { request_type: undefined, stream: false, openai_ws_mode: true },
    { request_type: 'unknown' as const, stream: true },
    { request_type: 'cyber' as const, stream: true },
  ])('supports WS and legacy streaming flags: %j', (flags) => {
    expect(formatUsageTps({ ...row, ...flags })).toBe('86.8 t/s')
  })
  it.each([
    { request_type: 'sync' as const }, { request_type: 'live' as const },
    { request_type: undefined, stream: false },
    { request_type: 'unknown' as const, stream: false },
    { request_type: 'cyber' as const, stream: false },
  ])('does not substitute full-request TPS for non-streaming records: %j', (flags) => {
    expect(formatUsageTps({ ...row, ...flags })).toBe('-')
  })
  it.each([
    { first_token_ms: null }, { duration_ms: null }, { duration_ms: 0 }, { duration_ms: -1 },
    { first_token_ms: -1 }, { duration_ms: 3380 }, { duration_ms: 3379 },
    { output_tokens: 0 }, { output_tokens: -1 }, { output_tokens: Number.NaN },
    { output_tokens: Number.POSITIVE_INFINITY }, { first_token_ms: Number.NaN },
    { first_token_ms: Number.POSITIVE_INFINITY }, { duration_ms: Number.NaN },
    { duration_ms: Number.POSITIVE_INFINITY }, { image_output_tokens: Number.NaN },
    { image_output_tokens: -1 }, { output_tokens: Number.MAX_VALUE, duration_ms: 3381 },
  ])('shows a placeholder for invalid data: %j', (values) => {
    expect(formatUsageTps({ ...row, ...values })).toBe('-')
  })
  it.each([
    { billing_mode: 'image' }, { billing_mode: 'video' }, { image_count: 1 },
    { image_count: 1, billing_mode: 'per_request' },
  ])('excludes image/video generation: %j', (values) => {
    expect(formatUsageTps({ ...row, ...values })).toBe('-')
  })
  it('uses text-only tokens for mixed token-billed output', () => {
    expect(formatUsageTps({ ...row, billing_mode: 'token', image_count: 1, image_output_tokens: 31 })).toBe('66.2 t/s')
    expect(formatUsageTps({ ...row, image_output_tokens: 131 })).toBe('-')
    expect(formatUsageTps({ ...row, image_output_tokens: 132 })).toBe('-')
  })
})
