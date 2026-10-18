package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codextransport"
)

func prepareCodexStrictSend(request *http.Request, repo AccountRepository, account *Account, plugins *PluginManager) (*http.Request, error) {
	if request == nil || account == nil || !account.IsOpenAIOAuthLike() {
		return request, nil
	}
	unsupportedRealtime := request.URL != nil && strings.HasPrefix(request.URL.Path, "/backend-api/codex/realtime/")
	if _, strict := codextransport.FromContext(request.Context()); strict {
		if unsupportedRealtime {
			return nil, codexStrictClientError(&codextransport.Error{Reason: "strict transport has no sampled Realtime protocol profile; disable strict transport for this operation"})
		}
		if plugins != nil && plugins.ShouldRouteOpenAIOAuth(account) {
			return nil, codexStrictClientError(&codextransport.Error{Reason: "the selected OpenAI transport plugin is incompatible with strict transport"})
		}
		return request, nil
	}
	source, err := resolveCredentialAccount(request.Context(), repo, account)
	if err != nil {
		return nil, err
	}
	if err := validateCodexVerifiedRuntimeFamily(request.Context(), repo, source, account, plugins); err != nil {
		return nil, err
	}
	if unsupportedRealtime && codexVerifiedTransportEnabled(source.Extra) {
		return nil, codexStrictClientError(&codextransport.Error{Reason: "strict transport has no sampled Realtime protocol profile; disable strict transport for this operation"})
	}
	return bindCodexTransportForAccounts(request, source, account, "http")
}

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	var err error
	request, err = prepareCodexStrictSend(request, s.accountRepo, account, s.pluginManager)
	if err != nil {
		return nil, err
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if profile := resolveCodexMacTLSProfile(account); profile != nil {
		return s.httpUpstream.DoWithTLS(request, proxyURL, account.ID, account.Concurrency, profile)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	var err error
	request, err = prepareCodexStrictSend(request, s.accountRepo, account, s.pluginManager)
	if err != nil {
		return nil, err
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	if profile := resolveCodexMacTLSProfile(account); profile != nil {
		return s.httpUpstream.DoWithTLS(request, proxyURL, account.ID, account.Concurrency, profile)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
