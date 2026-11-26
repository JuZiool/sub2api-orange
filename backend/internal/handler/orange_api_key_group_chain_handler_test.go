// Orange 特有：分组链 HTTP 层的接入测试（契约 C1/M2）。
//
// 覆盖与前端共享的关键点：
//   - 保存会移除成员时必须是 409 + reason=ORANGE_GROUP_CHAIN_PRUNE_CONFIRM；
//   - message 是「保存将移除 N 个分组：名1、名2；确认请重试」；
//   - metadata.pending_removals 是结构化 JSON 字符串，且与 message 的名单/计数一致；
//   - 其它错误（400/404）不受影响，仍由 response.ErrorFrom 统一映射。
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// orangeGroupChainTestContext 构造一个只用于写响应的 gin 上下文。
func orangeGroupChainTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/keys/29/group-chain", nil)
	return ctx, recorder
}

// TestOrangeWriteGroupChainErrorPrune 覆盖 409 的完整载荷（契约 C1/M2）。
func TestOrangeWriteGroupChainErrorPrune(t *testing.T) {
	ctx, recorder := orangeGroupChainTestContext(t)

	err := &service.OrangeGroupChainPruneError{Removed: []service.OrangePrunedChainGroup{
		{ID: 16, Name: "自用圆模"},
		{ID: 17, Name: "临时停用"},
		{ID: 18, Name: "#18"},
	}}
	require.True(t, orangeWriteGroupChainError(ctx, err))
	require.Equal(t, http.StatusConflict, recorder.Code)

	var body response.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, http.StatusConflict, body.Code)
	require.Equal(t, service.OrangeGroupChainPruneReason, body.Reason)
	require.Equal(t, "ORANGE_GROUP_CHAIN_PRUNE_CONFIRM", body.Reason)
	require.Equal(t, "保存将移除 3 个分组：自用圆模、临时停用、#18；确认请重试", body.Message)

	// metadata.pending_removals 是 JSON **字符串**（不是数组），与 message 的名单/计数一致。
	raw, ok := body.Metadata["pending_removals"]
	require.True(t, ok, "metadata.pending_removals is required")
	var pending []struct {
		GroupID int64  `json:"group_id"`
		Name    string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &pending))
	require.Equal(t, []struct {
		GroupID int64  `json:"group_id"`
		Name    string `json:"name"`
	}{
		{GroupID: 16, Name: "自用圆模"},
		{GroupID: 17, Name: "临时停用"},
		{GroupID: 18, Name: "#18"},
	}, pending)
}

// 包了一层的 prune 错误同样要被识别（服务层用 fmt.Errorf 包装时不能漏成 500）。
func TestOrangeWriteGroupChainErrorRecognizesWrappedPrune(t *testing.T) {
	ctx, recorder := orangeGroupChainTestContext(t)

	wrapped := fmt.Errorf("set orange key group chain: %w",
		&service.OrangeGroupChainPruneError{Removed: []service.OrangePrunedChainGroup{{ID: 5, Name: ""}}})
	require.True(t, orangeWriteGroupChainError(ctx, wrapped))
	require.Equal(t, http.StatusConflict, recorder.Code)

	var body response.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	// 名为空时用 "#id" 兜底，名单个数与计数保持一致。
	require.Equal(t, "保存将移除 1 个分组：#5；确认请重试", body.Message)
	require.JSONEq(t, `[{"group_id":5,"name":"#5"}]`, body.Metadata["pending_removals"])
}

// 非 prune 错误一律不接管，交回 response.ErrorFrom（保持 400/404/500 的原语义）。
func TestOrangeWriteGroupChainErrorIgnoresOtherErrors(t *testing.T) {
	ctx, recorder := orangeGroupChainTestContext(t)

	require.False(t, orangeWriteGroupChainError(ctx, service.ErrOrangeGroupChainInvalid))
	require.False(t, orangeWriteGroupChainError(ctx, service.ErrGroupNotFound))
	require.False(t, orangeWriteGroupChainError(ctx, nil))
	// 没有写出任何响应体，调用方会继续走 response.ErrorFrom。
	require.Equal(t, 0, recorder.Body.Len())
}
