//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// 生图聚合链 handler 测试桩
// ---------------------------------------------------------------------------

type imageChainAccountRepo struct {
	service.AccountRepository

	mu              sync.Mutex
	accountsByGroup map[int64][]service.Account
	visitedGroups   []int64
}

func (r *imageChainAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, accounts := range r.accountsByGroup {
		for i := range accounts {
			if accounts[i].ID == id {
				account := accounts[i]
				return &account, nil
			}
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func (r *imageChainAccountRepo) markVisit(groupID int64) {
	r.mu.Lock()
	r.visitedGroups = append(r.visitedGroups, groupID)
	r.mu.Unlock()
}

func (r *imageChainAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	r.markVisit(groupID)
	out := make([]service.Account, 0)
	for _, account := range r.accountsByGroup[groupID] {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r *imageChainAccountRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, groupID, service.PlatformOpenAI)
}

// simple 模式（RunModeSimple）只按平台列账号，不看分组；这里保持同样的可调度集合，
// 分组隔离由调度器的分组匹配与生图策略负责。
func (r *imageChainAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	r.mu.Lock()
	r.visitedGroups = append(r.visitedGroups, 0)
	r.mu.Unlock()
	out := make([]service.Account, 0)
	for _, accounts := range r.accountsByGroup {
		for _, account := range accounts {
			if account.Platform == platform {
				out = append(out, account)
			}
		}
	}
	return out, nil
}

func (r *imageChainAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, _ string) ([]service.Account, error) {
	return nil, nil
}

// ListModelAvailabilityCandidates 只服务“无可用账号”错误分类，不参与调度，
// 因此不记录访问序列（否则诊断查询会被误算成一次调度尝试）。
func (r *imageChainAccountRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, _ []string, _ bool) ([]service.Account, error) {
	return nil, nil
}

func (r *imageChainAccountRepo) visits() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.visitedGroups...)
}

// imageChainUserRepo 只提供余额查询所需的用户记录。
type imageChainUserRepo struct {
	service.UserRepository
	user service.User
}

func (r *imageChainUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	if id != r.user.ID {
		return nil, fmt.Errorf("user %d not found", id)
	}
	copied := r.user
	return &copied, nil
}

type imageChainUpstream struct {
	service.HTTPUpstream

	mu         sync.Mutex
	accountIDs []int64
	bodies     [][]byte
}

func (u *imageChainUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	var body []byte
	if req != nil && req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()
	payload := `{"created":1700000000,"data":[{"url":"https://example.test/img.png"}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_image_chain"},
		},
		Body: io.NopCloser(bytes.NewBufferString(payload)),
	}, nil
}

func (u *imageChainUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.accountIDs)
}

func (u *imageChainUpstream) lastAccount() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.accountIDs) == 0 {
		return 0
	}
	return u.accountIDs[len(u.accountIDs)-1]
}

func (u *imageChainUpstream) lastBody() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return nil
	}
	return append([]byte(nil), u.bodies[len(u.bodies)-1]...)
}

func newImageChainGroup(id int64, allowImage bool) *service.Group {
	return &service.Group{
		ID:                   id,
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		Hydrated:             true,
		RateMultiplier:       1,
		AllowImageGeneration: allowImage,
	}
}

func newImageChainAccount(id, groupID int64) service.Account {
	return service.Account{
		ID:            id,
		Name:          fmt.Sprintf("image-account-%d", id),
		Platform:      service.PlatformOpenAI,
		Type:          service.AccountTypeAPIKey,
		Status:        service.StatusActive,
		Schedulable:   true,
		Credentials:   map[string]any{"api_key": "sk-test"},
		AccountGroups: []service.AccountGroup{{GroupID: groupID}},
		GroupIDs:      []int64{groupID},
	}
}

func newImageChainKey(primary *service.Group, groups ...*service.Group) *service.APIKey {
	primaryID := primary.ID
	entries := []*service.APIKeyGroupEntry{{Group: primary, Priority: 1}}
	for i, group := range groups {
		entries = append(entries, &service.APIKeyGroupEntry{Group: group, Priority: i + 2})
	}
	return &service.APIKey{
		ID:      8001,
		UserID:  8002,
		GroupID: &primaryID,
		Group:   primary,
		User:    &service.User{ID: 8002, Status: service.StatusActive},
		Groups:  entries,
	}
}

func newImageChainHandler(t *testing.T, accounts []service.Account) (*OpenAIGatewayHandler, *imageChainAccountRepo, *imageChainUpstream) {
	t.Helper()
	byGroup := make(map[int64][]service.Account, len(accounts))
	for _, account := range accounts {
		for _, groupID := range account.GroupIDs {
			byGroup[groupID] = append(byGroup[groupID], account)
		}
	}
	repo := &imageChainAccountRepo{accountsByGroup: byGroup}
	upstream := &imageChainUpstream{}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	userRepo := &imageChainUserRepo{user: service.User{ID: 8002, Status: service.StatusActive, Balance: 100}}
	gatewayService := service.NewOpenAIGatewayService(
		repo,
		nil,
		nil,
		userRepo,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingCacheService := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	h := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(nil),
		billingCacheService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	h.maxAccountSwitches = 5
	return h, repo, upstream
}

func newImageChainContext(t *testing.T, method, path string, body []byte, apiKey *service.APIKey) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// 模拟认证中间件：安装请求级分组路由与分组上下文，生图 handler 依赖它们完成
	// 命中后组时的授权与计费归属切换。
	reqCtx := service.WithAPIKeyGroupFallbackRouting(req.Context(), apiKey)
	if apiKey.Group != nil {
		reqCtx = context.WithValue(reqCtx, ctxkey.Group, apiKey.Group)
	}
	c.Request = req.WithContext(reqCtx)
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: apiKey.UserID, Concurrency: 0})
	return c, rec
}

// A + I：generations 端点，首组禁用生图、后组允许且有账号 → 命中后组并真正转发。
func TestOpenAIImages_GenerationsRoutesToAllowedFallbackGroup(t *testing.T) {
	disabled := newImageChainGroup(8101, false)
	enabled := newImageChainGroup(8102, true)
	key := newImageChainKey(disabled, enabled)
	h, repo, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(1, enabled.ID)})

	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)

	h.Images(c)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s visits=%v", rec.Body.String(), repo.visits())
	require.Equal(t, 1, upstream.callCount(), "必须真正转发一次")
	require.Equal(t, int64(1), upstream.lastAccount())
	require.Equal(t, []int64{enabled.ID}, repo.visits(), "禁用生图的首组不得进入调度")
	require.Equal(t, enabled.ID, *key.GroupID, "请求级路由应指向实际命中分组")
}

// I：edits 端点走同一条策略。
func TestOpenAIImages_EditsRoutesToAllowedFallbackGroup(t *testing.T) {
	disabled := newImageChainGroup(8201, false)
	enabled := newImageChainGroup(8202, true)
	key := newImageChainKey(disabled, enabled)
	h, _, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(2, enabled.ID)})

	body := []byte(`{"model":"gpt-image-2","prompt":"edit a cat","images":[{"image_url":"https://example.test/in.png"}]}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/edits", body, key)

	h.Images(c)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, upstream.callCount())
	require.Equal(t, enabled.ID, *key.GroupID)
}

// C：全链禁用生图 → 403 权限拒绝，且不发生上游调用。
func TestOpenAIImages_AllGroupsDisabledRejectsWithoutUpstreamCall(t *testing.T) {
	groupA := newImageChainGroup(8301, false)
	groupB := newImageChainGroup(8302, false)
	key := newImageChainKey(groupA, groupB)
	h, repo, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(3, groupB.ID)})

	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)

	h.Images(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
	require.Zero(t, upstream.callCount(), "权限拒绝不得发生上游调用")
	require.Empty(t, repo.visits(), "权限拒绝不得进入调度")
	require.Equal(t, groupA.ID, *key.GroupID)
}

// D：允许生图但都无账号 → 容量错误，而不是权限错误。
func TestOpenAIImages_AllowedGroupsWithoutAccountsReportsCapacity(t *testing.T) {
	disabled := newImageChainGroup(8401, false)
	allowedA := newImageChainGroup(8402, true)
	allowedB := newImageChainGroup(8403, true)
	key := newImageChainKey(disabled, allowedA, allowedB)
	h, repo, upstream := newImageChainHandler(t, nil)

	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)

	h.Images(c)

	require.NotEqual(t, http.StatusForbidden, rec.Code, "容量不足不得报为权限拒绝: %s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
	require.Zero(t, upstream.callCount())
	// native 能力无账号时会回退 basic 再试一次，因此同一分组可能出现两次；
	// 关键是只尝试了允许生图的分组，且跳过了禁用生图的首组。
	visited := repo.visits()
	require.NotContains(t, visited, disabled.ID, "禁用生图的分组不得进入调度")
	require.Contains(t, visited, allowedA.ID)
	require.Contains(t, visited, allowedB.ID)
}

// E：模型只在后组白名单允许 → 只在后组执行。
func TestOpenAIImages_ModelAllowlistedOnlyInLaterGroup(t *testing.T) {
	primary := newImageChainGroup(8501, true)
	primary.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}
	fallback := newImageChainGroup(8502, true)
	fallback.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-image-2"}}
	key := newImageChainKey(primary, fallback)
	h, repo, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(4, fallback.ID)})

	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)

	h.Images(c)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, int64(4), upstream.lastAccount())
	require.Equal(t, []int64{fallback.ID}, repo.visits(), "首组白名单不允许该模型，不得在其内执行")
}

// F：后组开关开启但白名单禁止目标模型 → 不得在该组转发。
func TestOpenAIImages_AllowlistForbidsModelInAllowedGroup(t *testing.T) {
	primary := newImageChainGroup(8601, false)
	fallback := newImageChainGroup(8602, true)
	fallback.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}
	key := newImageChainKey(primary, fallback)
	h, repo, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(5, fallback.ID)})

	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)

	h.Images(c)

	require.Zero(t, upstream.callCount(), "白名单禁止的组不得转发")
	require.Empty(t, repo.visits())
	require.NotEqual(t, http.StatusOK, rec.Code)
}

// H：普通单组 Key 禁用生图仍拒绝。
func TestOpenAIImages_SingleDisabledGroupStillRejected(t *testing.T) {
	group := newImageChainGroup(8701, false)
	key := newImageChainKey(group)
	h, _, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(6, group.ID)})

	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)

	h.Images(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
	require.Zero(t, upstream.callCount())
}

// I：Responses 显式生图（原生 image_generation 工具）走同一策略；被动 namespace 不误伤。
func TestOpenAIResponses_ExplicitImageIntentRoutesToAllowedGroup(t *testing.T) {
	disabled := newImageChainGroup(8801, false)
	enabled := newImageChainGroup(8802, true)
	key := newImageChainKey(disabled, enabled)
	h, repo, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(7, enabled.ID)})

	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation","model":"gpt-image-2"}],"input":"draw a cat"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/responses", body, key)

	h.Responses(c)

	require.NotEqual(t, http.StatusForbidden, rec.Code, "显式生图不得被首组开关拒绝: %s", rec.Body.String())
	require.Equal(t, []int64{enabled.ID}, repo.visits())
	require.Positive(t, upstream.callCount())
}

func TestOpenAIResponses_PassiveImageNamespaceUnaffected(t *testing.T) {
	disabled := newImageChainGroup(8901, false)
	enabled := newImageChainGroup(8902, true)
	key := newImageChainKey(disabled, enabled)
	h, _, _ := newImageChainHandler(t, []service.Account{newImageChainAccount(8, enabled.ID)})

	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"tool_choice":"auto","input":"write code"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/responses", body, key)

	h.Responses(c)

	require.NotEqual(t, http.StatusForbidden, rec.Code, "被动 namespace 不得触发首组生图门禁: %s", rec.Body.String())
}

// G：命中后组时使用该分组的渠道映射（不是首组的映射）。
func TestOpenAIImages_UsesRoutedGroupChannelMapping(t *testing.T) {
	disabled := newImageChainGroup(9001, false)
	enabled := newImageChainGroup(9002, true)
	key := newImageChainKey(disabled, enabled)
	h, _, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(9, enabled.ID)})

	// 直接验证策略为每个候选分组预解析映射：未配置渠道时映射保持原模型，
	// 但键必须是候选分组 ID，命中后组时据此取用。
	policy, ok := service.ResolveImageGenerationGroupPolicy(key, "gpt-image-2",
		func(group *service.Group) (service.ChannelMappingResult, service.OpenAIImagesCapability) {
			return service.ChannelMappingResult{MappedModel: "gpt-image-2-" + fmt.Sprint(group.ID)}, ""
		})
	require.True(t, ok)
	require.Len(t, policy.Groups, 1)
	mapping, found := policy.MappingFor(enabled.ID)
	require.True(t, found)
	require.Equal(t, "gpt-image-2-"+fmt.Sprint(enabled.ID), mapping.MappedModel)

	body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
	c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)
	h.Images(c)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, upstream.callCount())
	// 上游收到的 body 必须是客户端模型名（未配置渠道映射时不改写）。
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody(), "model").String())
}

// J：并发请求互不污染分组路由。
func TestOpenAIImages_ConcurrentRequestsKeepIndependentRoutes(t *testing.T) {
	h, repo, upstream := newImageChainHandler(t, []service.Account{newImageChainAccount(10, 9102)})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 生产环境每次鉴权都会物化一份新的 Group 对象（认证快照恢复），
			// 因此并发请求之间不共享可变分组实例。
			disabled := newImageChainGroup(9101, false)
			enabled := newImageChainGroup(9102, true)
			key := newImageChainKey(disabled, enabled)
			body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
			c, rec := newImageChainContext(t, http.MethodPost, "/v1/images/generations", body, key)
			h.Images(c)
			require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
			require.Equal(t, enabled.ID, *key.GroupID)
		}()
	}
	wg.Wait()

	for _, groupID := range repo.visits() {
		require.Equal(t, int64(9102), groupID, "禁用生图的分组不得被并发请求选中")
	}
	require.Equal(t, 8, upstream.callCount())
}

// 保证测试里引用的 JSON 工具函数有效（避免未使用 import）。
var _ = json.Valid
