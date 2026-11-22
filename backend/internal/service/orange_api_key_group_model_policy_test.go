// Orange 特有：分组链模型策略的单元测试（链访问助手、按模型预选分组、目录合并）。
package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

func orangePolicyTestGroup(id int64, platform string) *Group {
	return &Group{
		ID:       id,
		Name:     "orange-test-group",
		Platform: platform,
		Status:   StatusActive,
		Hydrated: true,
	}
}

func orangePolicyTestAllowlist(models ...string) GroupModelAllowlist {
	return GroupModelAllowlist{Enabled: true, Models: models}
}

func orangePolicyTestAPIKey(groups ...*Group) *APIKey {
	entries := make([]*APIKeyGroupEntry, 0, len(groups))
	for i, group := range groups {
		entries = append(entries, &APIKeyGroupEntry{Group: group, Priority: i + 1})
	}
	headID := groups[0].ID
	return &APIKey{ID: 1, GroupID: &headID, Group: groups[0], Groups: entries}
}

func TestOrangeChainGroupsOrderDedupeAndActive(t *testing.T) {
	head := orangePolicyTestGroup(1, PlatformAnthropic)
	second := orangePolicyTestGroup(2, PlatformAnthropic)
	disabled := orangePolicyTestGroup(3, PlatformAnthropic)
	disabled.Status = StatusDisabled
	apiKey := orangePolicyTestAPIKey(head, second, disabled, second)

	groups := OrangeChainGroups(apiKey)
	if len(groups) != 2 {
		t.Fatalf("expected 2 usable chain groups, got %d", len(groups))
	}
	if groups[0].ID != head.ID || groups[1].ID != second.ID {
		t.Fatalf("expected priority order [1 2], got [%d %d]", groups[0].ID, groups[1].ID)
	}
	if got := OrangeChainGroups(nil); got != nil {
		t.Fatalf("nil api key should have no chain, got %v", got)
	}
	if got := OrangeChainGroups(&APIKey{Group: head}); got != nil {
		t.Fatalf("key without chain should return nil, got %v", got)
	}
}

func TestOrangeModelSupportedByGroupHonorsAllowlistAndImagePolicy(t *testing.T) {
	open := orangePolicyTestGroup(1, PlatformOpenAI)
	if !OrangeModelSupportedByGroup(open, "gpt-5.6", PlatformOpenAI) {
		t.Fatal("group without allowlist should support any model")
	}

	limited := orangePolicyTestGroup(2, PlatformOpenAI)
	limited.ModelAllowlist = orangePolicyTestAllowlist("gpt-6*")
	if !OrangeModelSupportedByGroup(limited, "gpt-6", PlatformOpenAI) {
		t.Fatal("allowlisted model should be supported")
	}
	if OrangeModelSupportedByGroup(limited, "gpt-5.6", PlatformOpenAI) {
		t.Fatal("model outside the allowlist must not be supported")
	}

	noImage := orangePolicyTestGroup(3, PlatformOpenAI)
	noImage.ModelAllowlist = orangePolicyTestAllowlist("gpt-image-*")
	if OrangeModelSupportedByGroup(noImage, "gpt-image-1", PlatformOpenAI) {
		t.Fatal("image model must be unsupported while the group disables image generation")
	}
	noImage.AllowImageGeneration = true
	if !OrangeModelSupportedByGroup(noImage, "gpt-image-1", PlatformOpenAI) {
		t.Fatal("image model should be supported once the group allows image generation")
	}

	gemini := orangePolicyTestGroup(4, PlatformGemini)
	gemini.ModelAllowlist = orangePolicyTestAllowlist("gemini-3-pro-image*")
	if OrangeModelSupportedByGroup(gemini, "gemini-3-pro-image-preview", PlatformGemini) {
		t.Fatal("gemini image model must be unsupported without image permission")
	}
	gemini.AllowImageGeneration = true
	if !OrangeModelSupportedByGroup(gemini, "gemini-3-pro-image-preview", PlatformGemini) {
		t.Fatal("gemini image model should be supported once the group allows image generation")
	}
}

func TestOrangeChainAllowsModelRequiresChain(t *testing.T) {
	head := orangePolicyTestGroup(1, PlatformAnthropic)
	head.ModelAllowlist = orangePolicyTestAllowlist("claude-opus-*")
	tail := orangePolicyTestGroup(2, PlatformAnthropic)
	tail.ModelAllowlist = orangePolicyTestAllowlist("claude-haiku-*")

	chained := orangePolicyTestAPIKey(head, tail)
	if !OrangeChainAllowsModel(chained, "claude-haiku-4") {
		t.Fatal("chain should allow a model supported by a later group")
	}
	if OrangeChainAllowsModel(chained, "claude-sonnet-4") {
		t.Fatal("chain must not allow a model no group supports")
	}

	plain := &APIKey{ID: 2, GroupID: &head.ID, Group: head}
	if OrangeChainAllowsModel(plain, "claude-haiku-4") {
		t.Fatal("key without chain must keep single-group semantics")
	}
}

func TestOrangeChainAllowsImageGeneration(t *testing.T) {
	head := orangePolicyTestGroup(1, PlatformOpenAI)
	tail := orangePolicyTestGroup(2, PlatformOpenAI)
	apiKey := orangePolicyTestAPIKey(head, tail)
	if OrangeChainAllowsImageGeneration(apiKey) {
		t.Fatal("chain without any image-enabled group must deny image generation")
	}
	tail.AllowImageGeneration = true
	if !OrangeChainAllowsImageGeneration(apiKey) {
		t.Fatal("chain with one image-enabled group must allow image generation")
	}

	plain := &APIKey{ID: 2, GroupID: &head.ID, Group: head}
	if OrangeChainAllowsImageGeneration(plain) {
		t.Fatal("single group without image permission must deny image generation")
	}
	if !OrangeChainAllowsImageGeneration(&APIKey{ID: 3}) {
		t.Fatal("ungrouped key keeps the upstream allow-all behavior")
	}
}

// orangePolicyTestRoutingCtx 构造一个带请求级分组链的上下文（与鉴权中间件一致）。
func orangePolicyTestRoutingCtx(apiKey *APIKey) (context.Context, *Group) {
	ctxGroup := *apiKey.Group
	ctx := context.WithValue(context.Background(), ctxkey.Group, &ctxGroup)
	return WithOrangeKeyGroupRouting(ctx, apiKey), &ctxGroup
}

func TestOrangePreselectChainGroupForModel(t *testing.T) {
	t.Run("A 与 B 都支持时仍从 A 开始", func(t *testing.T) {
		head := orangePolicyTestGroup(1, PlatformAnthropic)
		tail := orangePolicyTestGroup(2, PlatformAnthropic)
		apiKey := orangePolicyTestAPIKey(head, tail)
		ctx, ctxGroup := orangePolicyTestRoutingCtx(apiKey)

		nextCtx, groupID := orangePreselectChainGroupForModel(ctx, apiKey.GroupID, "claude-opus-4")
		if groupID == nil || *groupID != head.ID {
			t.Fatalf("expected to stay on head group %d, got %v", head.ID, groupID)
		}
		if apiKey.Group == nil || apiKey.Group.ID != head.ID {
			t.Fatalf("api key group must stay on head, got %+v", apiKey.Group)
		}
		if got, _ := nextCtx.Value(ctxkey.Group).(*Group); got == nil || got.ID != head.ID {
			t.Fatalf("context group must stay on head, got %+v", got)
		}
		if ctxGroup.ID != head.ID {
			t.Fatalf("context group pointer must not be rewritten, got %d", ctxGroup.ID)
		}
	})

	t.Run("当前分组白名单不支持时按链优先级预跳", func(t *testing.T) {
		head := orangePolicyTestGroup(1, PlatformAnthropic)
		head.ModelAllowlist = orangePolicyTestAllowlist("claude-opus-*")
		tail := orangePolicyTestGroup(2, PlatformAnthropic)
		tail.ModelAllowlist = orangePolicyTestAllowlist("claude-haiku-*")
		apiKey := orangePolicyTestAPIKey(head, tail)
		ctx, ctxGroup := orangePolicyTestRoutingCtx(apiKey)

		nextCtx, groupID := orangePreselectChainGroupForModel(ctx, apiKey.GroupID, "claude-haiku-4")
		if groupID == nil || *groupID != tail.ID {
			t.Fatalf("expected to preselect group %d, got %v", tail.ID, groupID)
		}
		if apiKey.Group.ID != tail.ID || apiKey.GroupID == nil || *apiKey.GroupID != tail.ID {
			t.Fatalf("request-level api key must point at the preselected group, got %+v", apiKey.Group)
		}
		if ctxGroup.ID != tail.ID {
			t.Fatalf("context group must be rewritten in place, got %d", ctxGroup.ID)
		}
		if got, _ := nextCtx.Value(ctxkey.Group).(*Group); got == nil || got.ID != tail.ID {
			t.Fatalf("returned context must expose the preselected group, got %+v", got)
		}
	})

	t.Run("生图模型预跳到允许生图的分组", func(t *testing.T) {
		head := orangePolicyTestGroup(1, PlatformOpenAI)
		tail := orangePolicyTestGroup(2, PlatformOpenAI)
		tail.AllowImageGeneration = true
		apiKey := orangePolicyTestAPIKey(head, tail)
		ctx, _ := orangePolicyTestRoutingCtx(apiKey)

		_, groupID := orangePreselectChainGroupForModel(ctx, apiKey.GroupID, "gpt-image-1")
		if groupID == nil || *groupID != tail.ID {
			t.Fatalf("expected image preselect to group %d, got %v", tail.ID, groupID)
		}
	})

	t.Run("没有分组支持时保持原分组", func(t *testing.T) {
		head := orangePolicyTestGroup(1, PlatformAnthropic)
		head.ModelAllowlist = orangePolicyTestAllowlist("claude-opus-*")
		tail := orangePolicyTestGroup(2, PlatformAnthropic)
		tail.ModelAllowlist = orangePolicyTestAllowlist("claude-haiku-*")
		apiKey := orangePolicyTestAPIKey(head, tail)
		ctx, _ := orangePolicyTestRoutingCtx(apiKey)

		_, groupID := orangePreselectChainGroupForModel(ctx, apiKey.GroupID, "claude-sonnet-4")
		if groupID == nil || *groupID != head.ID {
			t.Fatalf("expected to keep head group %d, got %v", head.ID, groupID)
		}
	})

	t.Run("普通 Key 不预选", func(t *testing.T) {
		head := orangePolicyTestGroup(1, PlatformAnthropic)
		head.ModelAllowlist = orangePolicyTestAllowlist("claude-opus-*")
		apiKey := &APIKey{ID: 2, GroupID: &head.ID, Group: head}
		ctx := context.Background()

		_, groupID := orangePreselectChainGroupForModel(ctx, apiKey.GroupID, "claude-haiku-4")
		if groupID == nil || *groupID != head.ID {
			t.Fatalf("plain key must not preselect, got %v", groupID)
		}
	})
}

func TestOrangeCodexModelIDsForGroupKeepsAllowedImageModels(t *testing.T) {
	group := orangePolicyTestGroup(1, PlatformOpenAI)
	modelIDs := []string{"gpt-5.6", "gpt-image-1", "custom-*"}

	filtered := OrangeCodexModelIDsForGroup(modelIDs, group)
	if !containsModelID(filtered, "gpt-5.6") {
		t.Fatalf("expected text model to survive, got %v", filtered)
	}
	if containsModelID(filtered, "gpt-image-1") {
		t.Fatalf("image model must stay hidden while the group disables image generation: %v", filtered)
	}
	if containsModelID(filtered, "custom-*") {
		t.Fatalf("wildcard mapping key must be filtered: %v", filtered)
	}

	group.AllowImageGeneration = true
	filtered = OrangeCodexModelIDsForGroup(modelIDs, group)
	if !containsModelID(filtered, "gpt-image-1") {
		t.Fatalf("image model must be listed once the group allows image generation: %v", filtered)
	}
}

func TestOrangeMergeAggregatedModelEntriesAppendsChainModels(t *testing.T) {
	head := orangePolicyTestGroup(10, PlatformOpenAI)
	tail := orangePolicyTestGroup(11, PlatformOpenAI)
	tail.AllowImageGeneration = true
	repo := splitCodexModelsAccountRepo{
		schedulable: map[int64][]Account{
			10: {{
				ID:       1,
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"base_url":      "https://head.example/v1",
					"model_mapping": map[string]any{"gpt-5.6": "gpt-5.6"},
				},
			}},
			11: {{
				ID:       2,
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"base_url":      "https://tail.example/v1",
					"model_mapping": map[string]any{"tail-alias": "gpt-6", "gpt-image-1": "gpt-image-1"},
				},
			}},
		},
	}
	svc := &OpenAIGatewayService{accountRepo: repo}
	apiKey := orangePolicyTestAPIKey(head, tail)

	listResponse := &OpenAIModelsResponse{Body: []byte(`{"object":"list","data":[{"id":"gpt-5.6","object":"model"}]}`)}
	if err := svc.OrangeMergeAggregatedModelEntries(context.Background(), OrangeChainGroups(apiKey), head.ID, listResponse, false, ""); err != nil {
		t.Fatalf("merge list catalogue: %v", err)
	}
	listIDs := orangeTestModelCatalogIDs(t, listResponse.Body, "data", "id")
	if !containsModelID(listIDs, "tail-alias") || !containsModelID(listIDs, "gpt-image-1") {
		t.Fatalf("expected later group models in the merged list, got %v", listIDs)
	}
	if orangeCountModelID(listIDs, "gpt-5.6") != 1 {
		t.Fatalf("expected merged list to keep a single gpt-5.6 entry, got %v", listIDs)
	}

	codexResponse := &OpenAIModelsResponse{Body: []byte(`{"models":[{"slug":"gpt-5.6"}]}`)}
	if err := svc.OrangeMergeAggregatedModelEntries(context.Background(), OrangeChainGroups(apiKey), head.ID, codexResponse, true, ""); err != nil {
		t.Fatalf("merge codex manifest: %v", err)
	}
	slugs := orangeTestModelCatalogIDs(t, codexResponse.Body, "models", "slug")
	if !containsModelID(slugs, "tail-alias") || !containsModelID(slugs, "gpt-image-1") {
		t.Fatalf("expected later group models in the merged Codex manifest, got %v", slugs)
	}

	// 普通 Key（无链）不得改动目录。
	plain := &APIKey{ID: 3, GroupID: &head.ID, Group: head}
	unchanged := &OpenAIModelsResponse{Body: []byte(`{"object":"list","data":[{"id":"gpt-5.6","object":"model"}]}`)}
	if err := svc.OrangeMergeAggregatedModelEntries(context.Background(), OrangeChainGroups(plain), head.ID, unchanged, false, ""); err != nil {
		t.Fatalf("merge without chain: %v", err)
	}
	if ids := orangeTestModelCatalogIDs(t, unchanged.Body, "data", "id"); len(ids) != 1 || ids[0] != "gpt-5.6" {
		t.Fatalf("catalogue without chain must stay unchanged, got %v", ids)
	}
}

func orangeTestModelCatalogIDs(t *testing.T, body []byte, field, idField string) []string {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode catalogue envelope: %v", err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(envelope[field], &entries); err != nil {
		t.Fatalf("decode catalogue %s: %v", field, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		var id string
		if err := json.Unmarshal(entry[idField], &id); err != nil {
			t.Fatalf("decode catalogue id: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func orangeCountModelID(ids []string, want string) int {
	count := 0
	for _, id := range ids {
		if id == want {
			count++
		}
	}
	return count
}
