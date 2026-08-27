package monitor

import (
	"reflect"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

func floatPtr(v float64) *float64 { return &v }

func TestUsageSampleFromResultPreservesExactBuckets(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 5, 0, 0, time.UTC)
	result := &connector.UsageResult{
		TotalAmount: floatPtr(10), TodayAmount: floatPtr(1), Currency: "USD", ObservedAt: now,
		Buckets: []connector.UsageBucketResult{{
			StartAt: now.Add(-5 * time.Minute), EndAt: now, ResolutionSeconds: 300,
			Amount: 0.2, Currency: "USD", Source: "newapi_stat",
			Quality: connector.UsageQualityExact, Complete: true,
		}},
	}

	sample := usageSampleFromResult(&storage.Channel{ID: 7, Type: storage.ChannelTypeNewAPI}, result)
	if len(sample.Buckets) != 1 {
		t.Fatalf("buckets length = %d, want 1", len(sample.Buckets))
	}
	bucket := sample.Buckets[0]
	if bucket.ChannelID != 7 || bucket.Amount != 0.2 || bucket.Quality != "exact" || bucket.CollectedAt != now {
		t.Fatalf("unexpected bucket: %#v", bucket)
	}
}

func TestUsageSampleFromResultAddsObservedFiveMinuteDelta(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 5, 12, 0, time.UTC)
	previousAt := now.Add(-5 * time.Minute)
	channel := &storage.Channel{
		ID: 9, Type: storage.ChannelTypeSub2API,
		LastUsageTotal: floatPtr(10), LastUsageAt: &previousAt,
	}
	result := &connector.UsageResult{
		TotalAmount: floatPtr(10.35), TodayAmount: floatPtr(1.2), Currency: "USD", ObservedAt: now,
	}

	sample := usageSampleFromResult(channel, result)
	if len(sample.Buckets) != 1 {
		t.Fatalf("buckets length = %d, want one observed bucket", len(sample.Buckets))
	}
	bucket := sample.Buckets[0]
	wantEnd := now.Truncate(5 * time.Minute)
	if !bucket.BucketStart.Equal(wantEnd.Add(-5*time.Minute)) || !bucket.BucketEnd.Equal(wantEnd) {
		t.Fatalf("unexpected interval: %s - %s", bucket.BucketStart, bucket.BucketEnd)
	}
	if bucket.Amount != 0.35 || bucket.Quality != "observed" || bucket.Complete != true {
		t.Fatalf("unexpected observed bucket: %#v", bucket)
	}
}

func TestUsageSampleFromResultSkipsObservedDeltaAfterCounterReset(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 5, 0, 0, time.UTC)
	previousAt := now.Add(-5 * time.Minute)
	channel := &storage.Channel{
		ID: 9, Type: storage.ChannelTypeSub2API,
		LastUsageTotal: floatPtr(10), LastUsageAt: &previousAt,
	}
	result := &connector.UsageResult{TotalAmount: floatPtr(1), Currency: "USD", ObservedAt: now}

	if buckets := usageSampleFromResult(channel, result).Buckets; len(buckets) != 0 {
		t.Fatalf("counter reset produced buckets: %#v", buckets)
	}
}

func TestUsageHistorySinceUsesOneDayWithoutWatermark(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 5, 0, 0, time.UTC)
	if got := usageHistorySince(now, nil); !got.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("usageHistorySince = %s, want one-day initial window", got)
	}
}

func TestUsageHistorySinceRefreshesLatestIncompleteHour(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 5, 0, 0, time.UTC)
	watermark := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	if got := usageHistorySince(now, &watermark); !got.Equal(watermark) {
		t.Fatalf("usageHistorySince = %s, want latest bucket start %s", got, watermark)
	}
}

type fakeRateSnapshotStore struct {
	keptChannel uint
	keptNames   []string
}

func (f *fakeRateSnapshotStore) Upsert(snapshot *storage.RateSnapshot) (*storage.RateSnapshot, error) {
	return nil, nil
}

func (f *fakeRateSnapshotStore) AppendChange(change *storage.RateChangeLog) error {
	return nil
}

func (f *fakeRateSnapshotStore) DeleteExcept(channelID uint, modelNames []string) error {
	f.keptChannel = channelID
	f.keptNames = append([]string(nil), modelNames...)
	return nil
}

func TestReconcileRatesKeepsOnlyGroupsReturnedByConnector(t *testing.T) {
	tests := []struct {
		name       string
		results    []connector.RateResult
		wantGroups []string
	}{
		{
			name: "linked groups remain",
			results: []connector.RateResult{
				{ModelName: "linked-a", Ratio: 0.5},
				{ModelName: "linked-b", Ratio: 0.8},
			},
			wantGroups: []string{"linked-a", "linked-b"},
		},
		{
			name:       "no active keys removes every group",
			results:    nil,
			wantGroups: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeRateSnapshotStore{}
			if _, err := reconcileRates(store, 7, tt.results, time.Now()); err != nil {
				t.Fatalf("reconcileRates returned error: %v", err)
			}
			if store.keptChannel != 7 {
				t.Fatalf("DeleteExcept channel = %d, want 7", store.keptChannel)
			}
			if !reflect.DeepEqual(store.keptNames, tt.wantGroups) {
				t.Fatalf("DeleteExcept groups = %#v, want %#v", store.keptNames, tt.wantGroups)
			}
		})
	}
}
