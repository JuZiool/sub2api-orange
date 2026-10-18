package repository

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codextransport"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// doCodexRequest deliberately skips the ordinary H2 fallback and Grok replay
// wrappers. An uncertain generation must be returned to the caller unchanged.
func (s *httpUpstreamService) doCodexRequest(req *http.Request, proxyURL string, accountID int64, concurrency int, binding codextransport.Binding) (*http.Response, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if binding.Plane != "http" {
		return nil, &codextransport.Error{Reason: "HTTP request has a non-HTTP binding"}
	}
	if err := s.validateRequestHost(req); err != nil {
		return nil, &codextransport.Error{Reason: err.Error()}
	}
	entry, err := s.acquireCodexClient(proxyURL, accountID, concurrency, binding)
	if err != nil {
		return nil, err
	}
	finish := func() {
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
	}
	client := *s.httpClientForUpstreamRequest(entry.client, req)
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 0 && via[len(via)-1].Method != http.MethodGet && via[len(via)-1].Method != http.MethodHead {
			return http.ErrUseLastResponse
		}
		if checkRedirect != nil {
			return checkRedirect(next, via)
		}
		return s.redirectChecker(next, via)
	}
	wire := req.Clone(req.Context())
	wire.GetBody = nil // Also prohibit 307/308 replay by http.Client itself.
	resp, err := doUpstreamRequest(&client, wire)
	if err != nil {
		finish()
		if !codextransport.IsError(err) && !codextransport.IsFailure(err) {
			err = &codextransport.Failure{Err: err}
		}
		return nil, err
	}
	if resp.Body == nil {
		finish()
		return resp, nil
	}
	resp.Body = wrapTrackedBody(&codexDecodedResponseBody{ReadCloser: resp.Body}, finish)
	return resp, nil
}

// Decoding happens in doUpstreamRequest, so decoder failures need the same
// uncertain-outcome marker as raw socket read failures.
type codexDecodedResponseBody struct{ io.ReadCloser }

func (b *codexDecodedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF && !codextransport.IsFailure(err) {
		err = &codextransport.Failure{Err: err}
	}
	return n, err
}

func (s *httpUpstreamService) acquireCodexClient(proxyURL string, accountID int64, concurrency int, binding codextransport.Binding) (*upstreamClientEntry, error) {
	isolation := s.getIsolationMode()
	proxyKey, parsed, err := normalizeProxyURL(proxyURL)
	if err != nil {
		return nil, &codextransport.Error{Reason: "invalid selected proxy"}
	}
	settings := s.applyProfilePoolSettings(s.resolvePoolSettings(isolation, concurrency), service.HTTPUpstreamProfileOpenAI)
	// The selected proxy is always part of the key, even when the ordinary pool
	// isolation mode is account-only. Do not store its credentials in diagnostics.
	proxyDigest := sha256.Sum256([]byte(proxyKey))
	key := fmt.Sprintf("codex:%s:proxy:%x:%s", binding.Key(), proxyDigest[:16], buildCacheKey(isolation, "selected", accountID, "codex_verified"))
	poolKey := buildPoolKey(settings, "codex_verified") + ":" + binding.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if entry, ok := s.clients[key]; ok {
		if s.shouldReuseEntry(entry, isolation, proxyKey, poolKey) {
			atomic.StoreInt64(&entry.lastUsed, now.UnixNano())
			atomic.AddInt64(&entry.inFlight, 1)
			return entry, nil
		}
		s.removeClientLocked(key, entry)
	}
	if s.maxUpstreamClients() > 0 && len(s.clients) >= s.maxUpstreamClients() {
		s.evictIdleLocked(now)
		if len(s.clients) >= s.maxUpstreamClients() && !s.evictOldestIdleLocked() {
			return nil, &codextransport.Error{Reason: errUpstreamClientLimitReached.Error()}
		}
	}
	rt, err := codextransport.New(binding, codextransport.Options{ProxyURL: parsed, DialContext: newUpstreamDialer().DialContext,
		MaxIdleConns: settings.maxIdleConns, MaxIdleConnsPerHost: settings.maxIdleConnsPerHost, MaxConnsPerHost: settings.maxConnsPerHost,
		IdleConnTimeout: settings.idleConnTimeout, ResponseHeaderTimeout: settings.responseHeaderTimeout})
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: rt}
	if s.shouldValidateResolvedIP() {
		client.CheckRedirect = s.redirectChecker
	}
	entry := &upstreamClientEntry{client: client, proxyKey: proxyKey, poolKey: poolKey, protocolMode: "codex_verified", lastUsed: now.UnixNano(), inFlight: 1}
	s.clients[key] = entry
	return entry, nil
}
