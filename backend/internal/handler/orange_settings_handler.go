// Orange 特有：Orange 功能开关的 HTTP 处理器。
//
// 端点：
//   - GET /api/v1/orange/features          公开开关快照（无需登录，仅布尔值）
//   - GET /api/v1/admin/orange/features    管理员读取开关真实值（含最近更新时间）
//   - PUT /api/v1/admin/orange/features    管理员局部更新开关（白名单键 + 布尔值校验）
//
// 设计说明：
//   - 为了零改动上游 setting_handler.go，三个方法以「同包扩展方法」形式加在既有 *SettingHandler 上；
//   - Orange 开关服务实例按 *service.SettingService 在包级注册表里复用（30s 缓存必须命中同一实例），
//     因此上游 SettingHandler 结构体不需要新增字段；
//   - 只读写 orange_settings 表，不写上游 settings 表，也不改上游设置项注册面。
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// orangeFeaturePatchMaxBytes 开关更新请求体上限（键值对象，正常不到 1KB）。
const orangeFeaturePatchMaxBytes = 4 << 10

// orangeSettingsServiceRegistry 按 SettingService 实例复用 Orange 开关服务。
var orangeSettingsServiceRegistry sync.Map // map[*service.SettingService]*service.OrangeSettingsService

// orangeAdminFeaturesResponse 管理端开关响应。
type orangeAdminFeaturesResponse struct {
	Items map[string]bool `json:"items"`
	// UpdatedAt 最近一次更新时间（RFC3339，按服务端时区）；无记录时为 null。
	UpdatedAt *string `json:"updated_at"`
}

// OrangePublicFeatures 公开开关快照（无需登录，仅布尔值）。
// GET /api/v1/orange/features
func (h *SettingHandler) OrangePublicFeatures(c *gin.Context) {
	// 读失败时 Snapshot 已回落默认值，服务层会在失败窗口起点打一次告警日志，这里不重复刷日志。
	snapshot, _ := h.orangeSettingsService().Snapshot(c.Request.Context())
	response.Success(c, snapshot)
}

// OrangeAdminGetFeatures 管理员读取开关真实值。
// GET /api/v1/admin/orange/features
func (h *SettingHandler) OrangeAdminGetFeatures(c *gin.Context) {
	settings := h.orangeSettingsService()
	ctx := c.Request.Context()

	snapshot, err := settings.Snapshot(ctx)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updatedAt, updateErr := settings.LastUpdatedAt(ctx)
	if updateErr != nil {
		// 更新时间仅用于展示，读失败不影响开关值。
		slog.Warn("orange features: read updated_at failed", "error", updateErr)
		updatedAt = nil
	}

	response.Success(c, orangeAdminFeaturesResponse{Items: snapshot, UpdatedAt: orangeFormatUpdatedAt(updatedAt)})
}

// OrangeAdminUpdateFeatures 管理员局部更新开关（白名单键 + 布尔值校验，写 updated_by）。
// PUT /api/v1/admin/orange/features
func (h *SettingHandler) OrangeAdminUpdateFeatures(c *gin.Context) {
	patch, err := parseOrangeFeaturePatch(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	settings := h.orangeSettingsService()
	ctx := c.Request.Context()

	snapshot, err := settings.Update(ctx, patch, orangeAdminUserID(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updatedAt, updateErr := settings.LastUpdatedAt(ctx)
	if updateErr != nil {
		slog.Warn("orange features: read updated_at failed after update", "error", updateErr)
		updatedAt = nil
	}

	response.Success(c, orangeAdminFeaturesResponse{Items: snapshot, UpdatedAt: orangeFormatUpdatedAt(updatedAt)})
}

// orangeSettingsService 取当前 handler 对应的 Orange 开关服务。
func (h *SettingHandler) orangeSettingsService() *service.OrangeSettingsService {
	if h == nil {
		return service.NewOrangeSettingsService(nil)
	}
	return orangeSettingsServiceFor(h.settingService)
}

// orangeSettingsServiceFor 返回与给定 SettingService 绑定的 Orange 开关服务单例。
// 进程内单实例才能让 30s 开关缓存与 singleflight 生效。
func orangeSettingsServiceFor(settingService *service.SettingService) *service.OrangeSettingsService {
	if settingService == nil {
		return service.NewOrangeSettingsService(nil)
	}
	if cached, ok := orangeSettingsServiceRegistry.Load(settingService); ok {
		if svc, ok := cached.(*service.OrangeSettingsService); ok && svc != nil {
			return svc
		}
	}
	created := service.NewOrangeSettingsService(settingService)
	if actual, loaded := orangeSettingsServiceRegistry.LoadOrStore(settingService, created); loaded {
		if svc, ok := actual.(*service.OrangeSettingsService); ok && svc != nil {
			return svc
		}
	}
	return created
}

// parseOrangeFeaturePatch 解析请求体：必须是 JSON 对象，值为布尔；
// 未知键由 service 层白名单校验拒绝（400）。
func parseOrangeFeaturePatch(c *gin.Context) (map[string]bool, error) {
	if c.Request == nil || c.Request.Body == nil {
		return nil, service.ErrOrangeSettingInvalidPayload
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, orangeFeaturePatchMaxBytes)).Decode(&raw); err != nil {
		return nil, service.ErrOrangeSettingInvalidPayload.WithCause(err)
	}
	if len(raw) == 0 {
		return nil, service.ErrOrangeSettingEmptyUpdate
	}

	patch := make(map[string]bool, len(raw))
	for key, value := range raw {
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			return nil, service.ErrOrangeSettingValueInvalid.WithMetadata(map[string]string{"key": key})
		}
		patch[key] = enabled
	}
	return patch, nil
}

// orangeAdminUserID 取当前管理员 users.id；拿不到时返回 0（仓库按 NULL 写入 updated_by）。
func orangeAdminUserID(c *gin.Context) int64 {
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		return subject.UserID
	}
	return 0
}

// orangeFormatUpdatedAt 把更新时间格式化为按服务端时区的 RFC3339 字符串；无值返回 nil。
func orangeFormatUpdatedAt(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.In(timezone.Location()).Format(time.RFC3339)
	return &formatted
}
