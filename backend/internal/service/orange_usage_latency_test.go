// Orange 特有：首字延迟拆分（gateway_ms / upstream_ms）的追踪与计算口径自检。
package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// orangeLatencyTestRepo 只实现 Orange 延迟拆分能力，其余 UsageLogRepository 方法靠内嵌接口占位。
type orangeLatencyTestRepo struct {
	UsageLogRepository
	upserts   map[string]OrangeUsageLatencyRecord
	loadCalls [][]string
	upsertErr error
	loadErr   error
}

func (r *orangeLatencyTestRepo) OrangeUpsertUsageLatency(_ context.Context, requestID string, record OrangeUsageLatencyRecord) error {
	if r.upsertErr != nil {
		return r.upsertErr
	}
	if r.upserts == nil {
		r.upserts = make(map[string]OrangeUsageLatencyRecord, 1)
	}
	r.upserts[requestID] = record
	return nil
}

func (r *orangeLatencyTestRepo) OrangeLoadUsageLatency(_ context.Context, requestIDs []string) (map[string]OrangeUsageLatencyRecord, error) {
	r.loadCalls = append(r.loadCalls, requestIDs)
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	out := make(map[string]OrangeUsageLatencyRecord, len(requestIDs))
	for _, id := range requestIDs {
		if record, ok := r.upserts[id]; ok {
			out[id] = record
		}
	}
	return out, nil
}

func newOrangeLatencyTraceAt(t *testing.T, start time.Time) *OrangeLatencyTrace {
	t.Helper()
	return NewOrangeLatencyTrace(start)
}

func TestOrangeLatencyTraceSplit(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	// 未打点「上游请求已发出」：拿不到时间点，调用方应跳过写库。
	trace := newOrangeLatencyTraceAt(t, start)
	_, _, ok := trace.Split(500)
	require.False(t, ok)

	trace.MarkSent(start.Add(40 * time.Millisecond))
	gatewayMs, upstreamMs, ok := trace.Split(500)
	require.True(t, ok)
	require.Equal(t, 40, gatewayMs)
	require.Equal(t, 460, upstreamMs)
	require.Equal(t, 500, gatewayMs+upstreamMs, "两段之和必须等于首字时间")

	// 首字时间缺失/负数：视为未采样。
	_, _, ok = trace.Split(-1)
	require.False(t, ok)
}

func TestOrangeLatencyTraceSplitClampsUpstreamToZero(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	trace := newOrangeLatencyTraceAt(t, start)
	trace.MarkSent(start.Add(300 * time.Millisecond))

	// first_token_ms 的起点比本追踪对象晚，倒挂时 gateway_ms 不超过首字时间、upstream_ms 夹到 0。
	gatewayMs, upstreamMs, ok := trace.Split(120)
	require.True(t, ok)
	require.Equal(t, 120, gatewayMs)
	require.Equal(t, 0, upstreamMs)
}

func TestOrangeLatencyTraceMarkSentKeepsFirstAttempt(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	trace := newOrangeLatencyTraceAt(t, start)

	first := start.Add(50 * time.Millisecond)
	trace.MarkSent(first)
	// 多账号故障转移会重试，网关准备耗时锚定第一次发出上游请求的时刻。
	trace.MarkSent(start.Add(900 * time.Millisecond))
	require.True(t, trace.SentAt().Equal(first))

	require.True(t, trace.HeadersAt().IsZero())
	trace.MarkHeaders(start.Add(200 * time.Millisecond))
	trace.MarkHeaders(start.Add(800 * time.Millisecond))
	require.True(t, trace.HeadersAt().Equal(start.Add(200*time.Millisecond)))

	require.True(t, trace.StartAt().Equal(start))
}

func TestOrangeLatencyTraceContextRoundTrip(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	ctx, trace := EnsureOrangeLatencyTrace(context.Background(), start)
	require.NotNil(t, trace)
	require.True(t, trace.StartAt().Equal(start))

	got, ok := OrangeLatencyTraceFromContext(ctx)
	require.True(t, ok)
	require.Same(t, trace, got)

	// 已安装时复用同一个追踪对象（不重置起始时间）。
	sameCtx, sameTrace := EnsureOrangeLatencyTrace(ctx, start.Add(time.Hour))
	require.Same(t, trace, sameTrace)
	require.Same(t, ctx, sameCtx)

	// 派生 ctx（如计费 worker 的 request 级 ctx）仍能取到同一个对象。
	derived, ok := OrangeLatencyTraceFromContext(context.WithValue(ctx, struct{ name string }{"k"}, "v"))
	require.True(t, ok)
	require.Same(t, trace, derived)

	require.Nil(t, WithOrangeLatencyTrace(context.Background(), nil).Value(orangeLatencyTraceKey{}))
	_, ok = OrangeLatencyTraceFromContext(nil)
	require.False(t, ok)
	_, ok = OrangeLatencyTraceFromContext(context.Background())
	require.False(t, ok)
}

func TestOrangeShouldRecordUsageLatency(t *testing.T) {
	firstTokenMs := 320

	require.False(t, OrangeShouldRecordUsageLatency(nil))
	require.False(t, OrangeShouldRecordUsageLatency(&UsageLog{Stream: true}), "缺首字时间不采样")
	require.False(t, OrangeShouldRecordUsageLatency(&UsageLog{Stream: true, OpenAIWSMode: true, FirstTokenMs: &firstTokenMs}))
	require.False(t, OrangeShouldRecordUsageLatency(&UsageLog{RequestType: RequestTypeSync, FirstTokenMs: &firstTokenMs}))
	require.True(t, OrangeShouldRecordUsageLatency(&UsageLog{Stream: true, FirstTokenMs: &firstTokenMs}))
}

func TestOrangeRecordUsageLatencyBestEffort(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	firstTokenMs := 400

	newSampledLog := func() *UsageLog {
		return &UsageLog{RequestID: "local:req-1", Stream: true, FirstTokenMs: &firstTokenMs}
	}

	trace := newOrangeLatencyTraceAt(t, start)
	trace.MarkSent(start.Add(25 * time.Millisecond))
	ctx := WithOrangeLatencyTrace(context.Background(), trace)

	repo := &orangeLatencyTestRepo{}
	OrangeRecordUsageLatency(ctx, repo, newSampledLog())
	require.Equal(t, OrangeUsageLatencyRecord{GatewayMs: 25, UpstreamMs: 375}, repo.upserts["local:req-1"])

	// 非流式 / 无首字 / 无追踪对象 / 请求 id 超长：全部静默跳过。
	skipped := &orangeLatencyTestRepo{}
	OrangeRecordUsageLatency(ctx, skipped, &UsageLog{RequestID: "sync-1", RequestType: RequestTypeSync, FirstTokenMs: &firstTokenMs})
	OrangeRecordUsageLatency(ctx, skipped, &UsageLog{RequestID: "no-ttft", Stream: true})
	OrangeRecordUsageLatency(context.Background(), skipped, newSampledLog())
	longID := strings.Repeat("r", orangeUsageLatencyMaxRequestIDBytes+1)
	OrangeRecordUsageLatency(ctx, skipped, &UsageLog{RequestID: longID, Stream: true, FirstTokenMs: &firstTokenMs})
	require.Empty(t, skipped.upserts)

	// 写库失败只记 warn 日志，不 panic、不影响调用方。
	failing := &orangeLatencyTestRepo{upsertErr: errors.New("db down")}
	require.NotPanics(t, func() { OrangeRecordUsageLatency(ctx, failing, newSampledLog()) })
	require.Empty(t, failing.upserts)
}

func TestUsageServiceOrangeUsageLatencyForLogs(t *testing.T) {
	repo := &orangeLatencyTestRepo{upserts: map[string]OrangeUsageLatencyRecord{
		"local:req-1": {GatewayMs: 30, UpstreamMs: 270},
	}}
	svc := NewUsageService(repo, nil, nil, nil)

	latencies := svc.OrangeUsageLatencyForLogs(context.Background(), []UsageLog{
		{RequestID: "local:req-1"},
		{RequestID: "local:req-1"}, // 去重
		{RequestID: ""},            // 空 id 跳过
		{RequestID: strings.Repeat("x", orangeUsageLatencyMaxRequestIDBytes+1)},
		{RequestID: "local:unknown"},
	})
	require.Equal(t, map[string]OrangeUsageLatencyRecord{
		"local:req-1": {GatewayMs: 30, UpstreamMs: 270},
	}, latencies)
	require.Len(t, repo.loadCalls, 1)
	require.Equal(t, []string{"local:req-1", "local:unknown"}, repo.loadCalls[0])

	// 空列表不发查询；读库失败回落 nil（字段保持 null）。
	require.Nil(t, svc.OrangeUsageLatencyForLogs(context.Background(), nil))
	require.Len(t, repo.loadCalls, 1)

	failing := &orangeLatencyTestRepo{loadErr: errors.New("db down")}
	require.Nil(t, NewUsageService(failing, nil, nil, nil).OrangeUsageLatencyForLogs(
		context.Background(), []UsageLog{{RequestID: "local:req-1"}}))

	// 仓储不具备该能力（未注入 / 上游 stub）：不再查询，返回 nil。
	require.Nil(t, NewUsageService(nil, nil, nil, nil).OrangeUsageLatencyForLogs(
		context.Background(), []UsageLog{{RequestID: "local:req-1"}}))
}
