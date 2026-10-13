package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// Orange 特有：GET /api/v1/admin/accounts/:id/subscription-grace。
func (h *AccountHandler) GetSubscriptionGrace(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	info, err := h.openaiOAuthService.GetAccountSubscriptionGrace(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, info)
}
