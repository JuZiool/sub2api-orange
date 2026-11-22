// Orange 特有：API Key 分组优先级链（密钥级兜底分组 + 聚合分组）的用户接口。
//
// 端点（挂在既有 *APIKeyHandler 上，同 package，无需改 api_key_handler.go、无需改 wire）：
//
//	GET /api/v1/keys/:id/group-chain   读取链（仅 Key 拥有者）
//	PUT /api/v1/keys/:id/group-chain   设置链（group_ids[0] 成为新的主分组）
//
// 数据落在自建表 orange_api_key_group_chain；对上游表只读，唯一例外是把链首写回
// api_keys.group_id（上游既有语义列，表示"这个 Key 属于哪个分组"）。
package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// OrangeAPIKeyGroupChainRequest 是 PUT /keys/:id/group-chain 的请求体。
// group_ids 为有序分组 ID 列表：[] = 清除链（保留当前主分组）。
type OrangeAPIKeyGroupChainRequest struct {
	GroupIDs []int64 `json:"group_ids"`
}

// GetGroupChain 读取某个 API Key 的分组优先级链
// GET /api/v1/keys/:id/group-chain
func (h *APIKeyHandler) GetGroupChain(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	view, err := h.apiKeyService.OrangeGetAPIKeyGroupChain(c.Request.Context(), subject.UserID, keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.OrangeAPIKeyGroupChainFromService(view).Localized(c.GetHeader("Accept-Language")))
}

// SetGroupChain 设置某个 API Key 的分组优先级链
// PUT /api/v1/keys/:id/group-chain
func (h *APIKeyHandler) SetGroupChain(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	var req OrangeAPIKeyGroupChainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	view, err := h.apiKeyService.OrangeSetAPIKeyGroupChain(c.Request.Context(), subject.UserID, keyID, req.GroupIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.OrangeAPIKeyGroupChainFromService(view).Localized(c.GetHeader("Accept-Language")))
}
