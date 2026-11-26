// Orange 特有：API Key 分组优先级链（密钥级兜底分组 + 聚合分组）的用户接口。
//
// 端点（挂在既有 *APIKeyHandler 上，同 package，无需改 api_key_handler.go、无需改 wire）：
//
//	GET  /api/v1/keys/:id/group-chain        读取链（纯读，绝不写库；返回全部成员 + state）
//	PUT  /api/v1/keys/:id/group-chain        设置链（group_ids[0] 成为新的主分组）
//	POST /api/v1/keys/:id/group-chain/heal   显式自愈（唯一的「读后写库」入口）
//
// 数据落在自建表 orange_api_key_group_chain；对上游表只读，唯一例外是把链首写回
// api_keys.group_id（上游既有语义列，表示"这个 Key 属于哪个分组"）。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// OrangeAPIKeyGroupChainRequest 是 PUT /keys/:id/group-chain 的请求体。
// group_ids 为有序分组 ID 列表：group_ids[0] 成为新的主分组（链首），[] = 清除链与模式。
// mode 是可选的展示模式："fallback"（兜底分组）或 "aggregate"（聚合密钥）；
// 缺省时按 group_ids 长度推断：<=2 个 → fallback，>=3 个 → aggregate。模式不影响选号逻辑。
type OrangeAPIKeyGroupChainRequest struct {
	GroupIDs []int64 `json:"group_ids"`
	Mode     string  `json:"mode"`
	// ExpectedGroupIDs 是客户端当前看到的完整成员集合（可选）：服务端据此算出
	// 「本次保存将移除哪些分组」，未显式确认则返回 409 ORANGE_GROUP_CHAIN_PRUNE_CONFIRM。
	// 不提供时按**服务端当前集合**计算（契约 C3：旧客户端同样不允许静默裁剪）。
	ExpectedGroupIDs []int64 `json:"expected_group_ids"`
	// ConfirmPrune 表示用户已确认「本次保存会移除分组」。
	ConfirmPrune bool `json:"confirm_prune"`
}

// orangeWriteGroupChainError 把服务层错误写成 HTTP 响应；返回 true 表示已写出、调用方直接 return。
//
// 只特判契约 C1/M2 的删除保护（409）：带结构化清单时才需要绕开 response.ErrorFrom
// （它只能用 *ApplicationError 的 message），其余 400/404 一律保持原样。
func orangeWriteGroupChainError(c *gin.Context, err error) bool {
	var pruneErr *service.OrangeGroupChainPruneError
	if !errors.As(err, &pruneErr) || pruneErr == nil {
		return false
	}
	response.ErrorWithDetails(
		c,
		http.StatusConflict,
		pruneErr.Message(),
		service.OrangeGroupChainPruneReason,
		map[string]string{"pending_removals": pruneErr.PendingRemovalsJSON()},
	)
	return true
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

	view, err := h.apiKeyService.OrangeSetAPIKeyGroupChain(c.Request.Context(), subject.UserID, keyID, service.OrangeSetGroupChainInput{
		GroupIDs:         req.GroupIDs,
		Mode:             req.Mode,
		ExpectedGroupIDs: req.ExpectedGroupIDs,
		ConfirmPrune:     req.ConfirmPrune,
	})
	if err != nil {
		// 删除保护（409）带结构化清单 pending_removals，必须绕过通用错误映射。
		if orangeWriteGroupChainError(c, err) {
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.OrangeAPIKeyGroupChainFromService(view).Localized(c.GetHeader("Accept-Language")))
}

// HealGroupChain 显式清理分组链里已失效的成员（失去绑定权限 / 分组已删除）
// POST /api/v1/keys/:id/group-chain/heal
//
// 自愈（写库）只在这里触发：GET 是纯读，避免"打开弹窗就改库"的副作用。
// 停用（disabled）与平台/订阅类型不一致（scope_mismatch）的成员不会被修剪。
func (h *APIKeyHandler) HealGroupChain(c *gin.Context) {
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

	view, err := h.apiKeyService.OrangeHealAPIKeyGroupChain(c.Request.Context(), subject.UserID, keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.OrangeAPIKeyGroupChainFromService(view).Localized(c.GetHeader("Accept-Language")))
}
