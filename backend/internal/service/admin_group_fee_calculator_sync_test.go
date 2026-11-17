//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateGroupFeeCalculatorSyncIsOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default_disabled", true: "explicitly_enabled"}[enabled], func(t *testing.T) {
			repo := &groupRepoStubForAdmin{}
			svc := &adminServiceImpl{groupRepo: repo}
			input := &CreateGroupInput{Name: "calculator", Platform: PlatformOpenAI, RateMultiplier: 1}
			if enabled {
				input.FeeCalculatorSyncEnabled = true
			}

			created, err := svc.CreateGroup(context.Background(), input)

			require.NoError(t, err)
			require.NotNil(t, repo.created)
			require.Equal(t, enabled, repo.created.FeeCalculatorSyncEnabled)
			require.Equal(t, enabled, created.FeeCalculatorSyncEnabled)
		})
	}
}

func TestUpdateGroupFeeCalculatorSyncPreservesOmittedAndSupportsDisable(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name     string
		initial  bool
		input    *bool
		expected bool
	}{
		{name: "omitted_preserves_enabled", initial: true, expected: true},
		{name: "omitted_preserves_disabled", initial: false, expected: false},
		{name: "explicit_false_disables", initial: true, input: &disabled, expected: false},
		{name: "explicit_true_enables", initial: false, input: &enabled, expected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{getByID: &Group{
				ID: 1, Name: "calculator", Platform: PlatformOpenAI, Status: StatusActive,
				FeeCalculatorSyncEnabled: tc.initial,
			}}
			svc := &adminServiceImpl{groupRepo: repo}

			updated, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{
				FeeCalculatorSyncEnabled: tc.input,
			})

			require.NoError(t, err)
			require.NotNil(t, repo.updated)
			require.Equal(t, tc.expected, repo.updated.FeeCalculatorSyncEnabled)
			require.Equal(t, tc.expected, updated.FeeCalculatorSyncEnabled)
		})
	}
}
