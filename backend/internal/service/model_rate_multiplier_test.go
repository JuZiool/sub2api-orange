package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeModelRateMultiplierRules(t *testing.T) {
	rules, err := NormalizeModelRateMultiplierRules([]ModelRateMultiplierRule{{Model: " gpt-5.6 ", Multiplier: 0.8}})
	require.NoError(t, err)
	require.Equal(t, []ModelRateMultiplierRule{{Model: "gpt-5.6", Multiplier: 0.8}}, rules)

	for _, rule := range []ModelRateMultiplierRule{{Model: "", Multiplier: 1}, {Model: "gpt-*", Multiplier: 1}, {Model: "gpt", Multiplier: 0}, {Model: "gpt", Multiplier: 1001}} {
		_, err = NormalizeModelRateMultiplierRules([]ModelRateMultiplierRule{rule})
		require.Error(t, err)
	}
	_, err = NormalizeModelRateMultiplierRules([]ModelRateMultiplierRule{{Model: "gpt", Multiplier: 1}, {Model: "gpt", Multiplier: 2}})
	require.Error(t, err)
}

func TestResolveModelRateMultiplierIsExactAndCaseSensitive(t *testing.T) {
	rules := []ModelRateMultiplierRule{{Model: "gpt-5.6", Multiplier: 0.8}}
	got, model, ok := ResolveModelRateMultiplier("gpt-5.6", rules)
	require.True(t, ok)
	require.Equal(t, "gpt-5.6", model)
	require.Equal(t, 0.8, got)
	_, _, ok = ResolveModelRateMultiplier("gpt-5.6-mini", rules)
	require.False(t, ok)
	_, _, ok = ResolveModelRateMultiplier("GPT-5.6", rules)
	require.False(t, ok)
}

func TestGroupHiddenModelsSupportExactPrefixAndGeminiPrefix(t *testing.T) {
	cfg := GroupModelsListConfig{HiddenModels: []string{"gpt-5.6", "gemini-3-*"}}
	require.True(t, IsGroupModelHidden(cfg, "gpt-5.6"))
	require.False(t, IsGroupModelHidden(cfg, "gpt-5.6-mini"))
	require.True(t, IsGroupModelHidden(cfg, "models/gemini-3-pro"))
	require.True(t, IsGroupModelHidden(cfg, "gemini-3-flash"))
	require.False(t, IsGroupModelHidden(cfg, "gemini-2.5-pro"))
}
