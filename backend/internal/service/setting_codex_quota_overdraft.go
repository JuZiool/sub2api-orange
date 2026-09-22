package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// codexQuotaOverdraftFallbackEnabled keeps the deployment YAML/env setting as
// the authoritative default until an administrator saves an override.
func (s *SettingService) codexQuotaOverdraftFallbackEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.CodexQuotaOverdraftEnabled
}

func parseCodexQuotaOverdraftEnabled(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func (s *SettingService) codexQuotaOverdraftEnabledFromSettings(settings map[string]string) bool {
	raw, exists := settings[SettingKeyCodexQuotaOverdraftEnabled]
	if !exists {
		return s.codexQuotaOverdraftFallbackEnabled()
	}
	if enabled, valid := parseCodexQuotaOverdraftEnabled(raw); valid {
		return enabled
	}
	return s.codexQuotaOverdraftFallbackEnabled()
}

// LoadCodexQuotaOverdraftEnabled publishes the effective runtime setting at
// startup. A missing key intentionally does not create a row: YAML/env remains
// the deployment-level default until an administrator explicitly saves a value.
func (s *SettingService) LoadCodexQuotaOverdraftEnabled(ctx context.Context) error {
	fallback := s.codexQuotaOverdraftFallbackEnabled()
	if s == nil || s.settingRepo == nil {
		setCodexQuotaOverdraftEnabledFallback(fallback)
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCodexQuotaOverdraftEnabled)
	if errors.Is(err, ErrSettingNotFound) {
		setCodexQuotaOverdraftEnabledFallback(fallback)
		return nil
	}
	if err != nil {
		setCodexQuotaOverdraftEnabledFallback(fallback)
		return fmt.Errorf("get %s: %w", SettingKeyCodexQuotaOverdraftEnabled, err)
	}

	enabled, valid := parseCodexQuotaOverdraftEnabled(raw)
	if !valid {
		setCodexQuotaOverdraftEnabledFallback(fallback)
		return fmt.Errorf("invalid %s value %q", SettingKeyCodexQuotaOverdraftEnabled, raw)
	}
	setCodexQuotaOverdraftEnabledFromPersistedSetting(enabled)
	return nil
}
