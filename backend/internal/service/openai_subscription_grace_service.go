package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Orange 特有：只读取订阅时间，不触发令牌刷新、隐私设置或数据库更新。
type OpenAISubscriptionGrace struct {
	ActiveUntil        string `json:"active_until"`
	BecameDelinquentAt string `json:"became_delinquent_timestamp"`
	GracePeriodEndsAt  string `json:"grace_period_end_timestamp"`
}

type chatGPTSubscriptionResponse struct {
	PlanType           string          `json:"plan_type"`
	ActiveUntil        string          `json:"active_until"`
	WillRenew          bool            `json:"will_renew"`
	ID                 string          `json:"id"`
	BecameDelinquentAt json.RawMessage `json:"became_delinquent_timestamp"`
	GracePeriodEndsAt  json.RawMessage `json:"grace_period_end_timestamp"`
}

func fetchChatGPTSubscription(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, accountID string) (*chatGPTSubscriptionResponse, error) {
	accountID = strings.TrimSpace(accountID)
	if strings.TrimSpace(accessToken) == "" || accountID == "" || clientFactory == nil {
		return nil, fmt.Errorf("subscription credentials or client unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := clientFactory(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("subscription client unavailable")
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetHeader("x-openai-target-path", "/backend-api/subscriptions").
		SetHeader("x-openai-target-route", "/backend-api/subscriptions").
		SetQueryParam("account_id", accountID).
		Get(chatGPTSubscriptionsURL)
	if err != nil {
		return nil, fmt.Errorf("subscription request or response decoding failed")
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("subscription request failed (HTTP %d)", resp.StatusCode)
	}
	var result chatGPTSubscriptionResponse
	body := strings.TrimSpace(resp.String())
	if !strings.HasPrefix(body, "{") {
		return nil, fmt.Errorf("subscription response is not a JSON object")
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, fmt.Errorf("subscription response decoding failed")
	}
	return &result, nil
}

func (s *OpenAIOAuthService) GetAccountSubscriptionGrace(ctx context.Context, account *Account) (*OpenAISubscriptionGrace, error) {
	if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsShadow() || !strings.EqualFold(strings.TrimSpace(account.GetCredential("plan_type")), "plus") {
		return nil, infraerrors.BadRequest("OPENAI_SUBSCRIPTION_INVALID_ACCOUNT", "subscription grace requires a Plus OAuth account")
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(account.GetCredential("subscription_expires_at")))
	if err != nil {
		return nil, infraerrors.BadRequest("OPENAI_SUBSCRIPTION_EXPIRY_MISSING", "subscription expiration is unavailable")
	}
	if expiresAt.After(time.Now()) {
		return &OpenAISubscriptionGrace{ActiveUntil: expiresAt.Format(time.RFC3339)}, nil
	}
	if s == nil || s.privacyClientFactory == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "OPENAI_SUBSCRIPTION_NOT_CONFIGURED", "subscription query is unavailable")
	}
	accessToken := account.GetCredential("access_token")
	accountID := strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	if accountID == "" {
		if claims, decodeErr := openai.DecodeIDToken(accessToken); decodeErr == nil {
			accountID = strings.TrimSpace(claims.GetUserInfo().ChatGPTAccountID)
		}
	}
	if accessToken == "" || accountID == "" {
		return nil, infraerrors.BadRequest("OPENAI_SUBSCRIPTION_CREDENTIALS_MISSING", "personal account credentials are unavailable")
	}
	var proxyURL string
	proxyID := account.ProxyID
	if proxyID == nil && len(account.ProxyIDs) > 0 {
		proxyID = &account.ProxyIDs[0]
	}
	if proxyID != nil {
		if s.proxyRepo == nil {
			return nil, infraerrors.New(http.StatusServiceUnavailable, "OPENAI_SUBSCRIPTION_PROXY_UNAVAILABLE", "account proxy is unavailable")
		}
		proxy, proxyErr := s.proxyRepo.GetByID(ctx, *proxyID)
		if proxyErr != nil || proxy == nil || !proxy.IsActive() || proxy.IsExpired(time.Now()) {
			return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_SUBSCRIPTION_PROXY_UNAVAILABLE", "account proxy is unavailable")
		}
		proxyURL = proxy.URL()
	}
	result, err := fetchChatGPTSubscription(ctx, s.privacyClientFactory, accessToken, proxyURL, accountID)
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_SUBSCRIPTION_QUERY_FAILED", err.Error())
	}
	info := &OpenAISubscriptionGrace{ActiveUntil: strings.TrimSpace(result.ActiveUntil)}
	if info.ActiveUntil != "" {
		if _, err := time.Parse(time.RFC3339, info.ActiveUntil); err != nil {
			return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_SUBSCRIPTION_INVALID_TIME", "subscription expiration is invalid")
		}
	}
	info.BecameDelinquentAt, err = parseChatGPTSubscriptionTimestamp(result.BecameDelinquentAt)
	if err == nil {
		info.GracePeriodEndsAt, err = parseChatGPTSubscriptionTimestamp(result.GracePeriodEndsAt)
	}
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_SUBSCRIPTION_INVALID_TIME", "subscription grace timestamp is invalid")
	}
	return info, nil
}

func parseChatGPTSubscriptionTimestamp(raw json.RawMessage) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return "", nil
	}
	if strings.HasPrefix(value, "\"") {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", nil
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed.UTC().Format(time.RFC3339), nil
		}
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return "", fmt.Errorf("invalid subscription timestamp")
	}
	if seconds == 0 {
		return "", nil
	}
	if seconds >= 1e12 {
		seconds /= 1000
	}
	if seconds > 253402300799 {
		return "", fmt.Errorf("subscription timestamp out of range")
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*1e9)).UTC().Format(time.RFC3339), nil
}
