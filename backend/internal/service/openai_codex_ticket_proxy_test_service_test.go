package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexProxyTestSettingRepo struct {
	SettingRepository
	raw string
	err error
}

func (r *codexProxyTestSettingRepo) GetValue(context.Context, string) (string, error) {
	return r.raw, r.err
}

type codexProxyTestRoundTripper func(*http.Request) (*http.Response, error)

func (f codexProxyTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func codexProxyTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func newCodexProxyTesterForTest(raw string) *OpenAICodexTicketProxyTester {
	return NewOpenAICodexTicketProxyTester(NewSettingService(&codexProxyTestSettingRepo{raw: raw}, &config.Config{}))
}

func TestCodexTicketProxyTestUsesSavedDeduplicatedPoolWithoutCredentials(t *testing.T) {
	first := "http://user:secret@first.example:8080"
	second := "socks5h://user:password@second.example:1080"
	tester := newCodexProxyTesterForTest("\n" + first + "\n" + first + "\n " + second + "\n")
	tester.newTransport = func(proxy string) (http.RoundTripper, error) {
		require.Equal(t, second, proxy)
		return codexProxyTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, codexTicketProxyTestURL, req.URL.String())
			require.Equal(t, http.MethodGet, req.Method)
			require.Empty(t, req.Header.Get("Authorization"))
			require.Empty(t, req.Header.Get("Cookie"))
			require.Empty(t, req.Header.Get("Proxy-Authorization"))
			return codexProxyTestResponse(200, `{"ip":"2606:4700:4700::1111"}`), nil
		}), nil
	}
	result := tester.Test(context.Background(), 2)
	require.True(t, result.Success)
	require.Equal(t, 2, result.ProxyIndex)
	require.Equal(t, "2606:4700:4700::1111", result.ExitIP)
	require.Empty(t, result.ErrorCode)
	// The setting service has harvesting disabled; diagnostics remain available.
	require.False(t, tester.settings.cfg.Gateway.OpenAICodexTicket.Enabled)
}

func TestCodexTicketProxyTestRejectsUnavailableSelectionWithoutTransport(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		index     int
		want      string
	}{
		{"empty", "", 1, "proxy_not_configured"},
		{"zero", "http://proxy.example:80", 0, "invalid_proxy_index"},
		{"past pool", "http://proxy.example:80", 2, "invalid_proxy_index"},
		{"invalid", "http://user:secret@", 1, "invalid_proxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := newCodexProxyTesterForTest(tc.raw)
			tester.newTransport = func(string) (http.RoundTripper, error) { t.Fatal("must not create a transport"); return nil, nil }
			result := tester.Test(context.Background(), tc.index)
			require.False(t, result.Success)
			require.Equal(t, tc.want, result.ErrorCode)
		})
	}
}

func TestCodexTicketProxyTestSettingsFallbackAndExplicitClear(t *testing.T) {
	repo := &codexProxyTestSettingRepo{err: ErrSettingNotFound}
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexTicket.HarvestProxyURL = "http://yaml.example:8080"
	tester := NewOpenAICodexTicketProxyTester(NewSettingService(repo, cfg))
	var calls int
	tester.newTransport = func(raw string) (http.RoundTripper, error) {
		calls++
		require.Equal(t, cfg.Gateway.OpenAICodexTicket.HarvestProxyURL, raw)
		return codexProxyTestRoundTripper(func(*http.Request) (*http.Response, error) {
			return codexProxyTestResponse(200, `{"ip":"8.8.8.8"}`), nil
		}), nil
	}
	require.True(t, tester.Test(context.Background(), 1).Success)
	repo.err = nil
	require.Equal(t, "proxy_not_configured", tester.Test(context.Background(), 1).ErrorCode)
	repo.err = errors.New("database failure with secret credentials")
	require.Equal(t, "settings_unavailable", tester.Test(context.Background(), 1).ErrorCode)
	require.Equal(t, 1, calls)
}

func TestCodexTicketProxyTestSafeErrorsAndResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		want   string
	}{
		{"proxy failure", 0, "", errors.New("http://user:password@proxy.example:8080 refused"), "proxy_error"},
		{"proxy auth", 407, "user:password", nil, "proxy_auth"},
		{"timeout", 0, "", context.DeadlineExceeded, "timeout"},
		{"canceled", 0, "", context.Canceled, "canceled"},
		{"upstream", 500, "secret response", nil, "upstream_error"},
		{"oversize", 200, strings.Repeat("x", codexTicketProxyTestMaxBody+1), nil, "invalid_response"},
		{"bad JSON", 200, "secret response", nil, "invalid_response"},
		{"bad IP", 200, `{"ip":"password"}`, nil, "invalid_response"},
		{"private IP", 200, `{"ip":"10.0.0.1"}`, nil, "invalid_response"},
		{"loopback", 200, `{"ip":"127.0.0.1"}`, nil, "invalid_response"},
		{"mapped private", 200, `{"ip":"::ffff:192.168.0.1"}`, nil, "invalid_response"},
		{"CGNAT", 200, `{"ip":"100.64.0.1"}`, nil, "invalid_response"},
		{"IPv6 zone", 200, `{"ip":"fe80::1%en0"}`, nil, "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := newCodexProxyTesterForTest("http://user:password@proxy.example:8080")
			tester.newTransport = func(string) (http.RoundTripper, error) {
				return codexProxyTestRoundTripper(func(*http.Request) (*http.Response, error) {
					if tc.err != nil {
						return nil, tc.err
					}
					return codexProxyTestResponse(tc.status, tc.body), nil
				}), nil
			}
			result := tester.Test(context.Background(), 1)
			require.False(t, result.Success)
			require.Equal(t, tc.want, result.ErrorCode)
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			for _, secret := range []string{"password", "proxy.example", "secret response", "http://"} {
				require.NotContains(t, string(encoded), secret)
			}
		})
	}
}

func TestCodexTicketProxyTestDoesNotFollowRedirect(t *testing.T) {
	tester := newCodexProxyTesterForTest("http://proxy.example:8080")
	calls := 0
	tester.newTransport = func(string) (http.RoundTripper, error) {
		return codexProxyTestRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			resp := codexProxyTestResponse(302, "")
			resp.Header.Set("Location", "http://127.0.0.1/private?secret=password")
			return resp, nil
		}), nil
	}
	require.Equal(t, "redirect_blocked", tester.Test(context.Background(), 1).ErrorCode)
	require.Equal(t, 1, calls)
}

func TestCodexTicketProxyTestCancellationReleasesAdmission(t *testing.T) {
	tester := newCodexProxyTesterForTest("http://proxy.example:8080")
	tester.newTransport = func(string) (http.RoundTripper, error) {
		return codexProxyTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.Equal(t, "timeout", tester.Test(ctx, 1).ErrorCode)
	require.Empty(t, tester.active)
}

func TestCodexTicketProxyTestConcurrentAndDuplicateAdmission(t *testing.T) {
	tester := newCodexProxyTesterForTest("http://first.example:80\nhttp://second.example:80\nhttp://third.example:80")
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	tester.newTransport = func(string) (http.RoundTripper, error) {
		return codexProxyTestRoundTripper(func(*http.Request) (*http.Response, error) {
			started <- struct{}{}
			<-release
			return codexProxyTestResponse(200, `{"ip":"1.1.1.1"}`), nil
		}), nil
	}
	done := make(chan OpenAICodexTicketProxyTestResult, 2)
	go func() { done <- tester.Test(context.Background(), 1) }()
	<-started
	require.Equal(t, "busy", tester.Test(context.Background(), 1).ErrorCode)
	go func() { done <- tester.Test(context.Background(), 2) }()
	<-started
	require.Equal(t, "busy", tester.Test(context.Background(), 3).ErrorCode)
	close(release)
	require.True(t, (<-done).Success)
	require.True(t, (<-done).Success)
}

func TestCodexTicketProxyTestTransportNeverFallsBackToDirect(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			tester := newCodexProxyTesterForTest(scheme + "://user:password@proxy.invalid:1234")
			calls := 0
			tester.newTransport = func(raw string) (http.RoundTripper, error) {
				rt, err := newCodexTicketProxyTestTransport(raw)
				require.NoError(t, err)
				transport := rt.(*http.Transport)
				transport.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
					calls++
					require.Equal(t, "proxy.invalid:1234", address)
					return nil, errors.New("unavailable")
				}
				return rt, nil
			}
			require.Equal(t, "proxy_error", tester.Test(context.Background(), 1).ErrorCode)
			require.Equal(t, 1, calls)
		})
	}
	for _, raw := range []string{"", "file:///tmp/proxy", "bad"} {
		transport, err := newCodexTicketProxyTestTransport(raw)
		require.Error(t, err)
		require.Nil(t, transport)
	}
	for _, scheme := range []string{"socks5", "socks5h"} {
		rt, err := newCodexTicketProxyTestTransport(scheme + "://proxy.example:1080")
		require.NoError(t, err)
		transport := rt.(*http.Transport)
		require.NotNil(t, transport.DialContext)
		require.Nil(t, transport.Proxy)
	}
}

func TestCodexTicketProxyTestConnectAuthFailureIsSafe(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, http.MethodConnect, r.Method)
		require.Equal(t, "api64.ipify.org:443", r.Host)
		w.WriteHeader(http.StatusProxyAuthRequired)
		_, _ = w.Write([]byte("proxy secret password"))
	}))
	defer proxy.Close()
	tester := newCodexProxyTesterForTest(proxy.URL)
	result := tester.Test(context.Background(), 1)
	require.Equal(t, "proxy_auth", result.ErrorCode)
	require.Equal(t, int32(1), calls.Load())
}
