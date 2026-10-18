package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codextransport"
	"github.com/gin-gonic/gin"
)

// Orange 特有：严格传输只属于显式启用的新模式凭据源，不使用全局开关。
const CodexVerifiedTransportExtraKey = "codex_verified_transport_enabled"

type codexVerifiedIdentityContextKey struct{}

type codexVerifiedOutboundIdentity struct {
	userAgent  string
	originator string
	version    string
}

func codexVerifiedTransportRevision(source *Account) string {
	if source == nil {
		return ""
	}
	seed, _ := codexFingerprintSeed(source.Extra)
	data, _ := json.Marshal(struct {
		Enabled  bool   `json:"enabled"`
		Mode     string `json:"mode"`
		Seed     string `json:"seed"`
		Device   string `json:"device"`
		Identity any    `json:"identity"`
	}{codexVerifiedTransportEnabled(source.Extra), string(source.GetCodexFingerprintMode()), seed,
		resolveCodexConversationInstallationID(source, seed), source.Extra[CodexClientIdentityExtraKey]})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:16])
}

func codexStrictClientError(err error) *UpstreamFailoverError {
	message := sanitizeUpstreamErrorMessage(err.Error())
	body, _ := json.Marshal(map[string]any{"error": map[string]string{
		"type": "codex_transport_error", "message": message,
	}})
	return &UpstreamFailoverError{
		StatusCode: http.StatusBadGateway, ResponseBody: body,
		Scope: GatewayFailureScopeRequest, NextAccountAction: NextAccountStop,
		SuppressAccountHealthPenalty: true, ClientStatusCode: http.StatusBadGateway,
		ClientMessage: message,
	}
}

// validateCodexVerifiedRuntimeFamily also protects old routes after concurrent
// configuration changes. It never silently edits a family member's mode.
func validateCodexVerifiedRuntimeFamily(ctx context.Context, repo AccountRepository, source, routed *Account, plugins *PluginManager) error {
	if source != nil {
		if raw, exists := source.Extra[CodexVerifiedTransportExtraKey]; exists {
			if _, valid := raw.(bool); !valid {
				return codexStrictClientError(&codextransport.Error{Reason: "the strict transport setting must be a boolean"})
			}
		}
	}
	if source == nil || !codexVerifiedTransportEnabled(source.Extra) {
		return nil
	}
	if _, _, err := codexVerifiedBinding(source, routed, "http"); err != nil {
		return err
	}
	family := []*Account{source}
	if routed != nil && routed.ID != source.ID {
		family = append(family, routed)
	}
	if repo != nil && source.ID > 0 {
		shadows, err := repo.ListShadowsByParent(ctx, source.ID)
		if err != nil {
			return codexStrictClientError(&codextransport.Error{Reason: "unable to validate the credential source's shadow accounts"})
		}
		family = append(family, shadows...)
	}
	for _, member := range family {
		if member == nil {
			continue
		}
		if member.GetCodexFingerprintMode() != codexFingerprintFixedDeviceConversations {
			return codexStrictClientError(&codextransport.Error{Reason: fmt.Sprintf("account %d must explicitly use fixed device conversations before strict transport can be used", member.ID)})
		}
		if plugins != nil && plugins.ShouldRouteOpenAIOAuth(member) {
			return codexStrictClientError(&codextransport.Error{Reason: "the credential family has an incompatible OpenAI transport plugin"})
		}
	}
	return nil
}

func codexVerifiedBinding(source, routed *Account, plane string) (codextransport.Binding, codexVerifiedOutboundIdentity, error) {
	var empty codextransport.Binding
	var identity codexVerifiedOutboundIdentity
	if source == nil || !source.IsOpenAIOAuthLike() || source.IsCredentialShadow() {
		return empty, identity, codexStrictClientError(&codextransport.Error{Reason: "strict transport requires an OpenAI credential source"})
	}
	if routed == nil {
		routed = source
	}
	if source.GetCodexFingerprintMode() != codexFingerprintFixedDeviceConversations || routed.GetCodexFingerprintMode() != codexFingerprintFixedDeviceConversations {
		return empty, identity, codexStrictClientError(&codextransport.Error{Reason: "strict transport requires fixed device conversations on both the routed account and its credential source"})
	}
	seed, ok := codexFingerprintSeed(source.Extra)
	if !ok {
		return empty, identity, codexStrictClientError(&codextransport.Error{Reason: "the credential source has no valid persistent fingerprint seed"})
	}
	profile, ok := codexClientIdentityFromExtra(source.Extra)
	if !ok {
		return empty, identity, codexStrictClientError(&codextransport.Error{Reason: "the credential source has no valid managed client identity; save its strict transport configuration again"})
	}
	device := resolveCodexConversationInstallationID(source, seed)
	if device == "" || strings.ContainsAny(device, "\r\n") || len(device) > 256 {
		return empty, identity, codexStrictClientError(&codextransport.Error{Reason: "the credential source has an invalid installation identity"})
	}
	identity = codexVerifiedOutboundIdentity{profile.UserAgent(codextransport.LatestVersion()), codexTUIOriginator, codextransport.LatestVersion()}
	scope := sha256.Sum256([]byte(codexAccountIdentityNamespace(source) + "\x00" + device))
	binding, err := codextransport.Select(identity.userAgent, fmt.Sprintf("%x", scope[:16]), plane)
	if err != nil {
		return empty, identity, codexStrictClientError(err)
	}
	binding.SourceID, binding.DeviceID, binding.Revision = source.ID, device, codexVerifiedTransportRevision(source)
	return binding, identity, nil
}

func codexTransportContext(ctx context.Context, headers http.Header, source, routed *Account, plane string) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source != nil {
		if raw, exists := source.Extra[CodexVerifiedTransportExtraKey]; exists {
			if _, valid := raw.(bool); !valid {
				return nil, codexStrictClientError(&codextransport.Error{Reason: "the strict transport setting must be a boolean"})
			}
		}
	}
	if source == nil || !codexVerifiedTransportEnabled(source.Extra) {
		return codextransport.WithoutBinding(ctx), nil
	}
	binding, identity, err := codexVerifiedBinding(source, routed, plane)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		headers.Set("User-Agent", identity.userAgent)
		headers.Set("Originator", identity.originator)
		headers.Set("Version", identity.version)
	}
	ctx = context.WithValue(ctx, codexVerifiedIdentityContextKey{}, identity)
	return codextransport.WithBinding(ctx, binding), nil
}

func bindCodexTransportForAccounts(request *http.Request, source, routed *Account, plane string) (*http.Request, error) {
	if request == nil {
		return nil, nil
	}
	ctx, err := codexTransportContext(request.Context(), request.Header, source, routed, plane)
	if err != nil {
		return nil, err
	}
	return request.WithContext(ctx), nil
}

func bindCodexAttemptRequest(c *gin.Context, request *http.Request, routed *Account) (*http.Request, error) {
	return bindCodexTransportForAccounts(request, codexAccountIdentitySource(c, routed), routed, "http")
}

// CodexAuthIdentityForContext keeps refresh HTTP headers and its frozen transport
// profile in sync. Initial authorization retains the ordinary canonical identity.
func CodexAuthIdentityForContext(ctx context.Context) (string, string) {
	if ctx != nil {
		if _, strict := codextransport.FromContext(ctx); strict {
			if identity, ok := ctx.Value(codexVerifiedIdentityContextKey{}).(codexVerifiedOutboundIdentity); ok {
				return identity.userAgent, identity.originator
			}
		}
	}
	return CodexCanonicalAuthIdentity()
}

func codexStrictResponseFailure(response *http.Response, err error) *UpstreamFailoverError {
	if err == nil || response == nil || response.Request == nil {
		return nil
	}
	if _, ok := codextransport.FromContext(response.Request.Context()); !ok {
		return nil
	}
	return codexStrictClientError(&codextransport.Failure{Err: err})
}
