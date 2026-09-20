package service

import (
	"fmt"
	"net/url"
	"strings"
)

const maxOpenAICodexTicketHarvestProxies = 64

// ParseOpenAICodexTicketHarvestProxyPool accepts one proxy URL per line. It also
// accepts the old single-URL value, ignores blank lines and preserves ordering.
// Errors only report a line number and validation rule, never credentials.
func ParseOpenAICodexTicketHarvestProxyPool(raw string) ([]string, error) {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for index, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := ValidateOpenAICodexTicketHarvestProxyURL(line); err != nil {
			return nil, fmt.Errorf("harvest proxy line %d: %s", index+1, err)
		}
		parsed, _ := url.Parse(line)
		proxy := parsed.String()
		if _, exists := seen[proxy]; exists {
			continue
		}
		if len(result) >= maxOpenAICodexTicketHarvestProxies {
			return nil, fmt.Errorf("harvest proxy pool must contain at most %d proxies", maxOpenAICodexTicketHarvestProxies)
		}
		seen[proxy] = struct{}{}
		result = append(result, proxy)
	}
	return result, nil
}

// MaskOpenAICodexTicketHarvestProxyPool masks each line independently. Invalid
// legacy lines are omitted rather than accidentally exposing their passwords.
func MaskOpenAICodexTicketHarvestProxyPool(raw string) string {
	result := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		if masked := MaskProxyURL(line); masked != "" {
			result = append(result, masked)
		}
	}
	return strings.Join(result, "\n")
}

// MergeOpenAICodexTicketHarvestProxyPool restores masked passwords by the exact
// URL identity returned by GET, never by line position. Editing a host/user or
// an ambiguous identity requires entering its password again.
func MergeOpenAICodexTicketHarvestProxyPool(raw, previous string) (string, error) {
	proxies, err := ParseOpenAICodexTicketHarvestProxyPool(raw)
	if err != nil {
		return "", err
	}
	known := make(map[string]string)
	ambiguous := make(map[string]bool)
	for _, line := range strings.Split(previous, "\n") {
		line = strings.TrimSpace(line)
		masked := MaskProxyURL(line)
		if masked == "" {
			continue
		}
		parsed, _ := url.Parse(line)
		canonical := parsed.String()
		if prior, exists := known[masked]; exists && prior != canonical {
			ambiguous[masked] = true
		}
		known[masked] = canonical
	}
	for index, proxy := range proxies {
		if !IsMaskedProxyURL(proxy) {
			continue
		}
		identity := MaskProxyURL(proxy)
		original, exists := known[identity]
		if !exists || ambiguous[identity] {
			return "", fmt.Errorf("harvest proxy entry %d: re-enter the password for a new, changed or ambiguous proxy", index+1)
		}
		proxies[index] = original
	}
	// A literal URL and its masked equivalent may resolve to the same proxy.
	proxies, err = ParseOpenAICodexTicketHarvestProxyPool(strings.Join(proxies, "\n"))
	return strings.Join(proxies, "\n"), err
}
