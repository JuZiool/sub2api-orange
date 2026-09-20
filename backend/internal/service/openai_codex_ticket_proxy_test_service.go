package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
)

const (
	codexTicketProxyTestURL           = "https://api64.ipify.org?format=json"
	codexTicketProxyTestTimeout       = 10 * time.Second
	codexTicketProxyTestMaxBody       = 4096
	codexTicketProxyTestMaxConcurrent = 2
)

var errCodexTicketProxyAuthentication = errors.New("proxy authentication failed")

// OpenAICodexTicketProxyTestResult deliberately contains no proxy URL, credentials,
// raw upstream errors, or response body. A probe never uses account credentials.
type OpenAICodexTicketProxyTestResult struct {
	ProxyIndex int    `json:"proxy_index"`
	Success    bool   `json:"success"`
	ExitIP     string `json:"exit_ip,omitempty"`
	LatencyMs  int64  `json:"latency_ms"`
	ErrorCode  string `json:"error_code,omitempty"`
}

// OpenAICodexTicketProxyTester is independent of harvesting and business traffic.
// Keep one instance per application to bound concurrent administrator probes.
type OpenAICodexTicketProxyTester struct {
	settings     *SettingService
	mu           sync.Mutex
	active       map[string]struct{}
	newTransport func(string) (http.RoundTripper, error)
}

func NewOpenAICodexTicketProxyTester(settings *SettingService) *OpenAICodexTicketProxyTester {
	return &OpenAICodexTicketProxyTester{settings: settings, active: make(map[string]struct{}), newTransport: newCodexTicketProxyTestTransport}
}

func (s *OpenAICodexTicketProxyTester) Test(ctx context.Context, index int) (result OpenAICodexTicketProxyTestResult) {
	result.ProxyIndex = index
	started := time.Now()
	defer func() { result.LatencyMs = time.Since(started).Milliseconds() }()
	if index < 1 || index > maxOpenAICodexTicketHarvestProxies {
		result.ErrorCode = "invalid_proxy_index"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, codexTicketProxyTestTimeout)
	defer cancel()
	if s == nil || s.settings == nil || s.settings.settingRepo == nil {
		result.ErrorCode = "settings_unavailable"
		return
	}
	// Read the persisted setting afresh. Never use stale cached credentials after
	// a failed settings read; an explicit empty setting must not restore YAML.
	raw, err := s.settings.settingRepo.GetValue(ctx, SettingKeyOpenAICodexTicketHarvestProxyURL)
	if errors.Is(err, ErrSettingNotFound) {
		raw = ""
		if s.settings.cfg != nil {
			raw = s.settings.cfg.Gateway.OpenAICodexTicket.HarvestProxyURL
		}
	} else if err != nil {
		result.ErrorCode = "settings_unavailable"
		return
	}
	pool, err := ParseOpenAICodexTicketHarvestProxyPool(raw)
	if err != nil {
		result.ErrorCode = "invalid_proxy"
		return
	}
	if len(pool) == 0 {
		result.ErrorCode = "proxy_not_configured"
		return
	}
	if index > len(pool) {
		result.ErrorCode = "invalid_proxy_index"
		return
	}
	proxy := pool[index-1]
	// Normalize only the admission identity (not ordering): socks5 and socks5h
	// aliases should not allow parallel tests of the same configured proxy.
	identity, parsed, err := proxyurl.Parse(proxy)
	if err != nil || parsed == nil {
		result.ErrorCode = "invalid_proxy"
		return
	}
	s.mu.Lock()
	_, duplicate := s.active[identity]
	if duplicate || len(s.active) >= codexTicketProxyTestMaxConcurrent {
		s.mu.Unlock()
		result.ErrorCode = "busy"
		return
	}
	s.active[identity] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, identity); s.mu.Unlock() }()
	transport, err := s.newTransport(proxy)
	if err != nil || transport == nil {
		result.ErrorCode = "invalid_proxy"
		return
	}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       codexTicketProxyTestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexTicketProxyTestURL, nil)
	if err != nil {
		result.ErrorCode = "invalid_response"
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Sub2API-Proxy-Test")
	resp, err := client.Do(req)
	if err != nil {
		result.ErrorCode = codexTicketProxyTestErrorCode(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		result.ErrorCode = "redirect_blocked"
		return
	}
	if resp.StatusCode == http.StatusProxyAuthRequired {
		result.ErrorCode = "proxy_auth"
		return
	}
	if resp.StatusCode != http.StatusOK {
		result.ErrorCode = "upstream_error"
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, codexTicketProxyTestMaxBody+1))
	if err != nil {
		result.ErrorCode = codexTicketProxyTestErrorCode(err)
		return
	}
	var payload struct {
		IP string `json:"ip"`
	}
	if len(body) > codexTicketProxyTestMaxBody || json.Unmarshal(body, &payload) != nil {
		result.ErrorCode = "invalid_response"
		return
	}
	address, err := netip.ParseAddr(strings.TrimSpace(payload.IP))
	if err != nil || address.Zone() != "" {
		result.ErrorCode = "invalid_response"
		return
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(address) {
		result.ErrorCode = "invalid_response"
		return
	}
	result.Success, result.ExitIP = true, address.String()
	return
}

func newCodexTicketProxyTestTransport(raw string) (http.RoundTripper, error) {
	_, parsed, err := proxyurl.Parse(raw)
	if err != nil || parsed == nil {
		return nil, errors.New("a valid proxy is required")
	}
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  codexTicketProxyTestTimeout,
		MaxResponseHeaderBytes: 8192,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxConnsPerHost:        1,
		OnProxyConnectResponse: func(_ context.Context, _ *url.URL, _ *http.Request, response *http.Response) error {
			if response.StatusCode == http.StatusProxyAuthRequired {
				return errCodexTicketProxyAuthentication
			}
			return nil
		},
	}
	if err := proxyutil.ConfigureTransportProxy(transport, parsed); err != nil {
		return nil, err
	}
	return transport, nil
}

func codexTicketProxyTestErrorCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, errCodexTicketProxyAuthentication) || strings.Contains(strings.ToLower(err.Error()), "username/password authentication failed") {
		return "proxy_auth"
	}
	return "proxy_error"
}
