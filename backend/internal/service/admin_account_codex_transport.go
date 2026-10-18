package service

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Orange 特有：严格传输归实际凭据来源所有，影子只选择会话身份模式。
func codexVerifiedTransportEnabled(extra map[string]any) bool {
	enabled, _ := extra[CodexVerifiedTransportExtraKey].(bool)
	return enabled
}

func validateCodexVerifiedTransportExtra(extra map[string]any) error {
	if raw, exists := extra[CodexVerifiedTransportExtraKey]; exists {
		if _, ok := raw.(bool); !ok {
			return infraerrors.BadRequest("CODEX_VERIFIED_TRANSPORT_INVALID", "codex_verified_transport_enabled must be a boolean")
		}
	}
	return nil
}

func prepareCodexManagedExtraForCreate(platform, accountType string, extra map[string]any) (map[string]any, error) {
	if err := validateCodexVerifiedTransportExtra(extra); err != nil {
		return nil, err
	}
	prepared := maps.Clone(extra)
	delete(prepared, CodexClientIdentityExtraKey)
	prepared = prepareCodexFingerprintExtraForCreate(platform, accountType, prepared)
	if codexVerifiedTransportEnabled(prepared) {
		prepared = ensureCodexClientIdentityExtra(platform, accountType, prepared, time.Now())
	}
	return prepared, nil
}

func prepareCodexManagedExtraForUpdate(account *Account, extra map[string]any) map[string]any {
	prepared := prepareCodexFingerprintExtraForUpdate(account, extra)
	if _, provided := extra[CodexVerifiedTransportExtraKey]; !provided && !account.IsCredentialShadow() {
		if value, exists := account.Extra[CodexVerifiedTransportExtraKey]; exists {
			if prepared == nil {
				prepared = make(map[string]any)
			}
			prepared[CodexVerifiedTransportExtraKey] = value
		}
	}
	delete(prepared, CodexClientIdentityExtraKey)
	if identity, exists := account.Extra[CodexClientIdentityExtraKey]; exists {
		if prepared == nil {
			prepared = make(map[string]any)
		}
		prepared[CodexClientIdentityExtraKey] = identity
	}
	if !account.IsCredentialShadow() && codexVerifiedTransportEnabled(prepared) {
		prepared = ensureCodexClientIdentityExtra(account.Platform, account.Type, prepared, time.Now())
	}
	return prepared
}

// overrides describes only explicitly selected changes, so a family is never silently migrated.
func (s *adminServiceImpl) validateCodexVerifiedTransportAccount(ctx context.Context, account *Account, overrides map[int64]*Account) error {
	if err := validateCodexVerifiedTransportExtra(account.Extra); err != nil {
		return err
	}
	if codexFingerprintModeFromExtra(account.Extra) == "fixed_device_conversations" && !account.IsOpenAIOAuthLike() {
		return infraerrors.BadRequest("CODEX_FINGERPRINT_ACCOUNT_INVALID", "fixed device conversations requires an OpenAI OAuth or SetupToken account")
	}
	source := account
	if account.IsCredentialShadow() {
		var err error
		source = overrides[*account.ParentAccountID]
		if source == nil {
			source, err = s.accountRepo.GetByID(ctx, *account.ParentAccountID)
			if err != nil {
				return err
			}
		}
		if _, exists := account.Extra[CodexVerifiedTransportExtraKey]; exists {
			return infraerrors.BadRequest("CODEX_VERIFIED_TRANSPORT_INHERITED", "strict transport is inherited; change it on the parent account")
		}
	}
	if !codexVerifiedTransportEnabled(source.Extra) {
		return nil
	}
	if !source.IsOpenAIOAuthLike() || source.IsCredentialShadow() {
		return infraerrors.BadRequest("CODEX_VERIFIED_TRANSPORT_ACCOUNT_INVALID", "strict transport requires an OpenAI OAuth or SetupToken credential source")
	}
	family := []*Account{source}
	if source.ID > 0 {
		shadows, err := s.accountRepo.ListShadowsByParent(ctx, source.ID)
		if err != nil {
			return fmt.Errorf("list strict transport credential family: %w", err)
		}
		family = append(family, shadows...)
	}
	if account.IsCredentialShadow() && account.ID == 0 {
		family = append(family, account)
	}
	var incompatible []string
	for _, member := range family {
		if member == nil {
			continue
		}
		if projected := overrides[member.ID]; projected != nil {
			member = projected
		} else if member.ID == account.ID {
			member = account
		}
		if codexFingerprintModeFromExtra(member.Extra) != "fixed_device_conversations" {
			incompatible = append(incompatible, strconv.FormatInt(member.ID, 10)+" ("+member.Name+")")
		}
		if gateway, ok := s.runtimeBlocker.(*OpenAIGatewayService); ok && gateway.pluginManager != nil && gateway.pluginManager.ShouldRouteOpenAIOAuth(member) {
			return infraerrors.BadRequest("CODEX_VERIFIED_TRANSPORT_PLUGIN_CONFLICT", "strict transport cannot be enabled while an OpenAI OAuth forwarding plugin handles account "+strconv.FormatInt(member.ID, 10)+" ("+member.Name+")")
		}
	}
	if len(incompatible) > 0 {
		return infraerrors.BadRequest("CODEX_VERIFIED_TRANSPORT_FAMILY_MODE", "strict transport requires the parent and all shadows to explicitly use fixed device conversations; incompatible accounts: "+strings.Join(incompatible, ", ")).WithMetadata(map[string]string{"accounts": strings.Join(incompatible, ", ")})
	}
	return nil
}

func codexPolicyExtraUpdated(extra map[string]any) bool {
	_, mode := extra[codexFingerprintModeExtraKey]
	_, strict := extra[CodexVerifiedTransportExtraKey]
	return mode || strict
}

func sanitizedCodexManagedExtraUpdates(extra map[string]any) map[string]any {
	cleaned := sanitizedCodexFingerprintExtraUpdates(extra)
	delete(cleaned, CodexClientIdentityExtraKey)
	return cleaned
}

// The routed shadow chooses the mode, while the parent owns the installation.
// Materialize only the parent's seed; its mode and transport policy remain explicit choices.
func (s *adminServiceImpl) ensureCodexConversationSourceSeed(ctx context.Context, routed *Account, sources map[int64]*Account) (*Account, error) {
	if !routed.IsCredentialShadow() || codexFingerprintModeFromExtra(routed.Extra) != "fixed_device_conversations" {
		return nil, nil
	}
	parentID := *routed.ParentAccountID
	source := sources[parentID]
	if source == nil {
		var err error
		source, err = s.accountRepo.GetByID(ctx, parentID)
		if err != nil {
			return nil, err
		}
	}
	if source == nil || !source.IsOpenAIOAuthLike() || source.IsCredentialShadow() {
		return nil, infraerrors.BadRequest("CODEX_FINGERPRINT_SOURCE_INVALID", "fixed device conversations requires a valid OpenAI credential source")
	}
	if _, valid := codexFingerprintSeed(source.Extra); valid {
		return source, nil
	}
	newModeExtra := maps.Clone(source.Extra)
	if newModeExtra == nil {
		newModeExtra = make(map[string]any)
	}
	newModeExtra[codexFingerprintModeExtraKey] = "fixed_device_conversations"
	prepared := prepareCodexFingerprintExtraForUpdate(source, newModeExtra)
	updated := *source
	updated.Extra = maps.Clone(source.Extra)
	if updated.Extra == nil {
		updated.Extra = make(map[string]any)
	}
	updated.Extra[codexFingerprintSeedExtraKey] = prepared[codexFingerprintSeedExtraKey]
	if err := s.accountRepo.Update(ctx, &updated); err != nil {
		return nil, err
	}
	if sources != nil {
		sources[parentID] = &updated
	}
	return &updated, nil
}
