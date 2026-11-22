// Orange 特有：使用记录的「首字延迟」拆分为两段（网关准备耗时 / 等待上游耗时）。
//
// 口径（与前端共用）：
//
//	gateway_ms  = 从网关收到请求（请求级起始时间）到「上游 HTTP 请求已发出」
//	upstream_ms = 从「上游请求已发出」到「收到首个内容块」= first_token_ms - gateway_ms（clamp >= 0）
//
// 设计约束：
//   - 只对流式请求采样；非流式 / OpenAI WebSocket / 拿不到时间点时不写记录（保持 NULL）；
//   - 数据只落 Orange 自建表 orange_usage_latency（迁移 8005），usage_logs 等上游表只读；
//   - 全程 best-effort：写库失败只记 warn 日志，绝不影响计费与主流程；
//   - 不扩展上游接口：能力通过类型断言（OrangeUsageLatencyStore）暴露，避免牵连 stub 与上游文件。
package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// orangeUsageLatencyMaxRequestIDBytes 对应 orange_usage_latency.request_id VARCHAR(128)。
// 超长 request_id 直接跳过，避免必然失败的写库（best-effort 路径不值得截断后产生歧义数据）。
const orangeUsageLatencyMaxRequestIDBytes = 128

// OrangeLatencyTrace 一次网关请求的延迟时间点。
//
// 三个时间点都在请求 ctx 上传递：中间件创建并写入 StartAt，httpUpstream 在真正发出上游请求前
// 写 SentAt、收到响应头时写 HeadersAt，usage log 落库时读取并计算两段耗时。
// 读写在不同的 goroutine（上游请求 goroutine、计费 worker）上发生，因此用互斥锁保护。
type OrangeLatencyTrace struct {
	mu        sync.Mutex
	startAt   time.Time // 网关收到请求（请求级起始时间）
	sentAt    time.Time // 上游 HTTP 请求已发出（首个成功发出的尝试）
	headersAt time.Time // 收到上游响应头
}

// NewOrangeLatencyTrace 以 startAt 作为「网关收到请求」时间点创建追踪对象。
// startAt 为零值时退化为创建时刻，避免产生负耗时。
func NewOrangeLatencyTrace(startAt time.Time) *OrangeLatencyTrace {
	if startAt.IsZero() {
		startAt = time.Now()
	}
	return &OrangeLatencyTrace{startAt: startAt}
}

// MarkSent 记录「上游 HTTP 请求已发出」。只记第一次：多账号故障转移会重试多次，
// 网关准备耗时应当锚定第一次真正发出上游请求的时刻，后续失败尝试的时间落在 upstream_ms。
func (t *OrangeLatencyTrace) MarkSent(now time.Time) {
	if t == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sentAt.IsZero() {
		t.sentAt = now
	}
}

// MarkHeaders 记录「收到上游响应头」。只记第一次；当前口径不参与计算，
// 仅用于排查（上游建连 / 排队 / 首字等待的区分）。
func (t *OrangeLatencyTrace) MarkHeaders(now time.Time) {
	if t == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.headersAt.IsZero() {
		t.headersAt = now
	}
}

// StartAt 返回网关收到请求的时间点（零值表示未安装追踪）。
func (t *OrangeLatencyTrace) StartAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startAt
}

// SentAt 返回上游请求发出的时间点（零值表示尚未发出或未采样）。
func (t *OrangeLatencyTrace) SentAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sentAt
}

// HeadersAt 返回收到上游响应头的时间点（零值表示尚未收到）。
func (t *OrangeLatencyTrace) HeadersAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.headersAt
}

// Split 计算 gateway_ms / upstream_ms。
// firstTokenMs 为 usage_logs.first_token_ms（该请求的首字时间）。
//
// 返回 ok=false 表示拿不到时间点（未安装追踪 / 上游请求未发出 / 首字时间缺失），调用方应跳过写库。
//
// 说明：上游 first_token_ms 的计时起点是转发函数入口，比本追踪对象的起点（中间件入口）更晚，
// 因此 firstTokenMs - gatewayMs 会少计「网关入口 → 转发入口」这段准备时间；按既定口径只做 clamp >= 0。
// 另做一次倒挂保护：gateway_ms 不超过 first_token_ms，保证两段之和恒等于首字时间，
// 避免极快上游（首字落在网关准备时间之内）出现「网关耗时 > 首字时间」的展示倒挂。
func (t *OrangeLatencyTrace) Split(firstTokenMs int) (gatewayMs int, upstreamMs int, ok bool) {
	if t == nil || firstTokenMs < 0 {
		return 0, 0, false
	}
	t.mu.Lock()
	startAt, sentAt := t.startAt, t.sentAt
	t.mu.Unlock()
	if startAt.IsZero() || sentAt.IsZero() {
		return 0, 0, false
	}

	gatewayMs = int(sentAt.Sub(startAt).Milliseconds())
	if gatewayMs < 0 {
		gatewayMs = 0
	}
	if gatewayMs > firstTokenMs {
		gatewayMs = firstTokenMs
	}
	upstreamMs = firstTokenMs - gatewayMs
	if upstreamMs < 0 {
		upstreamMs = 0
	}
	return gatewayMs, upstreamMs, true
}

// orangeLatencyTraceKey 追踪对象在请求 ctx 上的键（包内私有类型，不会与上游 ctx key 冲突）。
type orangeLatencyTraceKey struct{}

// WithOrangeLatencyTrace 把追踪对象挂到 ctx 上；trace 为 nil 时原样返回。
func WithOrangeLatencyTrace(ctx context.Context, trace *OrangeLatencyTrace) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if trace == nil {
		return ctx
	}
	return context.WithValue(ctx, orangeLatencyTraceKey{}, trace)
}

// OrangeLatencyTraceFromContext 取请求 ctx 上的追踪对象。
func OrangeLatencyTraceFromContext(ctx context.Context) (*OrangeLatencyTrace, bool) {
	if ctx == nil {
		return nil, false
	}
	trace, ok := ctx.Value(orangeLatencyTraceKey{}).(*OrangeLatencyTrace)
	if !ok || trace == nil {
		return nil, false
	}
	return trace, true
}

// EnsureOrangeLatencyTrace 取 ctx 上的追踪对象；不存在时以 startAt 创建并挂回 ctx。
// 返回的 ctx 必须写回 *http.Request，后续派生 ctx（含计费 worker 的 request 级 ctx）才能取到。
func EnsureOrangeLatencyTrace(ctx context.Context, startAt time.Time) (context.Context, *OrangeLatencyTrace) {
	if trace, ok := OrangeLatencyTraceFromContext(ctx); ok {
		return ctx, trace
	}
	trace := NewOrangeLatencyTrace(startAt)
	return WithOrangeLatencyTrace(ctx, trace), trace
}

// OrangeUsageLatencyRecord 一条首字延迟拆分记录（毫秒）。
type OrangeUsageLatencyRecord struct {
	GatewayMs  int
	UpstreamMs int
}

// OrangeUsageLatencyWriter 是 orange_usage_latency 的写入能力（计费链路使用）。
type OrangeUsageLatencyWriter interface {
	// OrangeUpsertUsageLatency 写入/更新一条记录（request_id 冲突时覆盖两段耗时）。
	OrangeUpsertUsageLatency(ctx context.Context, requestID string, record OrangeUsageLatencyRecord) error
}

// OrangeUsageLatencyReader 是 orange_usage_latency 的读取能力（使用记录列表回填使用）。
type OrangeUsageLatencyReader interface {
	// OrangeLoadUsageLatency 按 request_id 批量读取（单条 IN 查询），缺失的 id 不出现在结果里。
	OrangeLoadUsageLatency(ctx context.Context, requestIDs []string) (map[string]OrangeUsageLatencyRecord, error)
}

// OrangeUsageLatencyStore 是 orange_usage_latency（Orange 自建表）的完整读写能力。
// 由 repository 层的 usage log 仓储实现，service 侧用类型断言取用（读写各自独立断言）：
// 不扩展上游 UsageLogRepository 接口，避免牵连所有 stub 与上游文件。
type OrangeUsageLatencyStore interface {
	OrangeUsageLatencyWriter
	OrangeUsageLatencyReader
}

// OrangeShouldRecordUsageLatency 判断这条 usage log 是否属于「需要采样首字延迟拆分」的请求：
// 仅流式（非 WS）且拿到了首字时间的请求有意义。
func OrangeShouldRecordUsageLatency(usageLog *UsageLog) bool {
	if usageLog == nil || usageLog.FirstTokenMs == nil {
		return false
	}
	if *usageLog.FirstTokenMs < 0 {
		return false
	}
	// WS v2 / WS bridge 的轮次首字来自 WebSocket 事件，与本追踪对象的 HTTP 时间点不同源。
	if usageLog.OpenAIWSMode {
		return false
	}
	// RequestType 收口后以 EffectiveRequestType 为准（Stream 字段会被同步，但历史/中间态可能不同步）。
	return usageLog.EffectiveRequestType() == RequestTypeStream
}

// OrangeRecordUsageLatency 在 usage log 落库时 best-effort 写入 orange_usage_latency。
//
// 调用方（writeUsageLogBestEffort）已持有请求级 ctx；追踪对象在中间件入口安装，
// 经上游打点后在计费链路上读取。任何一步拿不到时间点都静默跳过，写库失败只记 warn 日志：
// 该数据只用于展示，不能影响计费结果或主流程。
func OrangeRecordUsageLatency(ctx context.Context, repo UsageLogRepository, usageLog *UsageLog) {
	if repo == nil || usageLog == nil || !OrangeShouldRecordUsageLatency(usageLog) {
		return
	}
	store, ok := repo.(OrangeUsageLatencyWriter)
	if !ok || store == nil {
		return
	}
	trace, ok := OrangeLatencyTraceFromContext(ctx)
	if !ok {
		return
	}
	gatewayMs, upstreamMs, ok := trace.Split(*usageLog.FirstTokenMs)
	if !ok {
		return
	}
	requestID := strings.TrimSpace(usageLog.RequestID)
	if requestID == "" || len(requestID) > orangeUsageLatencyMaxRequestIDBytes {
		return
	}

	// 与 usage log 写入同源：用 detached 超时窗口，避免客户端断开导致这次统计被取消。
	writeCtx, cancel := detachedBillingContext(ctx)
	defer cancel()
	if err := store.OrangeUpsertUsageLatency(writeCtx, requestID, OrangeUsageLatencyRecord{
		GatewayMs:  gatewayMs,
		UpstreamMs: upstreamMs,
	}); err != nil {
		slog.Warn("orange_usage_latency_upsert_failed",
			"request_id", requestID,
			"gateway_ms", gatewayMs,
			"upstream_ms", upstreamMs,
			"error", err,
		)
	}
}

// OrangeUsageLatency 按 request_id 批量读取首字延迟拆分（供使用记录列表回填）。
// 返回 nil 表示读取不可用或失败——调用方据此让两个字段保持 null（前端不显示额外行）。
func (s *UsageService) OrangeUsageLatency(ctx context.Context, requestIDs []string) map[string]OrangeUsageLatencyRecord {
	if s == nil || s.usageRepo == nil {
		return nil
	}
	store, ok := s.usageRepo.(OrangeUsageLatencyReader)
	if !ok || store == nil {
		return nil
	}
	ids := orangeUsageLatencyRequestIDs(requestIDs)
	if len(ids) == 0 {
		return nil
	}
	latencies, err := store.OrangeLoadUsageLatency(ctx, ids)
	if err != nil {
		slog.Warn("orange_usage_latency_load_failed",
			"request_id_count", len(ids),
			"error", err,
		)
		return nil
	}
	return latencies
}

// OrangeUsageLatencyForLogs 按一页 usage log 的 request_id 批量读取首字延迟拆分。
// 列表 handler 直接传入本页记录即可，无需自己提取 id；语义与 OrangeUsageLatency 相同。
func (s *UsageService) OrangeUsageLatencyForLogs(ctx context.Context, logs []UsageLog) map[string]OrangeUsageLatencyRecord {
	if len(logs) == 0 {
		return nil
	}
	requestIDs := make([]string, 0, len(logs))
	for i := range logs {
		if id := strings.TrimSpace(logs[i].RequestID); id != "" {
			requestIDs = append(requestIDs, id)
		}
	}
	return s.OrangeUsageLatency(ctx, requestIDs)
}

// orangeUsageLatencyRequestIDs 去空、去重并丢掉超长 id（列宽 VARCHAR(128)），保持原有顺序。
func orangeUsageLatencyRequestIDs(requestIDs []string) []string {
	if len(requestIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(requestIDs))
	out := make([]string, 0, len(requestIDs))
	for _, raw := range requestIDs {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > orangeUsageLatencyMaxRequestIDBytes {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
