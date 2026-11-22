// Orange 特有：Orange 功能开关（orange_settings 表）的服务层。
//
// 职责：
//   - 维护开关键白名单与默认值（缺行/读失败一律回落默认值，见默认开启的取舍说明）；
//   - 30s 进程内缓存 + singleflight，避免面板每个请求都读一次 orange_settings；
//   - 管理端保存后立即失效缓存，使当前节点的下一个请求即生效（多节点最迟 30s 生效）；
//   - 只读写 orange_settings 表；不读写上游 settings 表（上游 key 需同步改 9 处注册面）。
//
// 与上游的隔离方式：仓储实现由 repository 层通过 OrangeStore() 暴露，本文件用类型断言取用，
// 不扩展上游 SettingRepository 接口，因此不会牵连任何 stub 或上游文件。
package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/sync/singleflight"
)

// Orange 开关键名（白名单）。新增开关只需在 orangeSettingDefinitions 登记，接口与前端自动生效。
const (
	// OrangeSettingTokenRankingEnabled 用量排行（Token 排行榜）展示开关，默认开启。
	OrangeSettingTokenRankingEnabled = "token_ranking_enabled"
)

const (
	// orangeSettingCacheTTL 开关值的进程内缓存时长（读多写少，管理端保存后立即失效）。
	orangeSettingCacheTTL = 30 * time.Second
	// orangeSettingDBTimeout 单次读写 orange_settings 的超时。
	orangeSettingDBTimeout = 5 * time.Second
	// orangeSettingFailureTTL 读失败后的抑制窗口：期间直接回落默认值，不重复查库。
	orangeSettingFailureTTL = 5 * time.Second
	// orangeSettingSingleflightKey 同一实例内并发读的合并键。
	orangeSettingSingleflightKey = "orange_settings_snapshot"
)

// orangeSettingDefinition 描述一个 Orange 开关的元信息。
type orangeSettingDefinition struct {
	// Default 缺行/读失败时回落的默认值（布尔开关统一 "true"/"false"）。
	Default string
	// Boolean 是否为布尔开关（当前全部是；保留字段以便将来加入文本开关）。
	Boolean bool
}

// orangeSettingDefinitions Orange 开关白名单定义表（默认值取"与旧 Orange 行为一致"）。
var orangeSettingDefinitions = map[string]orangeSettingDefinition{
	OrangeSettingTokenRankingEnabled: {Default: "true", Boolean: true},
}

// orangeSettingKeyOrder 白名单键的稳定顺序（响应顺序稳定，便于对比与审计）。
var orangeSettingKeyOrder = []string{
	OrangeSettingTokenRankingEnabled,
}

var (
	// ErrOrangeSettingKeyUnknown 请求里出现白名单之外的键。
	ErrOrangeSettingKeyUnknown = infraerrors.BadRequest("ORANGE_SETTING_KEY_UNKNOWN", "unknown orange setting key")
	// ErrOrangeSettingValueInvalid 开关值不是布尔。
	ErrOrangeSettingValueInvalid = infraerrors.BadRequest("ORANGE_SETTING_VALUE_INVALID", "orange setting value must be a boolean")
	// ErrOrangeSettingEmptyUpdate 局部更新未提供任何键。
	ErrOrangeSettingEmptyUpdate = infraerrors.BadRequest("ORANGE_SETTING_EMPTY_UPDATE", "no orange setting provided")
	// ErrOrangeSettingInvalidPayload 请求体不是合法的键值 JSON 对象。
	ErrOrangeSettingInvalidPayload = infraerrors.BadRequest("ORANGE_SETTING_INVALID_PAYLOAD", "request body must be a JSON object of orange setting keys")
	// ErrOrangeSettingStoreUnavailable orange_settings 存储不可用（仓储未就绪）。
	ErrOrangeSettingStoreUnavailable = infraerrors.ServiceUnavailable("ORANGE_SETTING_STORE_UNAVAILABLE", "orange settings store is unavailable")
)

// OrangeSettingsStore orange_settings 表读写能力（由 repository 层 *settingRepository 实现）。
type OrangeSettingsStore interface {
	OrangeGetAll(ctx context.Context) (map[string]string, error)
	OrangeGet(ctx context.Context, key string) (value string, exists bool, err error)
	OrangeUpsert(ctx context.Context, key, value string, updatedBy int64) error
	OrangeUpdatedAt(ctx context.Context) (*time.Time, error)
}

// orangeSettingsStoreProvider 由仓储额外实现，服务层用它做类型断言取 store。
type orangeSettingsStoreProvider interface {
	OrangeStore() (OrangeSettingsStore, bool)
}

// OrangeSettingsService Orange 功能开关服务。
type OrangeSettingsService struct {
	store OrangeSettingsStore

	mu        sync.Mutex
	cache     map[string]string
	expiresAt time.Time
	// failedUntil 读失败后的短暂抑制窗口：DB 故障期间避免每个请求都打库、都刷警告日志。
	failedUntil time.Time
	sf          singleflight.Group
	now         func() time.Time
}

// NewOrangeSettingsService 基于既有 SettingService 构造 Orange 开关服务。
// 从 SettingService 的 settingRepo 上类型断言 OrangeStore()；断言失败时 store 为 nil，
// 所有读取回落默认值（fail-open），写入返回 ErrOrangeSettingStoreUnavailable。
func NewOrangeSettingsService(settingService *SettingService) *OrangeSettingsService {
	var store OrangeSettingsStore
	if settingService != nil && settingService.settingRepo != nil {
		if provider, ok := settingService.settingRepo.(orangeSettingsStoreProvider); ok && provider != nil {
			if s, ok := provider.OrangeStore(); ok && s != nil {
				store = s
			}
		}
	}
	return &OrangeSettingsService{store: store, now: time.Now}
}

// OrangeSettingKeys 返回白名单开关键（副本，调用方可安全修改）。
func OrangeSettingKeys() []string {
	keys := make([]string, len(orangeSettingKeyOrder))
	copy(keys, orangeSettingKeyOrder)
	return keys
}

// IsOrangeSettingKey 判断 key 是否在 Orange 开关白名单内。
func IsOrangeSettingKey(key string) bool {
	_, ok := orangeSettingDefinitions[key]
	return ok
}

// IsEnabled 返回开关当前值。
// 缺行 → 定义默认值；读失败 → 默认值 + 错误（调用方按 fail-open 继续用默认值）。
// 未知键 → ErrOrangeSettingKeyUnknown（属代码缺陷，不应出现在正常请求里）。
func (s *OrangeSettingsService) IsEnabled(ctx context.Context, key string) (bool, error) {
	def, ok := orangeSettingDefinitions[key]
	if !ok {
		return false, ErrOrangeSettingKeyUnknown.WithMetadata(map[string]string{"key": key})
	}
	if s == nil {
		return orangeParseSettingBool(def.Default), nil
	}
	values, err := s.values(ctx)
	if err != nil {
		// fail-open：开关只控制功能可见性，DB 抖动不应让页面或接口整体不可用（数据接口仍有鉴权兜底）。
		// 日志由 values() 在失败窗口起点打一次，这里不重复刷日志。
		return orangeParseSettingBool(def.Default), err
	}
	return orangeParseSettingBool(orangeSettingValueOrDefault(values, key, def.Default)), nil
}

// Snapshot 返回全部白名单开关的布尔快照（读失败时全为默认值，并返回错误供调用方记录日志）。
func (s *OrangeSettingsService) Snapshot(ctx context.Context) (map[string]bool, error) {
	var (
		values map[string]string
		err    error
	)
	if s != nil {
		values, err = s.values(ctx)
	} else {
		err = ErrOrangeSettingStoreUnavailable
	}

	snapshot := make(map[string]bool, len(orangeSettingKeyOrder))
	for _, key := range orangeSettingKeyOrder {
		def := orangeSettingDefinitions[key]
		snapshot[key] = orangeParseSettingBool(orangeSettingValueOrDefault(values, key, def.Default))
	}
	return snapshot, err
}

// LastUpdatedAt 返回开关表最近一次更新时间（管理端展示用，不参与缓存）。
func (s *OrangeSettingsService) LastUpdatedAt(ctx context.Context) (*time.Time, error) {
	if s == nil || s.store == nil {
		return nil, ErrOrangeSettingStoreUnavailable
	}
	dbCtx, cancel := context.WithTimeout(orangeSettingsContext(ctx), orangeSettingDBTimeout)
	defer cancel()
	return s.store.OrangeUpdatedAt(dbCtx)
}

// Update 局部更新白名单开关：未知键/空更新直接拒绝，写库成功后立即失效缓存，
// 并返回更新后的完整布尔快照（供管理端界面回填）。
// updatedBy 为当前管理员 users.id；拿不到时传 0，仓库写 NULL。
func (s *OrangeSettingsService) Update(ctx context.Context, patch map[string]bool, updatedBy int64) (map[string]bool, error) {
	if s == nil || s.store == nil {
		return nil, ErrOrangeSettingStoreUnavailable
	}
	if len(patch) == 0 {
		return nil, ErrOrangeSettingEmptyUpdate
	}

	keys := make([]string, 0, len(patch))
	for key := range patch {
		if !IsOrangeSettingKey(key) {
			return nil, ErrOrangeSettingKeyUnknown.WithMetadata(map[string]string{"key": key})
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	dbCtx, cancel := context.WithTimeout(orangeSettingsContext(ctx), orangeSettingDBTimeout)
	defer cancel()
	for _, key := range keys {
		if err := s.store.OrangeUpsert(dbCtx, key, strconv.FormatBool(patch[key]), updatedBy); err != nil {
			return nil, fmt.Errorf("update orange setting %q: %w", key, err)
		}
	}

	s.invalidateCache()
	return s.Snapshot(ctx)
}

// values 返回 orange_settings 的键值快照（进程内缓存 + singleflight）。
// 返回的 map 由缓存共享，调用方只读、不得修改。
func (s *OrangeSettingsService) values(ctx context.Context) (map[string]string, error) {
	if s == nil || s.store == nil {
		return nil, ErrOrangeSettingStoreUnavailable
	}
	if cached, ok := s.cachedValues(); ok {
		return cached, nil
	}
	if s.inFailureWindow() {
		return nil, ErrOrangeSettingStoreUnavailable
	}

	value, err, _ := s.sf.Do(orangeSettingSingleflightKey, func() (any, error) {
		// 二次检查：排队等待的 goroutine 不应重复查库。
		if cached, ok := s.cachedValues(); ok {
			return cached, nil
		}
		// 独立 context：断开请求取消链，避免客户端断连污染缓存写入。
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(orangeSettingsContext(ctx)), orangeSettingDBTimeout)
		defer cancel()

		values, err := s.store.OrangeGetAll(dbCtx)
		if err != nil {
			return nil, err
		}
		if values == nil {
			values = map[string]string{}
		}
		s.storeCache(values)
		return values, nil
	})
	if err != nil {
		s.markFailure()
		slog.Warn("orange settings: read orange_settings failed, serving defaults during suppression window",
			"error", err, "retry_after", orangeSettingFailureTTL.String())
		return nil, err
	}
	values, _ := value.(map[string]string)
	return values, nil
}

// cachedValues 读取未过期的进程内缓存。
func (s *OrangeSettingsService) cachedValues() (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil || s.expiresAt.IsZero() || s.nowTime().After(s.expiresAt) {
		return nil, false
	}
	return s.cache, true
}

// storeCache 写入进程内缓存（同时解除失败抑制窗口）。
func (s *OrangeSettingsService) storeCache(values map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = values
	s.expiresAt = s.nowTime().Add(orangeSettingCacheTTL)
	s.failedUntil = time.Time{}
}

// inFailureWindow 报告是否处于读失败后的抑制窗口内。
func (s *OrangeSettingsService) inFailureWindow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.failedUntil.IsZero() && s.nowTime().Before(s.failedUntil)
}

// markFailure 记录读失败，并在短窗口内抑制重复查库。
func (s *OrangeSettingsService) markFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedUntil = s.nowTime().Add(orangeSettingFailureTTL)
}

// invalidateCache 失效进程内缓存（管理端保存后调用）。
// 同时解除失败抑制窗口：写库成功即证明存储可用。
func (s *OrangeSettingsService) invalidateCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = nil
	s.expiresAt = time.Time{}
	s.failedUntil = time.Time{}
}

// nowTime 取当前时间（允许测试注入）。
func (s *OrangeSettingsService) nowTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// orangeSettingValueOrDefault 取键值；缺行或空串时回落默认值。
func orangeSettingValueOrDefault(values map[string]string, key, fallback string) string {
	if values != nil {
		if raw, ok := values[key]; ok && strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	return fallback
}

// orangeParseSettingBool 解析布尔开关文本；无法解析的值按 false 处理（等同关闭）。
func orangeParseSettingBool(raw string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return value
}

// orangeSettingsContext 归一化 context（nil 时用 Background，便于直接调用）。
func orangeSettingsContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
