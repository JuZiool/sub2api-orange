package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequestsDecodeFeeCalculatorSyncOptIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		omitted bool
		enabled bool
	}{
		{name: "omitted", body: `{}`, omitted: true},
		{name: "enabled", body: `{"fee_calculator_sync_enabled":true}`, enabled: true},
		{name: "disabled", body: `{"fee_calculator_sync_enabled":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var create CreateGroupRequest
			require.NoError(t, json.Unmarshal([]byte(tc.body), &create))
			require.Equal(t, tc.enabled, create.FeeCalculatorSyncEnabled)

			var update UpdateGroupRequest
			require.NoError(t, json.Unmarshal([]byte(tc.body), &update))
			if tc.omitted {
				require.Nil(t, update.FeeCalculatorSyncEnabled)
			} else {
				require.NotNil(t, update.FeeCalculatorSyncEnabled)
				require.Equal(t, tc.enabled, *update.FeeCalculatorSyncEnabled)
			}
		})
	}
}
