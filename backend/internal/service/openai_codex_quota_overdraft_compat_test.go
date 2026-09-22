//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexQuotaOverdraftInjectionAcceptsOfficialUserMessageWithoutType(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-terra","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)

	updated, changed, err := injectCodexQuotaOverdraft(body)

	require.NoError(t, err)
	require.True(t, changed)
	var document codexQuotaOverdraftDocument
	require.NoError(t, json.Unmarshal(updated, &document))
	require.Len(t, document.Input, 3)
	var call codexQuotaOverdraftInputItem
	require.NoError(t, json.Unmarshal(document.Input[1], &call))
	require.Equal(t, "custom_tool_call", call.Type)
}

func TestCodexQuotaOverdraftInjectionRejectsNonUserFinalItemWithoutType(t *testing.T) {
	body := []byte(`{"input":[{"role":"assistant","content":[]}]}`)

	updated, changed, err := injectCodexQuotaOverdraft(body)

	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, updated)
}
