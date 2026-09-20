package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketProxyPoolParse(t *testing.T) {
	first := "http://user:secret@first.example:8080"
	second := "socks5h://user:another@[::1]:1080"
	proxies, err := ParseOpenAICodexTicketHarvestProxyPool(" \r\n" + first + " \r\n\r\n" + second + "\n" + first)
	require.NoError(t, err)
	require.Equal(t, []string{first, second}, proxies)
	proxies, err = ParseOpenAICodexTicketHarvestProxyPool("\r\n \n")
	require.NoError(t, err)
	require.Empty(t, proxies)

	_, err = ParseOpenAICodexTicketHarvestProxyPool(first + "\n\nftp://user:hidden-secret@proxy.example:21")
	require.ErrorContains(t, err, "line 3")
	require.NotContains(t, err.Error(), "hidden-secret")
	lines := make([]string, maxOpenAICodexTicketHarvestProxies+1)
	for index := range lines {
		lines[index] = fmt.Sprintf("http://proxy-%d.example:8080", index)
	}
	_, err = ParseOpenAICodexTicketHarvestProxyPool(strings.Join(lines, "\n"))
	require.ErrorContains(t, err, "at most 64")
}

func TestCodexTicketProxyPoolMaskedReorderAndEdit(t *testing.T) {
	first := "http://user:first-secret@first.example:8080"
	second := "socks5h://user:second-secret@second.example:1080"
	previous := first + "\n" + second
	masked := MaskOpenAICodexTicketHarvestProxyPool(previous)
	require.NotContains(t, masked, "first-secret")
	require.NotContains(t, masked, "second-secret")
	require.Equal(t, MaskProxyURL(first)+"\n"+MaskProxyURL(second), masked)

	merged, err := MergeOpenAICodexTicketHarvestProxyPool(MaskProxyURL(second)+"\n"+MaskProxyURL(first), previous)
	require.NoError(t, err)
	require.Equal(t, second+"\n"+first, merged, "reordering must preserve each proxy's own password")
	merged, err = MergeOpenAICodexTicketHarvestProxyPool(MaskProxyURL(second), previous)
	require.NoError(t, err)
	require.Equal(t, second, merged, "deleting an entry must not keep the deleted proxy")
	merged, err = MergeOpenAICodexTicketHarvestProxyPool(first+"\n"+MaskProxyURL(first), previous)
	require.NoError(t, err)
	require.Equal(t, first, merged)
	merged, err = MergeOpenAICodexTicketHarvestProxyPool("", previous)
	require.NoError(t, err)
	require.Empty(t, merged)

	for _, modified := range []string{
		strings.Replace(MaskProxyURL(first), "first.example", "changed.example", 1),
		strings.Replace(MaskProxyURL(first), "user:", "changed:", 1),
	} {
		_, err = MergeOpenAICodexTicketHarvestProxyPool(modified, previous)
		require.ErrorContains(t, err, "re-enter the password")
		require.NotContains(t, err.Error(), "secret")
	}
	_, err = MergeOpenAICodexTicketHarvestProxyPool(MaskProxyURL(first), first+"\n"+strings.Replace(first, "first-secret", "another-secret", 1))
	require.ErrorContains(t, err, "ambiguous")
	require.Empty(t, MaskOpenAICodexTicketHarvestProxyPool("http://user:hidden-secret@"))
}

func TestCodexTicketProxyPoolRuntimeClearAndFallback(t *testing.T) {
	key := SettingKeyOpenAICodexTicketHarvestProxyURL
	fallback := "http://fallback.example:8080"
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	svc := NewSettingService(repo, &config.Config{})
	require.Equal(t, []string{fallback}, svc.GetOpenAICodexTicketHarvestProxyPool(context.Background(), fallback))
	repo.values[key] = "http://first.example:8080\nsocks5h://second.example:1080"
	svc.InvalidateOpenAICodexTicketHarvestProxyCache()
	require.Equal(t, []string{"http://first.example:8080", "socks5h://second.example:1080"}, svc.GetOpenAICodexTicketHarvestProxyPool(context.Background(), fallback))
	repo.values[key] = ""
	svc.InvalidateOpenAICodexTicketHarvestProxyCache()
	require.Empty(t, svc.GetOpenAICodexTicketHarvestProxyPool(context.Background(), fallback), "an explicit clear must not silently reactivate the YAML proxy")
	svc.InvalidateOpenAICodexTicketHarvestProxyCache()
	repo.err = errors.New("database temporarily unavailable")
	require.Empty(t, svc.GetOpenAICodexTicketHarvestProxyPool(context.Background(), fallback), "a transient error must preserve an explicitly empty cached pool")
	repo.err = nil
	delete(repo.values, key)
	svc.openAICodexTicketHarvestProxyCache.Store(&cachedOpenAICodexTicketHarvestProxy{expiresAt: time.Now().Add(-time.Second).UnixNano()})
	require.Equal(t, []string{fallback}, svc.GetOpenAICodexTicketHarvestProxyPool(context.Background(), fallback))
}

func TestCodexTicketProxyPoolSettingsDistinguishMissingAndCleared(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexTicket.HarvestProxyURL = "http://yaml.example:8080"
	svc := NewSettingService(nil, cfg)
	require.Equal(t, cfg.Gateway.OpenAICodexTicket.HarvestProxyURL, svc.parseSettings(map[string]string{}).OpenAICodexTicketHarvestProxyURL)
	require.Empty(t, svc.parseSettings(map[string]string{SettingKeyOpenAICodexTicketHarvestProxyURL: ""}).OpenAICodexTicketHarvestProxyURL)
}
