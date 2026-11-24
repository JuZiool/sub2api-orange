// Orange 特有：分组配置变更后的「分组链缓存清理」挂载点。
//
// 分组链数据（orange_api_key_group_chain）在进程内按 apiKeyID 缓存 45s（见
// orange_api_key_group_chain_loader.go），缓存项里带分组快照（is_exclusive / status /
// 平台 / 白名单等）。管理员把分组改成专属、停用或调整配置后，如果不清理缓存，最长一个
// TTL 内链成员仍按旧快照判定"是否可用/是否归属于用户"。
//
// 为了让上游分组变更流程只增加一行接线，这里把「可选接口断言」收敛成一个函数：
// 生产环境传进来的 invalidator 就是 *APIKeyService（wire.go:689）；测试替身或其它实现
// 未提供该能力时静默跳过，不影响上游既有行为。
package service

import "context"

// orangeGroupChainCacheInvalidator 是分组变更时需要清理分组链缓存的可选能力。
type orangeGroupChainCacheInvalidator interface {
	OrangeInvalidateGroupChainCacheByGroup(ctx context.Context, groupID int64)
}

// orangeInvalidateGroupChainCacheByGroup 在分组配置变更后清理分组链缓存（并顺带失效
// 该分组相关的认证快照缓存，见 APIKeyService.OrangeInvalidateGroupChainCacheByGroup）。
//
// 未启用分组链（或 invalidator 未实现该可选能力）时什么也不做。
func orangeInvalidateGroupChainCacheByGroup(ctx context.Context, invalidator APIKeyAuthCacheInvalidator, groupID int64) {
	if invalidator == nil || groupID <= 0 {
		return
	}
	chainInvalidator, ok := invalidator.(orangeGroupChainCacheInvalidator)
	if !ok {
		return
	}
	chainInvalidator.OrangeInvalidateGroupChainCacheByGroup(ctx, groupID)
}
