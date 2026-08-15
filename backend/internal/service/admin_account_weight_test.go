package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAccountForCreateSchedulingWeight(t *testing.T) {
	t.Run("defaults to one", func(t *testing.T) {
		account, err := buildAccountForCreate(&CreateAccountInput{}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, 1, account.Weight)
	})

	t.Run("preserves configured value", func(t *testing.T) {
		account, err := buildAccountForCreate(&CreateAccountInput{Weight: 25}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, 25, account.Weight)
	})

	t.Run("rejects values above limit", func(t *testing.T) {
		_, err := buildAccountForCreate(&CreateAccountInput{Weight: MaxAccountSchedulingWeight + 1}, map[string]any{})
		require.EqualError(t, err, "weight must be between 1 and 10000")
	})
}

func TestSchedulingWeightLegacySnapshotDefaultsToOne(t *testing.T) {
	require.Equal(t, 1, (&Account{}).SchedulingWeight())
	require.Equal(t, 7, (&Account{Weight: 7}).SchedulingWeight())
	require.Equal(t, 1, (*Account)(nil).SchedulingWeight())
}
