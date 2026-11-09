package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAIImageSizeTierUnsupported only reports a policy-wide size mismatch when
// every eligible group positively confirms it. A group with no matching model,
// missing mapping, or an unavailable query keeps the ordinary capacity error.
func (h *OpenAIGatewayHandler) openAIImageSizeTierUnsupported(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	size string,
	requiredCapability service.OpenAIEndpointCapability,
	requiredImageCapability service.OpenAIImagesCapability,
	policy service.ImageGenerationGroupPolicy,
	aggregated bool,
) (string, bool) {
	if !aggregated {
		return h.gatewayService.OpenAIImageSizeTierUnsupported(ctx, groupID, requestedModel, size, requiredCapability, requiredImageCapability)
	}
	tier := ""
	for _, group := range policy.Groups {
		if group == nil || !group.IsActive() {
			return "", false
		}
		mapping, ok := policy.MappingFor(group.ID)
		if !ok || mapping.MappedModel == "" {
			return "", false
		}
		candidateID := group.ID
		candidateTier, unsupported := h.gatewayService.OpenAIImageSizeTierUnsupported(
			ctx,
			&candidateID,
			mapping.MappedModel,
			size,
			requiredCapability,
			policy.CapabilityFor(group.ID, requiredImageCapability),
		)
		if !unsupported {
			return "", false
		}
		if tier != "" && candidateTier != tier {
			return "", false
		}
		tier = candidateTier
	}
	return tier, tier != ""
}
