package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/config"
	newapi "github.com/worryzyy/upstream-hub/internal/integration/newapi"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

type fakeAggregateClient struct {
	request  newapi.BillingAggregateRequest
	requests []newapi.BillingAggregateRequest
	result   newapi.BillingAggregate
	err      error
}

type setupAggregateClient struct {
	*fakeAggregateClient
	setup      newapi.Setup
	setupErr   error
	setupCalls int
}

func (f *setupAggregateClient) FetchSetup(_ context.Context) (newapi.Setup, error) {
	f.setupCalls++
	return f.setup, f.setupErr
}

type legacyDetailClient struct {
	*fakeAggregateClient
	details     newapi.BillingDetails
	detailErr   error
	detailCalls int
}

func (f *legacyDetailClient) FetchBillingDetails(_ context.Context, _ newapi.BillingDetailRequest) (newapi.BillingDetails, error) {
	f.detailCalls++
	return f.details, f.detailErr
}

type detailBillingRepository struct {
	*fakeBillingRepository
	events           []storage.NewAPIBillingEvent
	historicalRates  map[string]float64
	lookedUpEventKey []string
}

func (f *detailBillingRepository) SettleAndReplaceWindowWithEvents(_ time.Time, _ time.Time, _ int, _ []storage.NewAPIBillingBucket, events []storage.NewAPIBillingEvent, _ time.Time) error {
	f.events = events
	f.rebuildCalls++
	return nil
}

func (f *detailBillingRepository) SettleAndReplaceWindowAndAdvanceWithEvents(_ string, _ time.Time, _ time.Time, _ int, _ []storage.NewAPIBillingBucket, events []storage.NewAPIBillingEvent, state storage.BillingSyncState, _ time.Time) error {
	f.events = events
	f.atomicCalls++
	f.state = state
	return nil
}

func (f *detailBillingRepository) CreditRatesByEventKeys(keys []string) (map[string]float64, error) {
	f.lookedUpEventKey = append(f.lookedUpEventKey, keys...)
	return f.historicalRates, nil
}

func (f *fakeAggregateClient) FetchBillingAggregate(_ context.Context, request newapi.BillingAggregateRequest) (newapi.BillingAggregate, error) {
	f.request = request
	f.requests = append(f.requests, request)
	return f.result, f.err
}

type fakeBillingRepository struct {
	state           storage.BillingSyncState
	replaceCalls    int
	atomicCalls     int
	rebuildCalls    int
	replaceStart    time.Time
	replaceEnd      time.Time
	replacedBucket  []storage.NewAPIBillingBucket
	savedStates     []storage.BillingSyncState
	historicalRates map[string]float64
	lookedUpFacts   []string
	err             error
}

type mappedBillingRepository struct {
	*fakeBillingRepository
	mapping *storage.BillingMappingSnapshot
}

func (f *mappedBillingRepository) ResolveMapping(_ int, _ time.Time) (*storage.BillingMappingSnapshot, error) {
	return f.mapping, nil
}

type settlingBillingRepository struct {
	*fakeBillingRepository
	settleErr error
}

func (f *settlingBillingRepository) SettleWindow(time.Time, time.Time, []storage.NewAPIBillingBucket, time.Time) error {
	return f.settleErr
}

func (f *settlingBillingRepository) SettleAndReplaceWindowAndAdvance(string, time.Time, time.Time, int, []storage.NewAPIBillingBucket, storage.BillingSyncState, time.Time) error {
	return f.settleErr
}

func (f *fakeBillingRepository) GetSyncState(_ string) (storage.BillingSyncState, error) {
	return f.state, nil
}

func (f *fakeBillingRepository) ReplaceWindowAndAdvance(_ string, start, end time.Time, _ int, buckets []storage.NewAPIBillingBucket, state storage.BillingSyncState, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.replaceCalls++
	f.replaceStart = start
	f.replaceEnd = end
	f.replacedBucket = buckets
	f.state = state
	return nil
}

func (f *fakeBillingRepository) SettleAndReplaceWindowAndAdvance(_ string, start, end time.Time, _ int, buckets []storage.NewAPIBillingBucket, state storage.BillingSyncState, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.atomicCalls++
	f.replaceStart = start
	f.replaceEnd = end
	f.replacedBucket = buckets
	f.state = state
	return nil
}

func (f *fakeBillingRepository) SettleAndReplaceWindow(start, end time.Time, _ int, buckets []storage.NewAPIBillingBucket, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.rebuildCalls++
	f.replaceStart = start
	f.replaceEnd = end
	f.replacedBucket = buckets
	return nil
}

func (f *fakeBillingRepository) CreditRatesByFactKeys(factKeys []string) (map[string]float64, error) {
	f.lookedUpFacts = append(f.lookedUpFacts, factKeys...)
	return f.historicalRates, nil
}

func (f *fakeBillingRepository) SaveSyncState(state storage.BillingSyncState) error {
	f.savedStates = append(f.savedStates, state)
	f.state = state
	return nil
}

func TestServiceSyncUsesDelayedOverlappingWindowAndSnapshotsSale(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{
		Source: "new-api", StartAt: 0, EndAt: 0, BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
		Items: []newapi.BillingBucket{{
			BucketStart: now.Add(-90 * time.Minute).Unix(), BucketEnd: now.Add(-85 * time.Minute).Unix(), ChannelID: 12, Group: "vip", ModelName: "gpt-4o",
			EffectiveGroupRatio: 1.4, RatioSource: "group_ratio", NormalizationStatus: "exact",
			ConsumeQuota: 1400000, RefundQuota: 140000, NetQuota: 1260000, EventCount: 2,
		}},
	}}
	previousEnd := now.Add(-time.Hour)
	repo := &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd, InitialSyncCompletedAt: &previousEnd, Status: "success"}}
	service := NewService(client, repo, config.BillingConfig{
		Enabled: true, SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	wantEnd := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 8, 27, 10, 45, 0, 0, time.UTC)
	if client.request.StartAt != wantStart.Unix() || client.request.EndAt != wantEnd.Unix() || client.request.BucketSeconds != 300 {
		t.Fatalf("request = %#v, want [%d,%d,300]", client.request, wantStart.Unix(), wantEnd.Unix())
	}
	if repo.atomicCalls != 1 || repo.replaceCalls != 0 || !repo.replaceStart.Equal(wantStart) || !repo.replaceEnd.Equal(wantEnd) {
		t.Fatalf("commit calls = atomic:%d legacy:%d [%v,%v]", repo.atomicCalls, repo.replaceCalls, repo.replaceStart, repo.replaceEnd)
	}
	if len(repo.replacedBucket) != 1 || repo.replacedBucket[0].SaleCNY != 0.21 || repo.replacedBucket[0].CreditUSDPerCNY != 12 {
		t.Fatalf("stored bucket = %#v", repo.replacedBucket)
	}
	if repo.state.LastSuccessfulEndAt == nil || !repo.state.LastSuccessfulEndAt.Equal(wantEnd) || repo.state.Status != "success" {
		t.Fatalf("state = %#v", repo.state)
	}
}

func TestServiceSyncCommitsFactsProfitAndWatermarkAtomically(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{
		Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
	}}
	previousEnd := now.Add(-time.Hour)
	repo := &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd}}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if repo.atomicCalls != 1 {
		t.Fatalf("atomic commit calls = %d, want 1", repo.atomicCalls)
	}
	if repo.replaceCalls != 0 {
		t.Fatalf("legacy replacement calls = %d, want 0", repo.replaceCalls)
	}
}

func TestServiceSyncFallsBackToAggregateWhenDetailsEndpointIsUnsupported(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	client := &legacyDetailClient{
		fakeAggregateClient: &fakeAggregateClient{result: newapi.BillingAggregate{
			Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
		}},
		detailErr: errors.New("new-api request /api/internal/upstream-hub/billing/details: status 404"),
	}
	previousEnd := now.Add(-time.Hour)
	repo := &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd}}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if client.detailCalls != 1 {
		t.Fatalf("detail calls = %d, want 1", client.detailCalls)
	}
	if repo.atomicCalls != 1 {
		t.Fatalf("atomic commit calls = %d, want 1", repo.atomicCalls)
	}
}

func TestServiceSyncCheckpointsInitialWindowWhenDetailsTimesOut(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	end := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	start := end.Add(-240 * time.Hour)
	client := &legacyDetailClient{
		fakeAggregateClient: &fakeAggregateClient{result: newapi.BillingAggregate{
			Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
		}},
		detailErr: context.DeadlineExceeded,
	}
	repo := &fakeBillingRepository{}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, InitialLookbackHours: 240, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	firstWindowEnd := start.Add(24 * time.Hour)
	if client.detailCalls != 1 {
		t.Fatalf("detail calls = %d, want 1", client.detailCalls)
	}
	if repo.atomicCalls != 1 || !repo.replaceStart.Equal(start) || !repo.replaceEnd.Equal(firstWindowEnd) {
		t.Fatalf("commit = calls:%d window:[%v,%v], want [%v,%v]", repo.atomicCalls, repo.replaceStart, repo.replaceEnd, start, firstWindowEnd)
	}
	if repo.state.Status != "running" || repo.state.LastSuccessfulEndAt == nil || !repo.state.LastSuccessfulEndAt.Equal(firstWindowEnd) {
		t.Fatalf("checkpoint state = %#v", repo.state)
	}
}

func TestServiceSyncPreservesHistoricalCreditRateForBillingEvents(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	createdAt := time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC)
	client := &legacyDetailClient{
		fakeAggregateClient: &fakeAggregateClient{result: newapi.BillingAggregate{
			Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
			Items: []newapi.BillingBucket{{
				BucketStart: createdAt.Truncate(5 * time.Minute).Unix(), BucketEnd: createdAt.Truncate(5 * time.Minute).Add(5 * time.Minute).Unix(),
				ChannelID: 12, Group: "vip", ModelName: "gpt-4o", EffectiveGroupRatio: 1.4,
				RatioSource: "group_ratio", NormalizationStatus: storage.BillingStatusExact, ConsumeQuota: 1400000,
			}},
		}},
		details: newapi.BillingDetails{Complete: true, Items: []newapi.BillingEvent{{
			SourceLogID: 123, CreatedAt: createdAt.Unix(), EventType: "consume", ChannelID: 12,
			Group: "vip", ModelName: "gpt-4o", EffectiveGroupRatio: 1.4,
			RatioSource: "group_ratio", NormalizationStatus: storage.BillingStatusExact, Quota: 1400000,
		}}},
	}
	previousEnd := now.Add(-time.Hour)
	repo := &detailBillingRepository{
		fakeBillingRepository: &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd}},
		historicalRates:       map[string]float64{"log-123": 12},
	}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 10,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if len(repo.lookedUpEventKey) != 1 || repo.lookedUpEventKey[0] != "log-123" {
		t.Fatalf("event rate lookup = %#v, want log-123", repo.lookedUpEventKey)
	}
	if len(repo.events) != 1 || repo.events[0].CreditUSDPerCNY != 12 {
		t.Fatalf("event credit rate = %#v, want 12", repo.events)
	}
	if repo.events[0].SaleCNY != 2.8/12.0 {
		t.Fatalf("event sale = %v, want %v", repo.events[0].SaleCNY, 2.8/12.0)
	}
}

func TestServiceSyncPreservesHistoricalCreditRateDuringOverlap(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	bucketStart := time.Date(2026, 8, 27, 10, 45, 0, 0, time.UTC)
	bucketEnd := bucketStart.Add(5 * time.Minute)
	seed, err := storage.BuildNewAPIBillingBucket(storage.BillingFactInput{
		BucketStart: bucketStart, BucketEnd: bucketEnd, NewAPIChannelID: 12,
		Group: "vip", ModelName: "gpt-4o", EffectiveGroupRatio: 1.4,
		RatioSource: "group_ratio", NormalizationStatus: storage.BillingStatusExact,
		ConsumeQuota: 1400000, RefundQuota: 140000, EventCount: 2,
		QuotaPerUnit: 500000, Complete: true,
	}, 12, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeAggregateClient{result: newapi.BillingAggregate{
		Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
		Items: []newapi.BillingBucket{{
			BucketStart: bucketStart.Unix(), BucketEnd: bucketEnd.Unix(), ChannelID: 12,
			Group: "vip", ModelName: "gpt-4o", EffectiveGroupRatio: 1.4,
			RatioSource: "group_ratio", NormalizationStatus: storage.BillingStatusExact,
			ConsumeQuota: 1400000, RefundQuota: 140000, EventCount: 2,
		}},
	}}
	previousEnd := time.Date(2026, 8, 27, 11, 15, 0, 0, time.UTC)
	repo := &fakeBillingRepository{
		state:           storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd},
		historicalRates: map[string]float64{seed.FactKey: 12},
	}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 10,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if len(repo.lookedUpFacts) != 1 || repo.lookedUpFacts[0] != seed.FactKey {
		t.Fatalf("historical lookup = %#v, want %q", repo.lookedUpFacts, seed.FactKey)
	}
	if len(repo.replacedBucket) != 1 || repo.replacedBucket[0].CreditUSDPerCNY != 12 || repo.replacedBucket[0].SaleCNY != 0.21 {
		t.Fatalf("overlap bucket = %#v, want original rate and sale", repo.replacedBucket)
	}
}

func TestServiceSyncDoesNotAdvanceWatermarkWhenSourceFails(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	client := &fakeAggregateClient{err: errors.New("source unavailable")}
	end := now.Add(-time.Hour)
	repo := &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &end, InitialSyncCompletedAt: &end, Status: "success"}}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err == nil || !errors.Is(err, client.err) {
		t.Fatalf("Sync error = %v, want source error", err)
	}
	if repo.replaceCalls != 0 {
		t.Fatalf("replacement calls = %d, want 0", repo.replaceCalls)
	}
	if len(repo.savedStates) != 1 || repo.savedStates[0].Status != "failed" || repo.savedStates[0].LastSuccessfulEndAt == nil || !repo.savedStates[0].LastSuccessfulEndAt.Equal(end) {
		t.Fatalf("failure state = %#v", repo.savedStates)
	}
}

func TestServiceSyncDoesNotAdvanceWatermarkWhenAtomicCommitFails(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{
		Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
	}}
	previousEnd := now.Add(-time.Hour)
	previousSuccess := now.Add(-2 * time.Hour)
	repo := &fakeBillingRepository{
		state: storage.BillingSyncState{
			Source:                 billingSource,
			LastSuccessfulEndAt:    &previousEnd,
			InitialSyncCompletedAt: &previousSuccess,
			LastSuccessAt:          &previousSuccess,
			Status:                 "success",
		},
		err: errors.New("database write failed"),
	}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err == nil || !errors.Is(err, repo.err) {
		t.Fatalf("Sync error = %v, want replacement error", err)
	}
	if len(repo.savedStates) != 1 {
		t.Fatalf("saved states = %d, want 1", len(repo.savedStates))
	}
	failure := repo.savedStates[0]
	if failure.Status != "failed" || failure.LastAttemptAt == nil || !failure.LastAttemptAt.Equal(now) {
		t.Fatalf("failure state = %#v", failure)
	}
	if failure.LastSuccessfulEndAt == nil || !failure.LastSuccessfulEndAt.Equal(previousEnd) {
		t.Fatalf("successful watermark changed to %v, want %v", failure.LastSuccessfulEndAt, previousEnd)
	}
	if failure.LastSuccessAt == nil || !failure.LastSuccessAt.Equal(previousSuccess) {
		t.Fatalf("last success changed to %v, want %v", failure.LastSuccessAt, previousSuccess)
	}
}

func TestServiceSyncAppliesHistoricalMappingToBillingBucket(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	upstreamID := uint(42)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{
		Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
		Items: []newapi.BillingBucket{{
			BucketStart: now.Add(-90 * time.Minute).Unix(), BucketEnd: now.Add(-85 * time.Minute).Unix(),
			ChannelID: 12, Group: "vip", ModelName: "gpt-4o", EffectiveGroupRatio: 1.4,
			RatioSource: "group_ratio", NormalizationStatus: "exact", ConsumeQuota: 1400000,
		}},
	}}
	previousEnd := now.Add(-time.Hour)
	repo := &mappedBillingRepository{
		fakeBillingRepository: &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd}},
		mapping:               &storage.BillingMappingSnapshot{NewAPIChannelID: 12, UpstreamChannelID: &upstreamID, MappingStatus: "mapped"},
	}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if len(repo.replacedBucket) != 1 {
		t.Fatalf("stored buckets = %d, want 1", len(repo.replacedBucket))
	}
	bucket := repo.replacedBucket[0]
	if bucket.MappingStatus != "mapped" || bucket.UpstreamChannelID == nil || *bucket.UpstreamChannelID != upstreamID {
		t.Fatalf("mapping = status %q upstream=%v", bucket.MappingStatus, bucket.UpstreamChannelID)
	}
}

func TestServiceRebuildAppliesMappingWithoutAdvancingSyncWatermark(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	start := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	upstreamID := uint(4)
	previousEnd := now.Add(-time.Hour)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{
		Source: "new-api", BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true,
		Items: []newapi.BillingBucket{{
			BucketStart: start.Unix(), BucketEnd: start.Add(5 * time.Minute).Unix(), ChannelID: 27,
			Group: "default", ModelName: "gpt-5", EffectiveGroupRatio: 1.4,
			RatioSource: "group_ratio", NormalizationStatus: storage.BillingStatusExact,
			ConsumeQuota: 700000,
		}},
	}}
	repo := &mappedBillingRepository{
		fakeBillingRepository: &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd, Status: "success"}},
		mapping:               &storage.BillingMappingSnapshot{NewAPIChannelID: 27, UpstreamChannelID: &upstreamID, MappingStatus: "mapped"},
	}
	service := NewService(client, repo, config.BillingConfig{CreditUSDPerCNY: 12}, nowFunc(now))

	if err := service.Rebuild(context.Background(), start, end); err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	if client.request.StartAt != start.Unix() || client.request.EndAt != end.Unix() || client.request.BucketSeconds != storage.BillingResolutionSeconds {
		t.Fatalf("aggregate request = %#v", client.request)
	}
	if repo.rebuildCalls != 1 || repo.atomicCalls != 0 || !repo.replaceStart.Equal(start) || !repo.replaceEnd.Equal(end) {
		t.Fatalf("rebuild calls = rebuild:%d sync:%d window=[%v,%v]", repo.rebuildCalls, repo.atomicCalls, repo.replaceStart, repo.replaceEnd)
	}
	if repo.state.LastSuccessfulEndAt == nil || !repo.state.LastSuccessfulEndAt.Equal(previousEnd) {
		t.Fatalf("sync watermark changed: %#v", repo.state)
	}
	if len(repo.replacedBucket) != 1 || repo.replacedBucket[0].MappingStatus != "mapped" || repo.replacedBucket[0].UpstreamChannelID == nil || *repo.replacedBucket[0].UpstreamChannelID != upstreamID {
		t.Fatalf("rebuilt bucket = %#v", repo.replacedBucket)
	}
}

func TestServiceRebuildSplitsRequestsAtNewAPIMaximumRange(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(8 * 24 * time.Hour)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{QuotaPerUnit: 500000, Complete: true}}
	repo := &fakeBillingRepository{}
	service := NewService(client, repo, config.BillingConfig{CreditUSDPerCNY: 12}, nowFunc(end.Add(time.Hour)))

	if err := service.Rebuild(context.Background(), start, end); err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("aggregate requests = %#v, want 2", client.requests)
	}
	firstEnd := start.Add(time.Duration(newapi.BillingMaxRangeSeconds) * time.Second)
	if client.requests[0] != (newapi.BillingAggregateRequest{StartAt: start.Unix(), EndAt: firstEnd.Unix(), BucketSeconds: storage.BillingResolutionSeconds}) {
		t.Fatalf("first request = %#v", client.requests[0])
	}
	if client.requests[1] != (newapi.BillingAggregateRequest{StartAt: firstEnd.Unix(), EndAt: end.Unix(), BucketSeconds: storage.BillingResolutionSeconds}) {
		t.Fatalf("second request = %#v", client.requests[1])
	}
	if repo.rebuildCalls != 2 || repo.atomicCalls != 0 {
		t.Fatalf("commit calls = rebuild:%d sync:%d", repo.rebuildCalls, repo.atomicCalls)
	}
}

func TestServiceSyncCheckpointsConfiguredInitialLookbackByDay(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 17, 0, 0, time.UTC)
	end := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	start := end.Add(-48 * time.Hour)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{QuotaPerUnit: 500000, Complete: true}}
	repo := &fakeBillingRepository{}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, InitialLookbackHours: 48, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	firstEnd := start.Add(24 * time.Hour)
	if len(client.requests) != 1 {
		t.Fatalf("aggregate requests = %#v, want one initial checkpoint", client.requests)
	}
	if client.requests[0] != (newapi.BillingAggregateRequest{StartAt: start.Unix(), EndAt: firstEnd.Unix(), BucketSeconds: storage.BillingResolutionSeconds}) {
		t.Fatalf("first aggregate request = %#v", client.requests[0])
	}
	if repo.state.Status != "running" || repo.state.InitialSyncCompletedAt != nil {
		t.Fatalf("first checkpoint state = %#v", repo.state)
	}

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync returned error: %v", err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("aggregate requests = %#v, want two checkpoints", client.requests)
	}
	if client.requests[1] != (newapi.BillingAggregateRequest{StartAt: firstEnd.Unix(), EndAt: end.Unix(), BucketSeconds: storage.BillingResolutionSeconds}) {
		t.Fatalf("second aggregate request = %#v", client.requests[1])
	}
	if repo.atomicCalls != 2 || !repo.replaceStart.Equal(firstEnd) || !repo.replaceEnd.Equal(end) {
		t.Fatalf("commit = calls:%d window:[%v,%v]", repo.atomicCalls, repo.replaceStart, repo.replaceEnd)
	}
	if repo.state.Status != "success" || repo.state.InitialSyncCompletedAt == nil {
		t.Fatalf("final checkpoint state = %#v", repo.state)
	}
}

func TestServiceSyncUsesNewAPISetupInitializationTimeForInitialSync(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 17, 0, 0, time.UTC)
	initializedAt := time.Date(2026, 8, 17, 3, 17, 0, 0, time.UTC)
	client := &setupAggregateClient{
		fakeAggregateClient: &fakeAggregateClient{result: newapi.BillingAggregate{QuotaPerUnit: 500000, Complete: true}},
		setup:               newapi.Setup{InitializedAt: initializedAt.Unix()},
	}
	repo := &fakeBillingRepository{}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	wantStart := initializedAt.Truncate(time.Duration(storage.BillingResolutionSeconds) * time.Second)
	wantWindowEnd := wantStart.Add(billingSyncWindow)
	if client.setupCalls != 1 {
		t.Fatalf("setup calls = %d, want 1", client.setupCalls)
	}
	if client.requests[0].StartAt != wantStart.Unix() || client.requests[0].EndAt != wantWindowEnd.Unix() {
		t.Fatalf("aggregate request = %#v, want start=%d end=%d", client.requests[0], wantStart.Unix(), wantWindowEnd.Unix())
	}
	if !repo.replaceStart.Equal(wantStart) || !repo.replaceEnd.Equal(wantWindowEnd) {
		t.Fatalf("committed window = [%v,%v], want [%v,%v]", repo.replaceStart, repo.replaceEnd, wantStart, wantWindowEnd)
	}
	if repo.state.Status != "running" || repo.state.InitialSyncCompletedAt != nil {
		t.Fatalf("initial sync state = %#v, want running checkpoint", repo.state)
	}
}

func TestServiceSyncBackfillsExistingWatermarkFromNewAPISetupInitializationTime(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 17, 0, 0, time.UTC)
	end := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	initializedAt := end.Add(-30 * time.Hour)
	previousEnd := end.Add(-time.Hour)
	client := &setupAggregateClient{
		fakeAggregateClient: &fakeAggregateClient{result: newapi.BillingAggregate{QuotaPerUnit: 500000, Complete: true}},
		setup:               newapi.Setup{InitializedAt: initializedAt.Unix()},
	}
	repo := &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd, Status: "success"}}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("initial backfill returned error: %v", err)
	}
	wantStart := initializedAt.Truncate(time.Duration(storage.BillingResolutionSeconds) * time.Second)
	wantFirstEnd := wantStart.Add(billingSyncWindow)
	if client.requests[0].StartAt != wantStart.Unix() || client.requests[0].EndAt != wantFirstEnd.Unix() {
		t.Fatalf("backfill request = %#v, want start=%d end=%d", client.requests[0], wantStart.Unix(), wantFirstEnd.Unix())
	}
	if repo.state.InitialSyncCompletedAt != nil || repo.state.Status != "running" {
		t.Fatalf("initial sync checkpoint = %#v, want running", repo.state)
	}

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("incremental sync returned error: %v", err)
	}
	if len(client.requests) != 2 || client.requests[1].StartAt != wantFirstEnd.Unix() || client.requests[1].EndAt != end.Unix() {
		t.Fatalf("incremental request = %#v, want start=%d end=%d", client.requests, wantFirstEnd.Unix(), end.Unix())
	}
	if repo.state.InitialSyncCompletedAt == nil || repo.state.Status != "success" {
		t.Fatalf("final checkpoint = %#v, want success", repo.state)
	}
	if client.setupCalls != 1 {
		t.Fatalf("setup calls = %d, want one setup call", client.setupCalls)
	}
}

func TestServiceSyncFallsBackToConfiguredLookbackWhenSetupUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 17, 0, 0, time.UTC)
	end := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	client := &setupAggregateClient{
		fakeAggregateClient: &fakeAggregateClient{result: newapi.BillingAggregate{QuotaPerUnit: 500000, Complete: true}},
		setupErr:            errors.New("new-api setup: status 404"),
	}
	repo := &fakeBillingRepository{}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, InitialLookbackHours: 48, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	wantStart := end.Add(-48 * time.Hour)
	wantWindowEnd := wantStart.Add(billingSyncWindow)
	if client.setupCalls != 1 {
		t.Fatalf("setup calls = %d, want 1", client.setupCalls)
	}
	if client.requests[0].StartAt != wantStart.Unix() || client.requests[0].EndAt != wantWindowEnd.Unix() {
		t.Fatalf("aggregate request = %#v, want start=%d end=%d", client.requests[0], wantStart.Unix(), wantWindowEnd.Unix())
	}
}

func TestBuildMappingInputsRecordsMappedAmbiguousAndUnmappedChannels(t *testing.T) {
	items := buildMappingInputs(
		[]newapi.Identity{{ChannelID: 2}, {ChannelID: 1}, {ChannelID: 3}},
		[]newapi.Metric{
			{ChannelID: 1, UpstreamChannelID: 9, Group: "vip"},
			{ChannelID: 2, UpstreamChannelID: 9, Group: "cheap"},
			{ChannelID: 2, UpstreamChannelID: 10, Group: "cheap"},
		},
	)
	if len(items) != 3 {
		t.Fatalf("mapping items = %d, want 3", len(items))
	}
	if items[0].NewAPIChannelID != 1 || items[0].MappingStatus != "mapped" || items[0].UpstreamChannelID == nil || *items[0].UpstreamChannelID != 9 {
		t.Fatalf("channel 1 mapping = %#v", items[0])
	}
	if items[1].NewAPIChannelID != 2 || items[1].MappingStatus != "ambiguous" || items[1].UpstreamChannelID != nil {
		t.Fatalf("channel 2 mapping = %#v", items[1])
	}
	if items[2].NewAPIChannelID != 3 || items[2].MappingStatus != "unmapped" {
		t.Fatalf("channel 3 mapping = %#v", items[2])
	}
}

func TestServiceSyncDoesNotAdvanceWatermarkWhenSettlementFails(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	client := &fakeAggregateClient{result: newapi.BillingAggregate{BucketSeconds: 300, QuotaPerUnit: 500000, Complete: true}}
	previousEnd := now.Add(-time.Hour)
	settleErr := errors.New("usage query failed")
	repo := &settlingBillingRepository{
		fakeBillingRepository: &fakeBillingRepository{state: storage.BillingSyncState{Source: billingSource, LastSuccessfulEndAt: &previousEnd, InitialSyncCompletedAt: &previousEnd}},
		settleErr:             settleErr,
	}
	service := NewService(client, repo, config.BillingConfig{
		SettlementDelayMinutes: 15, OverlapMinutes: 30, InitialLookbackHours: 24, CreditUSDPerCNY: 12,
	}, nowFunc(now))

	if err := service.Sync(context.Background()); err == nil || !errors.Is(err, settleErr) {
		t.Fatalf("Sync error = %v, want settlement error", err)
	}
	if repo.replaceCalls != 0 {
		t.Fatalf("replace calls = %d, want 0", repo.replaceCalls)
	}
	if len(repo.savedStates) != 1 || repo.savedStates[0].LastSuccessfulEndAt == nil || !repo.savedStates[0].LastSuccessfulEndAt.Equal(previousEnd) || repo.savedStates[0].Status != "failed" {
		t.Fatalf("failure state = %#v", repo.savedStates)
	}
}

func nowFunc(now time.Time) func() time.Time { return func() time.Time { return now } }
