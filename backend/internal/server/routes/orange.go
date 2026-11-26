package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterOrangeRoutes 注册 Orange 特有接口。
//
// Orange 特有：所有数据落在自建的 orange_* 表，不读写上游 settings / api_keys / groups 等表的私有状态；
// 对上游表只做只读查询。上游文件改动仅限 router.go 调用本函数这一行。
//
// 路由清单（见 文档/功能分析/2026-11-22-用量排行与系统设置功能开关恢复分析.md §7.4）：
//
//	GET  /api/v1/orange/features           公开开关快照（无需登录，仅布尔值）
//	GET  /api/v1/usage/ranking             登录用户用量排行（用户榜 / 模型榜）
//	GET  /api/v1/admin/orange/features     管理员读取开关真实值
//	PUT  /api/v1/admin/orange/features     管理员局部更新开关
//	GET  /api/v1/admin/orange/groups/profit                 管理员分组利润统计（今天/昨天/累计，只读 usage_logs）
//	PUT  /api/v1/admin/orange/groups/:id/upstream-rate       管理员更新分组上游成本倍率（只写 orange_group_upstream_rate）
func RegisterOrangeRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	jwtAuth middleware2.JWTAuthMiddleware,
	adminAuth middleware2.AdminAuthMiddleware,
	auditLog middleware2.AuditLogMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware2.PanelRateLimiter,
) {
	// ① 公开接口：只返回布尔开关（无敏感数据），供前端首屏 / 侧栏 / 路由守卫读取。
	// 走与上游其它公开接口一致的按 IP 限流。
	public := v1.Group("/orange")
	public.Use(panelRateLimiter.PublicIP())
	{
		public.GET("/features", h.Setting.OrangePublicFeatures)
	}

	// ② 已登录用户：守卫链与 routes/user.go 的 /usage 组同构
	// （jwt → BackendModeUserGuard → 面板全局限流 → 审计），聚合查询再叠加 Heavy 限流。
	authenticated := v1.Group("")
	authenticated.Use(gin.HandlerFunc(jwtAuth))
	authenticated.Use(middleware2.BackendModeUserGuard(settingService))
	authenticated.Use(panelRateLimiter.Global())
	authenticated.Use(gin.HandlerFunc(auditLog))
	{
		authenticated.GET("/usage/ranking", panelRateLimiter.Heavy(), h.Usage.OrangeRanking)
		// Orange 特有：分组链显式自愈（GET 已改为纯读，写库只在这里触发）。
		authenticated.POST("/keys/:id/group-chain/heal", h.APIKey.HealGroupChain)
	}

	// ③ 管理端：守卫链与 routes/admin.go（以及 routes/payment.go 的 /admin/payment 组）同构
	// （adminAuth → 面板全局限流 → 审计 → 管理员合规确认）。
	admin := v1.Group("/admin")
	admin.Use(gin.HandlerFunc(adminAuth))
	admin.Use(panelRateLimiter.Global())
	admin.Use(gin.HandlerFunc(auditLog))
	admin.Use(middleware2.AdminComplianceGuard(settingService))
	{
		admin.GET("/orange/features", h.Setting.OrangeAdminGetFeatures)
		admin.PUT("/orange/features", h.Setting.OrangeAdminUpdateFeatures)

		// Orange 特有：分组利润统计与上游成本倍率。
		// 静态段 /groups/profit 与参数段 /groups/:id/... 同时存在于同一节点，
		// gin 支持这种写法（上游 admin.go 的 /groups/all 与 /groups/:id 已是同例）。
		admin.GET("/orange/groups/profit", h.Setting.OrangeAdminGroupProfit)
		admin.PUT("/orange/groups/:id/upstream-rate", h.Setting.OrangeAdminUpdateGroupUpstreamRate)
	}
}
