//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type schedulerOverdraftContextCache struct {
	SchedulerCache
	buckets []SchedulerBucket
}

func (c *schedulerOverdraftContextCache) GetSnapshot(_ context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	c.buckets = append(c.buckets, bucket)
	return []*Account{{ID: 1, Platform: PlatformOpenAI}}, true, nil
}

func TestSchedulerSnapshotUsesDistinctCodexOverdraftBucket(t *testing.T) {
	t.Cleanup(func() { SetCodexQuotaOverdraftEnabled(false) })
	SetCodexQuotaOverdraftEnabled(true)
	cache := &schedulerOverdraftContextCache{}
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)

	_, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	_, _, err = svc.ListSchedulableAccounts(WithCodexQuotaOverdraftScheduling(context.Background()), nil, PlatformOpenAI, false)
	require.NoError(t, err)

	require.Len(t, cache.buckets, 2)
	require.Empty(t, cache.buckets[0].Context)
	require.Equal(t, SchedulerContextCodexOverdraft, cache.buckets[1].Context)
	require.NotEqual(t, cache.buckets[0].String(), cache.buckets[1].String())
	parsed, ok := ParseSchedulerBucket(cache.buckets[1].String())
	require.True(t, ok)
	require.Equal(t, cache.buckets[1], parsed)
}

func TestSchedulerCanonicalBucketsIncludeCodexOverdraftContext(t *testing.T) {
	t.Cleanup(func() { SetCodexQuotaOverdraftEnabled(false) })
	SetCodexQuotaOverdraftEnabled(true)
	buckets := schedulerCanonicalBuckets(17)
	require.Contains(t, buckets, SchedulerBucket{GroupID: 17, Platform: PlatformOpenAI, Mode: SchedulerModeSingle, Context: SchedulerContextCodexOverdraft})
	require.Contains(t, buckets, SchedulerBucket{GroupID: 17, Platform: PlatformOpenAI, Mode: SchedulerModeForced, Context: SchedulerContextCodexOverdraft})
}
