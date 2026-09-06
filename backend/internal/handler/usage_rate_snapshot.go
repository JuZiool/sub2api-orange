package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func gatewayRateSnapshot(ctx context.Context, gateway *service.GatewayService, apiKey *service.APIKey, model string) *service.RateResolution {
	if gateway == nil || apiKey == nil {
		return nil
	}
	return gateway.ResolveRateResolution(ctx, apiKey.UserID, apiKey.Group, model)
}

func openAIRateSnapshot(ctx context.Context, gateway *service.OpenAIGatewayService, apiKey *service.APIKey, model string) *service.RateResolution {
	if gateway == nil || apiKey == nil {
		return nil
	}
	return gateway.ResolveRateResolution(ctx, apiKey.UserID, apiKey.Group, model)
}
