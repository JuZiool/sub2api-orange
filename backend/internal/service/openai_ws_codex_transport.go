package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codextransport"
	codexws "github.com/Wei-Shaw/sub2api/internal/pkg/codexws"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	coderws "github.com/coder/websocket"
)

func (d *coderOpenAIWSClientDialer) dialStrictCodex(ctx context.Context, target string, headers http.Header, proxy string, binding codextransport.Binding) (openAIWSClientConn, int, http.Header, error) {
	if d == nil {
		return nil, 0, nil, &codextransport.Error{Reason: "WebSocket dialer is unavailable"}
	}
	if err := binding.Validate(); err != nil {
		return nil, 0, nil, err
	}
	if binding.Plane != "ws" {
		return nil, 0, nil, &codextransport.Error{Reason: "WebSocket request has a non-WS binding"}
	}
	targetURL, err := url.Parse(target)
	if err != nil || targetURL.Scheme != "wss" || targetURL.Hostname() == "" {
		return nil, 0, nil, &codextransport.Error{Reason: "verified WebSocket transport requires a valid WSS URL"}
	}
	if HTTPUpstreamPublicHostsOnly(ctx) {
		if err := urlvalidator.ValidateResolvedIP(targetURL.Hostname()); err != nil {
			return nil, 0, nil, &codextransport.Error{Reason: err.Error()}
		}
	}
	normalizedProxy, parsed, err := proxyurl.Parse(proxy)
	if err != nil {
		return nil, 0, nil, &codextransport.Error{Reason: "invalid selected WebSocket proxy"}
	}
	proxyDigest := sha256.Sum256([]byte(normalizedProxy))
	key := fmt.Sprintf("codex:%s:proxy:%x", binding.Key(), proxyDigest[:16])
	d.proxyMu.Lock()
	if d.proxyClients == nil {
		d.proxyClients = make(map[string]*openAIWSProxyClientEntry)
	}
	now := time.Now().UnixNano()
	d.cleanupProxyClientsLocked(now)
	entry := d.proxyClients[key]
	if entry == nil || entry.client == nil {
		if len(d.proxyClients) >= openAIWSProxyClientCacheMaxEntries {
			var oldestKey string
			var oldestEntry *openAIWSProxyClientEntry
			for candidateKey, candidate := range d.proxyClients {
				if candidate != nil && candidate.inFlight.Load() == 0 && (oldestEntry == nil || candidate.lastUsedUnixNano < oldestEntry.lastUsedUnixNano) {
					oldestKey, oldestEntry = candidateKey, candidate
				}
			}
			if oldestEntry == nil {
				d.proxyMu.Unlock()
				return nil, 0, nil, &codextransport.Error{Reason: "WebSocket transport cache is busy"}
			}
			closeOpenAIWSProxyClient(oldestEntry.client)
			delete(d.proxyClients, oldestKey)
		}
		rt, err := codextransport.New(binding, codextransport.Options{ProxyURL: parsed, MaxIdleConns: openAIWSProxyTransportMaxIdleConns,
			MaxIdleConnsPerHost: openAIWSProxyTransportMaxIdleConnsPerHost, IdleConnTimeout: openAIWSProxyTransportIdleConnTimeout})
		if err != nil {
			d.proxyMu.Unlock()
			return nil, 0, nil, err
		}
		entry = &openAIWSProxyClientEntry{client: &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}
		d.proxyClients[key] = entry
		d.proxyMisses.Add(1)
	} else {
		d.proxyHits.Add(1)
	}
	entry.lastUsedUnixNano = now
	entry.inFlight.Add(1)
	d.ensureProxyClientCapacityLocked()
	d.proxyMu.Unlock()
	finish := func() {
		d.proxyMu.Lock()
		entry.inFlight.Add(-1)
		entry.lastUsedUnixNano = time.Now().UnixNano()
		d.proxyMu.Unlock()
	}
	wrapped := &strictCodexWSConn{release: finish}
	conn, resp, err := codexws.Dial(ctx, target, &codexws.DialOptions{HTTPClient: entry.client, HTTPHeader: cloneHeader(headers),
		CompressionMode: codexws.CompressionContextTakeover, ClientMaxWindowBits: true,
		OnPingReceived: func(context.Context, []byte) bool { wrapped.upstreamPings.Add(1); return true }})
	status := 0
	responseHeaders := http.Header(nil)
	if resp != nil {
		status = resp.StatusCode
		responseHeaders = cloneHeader(resp.Header)
	}
	if err != nil {
		finish()
		var body []byte
		if resp != nil && resp.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			_ = resp.Body.Close()
		}
		if status == 0 && !codextransport.IsError(err) && !codextransport.IsFailure(err) {
			err = &codextransport.Failure{Err: err}
		}
		return nil, status, responseHeaders, &openAIWSHandshakeError{Body: body, Err: err}
	}
	conn.SetReadLimit(openAIWSMessageReadLimitBytes)
	wrapped.conn = conn
	return wrapped, 0, responseHeaders, nil
}

type strictCodexWSConn struct {
	conn          *codexws.Conn
	upstreamPings atomic.Int64
	release       func()
	releaseOnce   sync.Once
}

func (c *strictCodexWSConn) WriteJSON(ctx context.Context, value any) error {
	if c == nil || c.conn == nil {
		return strictCodexWSError(errOpenAIWSConnClosed)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return strictCodexWSError(c.conn.Write(ctx, codexws.MessageText, data))
}
func (c *strictCodexWSConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if c == nil || c.conn == nil {
		return nil, strictCodexWSError(errOpenAIWSConnClosed)
	}
	_, data, err := c.conn.Read(ctx)
	return data, strictCodexWSError(err)
}
func (c *strictCodexWSConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.conn == nil {
		return coderws.MessageText, nil, strictCodexWSError(errOpenAIWSConnClosed)
	}
	kind, data, err := c.conn.Read(ctx)
	return coderws.MessageType(kind), data, strictCodexWSError(err)
}
func (c *strictCodexWSConn) WriteFrame(ctx context.Context, kind coderws.MessageType, data []byte) error {
	if c == nil || c.conn == nil {
		return strictCodexWSError(errOpenAIWSConnClosed)
	}
	return strictCodexWSError(c.conn.Write(ctx, codexws.MessageType(kind), data))
}
func (c *strictCodexWSConn) Ping(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return strictCodexWSError(errOpenAIWSConnClosed)
	}
	return strictCodexWSError(c.conn.Ping(ctx))
}
func (c *strictCodexWSConn) Close() error {
	if c == nil {
		return nil
	}
	defer c.releaseTransport()
	if c.conn == nil {
		return nil
	}
	_ = c.conn.Close(codexws.StatusNormalClosure, "")
	_ = c.conn.CloseNow()
	return nil
}
func (c *strictCodexWSConn) CloseNow() error {
	if c == nil {
		return nil
	}
	defer c.releaseTransport()
	if c.conn == nil {
		return nil
	}
	return c.conn.CloseNow()
}
func (c *strictCodexWSConn) releaseTransport() {
	c.releaseOnce.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
}
func (c *strictCodexWSConn) RequiresReaderLoop() bool            { return true }
func (c *strictCodexWSConn) SupportsIdlePingWithoutReader() bool { return false }
func (c *strictCodexWSConn) UpstreamPingCount() int64 {
	if c == nil {
		return 0
	}
	return c.upstreamPings.Load()
}

type codexCompatibleCloseError struct {
	original error
	compat   coderws.CloseError
}

func (e *codexCompatibleCloseError) Error() string   { return e.original.Error() }
func (e *codexCompatibleCloseError) Unwrap() []error { return []error{e.original, e.compat} }
func strictCodexWSError(err error) error {
	if err == nil {
		return nil
	}
	if status := codexws.CloseStatus(err); status != -1 {
		err = &codexCompatibleCloseError{original: err, compat: coderws.CloseError{Code: coderws.StatusCode(status), Reason: err.Error()}}
	}
	return &codextransport.Failure{Err: err}
}
