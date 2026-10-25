package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 回归 2026-10-25 治本方案（文档/方案/2026-10-25-上游SSE响应头错标致0计费治本方案.md）：
// new-api 系上游（Niko nikoapi.xyz 的 cmb- 渠道）对 stream:false 请求返回
// Content-Type: text/event-stream 但 body 是纯 JSON。非流式处理路径只看 CT 就
// 路由进 SSE 解析会导致 usage 全丢、按量计费记 0。修复后由实际报文形态
// （bodyHasSSEFraming）决定解析路径，CT 错标的 JSON 走 JSON 路径正常计费。

func newCTMislabelTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

// 用例 1：CT 错标 event-stream + 纯 JSON 带 usage（Niko cmb- 渠道实录形态）。
// 必须按 JSON 解析 usage（含 cached_tokens 拆分）、body 原样透传、下游响应头
// 强制回 application/json（cfg 零值时 ResponseHeaders.Enabled=false，尾部会
// 采纳上游 CT，此用例同时覆盖该采纳路径的 event-stream 排除）。
func TestHandleNonStreamingResponse_EventStreamCTWithJSONBodyParsesUsage(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, rec := newCTMislabelTestContext(t)
	body := `{"id":"resp_cmb_1","object":"response","status":"completed","output":[{"type":"message","status":"completed"}],"usage":{"input_tokens":47,"input_tokens_details":{"cached_tokens":15},"output_tokens":9,"total_tokens":56}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 73, Type: AccountTypeAPIKey}, "gpt-5.5", "gpt-5.5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 47, result.usage.InputTokens)
	require.Equal(t, 9, result.usage.OutputTokens)
	require.Equal(t, 15, result.usage.CacheReadInputTokens)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	// JSON 路径既有行为会给响应注入 created_at（ensureOpenAIResponseObjectCreatedAt），
	// 其余内容必须原样透传（不被 SSE 解析破坏）。
	require.Equal(t, "resp_cmb_1", gjson.Get(rec.Body.String(), "id").String())
	require.Equal(t, 47, int(gjson.Get(rec.Body.String(), "usage.input_tokens").Int()))
	require.Equal(t, 15, int(gjson.Get(rec.Body.String(), "usage.input_tokens_details.cached_tokens").Int()))
	require.Equal(t, 9, int(gjson.Get(rec.Body.String(), "usage.output_tokens").Int()))
	require.Equal(t, "completed", gjson.Get(rec.Body.String(), "output.0.status").String())
}

// 用例 2：CT 错标 event-stream + 合法 JSON 无 usage。请求实际成功，必须保持
// 旧行为的可用性（零用量成功透传，不再进 SSE 解析），且下游拿到 JSON 头。
func TestHandleNonStreamingResponse_EventStreamCTWithUsagelessJSONPassesThroughZeroUsage(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, rec := newCTMislabelTestContext(t)
	body := `{"id":"resp_cmb_2","object":"response","status":"completed","output":[]}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 73, Type: AccountTypeAPIKey}, "gpt-5.5", "gpt-5.5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Zero(t, result.usage.InputTokens)
	require.Zero(t, result.usage.OutputTokens)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, "resp_cmb_2", gjson.Get(rec.Body.String(), "id").String())
	require.Equal(t, "completed", gjson.Get(rec.Body.String(), "status").String())
}

// 用例 2b：CT 错标 + 非 JSON 非 SSE 垃圾体不再静默 200，与 JSON 路径对垃圾体
// 的既有语义一致（报错，交给上层 failover）。
func TestHandleNonStreamingResponse_EventStreamCTWithGarbageBodyErrors(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, _ := newCTMislabelTestContext(t)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("(upstream garbage)")),
	}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 73, Type: AccountTypeAPIKey}, "gpt-5.5", "gpt-5.5")

	require.Error(t, err)
	require.Nil(t, result)
	require.False(t, c.Writer.Written(), "garbage body must not be committed before failover")
}

// 用例 3（防回归）：CT 正确 event-stream + 真 SSE 帧，仍走 SSE→JSON 提取路径。
func TestHandleNonStreamingResponse_EventStreamCTWithRealSSEStillParsesSSE(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, rec := newCTMislabelTestContext(t)
	upstreamSSE := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_sse_ok","object":"response","status":"completed","output":[],"usage":{"input_tokens":31,"output_tokens":5,"total_tokens":36}}}`,
		``,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1, Type: AccountTypeAPIKey}, "gpt-5.5", "gpt-5.5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 31, result.usage.InputTokens)
	require.Equal(t, 5, result.usage.OutputTokens)
	// SSE→JSON 提取成功后下游收到 JSON 文档。
	require.Equal(t, "resp_sse_ok", gjson.Get(rec.Body.String(), "id").String())
}

// 用例 4（防回归）：CT=application/json + 真 SSE（APIKey 账号），usage 提取
// 失败后由 bodyLooksLikeSSE 兜底分支救回 SSE 解析。
func TestHandleNonStreamingResponse_JSONCTWithRealSSEStillFallsBackToSSE(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, rec := newCTMislabelTestContext(t)
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_sse_fb","object":"response","status":"completed","output":[],"usage":{"input_tokens":21,"output_tokens":4,"total_tokens":25}}}`,
		``,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1, Type: AccountTypeAPIKey}, "gpt-5.5", "gpt-5.5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 21, result.usage.InputTokens)
	require.Equal(t, 4, result.usage.OutputTokens)
	require.Equal(t, "resp_sse_fb", gjson.Get(rec.Body.String(), "id").String())
}

// 用例 5a：passthrough 路径同款——CT 错标 + JSON 带 usage，正常解析计费，
// 下游响应头强制回 application/json。
func TestHandleNonStreamingResponsePassthrough_EventStreamCTWithJSONBodyParsesUsage(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, rec := newCTMislabelTestContext(t)
	body := `{"id":"resp_cmb_pt","object":"response","status":"completed","output":[],"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":4},"output_tokens":6,"total_tokens":18}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	result, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, &Account{ID: 91, Type: AccountTypeOAuth}, "gpt-5.5", "gpt-5.5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 12, result.usage.InputTokens)
	require.Equal(t, 6, result.usage.OutputTokens)
	require.Equal(t, 4, result.usage.CacheReadInputTokens)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, body, rec.Body.String())
}

// 用例 5b：passthrough 路径防回归——CT 正确 + 真 SSE 仍走 SSE→JSON 提取。
func TestHandleNonStreamingResponsePassthrough_EventStreamCTWithRealSSEStillParsesSSE(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, rec := newCTMislabelTestContext(t)
	upstreamSSE := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_sse_pt","object":"response","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}}`,
		``,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, &Account{ID: 91, Type: AccountTypeOAuth}, "gpt-5.5", "gpt-5.5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 8, result.usage.InputTokens)
	require.Equal(t, 3, result.usage.OutputTokens)
	require.Equal(t, "resp_sse_pt", gjson.Get(rec.Body.String(), "id").String())
}
