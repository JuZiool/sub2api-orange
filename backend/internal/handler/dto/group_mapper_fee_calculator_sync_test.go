package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupMapperExposesFeeCalculatorSyncOnlyToAdmins(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			group := &service.Group{ID: 7, Name: "calculator", FeeCalculatorSyncEnabled: enabled}
			userJSON, err := json.Marshal(GroupFromService(group))
			require.NoError(t, err)
			require.NotContains(t, string(userJSON), "fee_calculator_sync_enabled")

			adminJSON, err := json.Marshal(GroupFromServiceAdmin(group))
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(adminJSON, &decoded))
			require.Contains(t, decoded, "fee_calculator_sync_enabled")
			require.Equal(t, enabled, decoded["fee_calculator_sync_enabled"])
		})
	}
}
