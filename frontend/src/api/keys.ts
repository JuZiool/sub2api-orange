/**
 * API Keys management endpoints
 * Handles CRUD operations for user API keys
 */

import { apiClient } from './client'
import type { ApiKey, ApiKeyGroupEntry, CreateApiKeyRequest, UpdateApiKeyRequest, PaginatedResponse } from '@/types'

/**
 * List all API keys for current user
 * @param page - Page number (default: 1)
 * @param pageSize - Items per page (default: 10)
 * @param filters - Optional filter parameters
 * @param options - Optional request options
 * @returns Paginated list of API keys
 */
export async function list(
  page: number = 1,
  pageSize: number = 10,
  filters?: {
    search?: string
    status?: string
    group_id?: number | string
    sort_by?: string
    sort_order?: 'asc' | 'desc'
  },
  options?: {
    signal?: AbortSignal
  }
): Promise<PaginatedResponse<ApiKey>> {
  const { data } = await apiClient.get<PaginatedResponse<ApiKey>>('/keys', {
    params: { page, page_size: pageSize, ...filters },
    signal: options?.signal
  })
  return data
}

/**
 * Get API key by ID
 * @param id - API key ID
 * @returns API key details
 */
export async function getById(id: number): Promise<ApiKey> {
  const { data } = await apiClient.get<ApiKey>(`/keys/${id}`)
  return data
}

/**
 * Create new API key
 * @param name - Key name
 * @param groupId - Optional group ID
 * @param customKey - Optional custom key value
 * @param ipWhitelist - Optional IP whitelist
 * @param ipBlacklist - Optional IP blacklist
 * @param quota - Optional quota limit in USD (0 = unlimited)
 * @param expiresInDays - Optional days until expiry (undefined = never expires)
 * @param rateLimitData - Optional rate limit fields
 * @returns Created API key
 */
export async function create(
  name: string,
  groupId?: number | null,
  customKey?: string,
  ipWhitelist?: string[],
  ipBlacklist?: string[],
  quota?: number,
  expiresInDays?: number,
  rateLimitData?: { rate_limit_5h?: number; rate_limit_1d?: number; rate_limit_7d?: number }
): Promise<ApiKey> {
  const payload: CreateApiKeyRequest = { name }
  if (groupId !== undefined) {
    payload.group_id = groupId
  }
  if (customKey) {
    payload.custom_key = customKey
  }
  if (ipWhitelist && ipWhitelist.length > 0) {
    payload.ip_whitelist = ipWhitelist
  }
  if (ipBlacklist && ipBlacklist.length > 0) {
    payload.ip_blacklist = ipBlacklist
  }
  if (quota !== undefined && quota > 0) {
    payload.quota = quota
  }
  if (expiresInDays !== undefined && expiresInDays > 0) {
    payload.expires_in_days = expiresInDays
  }
  if (rateLimitData?.rate_limit_5h && rateLimitData.rate_limit_5h > 0) {
    payload.rate_limit_5h = rateLimitData.rate_limit_5h
  }
  if (rateLimitData?.rate_limit_1d && rateLimitData.rate_limit_1d > 0) {
    payload.rate_limit_1d = rateLimitData.rate_limit_1d
  }
  if (rateLimitData?.rate_limit_7d && rateLimitData.rate_limit_7d > 0) {
    payload.rate_limit_7d = rateLimitData.rate_limit_7d
  }

  const { data } = await apiClient.post<ApiKey>('/keys', payload)
  return data
}

/**
 * Update API key
 * @param id - API key ID
 * @param updates - Fields to update
 * @returns Updated API key
 */
export async function update(id: number, updates: UpdateApiKeyRequest): Promise<ApiKey> {
  const { data } = await apiClient.put<ApiKey>(`/keys/${id}`, updates)
  return data
}

export interface BulkUpdateApiKeysResult {
  succeededIds: number[]
  failures: Array<{ id: number; error: unknown }>
}

/** Reuse per-key validation and permissions, with at most five requests in flight. */
export async function bulkUpdate(
  ids: number[],
  updates: UpdateApiKeyRequest
): Promise<BulkUpdateApiKeysResult> {
  const uniqueIds = [...new Set(ids)]
  const result: BulkUpdateApiKeysResult = { succeededIds: [], failures: [] }
  for (let offset = 0; offset < uniqueIds.length; offset += 5) {
    const batch = uniqueIds.slice(offset, offset + 5)
    const responses = await Promise.allSettled(batch.map((id) => update(id, updates)))
    responses.forEach((response, index) => {
      if (response.status === 'fulfilled') {
        result.succeededIds.push(batch[index])
      } else {
        result.failures.push({ id: batch[index], error: response.reason })
      }
    })
  }
  return result
}

/**
 * Delete API key
 * @param id - API key ID
 * @returns Success confirmation
 */
export async function deleteKey(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/keys/${id}`)
  return data
}

/**
 * Toggle API key status (active/inactive)
 * @param id - API key ID
 * @param status - New status
 * @returns Updated API key
 */
export async function toggleStatus(id: number, status: 'active' | 'inactive'): Promise<ApiKey> {
  return update(id, { status })
}

// ==================== Orange 特有：密钥分组链 ====================

/** 分组链模式：none = 普通 Key，fallback = 单兜底（链长 2），aggregate = 聚合（链长 ≥3）。 */
export type ApiKeyGroupChainMode = 'none' | 'fallback' | 'aggregate'

/** Orange 特有：GET/PUT /keys/:id/group-chain 的响应体。members[0] 恒为链首（主分组）。 */
export interface ApiKeyGroupChain {
  api_key_id: number
  mode: ApiKeyGroupChainMode
  primary_group_id: number
  members: ApiKeyGroupEntry[]
}

/**
 * Orange 特有：读取密钥的分组优先级链
 * @param id - API key ID
 */
export async function getKeyGroupChain(id: number): Promise<ApiKeyGroupChain> {
  const { data } = await apiClient.get<ApiKeyGroupChain>(`/keys/${id}/group-chain`)
  return data
}

/**
 * Orange 特有：保存密钥的分组优先级链（有序，index 0 = 新链首/主分组；[] = 清除链）
 * @param id - API key ID
 * @param groupIds - Ordered group ids
 */
export async function setKeyGroupChain(
  id: number,
  groupIds: number[],
  mode?: ApiKeyGroupChainMode
): Promise<ApiKeyGroupChain> {
  const { data } = await apiClient.put<ApiKeyGroupChain>(`/keys/${id}/group-chain`, {
    group_ids: groupIds,
    ...(mode ? { mode } : {})
  })
  return data
}

export const keysAPI = {
  list,
  getById,
  create,
  update,
  bulkUpdate,
  delete: deleteKey,
  toggleStatus,
  getKeyGroupChain,
  setKeyGroupChain
}

export default keysAPI
