//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestPromptDefaultsAndCustomPayloads(t *testing.T) {
	for _, prompt := range []string{"", " \n\t", "  请原样回复：\"你好🙂\"\n"} {
		want := prompt
		if strings.TrimSpace(want) == "" {
			want = "hi"
		}
		claude, err := createTestPayload("claude-sonnet-4-6", prompt)
		require.NoError(t, err)
		for _, tc := range []struct {
			name string
			body any
			path string
		}{
			{"claude", claude, "messages.0.content.0.text"},
			{"responses", createOpenAITestPayload("gpt-5.4", true, prompt), "input.0.content.0.text"},
			{"chat", createOpenAIChatCompletionsTestPayload("gpt-5.4", prompt), "messages.0.content"},
			{"gemini", json.RawMessage(createGeminiTestPayload("gemini-2.5-flash", prompt)), "contents.0.parts.0.text"},
		} {
			t.Run(tc.name+"/"+prompt, func(t *testing.T) {
				raw, err := json.Marshal(tc.body)
				require.NoError(t, err)
				require.Equal(t, want, gjson.GetBytes(raw, tc.path).String())
			})
		}
	}
	// Quota probes and older callers omit the new optional argument.
	raw, err := json.Marshal(createOpenAITestPayload("gpt-5.4", true))
	require.NoError(t, err)
	require.Equal(t, "hi", gjson.GetBytes(raw, "input.0.content.0.text").String())
}

func TestAccountTestPromptValidation(t *testing.T) {
	require.NoError(t, validateAccountTextTestPrompt(strings.Repeat("🙂", 8192), "gpt-5.4", "default"))
	for _, prompt := range []string{strings.Repeat("🙂", 8193), string([]byte{0xff})} {
		require.Error(t, validateAccountTextTestPrompt(prompt, "gpt-5.4", "default"))
	}
	for _, mode := range []string{"compact", "image", "video", "tts", "stt", "realtime", "search"} {
		require.NoError(t, validateAccountTextTestPrompt(strings.Repeat("x", 8193), "gpt-5.4", mode))
	}
	require.NoError(t, validateAccountTextTestPrompt(strings.Repeat("x", 8193), "gpt-image-1", "default"))
	require.NoError(t, validateAccountTextTestPrompt(strings.Repeat("x", 8193), "grok-imagine", "default"))
	require.Error(t, validateAccountTextTestPrompt(strings.Repeat("x", 8193), "grok-imagine-image", "text"))
	require.Error(t, validateAccountTextTestPrompt(strings.Repeat("x", 8193), "custom-image-alias", "default"))
	c, rec := newTestContext()
	// Validation must finish before accessing the account repository or upstream.
	err := (&AccountTestService{}).TestAccountConnection(c, 1, "gpt-5.4", strings.Repeat("x", 8193), "default")
	require.Error(t, err)
	require.Contains(t, rec.Body.String(), "8192")
}

func TestAccountTestPromptAdaptiveFinalRequestsAndIsolation(t *testing.T) {
	account := adaptiveCNAccountTestAccount(990, PlatformDeepseek)
	for _, prompt := range []string{"  请回复你好🙂\n", ""} {
		svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse(), adaptiveCNAnthropicTestResponse(), adaptiveCNResponsesTestResponse())
		c, _ := newTestContext()
		require.NoError(t, svc.TestAccountConnection(c, account.ID, "deepseek-v4-pro", prompt, ""))
		require.Len(t, upstream.bodies, 3)
		for i, path := range []string{"messages.0.content", "messages.0.content.0.text", "input.0.content.0.text"} {
			require.Equal(t, resolveAccountTestPrompt(prompt), gjson.GetBytes(upstream.bodies[i], path).String())
		}
	}
}

func TestAccountTestPromptOpenAIOAuthFinalRequest(t *testing.T) {
	account := adaptiveCNAccountTestAccount(991, PlatformOpenAI)
	account.Type = AccountTypeOAuth
	account.Credentials = map[string]any{"access_token": "test-token"}
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNResponsesTestResponse())
	c, _ := newTestContext()
	prompt := "  请原样回复你好🙂\n"
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "gpt-5.4", prompt, "default"))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, prompt, gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Equal(t, "Bearer test-token", upstream.lastReq.Header.Get("Authorization"))
}

func TestAccountTestPromptAntigravityDefaultsAndCustom(t *testing.T) {
	svc := &AntigravityGatewayService{}
	for _, builder := range []struct {
		name  string
		model string
		build func(string, string, string) ([]byte, error)
	}{
		{"gemini", "gemini-2.5-flash", svc.buildGeminiTestRequestWithPrompt},
		{"claude", "claude-sonnet-4-6", func(projectID, model, prompt string) ([]byte, error) {
			return svc.buildClaudeTestRequest(projectID, model, prompt)
		}},
	} {
		for _, tc := range []struct {
			prompt string
			want   string
			limit  int64
		}{
			{"", ".", 1},
			{" \n", ".", 1},
			{"  中文\"🙂\"\n", "  中文\"🙂\"\n", 1024},
		} {
			raw, err := builder.build("test-project", builder.model, tc.prompt)
			require.NoError(t, err)
			want := tc.want
			if builder.name == "claude" {
				// The existing native Claude-to-Gemini transformer trims text.
				want = strings.TrimSpace(want)
			}
			require.Equal(t, want, gjson.GetBytes(raw, "request.contents.0.parts.0.text").String())
			require.Equal(t, tc.limit, gjson.GetBytes(raw, "request.generationConfig.maxOutputTokens").Int())
		}
	}
}

func TestAccountTestPromptGrokFinalRequest(t *testing.T) {
	account := adaptiveCNAccountTestAccount(992, PlatformGrok)
	account.Credentials = map[string]any{"api_key": "test-token"}
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNResponsesTestResponse())
	c, _ := newTestContext()
	prompt := "  请原样回复你好🙂\n"
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "grok-4.3", prompt, "text"))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, prompt, gjson.GetBytes(upstream.lastBody, "input").String())
}

func TestAccountTestPromptAntigravityFinalRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		prompt string
		image  bool
	}{
		{"gemini_default", "gemini-2.5-flash", "", false},
		{"gemini_custom", "gemini-2.5-flash", "  请回复你好🙂\n", false},
		{"claude_default", "claude-sonnet-4-6", "", false},
		{"claude_custom", "claude-sonnet-4-6", "  请回复你好🙂\n", false},
		{"image_default", "gemini-3.1-flash-image", "", true},
		{"image_custom", "gemini-3.1-flash-image", "画一只猫", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := adaptiveCNAccountTestAccount(993, PlatformAntigravity)
			account.Type = AccountTypeOAuth
			account.Credentials = map[string]any{
				"access_token":  "test-token",
				"project_id":    "test-project",
				"model_mapping": map[string]any{"probe-alias": tc.model},
			}
			parts := `{"text":"ok"}`
			if tc.image {
				parts += `,{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}`
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(`data: {"response":{"candidates":[{"content":{"parts":[` + parts + "]}}]}}\n\n")),
			}}}
			svc, _ := adaptiveCNAccountTestService(account)
			svc.antigravityGatewayService = &AntigravityGatewayService{
				tokenProvider: &AntigravityTokenProvider{},
				httpUpstream:  upstream,
			}
			c, rec := newTestContext()
			require.NoError(t, svc.TestAccountConnection(c, account.ID, "probe-alias", tc.prompt, "default"))
			require.Len(t, upstream.requestBodies, 1)
			raw := upstream.requestBodies[0]
			require.Equal(t, tc.model, gjson.GetBytes(raw, "model").String())
			wantPrompt, wantLimit := resolveAntigravityTestPrompt(tc.prompt)
			if tc.image {
				wantPrompt = tc.prompt
				if wantPrompt == "" {
					wantPrompt = defaultGeminiImageTestPrompt
				}
				require.Equal(t, `["TEXT","IMAGE"]`, gjson.GetBytes(raw, "request.generationConfig.responseModalities").Raw)
				require.Contains(t, rec.Body.String(), `"type":"image"`)
				require.Contains(t, rec.Body.String(), "data:image/png;base64,aW1hZ2U=")
			} else {
				require.EqualValues(t, wantLimit, gjson.GetBytes(raw, "request.generationConfig.maxOutputTokens").Int())
				if strings.HasPrefix(tc.model, "claude-") {
					wantPrompt = strings.TrimSpace(wantPrompt)
				}
			}
			require.Equal(t, wantPrompt, gjson.GetBytes(raw, "request.contents.0.parts.0.text").String())
			require.Contains(t, rec.Body.String(), `"type":"test_complete","success":true`)
		})
	}
}
