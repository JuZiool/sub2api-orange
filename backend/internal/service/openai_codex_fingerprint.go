package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// codexFingerprintIDsContextKey 是暂存在 gin context 的收敛 ID 集合键。
// 由 Forward（非透传）或 forwardOpenAIPassthrough（透传）解析后写入，请求
// 构造器读取用于出站头改写——请求体与出站头必须共享同一份 IDs，保证
// turn_id 等随机字段一致。
const codexFingerprintIDsContextKey = "codex_fingerprint_ids"

// stageCodexFingerprintIDs 将本 attempt 解析出的收敛 ID 暂存到 gin context。
// 必须无条件覆写（含 nil）：failover 从收敛账号切到 off 账号时，上一账号的
// IDs 不得残留并被误应用到新账号的出站头（typed-nil 由应用侧 nil 守卫吸收）。
func stageCodexFingerprintIDs(c *gin.Context, ids *codexFingerprintIDs) {
	if c == nil {
		return
	}
	if prev, ok := c.Get(codexFingerprintIDsContextKey); ok {
		if prevIDs, ok := prev.(*codexFingerprintIDs); ok {
			releaseCodexFingerprintLease(prevIDs)
		}
	}
	holdCodexFingerprintLease(ids)
	c.Set(codexFingerprintIDsContextKey, ids)
}

func holdCodexFingerprintLease(ids *codexFingerprintIDs) {
	if ids == nil || ids.mode != codexFingerprintSingleMachineMultiWindow || ids.accountID <= 0 || ids.lease != nil {
		return
	}
	ids.lease = singleMachineInFlight.acquire(ids.accountID, ids.rootIndex)
}

func releaseCodexFingerprintLease(ids *codexFingerprintIDs) {
	if ids == nil {
		return
	}
	ids.lease.release()
	ids.lease = nil
}

func releaseStagedCodexFingerprintLease(c *gin.Context) {
	if c == nil {
		return
	}
	if prev, ok := c.Get(codexFingerprintIDsContextKey); ok {
		if ids, ok := prev.(*codexFingerprintIDs); ok {
			releaseCodexFingerprintLease(ids)
		}
	}
}

func stagedCodexFingerprintIDs(c *gin.Context, account *Account) *codexFingerprintIDs {
	if c == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return nil
	}
	value, ok := c.Get(codexFingerprintIDsContextKey)
	if !ok {
		return nil
	}
	ids, ok := value.(*codexFingerprintIDs)
	if !ok || ids == nil || ids.accountID != account.ID {
		return nil
	}
	return ids
}

// applyStagedCodexFingerprintHeaders 读取 context 暂存的收敛 ID 并改写出站头。
// 非透传与透传两个请求构造器共用本函数，防止应用语义漂移。仅解析该
// snapshot 的 OAuth 账号可读取，避免 stale context 跨账号 failover 泄漏。
func applyStagedCodexFingerprintHeaders(c *gin.Context, account *Account, h http.Header) {
	applyCodexFingerprintHeaders(h, stagedCodexFingerprintIDs(c, account))
}

func applyStagedCodexFingerprintClientMetadata(c *gin.Context, account *Account, reqBody map[string]any) bool {
	return applyCodexFingerprintClientMetadata(reqBody, stagedCodexFingerprintIDs(c, account))
}

// ensureStagedCodexFingerprintIDs 为没有经过 HTTP JSON 转发入口的 WS 请求
// 初始化账号级指纹快照。快照放在 gin context 中，保证同一轮请求的请求体和
// 握手头使用同一份 IDs；已有快照则复用，避免 session/full 模式生成两套随机 ID。
func ensureStagedCodexFingerprintIDs(c *gin.Context, account *Account, enabled bool) *codexFingerprintIDs {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	if ids := stagedCodexFingerprintIDs(c, account); ids != nil {
		return ids
	}
	var clientHeaders http.Header
	if c != nil && c.Request != nil {
		clientHeaders = c.Request.Header
	}
	ids := resolveCodexFingerprintIDsFromRequest(account, clientHeaders, enabled)
	// 即使结果为 nil 也要覆写 context，防止 failover 后沿用上一账号快照。
	stageCodexFingerprintIDs(c, ids)
	return ids
}

// codexFingerprintMode 控制 OAuth 账号出站请求的设备指纹收敛强度。
// 多人共享同一 OAuth 账号时，每个用户的 Codex 客户端会携带各自不同的
// installation_id / session_id / thread_id，上游据此判定设备数和会话数。
// 收敛模式将这些标识改写为账号级恒定值，减少上游可见的设备/会话指纹。
type codexFingerprintMode string

const (
	// codexFingerprintOff 不做任何收敛，原样透传客户端标识。
	// 账号 extra 未显式配置模式时，GetCodexFingerprintMode 返回此值；
	// 出站请求是否按全局开关提升到 device 模式由 resolveCodexFingerprintMode 决定。
	codexFingerprintOff codexFingerprintMode = "off"
	// codexFingerprintAccountDevice 是页面显示的“CPA 指纹出口”兼容模式：
	// 使用账号 ID（已有系统种子优先）派生唯一且稳定的 installation_id。
	// 为兼容已有 account_device 配置，它只收敛设备信号，不强制收敛会话/线程。
	codexFingerprintAccountDevice codexFingerprintMode = "account_device"
	// codexFingerprintDevice 仅收敛 installation_id 为账号级恒定值。
	// 上游看到 1 台设备 + 多会话（每用户各自的 session）。
	codexFingerprintDevice codexFingerprintMode = "device"
	// codexFingerprintSession 收敛 installation_id + session_id，
	// thread_id 按客户端原始 session-id 确定性派生（每个真实 Codex 会话一个独立线程）。
	// 上游看到 1 台设备 + 1 会话 + N 线程，最接近正常用户 spawn 子代理的模式。
	codexFingerprintSession codexFingerprintMode = "session"
	// codexFingerprintFull 收敛所有标识：installation_id + session_id + thread_id。
	// 上游看到 1 台设备 + 1 会话 + 1 线程，最激进。
	codexFingerprintFull codexFingerprintMode = "full"
	// codexFingerprintSingleMachineMultiWindow keeps one stable device per
	// account, maps downstream sessions onto a small pool of Codex windows,
	// and renders overflow occupancy as spawned sub-agents of those windows.
	codexFingerprintSingleMachineMultiWindow codexFingerprintMode = "single_machine_multi_window"
)

const (
	codexFingerprintModeExtraKey = "codex_fingerprint_mode"
	codexFingerprintSeedExtraKey = "codex_fingerprint_seed"
)

func canonicalCodexFingerprintSeed(value any) (string, bool) {
	raw, ok := value.(string)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(raw)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || trimmed != parsed.String() {
		return "", false
	}
	return trimmed, true
}

func newCodexFingerprintSeed() string {
	return uuid.NewString()
}

func stripCodexFingerprintSeed(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	stripped := maps.Clone(extra)
	delete(stripped, codexFingerprintSeedExtraKey)
	return stripped
}

func codexFingerprintModeFromExtra(extra map[string]any) codexFingerprintMode {
	if extra == nil {
		return codexFingerprintOff
	}
	raw, _ := extra[codexFingerprintModeExtraKey].(string)
	switch codexFingerprintMode(strings.TrimSpace(raw)) {
	case codexFingerprintOff, codexFingerprintAccountDevice, codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull, codexFingerprintSingleMachineMultiWindow:
		return codexFingerprintMode(strings.TrimSpace(raw))
	default:
		return codexFingerprintOff
	}
}

func codexFingerprintModeRequiresSeed(mode codexFingerprintMode) bool {
	switch mode {
	case codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull, codexFingerprintSingleMachineMultiWindow:
		return true
	default:
		return false
	}
}

func codexFingerprintSeed(extra map[string]any) (string, bool) {
	if extra == nil {
		return "", false
	}
	return canonicalCodexFingerprintSeed(extra[codexFingerprintSeedExtraKey])
}

func prepareCodexFingerprintExtraForCreate(platform, accountType string, extra map[string]any) map[string]any {
	prepared := stripCodexFingerprintSeed(extra)
	if platform != PlatformOpenAI || (accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken) || !codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(prepared)) {
		return prepared
	}
	if prepared == nil {
		prepared = make(map[string]any, 1)
	}
	prepared[codexFingerprintSeedExtraKey] = newCodexFingerprintSeed()
	return prepared
}

func prepareCodexFingerprintExtraForUpdate(account *Account, extra map[string]any) map[string]any {
	prepared := stripCodexFingerprintSeed(extra)
	if account == nil || !account.IsOpenAIOAuthLike() {
		return prepared
	}
	if seed, ok := codexFingerprintSeed(account.Extra); ok {
		if prepared == nil {
			prepared = make(map[string]any, 1)
		}
		prepared[codexFingerprintSeedExtraKey] = seed
		return prepared
	}
	if codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(prepared)) {
		if prepared == nil {
			prepared = make(map[string]any, 1)
		}
		prepared[codexFingerprintSeedExtraKey] = newCodexFingerprintSeed()
	}
	return prepared
}

func sanitizedCodexFingerprintExtraUpdates(updates map[string]any) map[string]any {
	if updates == nil {
		return nil
	}
	sanitized := maps.Clone(updates)
	delete(sanitized, codexFingerprintSeedExtraKey)
	return sanitized
}

// ShouldEnsureCodexFingerprintSeedForExtraUpdates reports whether a JSONB key-level
// extra update is enabling Codex fingerprint convergence and therefore must atomically
// preserve or create the system-managed per-account seed in the repository update.
func ShouldEnsureCodexFingerprintSeedForExtraUpdates(updates map[string]any) bool {
	if updates == nil {
		return false
	}
	return codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(updates))
}

// GetCodexFingerprintMode 从账号 extra JSON 读取指纹收敛模式。
//
// **收敛是显式 opt-in**：未设置、空值或非法值一律按 off 处理，只有管理员
// 明确配置 device / session / full 才收敛。
//
// 历史：v0.1.175（#5553）把缺省值当作 session，导致升级后存量 OAuth 账号
// （普遍没有这个 extra 键）的每个非透传请求都被静默改写 installation /
// session / thread / turn / window 五类标识；#5555、#5556、#5582 报告的额度
// 缩水都卡在该版本边界，并有"回退 v0.1.173 即恢复"与"新账号开收敛后降额"
// 的 A/B 实测。上游的配额判定策略不可观测，因此这里取兼容安全的一侧：
// 不显式 opt-in 就保持 v0.1.175 之前的客户端身份（#5610）。
func (a *Account) GetCodexFingerprintMode() codexFingerprintMode {
	if a == nil || !a.IsOpenAIOAuthLike() {
		return codexFingerprintOff
	}
	return codexFingerprintModeFromExtra(a.Extra)
}

// resolveCodexFingerprintMode resolves the effective account mode. An explicit
// per-account value always wins; when the global switch is enabled and the
// account has no mode key, device-level convergence is enabled by default.
func resolveCodexFingerprintMode(account *Account, _ bool) (codexFingerprintMode, bool) {
	if account == nil || !account.IsOpenAIOAuth() {
		return codexFingerprintOff, false
	}
	if account.Extra != nil {
		if _, configured := account.Extra[codexFingerprintModeExtraKey]; configured {
			mode := codexFingerprintModeFromExtra(account.Extra)
			return mode, mode == codexFingerprintAccountDevice
		}
	}
	// New accounts use the unified single-machine multi-window identity by default.
	return codexFingerprintSingleMachineMultiWindow, true
}

// deriveAccountCodexFingerprintSeed gives existing accounts a stable seed even
// before a database migration has materialized codex_fingerprint_seed. Account
// IDs are unique and durable, so the same account keeps the same device ID
// across requests and process restarts without mutating account extra.
func deriveAccountCodexFingerprintSeed(account *Account) string {
	if account == nil || account.ID <= 0 {
		return ""
	}
	return deriveStableUUIDv4(fmt.Sprintf("sub2api:openai-account-fingerprint:v1:%d", account.ID))
}

// deriveStableUUIDv4 从种子确定性派生一个 UUIDv4 格式的字符串。
// 同一种子永远返回同一值。
func deriveStableUUIDv4(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return uuid.UUID(b).String()
}

// deriveStableUUIDv7 从种子和毫秒时间戳确定性派生 UUIDv7。
// 真 Codex 的 thread / session ID 是 UUIDv7；时间戳必须由调用方提供且保持稳定，
// 不能用 time.Now()，否则重启后身份会漂。
func deriveStableUUIDv7(seed string, unixMs int64) string {
	h := sha256.Sum256([]byte(seed))
	if unixMs < 0 {
		unixMs = 0
	}
	ts := uint64(unixMs) & 0xffffffffffff
	var b [16]byte
	b[0] = byte(ts >> 40)
	b[1] = byte(ts >> 32)
	b[2] = byte(ts >> 24)
	b[3] = byte(ts >> 16)
	b[4] = byte(ts >> 8)
	b[5] = byte(ts)
	b[6] = (h[6] & 0x0f) | 0x70 // version 7
	b[7] = h[7]
	b[8] = (h[8] & 0x3f) | 0x80 // variant 1
	copy(b[9:], h[9:16])
	return uuid.UUID(b).String()
}

// resolveConvergedInstallationID 返回账号级恒定的 installation_id。
// 优先使用管理员配置的真实 device_id，无则从系统管理的账号随机种子确定性派生。
func resolveConvergedInstallationID(account *Account, seed string) string {
	if account == nil {
		return ""
	}
	if deviceID := account.GetOpenAIDeviceID(); deviceID != "" {
		return deviceID
	}
	if seed == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
}

// resolveConvergedSessionID 返回账号级恒定的 session_id。
func resolveConvergedSessionID(seed string) string {
	if seed == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-session-id:v2:" + seed)
}

// resolveConvergedThreadID 按客户端原始 session-id 确定性派生 thread_id。
// 每个真实 Codex 会话（不同客户端启动实例）获得一个独立线程，
// 模拟正常用户 spawn 子代理或开多窗口的模式。
func resolveConvergedThreadID(seed, clientSessionID string) string {
	if seed == "" || clientSessionID == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-thread-id:v2:" + seed + ":" + clientSessionID)
}

const (
	// 单机多窗口把下游会话收敛到少量根对话，避免一台机器出现无界 Codex 进程数。
	singleMachineRootWindowCount   = 3
	singleMachineChildSlotsPerRoot = 3
	// 2025-01-01 UTC。根/子线程的 UUIDv7 时间戳从账号种子稳定散开，看起来像
	// 这台机器上陆续开过的对话，而不是每次请求现造。
	singleMachineUUIDEpochMs int64 = 1735689600000
	singleMachineUUIDSpanMs  int64 = 400 * 24 * 60 * 60 * 1000
	singleMachineThreadGapMs int64 = 47 * 60 * 1000
)

// 不要使用 review / guardian：网关会把这两种 kind 当成 Codex 官方 review 子智能体
// 去做父线程粘性调度。
var singleMachineSubagentKinds = [...]string{"explore", "worker", "implementer"}

type singleMachineWindowIdentity struct {
	rootIndex      int
	sessionID      string
	threadID       string
	parentThreadID string
	subagentKind   string
	windowID       string
}

type singleMachineInFlightKey struct {
	accountID int64
	rootIndex int
}

type singleMachineLease struct {
	registry *singleMachineInFlightRegistry
	key      singleMachineInFlightKey
	released atomic.Uint32
}

type singleMachineInFlightRegistry struct {
	mu     sync.Mutex
	counts map[singleMachineInFlightKey]int
}

var singleMachineInFlight = &singleMachineInFlightRegistry{
	counts: make(map[singleMachineInFlightKey]int),
}

func (r *singleMachineInFlightRegistry) peek(accountID int64, rootIndex int) int {
	if r == nil || accountID <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[singleMachineInFlightKey{accountID: accountID, rootIndex: rootIndex}]
}

func (r *singleMachineInFlightRegistry) acquire(accountID int64, rootIndex int) *singleMachineLease {
	if r == nil || accountID <= 0 {
		return nil
	}
	key := singleMachineInFlightKey{accountID: accountID, rootIndex: rootIndex}
	r.mu.Lock()
	r.counts[key]++
	r.mu.Unlock()
	return &singleMachineLease{registry: r, key: key}
}

func (l *singleMachineLease) release() {
	if l == nil || l.registry == nil || !l.released.CompareAndSwap(0, 1) {
		return
	}
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	n := l.registry.counts[l.key] - 1
	if n <= 0 {
		delete(l.registry.counts, l.key)
		return
	}
	l.registry.counts[l.key] = n
}

func singleMachineThreadTimestampMs(seed string, rootIndex, childSlot int) int64 {
	h := sha256.Sum256([]byte("sub2api:codex-single-machine-ts:v1:" + seed))
	base := singleMachineUUIDEpochMs + int64(binary.BigEndian.Uint64(h[:8])%uint64(singleMachineUUIDSpanMs))
	slot := rootIndex*(singleMachineChildSlotsPerRoot+1) + childSlot
	return base + int64(slot)*singleMachineThreadGapMs
}

func deriveSingleMachineThreadID(seed string, rootIndex, childSlot int) string {
	if seed == "" {
		return ""
	}
	name := fmt.Sprintf("sub2api:codex-single-machine-thread:v1:%s:%d:%d", seed, rootIndex, childSlot)
	return deriveStableUUIDv7(name, singleMachineThreadTimestampMs(seed, rootIndex, childSlot))
}

func singleMachineRootIndex(seed, clientSessionID string) int {
	clientSessionID = strings.TrimSpace(clientSessionID)
	if seed == "" || clientSessionID == "" {
		return 0
	}
	h := sha256.Sum256([]byte("sub2api:codex-single-machine-slot:v1:" + seed + ":" + clientSessionID))
	return int(binary.BigEndian.Uint64(h[0:8]) % uint64(singleMachineRootWindowCount))
}

func singleMachineOccupancyHash(seed, clientSessionID string) uint64 {
	clientSessionID = strings.TrimSpace(clientSessionID)
	if seed == "" || clientSessionID == "" {
		return 0
	}
	h := sha256.Sum256([]byte("sub2api:codex-single-machine-slot:v1:" + seed + ":" + clientSessionID))
	return binary.BigEndian.Uint64(h[8:16])
}

// resolveSingleMachineMultiWindowIdentity 把下游 session 映射到少量根窗口。
// 默认都是根对话；只有该窗口已经有进行中的请求时，溢出并发才降级成子智能体。
// 空 session 固定落在根窗口 0，且不会变成子智能体。
func resolveSingleMachineMultiWindowIdentity(seed, clientSessionID string, windowBusy bool) singleMachineWindowIdentity {
	if seed == "" {
		return singleMachineWindowIdentity{}
	}
	rootIndex := singleMachineRootIndex(seed, clientSessionID)
	rootThreadID := deriveSingleMachineThreadID(seed, rootIndex, 0)
	if rootThreadID == "" {
		return singleMachineWindowIdentity{}
	}
	ident := singleMachineWindowIdentity{
		rootIndex: rootIndex,
		sessionID: rootThreadID,
		threadID:  rootThreadID,
		windowID:  rootThreadID + ":0",
	}
	if !windowBusy || strings.TrimSpace(clientSessionID) == "" {
		return ident
	}
	childSlot := int(singleMachineOccupancyHash(seed, clientSessionID)%uint64(singleMachineChildSlotsPerRoot)) + 1
	childThreadID := deriveSingleMachineThreadID(seed, rootIndex, childSlot)
	ident.threadID = childThreadID
	ident.parentThreadID = rootThreadID
	ident.subagentKind = singleMachineSubagentKinds[(rootIndex+childSlot-1)%len(singleMachineSubagentKinds)]
	ident.windowID = childThreadID + ":0"
	return ident
}

func isCodexFingerprintClient(h http.Header) bool {
	if h == nil {
		return false
	}
	if openai.IsCodexOfficialClientByHeaders(h.Get("User-Agent"), h.Get("originator")) {
		return true
	}
	for key := range h {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "x-codex-") {
			return true
		}
	}
	return strings.TrimSpace(h.Get("session-id")) != "" && strings.TrimSpace(h.Get("thread-id")) != ""
}

// codexFingerprintIDs 收敛后的完整 ID 集合。
// 由 resolveCodexFingerprintIDs 一次性生成，同一个实例在头改写和体改写之间共享，
// 确保所有载体中的 turn_id 等随机字段一致。体改写时还会补记原始
// client_metadata.session_id：session/full 用它识别默认 prompt_cache_key；
// single_machine_multi_window 只收敛身份窗口，不改写缓存键。
type codexFingerprintIDs struct {
	accountID                     int64
	mode                          codexFingerprintMode
	installationID                string
	sessionID                     string
	threadID                      string
	turnID                        string
	windowID                      string
	parentThreadID                string
	subagentKind                  string
	rootIndex                     int
	lease                         *singleMachineLease
	turnStartedAtUnixMs           int64
	originalClientSessionID       string
	originalBodySessionID         string
	originalBodySessionIDCaptured bool
}

// resolveCodexFingerprintIDs 按收敛模式计算出站 ID 集合。
// clientSessionID 是客户端原始的 session-id 头值（连字符形式），用于 session 模式下
// 的 thread_id 派生——每个真实 Codex 会话得到一个独立线程。
// 返回 nil 表示 off 模式，不需要改写。
// 注意：包含随机生成的 turn_id，调用方必须只调用一次并共享结果给头改写和体改写。
func resolveCodexFingerprintIDs(account *Account, clientSessionID string, mode codexFingerprintMode) *codexFingerprintIDs {
	return resolveCodexFingerprintIDsWithSeed(account, clientSessionID, mode, "")
}

func resolveCodexFingerprintIDsWithSeed(account *Account, clientSessionID string, mode codexFingerprintMode, seedOverride string) *codexFingerprintIDs {
	if account == nil || mode == codexFingerprintOff {
		return nil
	}
	seed := strings.TrimSpace(seedOverride)
	if seed == "" {
		var ok bool
		seed, ok = codexFingerprintSeed(account.Extra)
		if !ok {
			return nil
		}
	}
	if seed == "" {
		return nil
	}

	ids := &codexFingerprintIDs{
		accountID:           account.ID,
		mode:                mode,
		turnStartedAtUnixMs: time.Now().UnixMilli(),
	}

	ids.installationID = resolveConvergedInstallationID(account, seed)
	if ids.installationID == "" {
		return nil
	}

	switch mode {
	case codexFingerprintAccountDevice, codexFingerprintDevice:
		return ids

	case codexFingerprintSession:
		ids.sessionID = resolveConvergedSessionID(seed)
		ids.threadID = resolveConvergedThreadID(seed, clientSessionID)
		if ids.threadID == "" {
			ids.threadID = ids.sessionID
		}
		ids.originalClientSessionID = strings.TrimSpace(clientSessionID)
		ids.turnID = uuid.Must(uuid.NewV7()).String()
		ids.windowID = ids.threadID + ":0"
		return ids

	case codexFingerprintFull:
		ids.sessionID = resolveConvergedSessionID(seed)
		ids.threadID = ids.sessionID
		ids.originalClientSessionID = strings.TrimSpace(clientSessionID)
		ids.turnID = uuid.Must(uuid.NewV7()).String()
		ids.windowID = ids.threadID + ":0"
		return ids

	case codexFingerprintSingleMachineMultiWindow:
		rootIndex := singleMachineRootIndex(seed, clientSessionID)
		windowBusy := account.ID > 0 && singleMachineInFlight.peek(account.ID, rootIndex) > 0
		ident := resolveSingleMachineMultiWindowIdentity(seed, clientSessionID, windowBusy)
		if ident.threadID == "" {
			return nil
		}
		ids.sessionID = ident.sessionID
		ids.threadID = ident.threadID
		ids.parentThreadID = ident.parentThreadID
		ids.subagentKind = ident.subagentKind
		ids.rootIndex = ident.rootIndex
		ids.originalClientSessionID = strings.TrimSpace(clientSessionID)
		ids.turnID = uuid.Must(uuid.NewV7()).String()
		ids.windowID = ident.windowID
		return ids
	}

	return nil
}

// extractClientSessionID 从请求头中提取客户端原始的会话标识。
// 优先取 session-id（连字符形式，Codex CLI 标准），回退到 session_id（下划线形式）。
// 返回的值尚未被 isolateOpenAISessionID 改写，是客户端的真实标识。
func extractClientSessionID(h http.Header) string {
	if v := strings.TrimSpace(h.Get("session-id")); v != "" {
		return v
	}
	return strings.TrimSpace(h.Get("session_id"))
}

// resolveCodexFingerprintIDsFromRequest 从客户端原始请求头中提取 session-id，
// 结合账号配置一次性解析收敛 ID 集合。调用方应将返回的 ids 同时传给
// applyCodexFingerprintHeaders 和 applyCodexFingerprintClientMetadata。
func resolveCodexFingerprintIDsFromRequest(account *Account, clientHeaders http.Header, uniqueFingerprintEnabled ...bool) *codexFingerprintIDs {
	if account == nil {
		return nil
	}
	enabled := len(uniqueFingerprintEnabled) > 0 && uniqueFingerprintEnabled[0]
	mode, isDefault := resolveCodexFingerprintMode(account, enabled)
	if mode == codexFingerprintOff {
		return nil
	}
	clientSessionID := ""
	if clientHeaders != nil {
		clientSessionID = extractClientSessionID(clientHeaders)
	}
	if mode == codexFingerprintSingleMachineMultiWindow && !isCodexFingerprintClient(clientHeaders) {
		return nil
	}
	if isDefault {
		seed := deriveAccountCodexFingerprintSeed(account)
		if persisted, ok := codexFingerprintSeed(account.Extra); ok {
			seed = persisted
		}
		return resolveCodexFingerprintIDsWithSeed(account, clientSessionID, mode, seed)
	}
	return resolveCodexFingerprintIDs(account, clientSessionID, mode)
}

// applyCodexFingerprintHeaders 按预计算的收敛 ID 改写出站 HTTP 头中的设备指纹。
// 在 buildUpstreamRequest 的白名单透传之后、enforceCodexIdentityHeaders 之前调用。
func applyCodexFingerprintHeaders(h http.Header, ids *codexFingerprintIDs) {
	if h == nil || ids == nil {
		return
	}

	// 所有非 off 模式都收敛 installation_id
	h.Set("x-codex-installation-id", ids.installationID)

	if ids.mode == codexFingerprintAccountDevice || ids.mode == codexFingerprintDevice {
		rewriteCodexTurnMetadataFields(h, map[string]any{
			"installation_id": ids.installationID,
		})
		return
	}

	// session / full / single-machine 模式：改写所有相关头
	h.Set("x-codex-window-id", ids.windowID)
	h.Set("x-client-request-id", ids.threadID)
	h.Set("session-id", ids.sessionID)
	h.Set("session_id", ids.sessionID)
	h.Set("conversation_id", ids.sessionID)
	h.Set("thread-id", ids.threadID)
	if ids.mode == codexFingerprintSingleMachineMultiWindow {
		h.Del("thread_id")
		if ids.parentThreadID != "" {
			h.Set(codexParentThreadIDHeader, ids.parentThreadID)
			h.Set(openAISubagentHeader, ids.subagentKind)
		} else {
			h.Del(codexParentThreadIDHeader)
			h.Del(openAISubagentHeader)
		}
	}

	fields, deleteKeys := codexFingerprintTurnMetadataFields(ids)
	applyCodexTurnMetadataHeader(h, fields, deleteKeys, ids.mode == codexFingerprintSingleMachineMultiWindow)
}

// rewriteCodexTurnMetadataFields 解析 x-codex-turn-metadata 头中的 JSON，
// 替换指定字段后回写。合法对象保留未指定字段（如 sandbox、thread_source）；
// 非法/非对象值重建为最小合法 metadata，避免 flat 与 embedded identity 分裂。
func rewriteCodexTurnMetadataFields(h http.Header, fields map[string]any) {
	applyCodexTurnMetadataHeader(h, fields, nil, false)
}

func codexFingerprintTurnMetadataFields(ids *codexFingerprintIDs) (map[string]any, []string) {
	if ids == nil {
		return nil, nil
	}
	if ids.mode == codexFingerprintAccountDevice || ids.mode == codexFingerprintDevice {
		return map[string]any{"installation_id": ids.installationID}, nil
	}
	fields := map[string]any{
		"installation_id":         ids.installationID,
		"session_id":              ids.sessionID,
		"thread_id":               ids.threadID,
		"turn_id":                 ids.turnID,
		"window_id":               ids.windowID,
		"turn_started_at_unix_ms": ids.turnStartedAtUnixMs,
	}
	if ids.mode != codexFingerprintSingleMachineMultiWindow {
		return fields, nil
	}
	if ids.parentThreadID != "" {
		fields["parent_thread_id"] = ids.parentThreadID
		fields["subagent_kind"] = ids.subagentKind
		return fields, nil
	}
	return fields, []string{"parent_thread_id", "subagent_kind"}
}

func mergeCodexTurnMetadataJSON(raw string, fields map[string]any, deleteKeys []string, createIfMissing bool) (string, bool) {
	if strings.TrimSpace(raw) == "" && !createIfMissing {
		return raw, false
	}
	var metadata map[string]any
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &metadata); err != nil || metadata == nil {
			metadata = make(map[string]any, len(fields))
		}
	} else {
		metadata = make(map[string]any, len(fields))
	}
	for k, v := range fields {
		metadata[k] = v
	}
	for _, key := range deleteKeys {
		delete(metadata, key)
	}
	rebuilt, err := json.Marshal(metadata)
	if err != nil {
		return raw, false
	}
	return string(rebuilt), true
}

func applyCodexTurnMetadataHeader(h http.Header, fields map[string]any, deleteKeys []string, createIfMissing bool) {
	if h == nil {
		return
	}
	next, ok := mergeCodexTurnMetadataJSON(h.Get("x-codex-turn-metadata"), fields, deleteKeys, createIfMissing)
	if !ok {
		return
	}
	h.Set("x-codex-turn-metadata", next)
}

// applyCodexFingerprintClientMetadata 按预计算的收敛 ID 改写请求体中的 client_metadata。
// 使用与头改写相同的 ids 实例，确保 turn_id 等随机字段一致。
func applyCodexFingerprintClientMetadata(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil || ids == nil {
		return false
	}

	captureCodexFingerprintOriginalBodySessionID(ids, reqBody["client_metadata"])
	existing, _ := reqBody["client_metadata"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}

	modified := false
	if applyCodexFingerprintToClientMetadataMap(existing, ids) {
		reqBody["client_metadata"] = existing
		modified = true
	}
	if applyCodexFingerprintPromptCacheKey(reqBody, ids) {
		modified = true
	}
	return modified
}

// applyCodexFingerprintToClientMetadataMap 是 client_metadata 改写的共享核心，
// map 版（非透传，body 已解码）与 raw 字节版（透传热路径）都经由它，保证两条
// 路径的收敛语义永不漂移。
func applyCodexFingerprintToClientMetadataMap(existing map[string]any, ids *codexFingerprintIDs) bool {
	if existing == nil || ids == nil {
		return false
	}

	modified := false

	if ids.installationID != "" {
		existing["x-codex-installation-id"] = ids.installationID
		modified = true
	}

	if ids.mode == codexFingerprintAccountDevice || ids.mode == codexFingerprintDevice {
		fields, deleteKeys := codexFingerprintTurnMetadataFields(ids)
		applyClientMetadataEmbeddedTurnMetadata(existing, fields, deleteKeys, false)
		return modified
	}

	// session / full / single-machine 模式
	existing["session_id"] = ids.sessionID
	existing["thread_id"] = ids.threadID
	existing["turn_id"] = ids.turnID
	existing["x-codex-window-id"] = ids.windowID

	fields, deleteKeys := codexFingerprintTurnMetadataFields(ids)
	applyClientMetadataEmbeddedTurnMetadata(existing, fields, deleteKeys, ids.mode == codexFingerprintSingleMachineMultiWindow)
	return true
}

func captureCodexFingerprintOriginalBodySessionID(ids *codexFingerprintIDs, clientMetadata any) {
	if ids == nil || ids.originalBodySessionIDCaptured {
		return
	}
	ids.originalBodySessionIDCaptured = true
	if clientMetadata == nil {
		return
	}
	switch metadata := clientMetadata.(type) {
	case map[string]any:
		if sessionID, ok := metadata["session_id"].(string); ok {
			ids.originalBodySessionID = strings.TrimSpace(sessionID)
		}
	case map[string]string:
		ids.originalBodySessionID = strings.TrimSpace(metadata["session_id"])
	}
}

func captureCodexFingerprintOriginalBodySessionIDRaw(ids *codexFingerprintIDs, value gjson.Result) {
	if ids == nil || ids.originalBodySessionIDCaptured {
		return
	}
	ids.originalBodySessionIDCaptured = true
	if value.Exists() && value.Type == gjson.String {
		ids.originalBodySessionID = strings.TrimSpace(value.String())
	}
}

// shouldRewriteCodexFingerprintPromptCacheKey 仅在 session/full 把默认
// prompt_cache_key（等于原始 body/header session）收敛到账号级 session。
// single_machine_multi_window 必须保留下游会话自己的 cache key：身份窗口可以
// 只有 3 个，但官方 prompt cache 大约 15 RPM/key，把无关对话压到 3 把 key 上
// 会溢到新机器并打穿缓存率。
func shouldRewriteCodexFingerprintPromptCacheKey(ids *codexFingerprintIDs, promptCacheKey string) bool {
	if ids == nil || !ids.originalBodySessionIDCaptured || ids.originalBodySessionID == "" || ids.sessionID == "" {
		return false
	}
	if ids.mode != codexFingerprintSession && ids.mode != codexFingerprintFull {
		return false
	}
	if promptCacheKey == ids.originalBodySessionID {
		return true
	}
	return ids.originalClientSessionID != "" && promptCacheKey == ids.originalClientSessionID
}

func applyCodexFingerprintPromptCacheKey(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil {
		return false
	}
	promptCacheKey, ok := reqBody["prompt_cache_key"].(string)
	if !ok || strings.TrimSpace(promptCacheKey) == "" || !shouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey) {
		return false
	}
	if promptCacheKey == ids.sessionID {
		return false
	}
	reqBody["prompt_cache_key"] = ids.sessionID
	return true
}

// applyCodexFingerprintClientMetadataRaw 在原始 JSON 字节上改写 client_metadata，
// 供透传路径使用——透传是热路径，禁止对可能高达数十 MB 的 body 做全量
// Unmarshal（见 forwardOpenAIPassthrough 的轻量提取注释）。实现为：gjson 提取
// client_metadata 小对象单独解码，经共享核心改写后 sjson 一次性拼回，body
// 其余字节原样保留；session/full 的默认 prompt_cache_key 仅在可证明是
// body/header session 默认值时做标量改写。single_machine 不改缓存键。
// 语义与 applyCodexFingerprintClientMetadata 逐点一致（含
// "非对象值整体替换为收敛集合"的行为）。
func applyCodexFingerprintClientMetadataRaw(body []byte, ids *codexFingerprintIDs) ([]byte, bool, error) {
	if len(body) == 0 || ids == nil {
		return body, false, nil
	}
	// 非 JSON 对象的 body（数组/标量/畸形）没有 client_metadata 语义，
	// sjson 在这类根上写字段会改写整体结构，直接放行保持原样。
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
		return body, false, nil
	}

	existing := map[string]any{}
	if cm := gjson.GetBytes(body, "client_metadata"); cm.IsObject() {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.GetBytes(body, "client_metadata.session_id"))
		if err := json.Unmarshal([]byte(cm.Raw), &existing); err != nil {
			return body, false, fmt.Errorf("decode client_metadata for fingerprint: %w", err)
		}
	} else {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
	}

	next := body
	modified := false
	if applyCodexFingerprintToClientMetadataMap(existing, ids) {
		raw, err := json.Marshal(existing)
		if err != nil {
			return body, false, fmt.Errorf("encode converged client_metadata: %w", err)
		}
		var setErr error
		next, setErr = sjson.SetRawBytes(body, "client_metadata", raw)
		if setErr != nil {
			return body, false, fmt.Errorf("splice converged client_metadata: %w", setErr)
		}
		modified = true
	}
	promptCacheKey := gjson.GetBytes(body, "prompt_cache_key")
	if promptCacheKey.Exists() && promptCacheKey.Type == gjson.String && strings.TrimSpace(promptCacheKey.String()) != "" && shouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey.String()) {
		rewritten, err := sjson.SetBytes(next, "prompt_cache_key", ids.sessionID)
		if err != nil {
			return body, false, fmt.Errorf("splice converged prompt_cache_key: %w", err)
		}
		next = rewritten
		modified = true
	}
	return next, modified, nil
}

// rewriteClientMetadataEmbeddedTurnMetadata 改写 client_metadata 中内嵌的
// x-codex-turn-metadata JSON 字符串里的指定字段。非法/非对象值会重建，
// 避免 flat client_metadata 与 embedded metadata 暴露两套身份。
func rewriteClientMetadataEmbeddedTurnMetadata(clientMetadata map[string]any, fields map[string]any) {
	applyClientMetadataEmbeddedTurnMetadata(clientMetadata, fields, nil, false)
}

func applyClientMetadataEmbeddedTurnMetadata(clientMetadata map[string]any, fields map[string]any, deleteKeys []string, createIfMissing bool) {
	if clientMetadata == nil {
		return
	}
	raw, _ := clientMetadata["x-codex-turn-metadata"].(string)
	next, ok := mergeCodexTurnMetadataJSON(raw, fields, deleteKeys, createIfMissing)
	if !ok {
		return
	}
	clientMetadata["x-codex-turn-metadata"] = next
}
