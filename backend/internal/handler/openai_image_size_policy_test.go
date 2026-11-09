//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func imagePolicySizeAccount(id, groupID int64, model, tier string) service.Account {
	account := newImageChainAccount(id, groupID)
	account.Credentials["model_mapping"] = map[string]any{model: model}
	account.Extra = map[string]any{service.AccountExtraOpenAIImageSizeTiers: []any{tier}}
	return account
}

func TestOpenAIImageSizePolicyUsesEachCandidateMapping(t *testing.T) {
	first := newImageChainGroup(95101, true)
	second := newImageChainGroup(95102, true)
	policy := service.ImageGenerationGroupPolicy{
		Groups: []*service.Group{first, second},
		Mappings: map[int64]service.ChannelMappingResult{
			first.ID:  {MappedModel: "image-first"},
			second.ID: {MappedModel: "image-second"},
		},
	}
	h, repo, _ := newImageChainHandler(t, nil)
	firstAccount := imagePolicySizeAccount(1, first.ID, "image-first", "1K")
	secondAccount := imagePolicySizeAccount(2, second.ID, "image-second", "1K")
	repo.availabilityByGroup = map[int64][]service.Account{
		first.ID: {firstAccount}, second.ID: {secondAccount},
	}
	tier, unsupported := h.openAIImageSizeTierUnsupported(context.Background(), &first.ID,
		"public-image", "3840x2160", "", service.OpenAIImagesCapabilityNative, policy, true)
	require.True(t, unsupported, "both independently mapped candidate groups lack 4K")
	require.Equal(t, "4K", tier)

	secondAccount.Extra[service.AccountExtraOpenAIImageSizeTiers] = []any{"4K"}
	resetAt := time.Now().Add(time.Hour)
	secondAccount.RateLimitResetAt = &resetAt
	repo.availabilityByGroup[second.ID] = []service.Account{secondAccount}
	_, unsupported = h.openAIImageSizeTierUnsupported(context.Background(), &first.ID,
		"public-image", "3840x2160", "", service.OpenAIImagesCapabilityNative, policy, true)
	require.False(t, unsupported, "a temporarily limited 4K candidate must keep a capacity error")

	delete(repo.availabilityByGroup, second.ID)
	_, unsupported = h.openAIImageSizeTierUnsupported(context.Background(), &first.ID,
		"public-image", "3840x2160", "", service.OpenAIImagesCapabilityNative, policy, true)
	require.False(t, unsupported, "a group without matching accounts cannot confirm a size mismatch")
}

func TestOpenAIImageSizePolicyClassifiesEntireChainAtBothEndpoints(t *testing.T) {
	for _, endpoint := range []string{"images", "responses"} {
		for _, fallbackSupportsSize := range []bool{false, true} {
			name := endpoint + "/all_groups_lack_size"
			if fallbackSupportsSize {
				name = endpoint + "/fallback_size_temporarily_unavailable"
			}
			t.Run(name, func(t *testing.T) {
				first := newImageChainGroup(95201, true)
				second := newImageChainGroup(95202, true)
				model := "gpt-image-2"
				path := "/v1/images/generations"
				body := []byte(`{"model":"gpt-image-2","prompt":"cat","size":"3840x2160"}`)
				if endpoint == "responses" {
					model = "gpt-5.4"
					path = "/v1/responses"
					body = []byte(`{"model":"gpt-5.4","stream":false,"input":"cat","tools":[{"type":"image_generation","size":"3840x2160"}]}`)
				}
				firstAccount := imagePolicySizeAccount(1, first.ID, model, "1K")
				secondAccount := imagePolicySizeAccount(2, second.ID, model, "1K")
				if fallbackSupportsSize {
					secondAccount.Extra[service.AccountExtraOpenAIImageSizeTiers] = []any{"4K"}
					resetAt := time.Now().Add(time.Hour)
					secondAccount.RateLimitResetAt = &resetAt
				}
				h, repo, upstream := newImageChainHandler(t, []service.Account{firstAccount, secondAccount})
				repo.availabilityByGroup = map[int64][]service.Account{
					first.ID: {firstAccount}, second.ID: {secondAccount},
				}
				c, rec := newImageChainContext(t, http.MethodPost, path, body, newImageChainKey(first, second))
				if endpoint == "responses" {
					h.Responses(c)
				} else {
					h.Images(c)
				}
				wantStatus := http.StatusBadRequest
				if fallbackSupportsSize {
					wantStatus = http.StatusServiceUnavailable
				}
				require.Equal(t, wantStatus, rec.Code, rec.Body.String())
				require.Zero(t, upstream.callCount())
			})
		}
	}
}
