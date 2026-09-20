package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fingerprintTransportProbe struct {
	profile     *tlsfingerprint.Profile
	concurrency int
}

func (p *fingerprintTransportProbe) Do(_ *http.Request, _ string, _ int64, concurrency int) (*http.Response, error) {
	p.concurrency = concurrency
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (p *fingerprintTransportProbe) DoWithTLS(r *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	p.profile = profile
	return p.Do(r, proxy, id, concurrency)
}

func TestCodexFingerprintTransportPreservesConfiguredConcurrency(t *testing.T) {
	for _, mode := range []string{"off", "single_machine_multi_window"} {
		for _, path := range []string{"gateway", "test"} {
			t.Run(mode+"/"+path, func(t *testing.T) {
				upstream := &fingerprintTransportProbe{}
				account := newTestOAuthAccount(95, map[string]any{codexFingerprintModeExtraKey: mode})
				account.Concurrency = 47
				req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
				var response *http.Response
				var err error
				if path == "gateway" {
					response, err = (&OpenAIGatewayService{httpUpstream: upstream}).doOpenAIUpstream(req, "", account)
				} else {
					response, err = (&AccountTestService{httpUpstream: upstream}).doOpenAIAccountTestUpstream(req, "", account, false)
				}
				require.NoError(t, err)
				defer response.Body.Close()
				require.Equal(t, 47, upstream.concurrency, "fingerprint must not impose a concurrency cap")
				if mode == "off" {
					require.Nil(t, upstream.profile)
				} else {
					require.Equal(t, resolveCodexMacTLSProfile(account), upstream.profile)
				}
			})
		}
	}
}

func TestCodexFingerprintAccountTestBodyHeaderParity(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	account := newTestOAuthAccount(96, map[string]any{codexFingerprintModeExtraKey: "single_machine_multi_window"})
	defer releaseStagedCodexFingerprintLease(c)
	payload := map[string]any{"model": "test", "input": "keep", "prompt_cache_key": "keep-cache"}
	applyCodexFingerprintTestPayload(c, account, payload)
	headers := make(http.Header)
	applyStagedCodexFingerprintHeaders(c, account, headers)
	ids := stagedCodexFingerprintIDs(c, account)
	require.NotNil(t, ids)
	require.Equal(t, ids.installationID, headers.Get("x-codex-installation-id"))
	metadata := payload["client_metadata"].(map[string]any)
	require.Equal(t, metadata["session_id"], headers.Get("session_id"))
	require.Equal(t, metadata["thread_id"], headers.Get("thread-id"))
	require.Equal(t, "keep-cache", payload["prompt_cache_key"])
	require.Equal(t, "keep", payload["input"])
	require.Same(t, ids, prepareCodexFingerprintTestIDs(c, account), "compatibility retries must retain identity")
	off := newTestOAuthAccount(97, map[string]any{codexFingerprintModeExtraKey: "off"})
	raw := []byte(`{"input":"keep", "prompt_cache_key":"original"}`)
	got, err := applyCodexFingerprintTestPayloadRaw(c, off, raw)
	require.NoError(t, err)
	require.Equal(t, raw, got)
}
