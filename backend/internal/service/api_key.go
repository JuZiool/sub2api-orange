package service

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

// API Key status constants
const (
	StatusAPIKeyActive         = "active"
	StatusAPIKeyDisabled       = "disabled"
	StatusAPIKeyQuotaExhausted = "quota_exhausted"
	StatusAPIKeyExpired        = "expired"
)

// Rate limit window durations
const (
	RateLimitWindow5h = 5 * time.Hour
	RateLimitWindow1d = 24 * time.Hour
	RateLimitWindow7d = 7 * 24 * time.Hour
)

// IsWindowExpired returns true if the window starting at windowStart has exceeded the given duration.
// A nil windowStart is treated as expired — no initialized window means any accumulated usage is stale.
func IsWindowExpired(windowStart *time.Time, duration time.Duration) bool {
	return windowStart == nil || time.Since(*windowStart) >= duration
}

type APIKey struct {
	ID              int64
	UserID          int64
	Key             string
	Name            string
	GroupID         *int64
	FallbackGroupID *int64
	Status          string
	IPWhitelist     []string
	IPBlacklist     []string
	// 预编译的 IP 规则，用于认证热路径避免重复 ParseIP/ParseCIDR。
	CompiledIPWhitelist *ip.CompiledIPRules `json:"-"`
	CompiledIPBlacklist *ip.CompiledIPRules `json:"-"`
	LastUsedAt          *time.Time
	LastUsedIP          *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	User                *User
	Group               *Group
	FallbackGroup       *Group
	// Groups 是 Orange 聚合密钥的分组优先级链（含主分组，按 priority 升序）。
	// 为空表示普通密钥或旧式单兜底密钥；Group/FallbackGroup 仍按原有语义填充。
	Groups             []*APIKeyGroupEntry
	CurrentConcurrency int

	// Quota fields
	Quota     float64    // Quota limit in USD (0 = unlimited)
	QuotaUsed float64    // Used quota amount
	ExpiresAt *time.Time // Expiration time (nil = never expires)

	// Rate limit fields
	RateLimit5h   float64    // Rate limit in USD per 5h (0 = unlimited)
	RateLimit1d   float64    // Rate limit in USD per 1d (0 = unlimited)
	RateLimit7d   float64    // Rate limit in USD per 7d (0 = unlimited)
	Usage5h       float64    // Used amount in current 5h window
	Usage1d       float64    // Used amount in current 1d window
	Usage7d       float64    // Used amount in current 7d window
	Window5hStart *time.Time // Start of current 5h window
	Window1dStart *time.Time // Start of current 1d window
	Window7dStart *time.Time // Start of current 7d window
}

func (k *APIKey) IsActive() bool {
	return k.Status == StatusActive
}

// APIKeyGroupEntry 是聚合密钥分组链上的一项。
type APIKeyGroupEntry struct {
	Group    *Group
	Priority int
}

// IsAggregated 报告该 Key 是否启用了聚合分组链（链上至少 2 个分组）。
// 聚合密钥自带多级候选，不再叠加任何分组级兜底。
func (k *APIKey) IsAggregated() bool {
	return len(k.GroupChain()) > 1
}

// GroupChain 返回按 priority 升序的去重分组链；未配置聚合链时返回 nil。
// 主分组（api_keys.group_id）始终是链上的第一项。
func (k *APIKey) GroupChain() []*APIKeyGroupEntry {
	if k == nil || len(k.Groups) == 0 {
		return nil
	}
	chain := make([]*APIKeyGroupEntry, 0, len(k.Groups))
	for _, entry := range k.Groups {
		if entry == nil || entry.Group == nil || entry.Group.ID <= 0 {
			continue
		}
		chain = append(chain, entry)
	}
	if len(chain) == 0 {
		return nil
	}
	return chain
}

// FallbackGroupIDs 返回聚合链上除主分组外的分组 ID，按优先级升序。
func (k *APIKey) FallbackGroupIDs() []int64 {
	chain := k.GroupChain()
	if len(chain) < 2 {
		return nil
	}
	primaryID := chain[0].Group.ID
	if k != nil && k.GroupID != nil {
		primaryID = *k.GroupID
	}
	out := make([]int64, 0, len(chain)-1)
	for _, entry := range chain {
		if entry.Group.ID == primaryID {
			continue
		}
		out = append(out, entry.Group.ID)
	}
	return out
}

// ModelListingGroups 返回模型列表应当合并的分组（含主分组，按优先级升序）。
// 普通密钥只有主分组一项；聚合密钥返回整条链，使模型目录反映"这些组都能用"。
func (k *APIKey) ModelListingGroups() []*Group {
	chain := k.GroupChain()
	if len(chain) < 2 {
		if k == nil || k.Group == nil {
			return nil
		}
		return []*Group{k.Group}
	}
	out := make([]*Group, 0, len(chain))
	seen := make(map[int64]struct{}, len(chain))
	for _, entry := range chain {
		if entry.Group == nil {
			continue
		}
		if _, dup := seen[entry.Group.ID]; dup {
			continue
		}
		seen[entry.Group.ID] = struct{}{}
		out = append(out, entry.Group)
	}
	return out
}

// HasRateLimits returns true if any rate limit window is configured
func (k *APIKey) HasRateLimits() bool {
	return k.RateLimit5h > 0 || k.RateLimit1d > 0 || k.RateLimit7d > 0
}

// IsExpired checks if the API key has expired
func (k *APIKey) IsExpired() bool {
	if k.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*k.ExpiresAt)
}

// IsQuotaExhausted checks if the API key quota is exhausted
func (k *APIKey) IsQuotaExhausted() bool {
	if k.Quota <= 0 {
		return false // unlimited
	}
	return k.QuotaUsed >= k.Quota
}

// GetQuotaRemaining returns remaining quota (-1 for unlimited)
func (k *APIKey) GetQuotaRemaining() float64 {
	if k.Quota <= 0 {
		return -1 // unlimited
	}
	remaining := k.Quota - k.QuotaUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetDaysUntilExpiry returns days until expiry (-1 for never expires)
func (k *APIKey) GetDaysUntilExpiry() int {
	if k.ExpiresAt == nil {
		return -1 // never expires
	}
	duration := time.Until(*k.ExpiresAt)
	if duration < 0 {
		return 0
	}
	return int(duration.Hours() / 24)
}

// EffectiveUsage5h returns the 5h window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage5h() float64 {
	if IsWindowExpired(k.Window5hStart, RateLimitWindow5h) {
		return 0
	}
	return k.Usage5h
}

// EffectiveUsage1d returns the 1d window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage1d() float64 {
	if IsWindowExpired(k.Window1dStart, RateLimitWindow1d) {
		return 0
	}
	return k.Usage1d
}

// EffectiveUsage7d returns the 7d window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage7d() float64 {
	if IsWindowExpired(k.Window7dStart, RateLimitWindow7d) {
		return 0
	}
	return k.Usage7d
}

// APIKeyListFilters holds optional filtering parameters for listing API keys.
type APIKeyListFilters struct {
	Search  string
	Status  string
	GroupID *int64 // nil=不筛选, 0=无分组, >0=指定分组
}
