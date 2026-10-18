package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexConversationIdentityVersion = "orange-conversations-v2"
	codexConversationSnapshotKey     = "openai_codex_conversation_snapshot"
	codexConversationFallbackKey     = "openai_codex_conversation_fallback"
	codexConversationContinuationKey = "openai_codex_conversation_continuation"
	codexConversationResponsesKey    = "openai_codex_conversation_responses"
)

// One attempt owns one projection. Body and handshake consume the same result;
// neither the legacy v1 mapper nor the window-slot mapper runs on this branch.
type codexConversationIdentitySnapshot struct {
	accountID               int64
	sourceID                int64
	apiKeyID                int64
	revision                string
	installationID          string
	metadata                map[string]any
	clientMetadata          map[string]any
	projectedClientMetadata string
	originalSession         string
	originalHeaderSession   string
	requestID               string
	originalGraph           map[string]any
}

type codexConversationResponses struct {
	mu     sync.Mutex
	graphs map[string]map[string]any
	order  []string
}

var codexConversationFlatFields = map[string]string{
	"installation_id": "installation_id", "x-codex-installation-id": "installation_id",
	"session_id": "session_id", "session-id": "session_id",
	"thread_id": "thread_id", "thread-id": "thread_id",
	"window_id": "window_id", "x-codex-window-id": "window_id",
	"parent_thread_id": "parent_thread_id", "x-codex-parent-thread-id": "parent_thread_id",
	"forked_from_thread_id": "forked_from_thread_id", "turn_id": "turn_id",
	"parent_turn_id": "parent_turn_id", "root_turn_id": "root_turn_id",
	"x-client-request-id": "request_id",
}

var codexConversationHeaderFields = []string{
	"installation_id", "session_id", "thread_id", "agent_name", "turn_id", "window_id", "window_number",
	"context_window_id", "request_kind", "history_initialization", "forked_from_thread_id",
	"forked_from_ordinal_exclusive", "parent_thread_id", "parent_turn_id", "root_turn_id", "subagent_kind",
	"thread_source", "turn_trigger", "sandbox", "sandbox_mode", "auto_review_enabled",
	"node_repl_auto_review_required", "node_repl_disabled", "turn_started_at_unix_ms", "analytics_enabled",
}

func usesCodexConversationIdentity(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && account.GetCodexFingerprintMode() == codexFingerprintFixedDeviceConversations
}

func resolveCodexConversationInstallationID(source *Account, seed string) string {
	if source != nil && source.IsOpenAIOAuthLike() {
		if device := strings.TrimSpace(source.GetExtraString("openai_device_id")); device != "" {
			return device
		}
	}
	return resolveConvergedInstallationID(source, seed)
}

func codexConversationPolicyRevision(source *Account) string {
	if source == nil {
		return ""
	}
	seed, _ := codexFingerprintSeed(source.Extra)
	sum := sha256.Sum256([]byte(codexConversationIdentityVersion + ":" + seed + ":" + resolveCodexConversationInstallationID(source, seed)))
	return fmt.Sprintf("%x", sum[:12])
}

func codexConversationClientError(err error) error {
	return &UpstreamFailoverError{
		Scope: GatewayFailureScopeRequest, NextAccountAction: NextAccountStop,
		SuppressAccountHealthPenalty: true, ClientStatusCode: http.StatusBadRequest,
		ClientMessage: err.Error(),
	}
}

func codexConversationConfigurationError() error {
	return &UpstreamFailoverError{
		Scope: GatewayFailureScopeRequest, NextAccountAction: NextAccountStop,
		SuppressAccountHealthPenalty: true, ClientStatusCode: http.StatusBadRequest,
		ClientMessage: "固定设备，独立对话模式缺少有效的系统设备种子，请重新保存凭据源账号配置",
	}
}

func scopeCodexConversationValue(source *Account, apiKeyID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	namespace := codexAccountIdentityNamespace(source)
	if raw == "" || namespace == "" {
		return raw
	}
	if index := strings.LastIndexByte(raw, ':'); index > 0 && index < len(raw)-1 {
		if _, err := strconv.ParseUint(raw[index+1:], 10, 64); err == nil {
			return scopeCodexConversationAtom(namespace, apiKeyID, raw[:index]) + raw[index:]
		}
	}
	return scopeCodexConversationAtom(namespace, apiKeyID, raw)
}

func scopeCodexConversationAtom(namespace string, apiKeyID int64, raw string) string {
	canonical := raw
	parsed, parseErr := uuid.Parse(raw)
	if parseErr == nil {
		canonical = parsed.String()
	}
	seed := fmt.Sprintf("sub2api:codex-account-identity:%s:user:%d:account:%s:value:%s", codexConversationIdentityVersion, apiKeyID, namespace, canonical)
	if parseErr != nil || parsed.Version() != 7 {
		return deriveStableUUIDv4(seed)
	}
	h := sha256.Sum256([]byte(seed))
	var out uuid.UUID
	copy(out[:6], parsed[:6])
	out[6], out[7], out[8] = 0x70|(h[6]&0x0f), h[7], 0x80|(h[8]&0x3f)
	copy(out[9:], h[9:16])
	return out.String()
}

func decodeCodexConversationMetadata(raw string) (map[string]any, error) {
	metadata := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return metadata, nil
	}
	if !json.Valid([]byte(raw)) || decodeOpenAIJSONUseNumber([]byte(raw), &metadata) != nil || metadata == nil {
		return nil, fmt.Errorf("x-codex-turn-metadata must contain a JSON object")
	}
	return metadata, nil
}

func canonicalCodexConversationMetadata(flat map[string]any) (map[string]any, []string, error) {
	canonical := map[string]any{}
	if value, exists := flat[openAIWSTurnMetadataHeader]; exists {
		raw, ok := value.(string)
		if !ok || strings.TrimSpace(raw) == "" {
			return nil, nil, fmt.Errorf("client_metadata.x-codex-turn-metadata must contain a JSON object")
		}
		var err error
		canonical, err = decodeCodexConversationMetadata(raw)
		if err != nil {
			return nil, nil, err
		}
	}
	keys := make([]string, 0, len(codexConversationFlatFields))
	for key := range codexConversationFlatFields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	conflicts := []string{}
	for _, key := range keys {
		value, exists := flat[key]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, nil, fmt.Errorf("client_metadata.%s must be a string", key)
		}
		name := codexConversationFlatFields[key]
		if previous, exists := canonical[name]; exists {
			if !reflect.DeepEqual(previous, text) {
				conflicts = append(conflicts, key)
			}
		} else {
			canonical[name] = text
		}
	}
	for _, name := range []string{"installation_id", "session_id", "thread_id", "window_id", "turn_id", "parent_thread_id", "parent_turn_id", "root_turn_id", "forked_from_thread_id", "request_kind", "subagent_kind", "thread_source", "sandbox", "sandbox_mode"} {
		if value, exists := canonical[name]; exists {
			if _, ok := value.(string); !ok {
				return nil, nil, fmt.Errorf("codex turn metadata %s must be a string", name)
			}
		}
	}
	return canonical, conflicts, nil
}

func codexConversationString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func stagedCodexConversationIdentity(c *gin.Context, account *Account) *codexConversationIdentitySnapshot {
	if c == nil || !usesCodexConversationIdentity(account) {
		return nil
	}
	value, _ := c.Get(codexConversationSnapshotKey)
	snapshot, _ := value.(*codexConversationIdentitySnapshot)
	if snapshot == nil || snapshot.accountID != account.ID {
		return nil
	}
	return snapshot
}

// A new independent WS response.create gets its own missing-ID root. A confirmed
// previous_response_id continuation retains the current root; ownership/replay
// validation remains in the existing WS controller.
func beginCodexConversationTurn(c *gin.Context, account *Account, payload []byte) {
	if c == nil || !usesCodexConversationIdentity(account) {
		return
	}
	c.Set(codexConversationSnapshotKey, (*codexConversationIdentitySnapshot)(nil))
	c.Set(codexConversationContinuationKey, (map[string]any)(nil))
	previous := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
	if previous == "" {
		c.Set(codexConversationFallbackKey, uuid.Must(uuid.NewV7()).String())
	} else if value, ok := c.Get(codexConversationResponsesKey); ok {
		if responses, ok := value.(*codexConversationResponses); ok {
			responses.mu.Lock()
			graph := maps.Clone(responses.graphs[previous])
			responses.mu.Unlock()
			c.Set(codexConversationContinuationKey, graph)
		}
	}
}

func recordCodexConversationResponse(c *gin.Context, account *Account, responseID string) {
	snapshot := stagedCodexConversationIdentity(c, account)
	if snapshot == nil || strings.TrimSpace(responseID) == "" {
		return
	}
	value, ok := c.Get(codexConversationResponsesKey)
	responses, _ := value.(*codexConversationResponses)
	if !ok || responses == nil {
		responses = &codexConversationResponses{graphs: map[string]map[string]any{}}
		c.Set(codexConversationResponsesKey, responses)
	}
	responses.mu.Lock()
	defer responses.mu.Unlock()
	if _, exists := responses.graphs[responseID]; !exists {
		responses.order = append(responses.order, responseID)
	}
	responses.graphs[responseID] = maps.Clone(snapshot.originalGraph)
	// Only the current connection's recent confirmed continuations are retained.
	if len(responses.order) > 64 {
		delete(responses.graphs, responses.order[0])
		responses.order = responses.order[1:]
	}
}

func prepareCodexConversationIdentity(c *gin.Context, routed *Account, body map[string]any, inputHeaders ...http.Header) (*codexConversationIdentitySnapshot, error) {
	source := codexAccountIdentitySource(c, routed)
	seed, ok := codexFingerprintSeed(source.Extra)
	if !ok {
		return nil, codexConversationConfigurationError()
	}
	flat := map[string]any{}
	if value, exists := body["client_metadata"]; exists {
		var valid bool
		flat, valid = value.(map[string]any)
		if !valid {
			return nil, codexConversationClientError(fmt.Errorf("client_metadata must be an object"))
		}
	}
	encodedFlat, _ := json.Marshal(flat)
	if snapshot := stagedCodexConversationIdentity(c, routed); snapshot != nil && snapshot.sourceID == source.ID && snapshot.revision == codexConversationPolicyRevision(source) && snapshot.apiKeyID == getAPIKeyIDFromContext(c) && snapshot.projectedClientMetadata == string(encodedFlat) {
		return snapshot, nil
	}
	canonical, conflicts, err := canonicalCodexConversationMetadata(flat)
	if err != nil {
		return nil, codexConversationClientError(err)
	}
	headerMetadata := map[string]any{}
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	if len(inputHeaders) > 0 {
		headers = inputHeaders[0]
	}
	if headers != nil {
		headerMetadata, err = decodeCodexConversationMetadata(headers.Get(openAIWSTurnMetadataHeader))
		if err != nil {
			return nil, codexConversationClientError(err)
		}
		for _, field := range [][2]string{{"session-id", "session_id"}, {"session_id", "session_id"}, {"thread-id", "thread_id"}, {"thread_id", "thread_id"}, {"x-codex-window-id", "window_id"}, {"x-codex-parent-thread-id", "parent_thread_id"}} {
			if raw := strings.TrimSpace(headers.Get(field[0])); raw != "" {
				if previous, exists := headerMetadata[field[1]]; !exists {
					headerMetadata[field[1]] = raw
				} else if !reflect.DeepEqual(previous, raw) {
					conflicts = append(conflicts, "header."+field[0])
				}
			}
		}
	}
	if c != nil {
		if value, ok := c.Get(codexConversationContinuationKey); ok {
			if graph, ok := value.(map[string]any); ok {
				for key, value := range graph {
					headerMetadata[key] = value
				}
			}
		}
	}
	for key, value := range canonical {
		if previous, exists := headerMetadata[key]; exists && !reflect.DeepEqual(previous, value) {
			conflicts = append(conflicts, "header."+key)
		}
		headerMetadata[key] = value
	}
	canonical = headerMetadata
	for _, name := range []string{"installation_id", "session_id", "thread_id", "window_id", "turn_id", "parent_thread_id", "parent_turn_id", "root_turn_id", "forked_from_thread_id", "request_id"} {
		if value, exists := canonical[name]; exists {
			if _, ok := value.(string); !ok {
				return nil, codexConversationClientError(fmt.Errorf("codex turn metadata %s must be a string", name))
			}
		}
	}
	session := codexConversationString(canonical, "session_id")
	thread := codexConversationString(canonical, "thread_id")
	if session == "" {
		session = thread
	}
	if session == "" {
		if c != nil {
			session = c.GetString(codexConversationFallbackKey)
		}
		if session == "" {
			session = uuid.Must(uuid.NewV7()).String()
			if c != nil {
				c.Set(codexConversationFallbackKey, session)
			}
		}
	}
	if thread == "" {
		thread = session
	}
	canonical["session_id"], canonical["thread_id"] = session, thread
	if codexConversationString(canonical, "window_id") == "" {
		canonical["window_id"] = thread + ":0"
	}
	installation := resolveCodexConversationInstallationID(source, seed)
	if installation == "" || len(installation) > 256 || strings.ContainsAny(installation, "\r\n\x00") {
		return nil, codexConversationClientError(fmt.Errorf("凭据源账号的设备标识无效，请重新保存账号配置"))
	}
	snapshot := &codexConversationIdentitySnapshot{accountID: routed.ID, sourceID: source.ID, apiKeyID: getAPIKeyIDFromContext(c), revision: codexConversationPolicyRevision(source), installationID: installation, originalSession: session, originalHeaderSession: extractClientSessionID(headers)}
	snapshot.originalGraph = map[string]any{}
	for _, key := range []string{"session_id", "thread_id", "window_id", "parent_thread_id", "forked_from_thread_id"} {
		if value, exists := canonical[key]; exists {
			snapshot.originalGraph[key] = value
		}
	}
	for _, key := range []string{"session_id", "thread_id", "window_id", "parent_thread_id", "forked_from_thread_id", "request_id"} {
		if raw := codexConversationString(canonical, key); raw != "" {
			canonical[key] = scopeCodexConversationValue(source, snapshot.apiKeyID, raw)
		}
	}
	canonical["installation_id"] = installation
	snapshot.metadata = canonical
	snapshot.requestID = codexConversationString(canonical, "thread_id")
	if requestID := codexConversationString(canonical, "request_id"); requestID != "" {
		snapshot.requestID = requestID
	} else if raw := strings.TrimSpace(headers.Get("x-client-request-id")); raw != "" {
		snapshot.requestID = scopeCodexConversationValue(source, snapshot.apiKeyID, raw)
	}
	cm := maps.Clone(flat)
	for key, name := range codexConversationFlatFields {
		if value, exists := canonical[name]; exists {
			if _, exists := cm[key]; exists || key == "session_id" || key == "thread_id" || key == "x-codex-window-id" || key == "x-codex-installation-id" {
				cm[key] = value
			}
		}
	}
	if raw, marshalErr := marshalCodexTurnMetadata(canonical); marshalErr == nil {
		cm[openAIWSTurnMetadataHeader] = string(raw)
	} else {
		return nil, marshalErr
	}
	snapshot.clientMetadata = cm
	encodedFlat, _ = json.Marshal(cm)
	snapshot.projectedClientMetadata = string(encodedFlat)
	if len(conflicts) > 0 {
		slices.Sort(conflicts)
		slog.Debug("codex conversation metadata canonical projection", "conflicting_fields", slices.Compact(conflicts))
	}
	if c != nil {
		c.Set(codexConversationSnapshotKey, snapshot)
	}
	return snapshot, nil
}

func (s *OpenAIGatewayService) scopeCodexRequestMetadataMap(c *gin.Context, routed *Account, body map[string]any) (bool, error) {
	if !usesCodexConversationIdentity(routed) {
		return applyCodexAccountIdentityClientMetadataMap(body, codexAccountIdentitySource(c, routed), getAPIKeyIDFromContext(c)), nil
	}
	return projectCodexConversationMetadataMap(c, routed, body, isOpenAIResponsesCompactPath(c))
}

func projectCodexConversationMetadataMap(c *gin.Context, routed *Account, body map[string]any, compact bool) (bool, error) {
	snapshot, err := prepareCodexConversationIdentity(c, routed, body)
	if err != nil {
		return false, err
	}
	if !compact {
		body["client_metadata"] = maps.Clone(snapshot.clientMetadata)
	}
	changed := !compact
	if raw, ok := body["prompt_cache_key"].(string); ok && raw != "" && (raw == snapshot.originalSession || raw == snapshot.originalHeaderSession) {
		body["prompt_cache_key"] = codexConversationString(snapshot.metadata, "session_id")
		changed = true
	}
	return changed, nil
}

func (s *OpenAIGatewayService) scopeCodexRequestMetadataRaw(c *gin.Context, routed *Account, body []byte) ([]byte, bool, error) {
	if !usesCodexConversationIdentity(routed) {
		return applyCodexAccountIdentityClientMetadataRaw(body, codexAccountIdentitySource(c, routed), getAPIKeyIDFromContext(c))
	}
	return projectCodexConversationMetadataRaw(c, routed, body, isOpenAIResponsesCompactPath(c))
}

func projectCodexConversationMetadataRaw(c *gin.Context, routed *Account, body []byte, compact bool) ([]byte, bool, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return body, false, codexConversationClientError(fmt.Errorf("request body must contain a JSON object"))
	}
	projection := map[string]any{}
	if cm := gjson.GetBytes(body, "client_metadata"); cm.Exists() {
		if !cm.IsObject() {
			return body, false, codexConversationClientError(fmt.Errorf("client_metadata must be an object"))
		}
		metadata := map[string]any{}
		if err := decodeOpenAIJSONUseNumber([]byte(cm.Raw), &metadata); err != nil {
			return body, false, codexConversationClientError(err)
		}
		projection["client_metadata"] = metadata
	}
	cache := gjson.GetBytes(body, "prompt_cache_key")
	if cache.Type == gjson.String {
		projection["prompt_cache_key"] = cache.String()
	}
	if _, err := projectCodexConversationMetadataMap(c, routed, projection, compact); err != nil {
		return body, false, err
	}
	next := body
	if !compact {
		raw, err := json.Marshal(projection["client_metadata"])
		if err != nil {
			return body, false, err
		}
		next, err = sjson.SetRawBytes(next, "client_metadata", raw)
		if err != nil {
			return body, false, err
		}
	}
	if key, ok := projection["prompt_cache_key"].(string); ok && key != cache.String() {
		var err error
		next, err = sjson.SetBytes(next, "prompt_cache_key", key)
		if err != nil {
			return body, false, err
		}
	}
	return next, string(next) != string(body), nil
}

func applyCodexConversationIdentityHeaders(c *gin.Context, routed *Account, h http.Header) {
	snapshot := stagedCodexConversationIdentity(c, routed)
	if snapshot == nil || h == nil {
		return
	}
	metadata := snapshot.metadata
	if isOpenAIResponsesCompactPath(c) {
		h.Set("session_id", codexConversationString(metadata, "session_id"))
		for header, name := range map[string]string{"session-id": "session_id", "thread-id": "thread_id", "x-codex-window-id": "window_id", "x-codex-parent-thread-id": "parent_thread_id"} {
			if h.Get(header) != "" {
				h.Set(header, codexConversationString(metadata, name))
			}
		}
		if h.Get("x-client-request-id") != "" {
			h.Set("x-client-request-id", snapshot.requestID)
		}
		if h.Get("x-codex-installation-id") != "" {
			h.Set("x-codex-installation-id", snapshot.installationID)
		}
		h.Del(openAIWSTurnMetadataHeader)
		return
	}
	h.Set("x-codex-installation-id", snapshot.installationID)
	h.Set("session-id", codexConversationString(metadata, "session_id"))
	h.Set("thread-id", codexConversationString(metadata, "thread_id"))
	h.Set("x-client-request-id", snapshot.requestID)
	h.Set("x-codex-window-id", codexConversationString(metadata, "window_id"))
	if parent := codexConversationString(metadata, "parent_thread_id"); parent != "" {
		h.Set(codexParentThreadIDHeader, parent)
	} else {
		h.Del(codexParentThreadIDHeader)
	}
	// Compact retains its legacy protocol carriers; ordinary Codex requests use
	// the hyphenated identity carriers and a bounded metadata projection.
	h.Del("session_id")
	h.Del("conversation_id")
	bounded := map[string]any{}
	for _, key := range codexConversationHeaderFields {
		if value, exists := metadata[key]; exists {
			bounded[key] = value
		}
	}
	if raw, err := marshalCodexTurnMetadata(bounded); err == nil {
		h.Set(openAIWSTurnMetadataHeader, string(raw))
	}
}

func applyCodexRequestIdentityHeaders(c *gin.Context, routed *Account, h http.Header) {
	if usesCodexConversationIdentity(routed) {
		applyCodexConversationIdentityHeaders(c, routed, h)
		return
	}
	applyCodexAccountIdentityHeaders(h, codexAccountIdentitySource(c, routed), getAPIKeyIDFromContext(c))
}

func codexWSIdentityRevision(c *gin.Context, routed *Account) string {
	if !usesCodexConversationIdentity(routed) {
		return ""
	}
	source := codexAccountIdentitySource(c, routed)
	return codexConversationPolicyRevision(source) + ":" + codexVerifiedTransportRevision(source)
}

func (s *AccountTestService) prepareCodexProbeIdentity(ctx context.Context, c *gin.Context, routed, source *Account) error {
	if c != nil {
		c.Set(codexAccountIdentitySourceContextKey, source)
	}
	if usesCodexConversationIdentity(routed) {
		if _, ok := codexFingerprintSeed(source.Extra); !ok {
			return codexConversationConfigurationError()
		}
	}
	return validateCodexVerifiedRuntimeFamily(ctx, s.accountRepo, source, routed, s.pluginManager)
}

func prepareCodexConversationAuxiliary(c *gin.Context, routed *Account) (*codexConversationIdentitySnapshot, error) {
	root := ""
	if c != nil {
		root = c.GetString(codexConversationFallbackKey)
	}
	if root == "" {
		root = uuid.Must(uuid.NewV7()).String()
		if c != nil {
			c.Set(codexConversationFallbackKey, root)
		}
	}
	logical := map[string]any{"client_metadata": map[string]any{"session_id": root, "thread_id": root}}
	return prepareCodexConversationIdentity(c, routed, logical, make(http.Header))
}

func projectCodexConversationAuxiliaryMap(c *gin.Context, routed *Account, body map[string]any) error {
	snapshot, err := prepareCodexConversationAuxiliary(c, routed)
	if err != nil {
		return err
	}
	body["client_metadata"] = maps.Clone(snapshot.clientMetadata)
	return nil
}

func projectCodexConversationAuxiliaryRaw(c *gin.Context, routed *Account, body []byte) ([]byte, error) {
	snapshot, err := prepareCodexConversationAuxiliary(c, routed)
	if err != nil {
		return body, err
	}
	raw, err := json.Marshal(snapshot.clientMetadata)
	if err != nil {
		return body, err
	}
	return sjson.SetRawBytes(body, "client_metadata", raw)
}

func applyCodexConversationAlphaHeader(c *gin.Context, routed *Account, h http.Header) {
	snapshot := stagedCodexConversationIdentity(c, routed)
	if snapshot == nil {
		return
	}
	bounded := map[string]any{}
	for _, key := range codexConversationHeaderFields {
		if value, exists := snapshot.metadata[key]; exists {
			bounded[key] = value
		}
	}
	if raw, err := marshalCodexTurnMetadata(bounded); err == nil {
		h.Set(openAIWSTurnMetadataHeader, string(raw))
	}
}

// Existing WS turns finish on their frozen policy. A policy edit takes effect
// before the next turn by requesting a fresh client handshake, never by replay.
func (s *OpenAIGatewayService) checkCodexConversationPolicy(ctx context.Context, c *gin.Context, routed *Account) error {
	if !usesCodexConversationIdentity(routed) || s.accountRepo == nil || routed.ID <= 0 {
		return nil
	}
	fresh, err := s.accountRepo.GetByID(ctx, routed.ID)
	if err != nil {
		return err
	}
	if !usesCodexConversationIdentity(fresh) {
		return fmt.Errorf("Codex identity mode changed; please open a new websocket conversation")
	}
	source := fresh
	if fresh.IsShadow() {
		source, err = resolveCredentialAccount(ctx, s.accountRepo, fresh)
		if err != nil {
			return err
		}
	}
	if err := validateCodexVerifiedRuntimeFamily(ctx, s.accountRepo, source, fresh, s.pluginManager); err != nil {
		return err
	}
	currentSource := codexAccountIdentitySource(c, routed)
	if codexConversationPolicyRevision(source) != codexConversationPolicyRevision(currentSource) || codexVerifiedTransportRevision(source) != codexVerifiedTransportRevision(currentSource) {
		return fmt.Errorf("Codex identity or transport configuration changed; please open a new websocket conversation")
	}
	return nil
}
