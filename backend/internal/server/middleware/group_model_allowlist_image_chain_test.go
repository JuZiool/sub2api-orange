package middleware

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/require"
)

// 聚合密钥：生图模型只在后组白名单中允许时，入口白名单中间件不得用首组白名单
// 提前拒绝；非生图模型仍按首组白名单拒绝。
func TestGroupModelAllowlistAggregateImageAdmissionForLaterGroupModel(t *testing.T) {
	primary := &service.Group{
		ID:                   7001,
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		Hydrated:             true,
		AllowImageGeneration: false,
		ModelAllowlist:       service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}},
	}
	fallback := &service.Group{
		ID:                   7002,
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ModelAllowlist:       service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-image-2"}},
	}
	primaryID := primary.ID
	apiKey := &service.APIKey{
		ID:      7003,
		GroupID: &primaryID,
		Group:   primary,
		Groups: []*service.APIKeyGroupEntry{
			{Group: primary, Priority: 1},
			{Group: fallback, Priority: 2},
		},
	}

	router, calls := newGroupModelAllowlistTestRouter(apiKey, "/v1")

	// 生图模型仅后组允许：入口放行，交由 handler 的请求级策略在合法分组内执行。
	image := doJSON(t, router, http.MethodPost, "/v1/images/edits", `{"model":"gpt-image-2"}`)
	require.Equal(t, http.StatusOK, image.Code, "仅后组允许的生图模型不得被首组白名单提前拒绝: %s", image.Body.String())
	require.Contains(t, *calls, "handler:/v1/images/edits")

	// 非生图入口不放宽首组白名单（模型本身不在首组白名单里）。
	text := doJSON(t, router, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4.1"}`)
	require.Equal(t, http.StatusNotFound, text.Code, "非生图入口不放宽首组白名单")
}

// 链上没有任何分组同时满足“允许生图 + 白名单允许该模型”时，入口仍然拒绝。
func TestGroupModelAllowlistAggregateImageAdmissionStaysBlockedWithoutCandidate(t *testing.T) {
	primary := &service.Group{
		ID:                   7101,
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ModelAllowlist:       service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}},
	}
	fallback := &service.Group{
		ID:                   7102,
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		Hydrated:             true,
		AllowImageGeneration: false,
		ModelAllowlist:       service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-image-2"}},
	}
	primaryID := primary.ID
	apiKey := &service.APIKey{
		ID:      7103,
		GroupID: &primaryID,
		Group:   primary,
		Groups: []*service.APIKeyGroupEntry{
			{Group: primary, Priority: 1},
			{Group: fallback, Priority: 2},
		},
	}

	router, _ := newGroupModelAllowlistTestRouter(apiKey, "/v1")
	w := doJSON(t, router, http.MethodPost, "/v1/images/edits", `{"model":"gpt-image-2"}`)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "not available for this group")
}

// 普通单分组密钥不受并集准入影响：禁用生图的分组白名单仍然只按主分组裁决。
func TestGroupModelAllowlistSingleGroupUnaffected(t *testing.T) {
	group := &service.Group{
		ID:                   7201,
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		Hydrated:             true,
		AllowImageGeneration: false,
		ModelAllowlist:       service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}},
	}
	groupID := group.ID
	apiKey := &service.APIKey{ID: 7202, GroupID: &groupID, Group: group}

	router, _ := newGroupModelAllowlistTestRouter(apiKey, "/v1")
	w := doJSON(t, router, http.MethodPost, "/v1/images/edits", `{"model":"gpt-image-2"}`)
	require.Equal(t, http.StatusNotFound, w.Code, "普通密钥保持原有首组白名单语义")
}
