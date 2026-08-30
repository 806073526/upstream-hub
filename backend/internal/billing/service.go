package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/worryzyy/upstream-hub/internal/config"
	newapi "github.com/worryzyy/upstream-hub/internal/integration/newapi"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

const (
	billingSource     = "new-api"
	billingSyncWindow = 24 * time.Hour
)

type AggregateClient interface {
	FetchBillingAggregate(context.Context, newapi.BillingAggregateRequest) (newapi.BillingAggregate, error)
}

type setupClient interface {
	FetchSetup(context.Context) (newapi.Setup, error)
}

type DetailClient interface {
	FetchBillingDetails(context.Context, newapi.BillingDetailRequest) (newapi.BillingDetails, error)
}

type Repository interface {
	GetSyncState(string) (storage.BillingSyncState, error)
	SettleAndReplaceWindow(time.Time, time.Time, int, []storage.NewAPIBillingBucket, time.Time) error
	SettleAndReplaceWindowAndAdvance(string, time.Time, time.Time, int, []storage.NewAPIBillingBucket, storage.BillingSyncState, time.Time) error
	SaveSyncState(storage.BillingSyncState) error
}

type detailRepository interface {
	SettleAndReplaceWindowWithEvents(time.Time, time.Time, int, []storage.NewAPIBillingBucket, []storage.NewAPIBillingEvent, time.Time) error
	SettleAndReplaceWindowAndAdvanceWithEvents(string, time.Time, time.Time, int, []storage.NewAPIBillingBucket, []storage.NewAPIBillingEvent, storage.BillingSyncState, time.Time) error
}

type personalUsageRepository interface {
	SettleAndReplaceWindowWithPersonalUsage(time.Time, time.Time, int, []storage.NewAPIBillingBucket, []storage.NewAPIPersonalUsageBucket, time.Time) error
	SettleAndReplaceWindowAndAdvanceWithPersonalUsage(string, time.Time, time.Time, int, []storage.NewAPIBillingBucket, []storage.NewAPIPersonalUsageBucket, storage.BillingSyncState, time.Time) error
}

type personalUsageDetailRepository interface {
	SettleAndReplaceWindowWithEventsAndPersonalUsage(time.Time, time.Time, int, []storage.NewAPIBillingBucket, []storage.NewAPIBillingEvent, []storage.NewAPIPersonalUsageBucket, time.Time) error
	SettleAndReplaceWindowAndAdvanceWithEventsAndPersonalUsage(string, time.Time, time.Time, int, []storage.NewAPIBillingBucket, []storage.NewAPIBillingEvent, []storage.NewAPIPersonalUsageBucket, storage.BillingSyncState, time.Time) error
}

type mappingResolver interface {
	ResolveMapping(int, time.Time) (*storage.BillingMappingSnapshot, error)
}

type mappingSnapshotSaver interface {
	SaveMappingSnapshots([]storage.BillingMappingInput, time.Time) error
}

type historicalCreditRateResolver interface {
	CreditRatesByFactKeys([]string) (map[string]float64, error)
}

type historicalEventCreditRateResolver interface {
	CreditRatesByEventKeys([]string) (map[string]float64, error)
}

type Service struct {
	client AggregateClient
	repo   Repository
	cfg    config.BillingConfig
	now    func() time.Time
}

func NewService(client AggregateClient, repo Repository, cfg config.BillingConfig, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{client: client, repo: repo, cfg: cfg, now: now}
}

func (s *Service) Sync(ctx context.Context) error {
	if s == nil || s.client == nil || s.repo == nil {
		return errors.New("billing service is not configured")
	}
	now := s.now().UTC()
	state, err := s.repo.GetSyncState(billingSource)
	if err != nil {
		return err
	}
	state.Source = billingSource
	currentState := state

	markFailure := func(cause error) error {
		failureState := currentState
		failureState.Source = billingSource
		failureState.LastAttemptAt = &now
		failureState.Status = "failed"
		failureState.LastError = cause.Error()
		failureState.UpdatedAt = now
		_ = s.repo.SaveSyncState(failureState)
		return cause
	}

	end := now.Truncate(time.Duration(storage.BillingResolutionSeconds) * time.Second).
		Add(-time.Duration(s.cfg.SettlementDelayMinutes) * time.Minute)
	if end.IsZero() || !end.After(time.Unix(0, 0)) {
		return markFailure(errors.New("billing settlement window has no complete buckets"))
	}

	initialSync := state.LastSuccessfulEndAt == nil || state.LastSuccessfulEndAt.IsZero() ||
		state.InitialSyncCompletedAt == nil || state.InitialSyncCompletedAt.IsZero()
	var start, targetEnd time.Time
	if initialSync {
		if state.LastSuccessfulEndAt != nil && !state.LastSuccessfulEndAt.IsZero() &&
			state.InitialSyncStartedAt != nil && !state.InitialSyncStartedAt.IsZero() &&
			state.InitialSyncTargetEndAt != nil && !state.InitialSyncTargetEndAt.IsZero() {
			start = *state.LastSuccessfulEndAt
			targetEnd = *state.InitialSyncTargetEndAt
		} else {
			if s.cfg.InitialLookbackHours <= 0 {
				return markFailure(errors.New("billing initialLookbackHours must be positive"))
			}
			start = end.Add(-time.Duration(s.cfg.InitialLookbackHours) * time.Hour)
			if setupProvider, ok := s.client.(setupClient); ok {
				if setup, setupErr := setupProvider.FetchSetup(ctx); setupErr == nil && setup.InitializedAt > 0 {
					initializedAt := time.Unix(setup.InitializedAt, 0).UTC()
					if initializedAt.Before(end) {
						start = initializedAt
					}
				}
			}
			targetEnd = end
			start = start.UTC().Truncate(time.Duration(storage.BillingResolutionSeconds) * time.Second)
			startedAt := start
			checkpoint := state
			checkpoint.InitialSyncStartedAt = &startedAt
			checkpoint.InitialSyncTargetEndAt = &targetEnd
			checkpoint.LastSuccessfulEndAt = &startedAt
			checkpoint.LastAttemptAt = &now
			checkpoint.Status = "running"
			checkpoint.LastError = ""
			checkpoint.UpdatedAt = now
			if err := s.repo.SaveSyncState(checkpoint); err != nil {
				return err
			}
			state = checkpoint
			currentState = checkpoint
		}
	} else {
		start = state.LastSuccessfulEndAt.Add(-time.Duration(s.cfg.OverlapMinutes) * time.Minute)
		targetEnd = end
	}
	start = start.UTC().Truncate(time.Duration(storage.BillingResolutionSeconds) * time.Second)
	targetEnd = targetEnd.UTC().Truncate(time.Duration(storage.BillingResolutionSeconds) * time.Second)
	if !start.Before(targetEnd) {
		return markFailure(errors.New("billing settlement window is empty"))
	}
	windowEnd := start.Add(billingSyncWindow)
	if windowEnd.After(targetEnd) {
		windowEnd = targetEnd
	}

	buckets, personal, err := s.collectBuckets(ctx, start, windowEnd, now)
	if err != nil {
		return markFailure(err)
	}
	events, detailsAvailable, err := s.collectEvents(ctx, start, windowEnd, aggregateQuotaPerUnit(buckets), now)
	if err != nil {
		return markFailure(err)
	}

	successState := currentState
	successState.Source = billingSource
	successState.LastAttemptAt = &now
	successState.Status = "success"
	successState.LastError = ""
	successState.LastSuccessAt = &now
	successState.LastSuccessfulEndAt = &windowEnd
	if initialSync && windowEnd.Before(targetEnd) {
		successState.Status = "running"
	} else if initialSync {
		initialSyncCompletedAt := now
		successState.InitialSyncCompletedAt = &initialSyncCompletedAt
	}
	successState.UpdatedAt = now
	var commitErr error
	if detailsAvailable {
		if repo, ok := s.repo.(personalUsageDetailRepository); ok {
			commitErr = repo.SettleAndReplaceWindowAndAdvanceWithEventsAndPersonalUsage(billingSource, start, windowEnd, storage.BillingResolutionSeconds, buckets, events, personal, successState, now)
		} else if repo, ok := s.repo.(detailRepository); ok {
			commitErr = repo.SettleAndReplaceWindowAndAdvanceWithEvents(billingSource, start, windowEnd, storage.BillingResolutionSeconds, buckets, events, successState, now)
		} else if repo, ok := s.repo.(personalUsageRepository); ok {
			commitErr = repo.SettleAndReplaceWindowAndAdvanceWithPersonalUsage(billingSource, start, windowEnd, storage.BillingResolutionSeconds, buckets, personal, successState, now)
		} else {
			commitErr = s.repo.SettleAndReplaceWindowAndAdvance(billingSource, start, windowEnd, storage.BillingResolutionSeconds, buckets, successState, now)
		}
	} else {
		if repo, ok := s.repo.(personalUsageRepository); ok {
			commitErr = repo.SettleAndReplaceWindowAndAdvanceWithPersonalUsage(billingSource, start, windowEnd, storage.BillingResolutionSeconds, buckets, personal, successState, now)
		} else {
			commitErr = s.repo.SettleAndReplaceWindowAndAdvance(billingSource, start, windowEnd, storage.BillingResolutionSeconds, buckets, successState, now)
		}
	}
	if commitErr != nil {
		return markFailure(commitErr)
	}
	return nil
}

// Rebuild recalculates already-settled source windows without modifying the
// normal incremental-sync watermark. Large requests are divided to fit the
// NewAPI aggregation endpoint's seven-day maximum range.
func (s *Service) Rebuild(ctx context.Context, start, end time.Time) error {
	if s == nil || s.client == nil || s.repo == nil {
		return errors.New("billing service is not configured")
	}
	resolution := time.Duration(storage.BillingResolutionSeconds) * time.Second
	start = start.UTC().Truncate(resolution)
	end = end.UTC().Truncate(resolution)
	if !start.Before(end) {
		return errors.New("billing rebuild window is empty")
	}
	now := s.now().UTC()
	settledEnd := now.Truncate(resolution).Add(-time.Duration(s.cfg.SettlementDelayMinutes) * time.Minute)
	if end.After(settledEnd) {
		return errors.New("billing rebuild window includes unsettled buckets")
	}

	maxRange := time.Duration(newapi.BillingMaxRangeSeconds) * time.Second
	for windowStart := start; windowStart.Before(end); {
		windowEnd := windowStart.Add(maxRange)
		if windowEnd.After(end) {
			windowEnd = end
		}
		buckets, personal, err := s.collectBuckets(ctx, windowStart, windowEnd, now)
		if err != nil {
			return err
		}
		events, detailsAvailable, err := s.collectEvents(ctx, windowStart, windowEnd, aggregateQuotaPerUnit(buckets), now)
		if err != nil {
			return err
		}
		var commitErr error
		if detailsAvailable {
			if repo, ok := s.repo.(personalUsageDetailRepository); ok {
				commitErr = repo.SettleAndReplaceWindowWithEventsAndPersonalUsage(windowStart, windowEnd, storage.BillingResolutionSeconds, buckets, events, personal, now)
			} else if repo, ok := s.repo.(detailRepository); ok {
				commitErr = repo.SettleAndReplaceWindowWithEvents(windowStart, windowEnd, storage.BillingResolutionSeconds, buckets, events, now)
			} else if repo, ok := s.repo.(personalUsageRepository); ok {
				commitErr = repo.SettleAndReplaceWindowWithPersonalUsage(windowStart, windowEnd, storage.BillingResolutionSeconds, buckets, personal, now)
			} else {
				commitErr = s.repo.SettleAndReplaceWindow(windowStart, windowEnd, storage.BillingResolutionSeconds, buckets, now)
			}
		} else {
			if repo, ok := s.repo.(personalUsageRepository); ok {
				commitErr = repo.SettleAndReplaceWindowWithPersonalUsage(windowStart, windowEnd, storage.BillingResolutionSeconds, buckets, personal, now)
			} else {
				commitErr = s.repo.SettleAndReplaceWindow(windowStart, windowEnd, storage.BillingResolutionSeconds, buckets, now)
			}
		}
		if commitErr != nil {
			return commitErr
		}
		windowStart = windowEnd
	}
	return nil
}

func (s *Service) collectBuckets(ctx context.Context, start, end, now time.Time) ([]storage.NewAPIBillingBucket, []storage.NewAPIPersonalUsageBucket, error) {
	aggregate, err := s.client.FetchBillingAggregate(ctx, newapi.BillingAggregateRequest{
		StartAt: start.Unix(), EndAt: end.Unix(), BucketSeconds: storage.BillingResolutionSeconds,
	})
	if err != nil {
		return nil, nil, err
	}
	if aggregate.QuotaPerUnit <= 0 {
		return nil, nil, errors.New("new-api billing aggregate has invalid quota_per_unit")
	}
	credit := s.cfg.CreditUSDPerCNY
	if credit <= 0 {
		credit = storage.DefaultCreditUSDPerCNY
	}
	buckets := make([]storage.NewAPIBillingBucket, 0, len(aggregate.Items))
	for _, item := range aggregate.Items {
		bucketStart := time.Unix(item.BucketStart, 0).UTC()
		bucketEnd := time.Unix(item.BucketEnd, 0).UTC()
		if bucketStart.Before(start) || !bucketStart.Before(end) || !bucketEnd.After(bucketStart) {
			continue
		}
		var upstreamChannelID *uint
		mappingStatus := "unmapped"
		if resolver, ok := s.repo.(mappingResolver); ok {
			mapping, resolveErr := resolver.ResolveMapping(item.ChannelID, bucketStart)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("resolve billing mapping: %w", resolveErr)
			}
			if mapping != nil {
				mappingStatus = strings.TrimSpace(mapping.MappingStatus)
				if mappingStatus == "" {
					mappingStatus = "unmapped"
				}
				if mappingStatus == "mapped" && mapping.UpstreamChannelID != nil && *mapping.UpstreamChannelID > 0 {
					id := *mapping.UpstreamChannelID
					upstreamChannelID = &id
				} else if mappingStatus == "mapped" {
					mappingStatus = "unmapped"
				}
			}
		}
		bucket, buildErr := storage.BuildNewAPIBillingBucket(storage.BillingFactInput{
			BucketStart: bucketStart, BucketEnd: bucketEnd, NewAPIChannelID: item.ChannelID, NewAPIChannelName: item.ChannelName,
			UpstreamChannelID: upstreamChannelID, MappingStatus: mappingStatus,
			Group: item.Group, ModelName: item.ModelName, EffectiveGroupRatio: item.EffectiveGroupRatio,
			RatioSource: item.RatioSource, NormalizationStatus: item.NormalizationStatus,
			ConsumeQuota: item.ConsumeQuota, RefundQuota: item.RefundQuota, EventCount: item.EventCount,
			QuotaPerUnit: aggregate.QuotaPerUnit, Complete: aggregate.Complete,
		}, credit, now)
		if buildErr != nil {
			return nil, nil, fmt.Errorf("build billing bucket: %w", buildErr)
		}
		buckets = append(buckets, bucket)
	}
	if resolver, ok := s.repo.(historicalCreditRateResolver); ok && len(buckets) > 0 {
		factKeys := make([]string, 0, len(buckets))
		for _, bucket := range buckets {
			factKeys = append(factKeys, bucket.FactKey)
		}
		historicalRates, resolveErr := resolver.CreditRatesByFactKeys(factKeys)
		if resolveErr != nil {
			return nil, nil, fmt.Errorf("load historical billing credit rates: %w", resolveErr)
		}
		for i := range buckets {
			rate, found := historicalRates[buckets[i].FactKey]
			if !found || rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
				continue
			}
			buckets[i].CreditUSDPerCNY = rate
			if buckets[i].NormalizationStatus != storage.BillingStatusUnavailable {
				buckets[i].SaleCNY = float64(buckets[i].NetQuota) / (buckets[i].QuotaPerUnit * rate)
			} else {
				buckets[i].SaleCNY = 0
			}
		}
	}
	personal, personalErr := buildPersonalUsageBuckets(aggregate.PersonalUsageItems, aggregate.PersonalUsageComplete, start, end, aggregate.QuotaPerUnit, credit, now)
	if personalErr != nil {
		return nil, nil, personalErr
	}
	return buckets, personal, nil
}

func buildPersonalUsageBuckets(items []newapi.PersonalUsageBucket, complete bool, start, end time.Time, quotaPerUnit, credit float64, collectedAt time.Time) ([]storage.NewAPIPersonalUsageBucket, error) {
	if quotaPerUnit <= 0 || math.IsNaN(quotaPerUnit) || math.IsInf(quotaPerUnit, 0) {
		return nil, errors.New("new-api personal usage has invalid quota_per_unit")
	}
	if credit <= 0 || math.IsNaN(credit) || math.IsInf(credit, 0) {
		return nil, errors.New("new-api personal usage has invalid credit_usd_per_cny")
	}
	result := make([]storage.NewAPIPersonalUsageBucket, 0, len(items))
	present := make(map[int64]struct{}, len(items))
	for _, item := range items {
		bucketStart := time.Unix(item.BucketStart, 0).UTC()
		bucketEnd := time.Unix(item.BucketEnd, 0).UTC()
		if bucketStart.Before(start) || !bucketStart.Before(end) || !bucketEnd.After(bucketStart) {
			continue
		}
		bucket, err := storage.BuildNewAPIPersonalUsageBucket(storage.PersonalUsageFactInput{
			BucketStart: bucketStart, BucketEnd: bucketEnd, ConsumeQuota: item.ConsumeQuota,
			RefundQuota: item.RefundQuota, EventCount: item.EventCount, QuotaPerUnit: quotaPerUnit, Complete: complete,
		}, credit, collectedAt)
		if err != nil {
			return nil, fmt.Errorf("build personal usage bucket: %w", err)
		}
		result = append(result, bucket)
		present[bucketStart.Unix()] = struct{}{}
	}
	// A complete response with no Root-user logs still needs explicit zero
	// rows; otherwise a later dashboard read cannot distinguish zero usage from
	// a missing/incomplete response.
	if complete {
		resolution := time.Duration(storage.BillingResolutionSeconds) * time.Second
		for cursor := start.UTC().Truncate(resolution); cursor.Before(end); cursor = cursor.Add(resolution) {
			if _, ok := present[cursor.Unix()]; ok {
				continue
			}
			bucket, err := storage.BuildNewAPIPersonalUsageBucket(storage.PersonalUsageFactInput{
				BucketStart: cursor, BucketEnd: cursor.Add(resolution), QuotaPerUnit: quotaPerUnit, Complete: true,
			}, credit, collectedAt)
			if err != nil {
				return nil, fmt.Errorf("build empty personal usage bucket: %w", err)
			}
			result = append(result, bucket)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].BucketStart.Before(result[j].BucketStart) })
	return result, nil
}

func aggregateQuotaPerUnit(buckets []storage.NewAPIBillingBucket) float64 {
	for _, bucket := range buckets {
		if bucket.QuotaPerUnit > 0 {
			return bucket.QuotaPerUnit
		}
	}
	return 1
}

func (s *Service) collectEvents(ctx context.Context, start, end time.Time, quotaPerUnit float64, now time.Time) ([]storage.NewAPIBillingEvent, bool, error) {
	client, ok := s.client.(DetailClient)
	if !ok {
		return nil, false, nil
	}
	credit := s.cfg.CreditUSDPerCNY
	if credit <= 0 || math.IsNaN(credit) || math.IsInf(credit, 0) {
		credit = storage.DefaultCreditUSDPerCNY
	}
	events := make([]storage.NewAPIBillingEvent, 0)
	for page := 1; ; page++ {
		details, err := client.FetchBillingDetails(ctx, newapi.BillingDetailRequest{StartAt: start.Unix(), EndAt: end.Unix(), Page: page, PageSize: 500})
		if err != nil {
			if isUnavailableDetailsEndpoint(err) {
				// Detail rows are an optional audit enhancement. A slow or older
				// NewAPI can still settle sales and personal usage from aggregate data.
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("fetch new-api billing details: %w", err)
		}
		for _, item := range details.Items {
			createdAt := time.Unix(item.CreatedAt, 0).UTC()
			if createdAt.Before(start) || !createdAt.Before(end) {
				continue
			}
			var upstreamID *uint
			mappingStatus := "unmapped"
			if resolver, ok := s.repo.(mappingResolver); ok {
				mapping, resolveErr := resolver.ResolveMapping(item.ChannelID, createdAt)
				if resolveErr != nil {
					return nil, false, fmt.Errorf("resolve billing event mapping: %w", resolveErr)
				}
				if mapping != nil {
					mappingStatus = strings.TrimSpace(mapping.MappingStatus)
					if mappingStatus == "mapped" && mapping.UpstreamChannelID != nil && *mapping.UpstreamChannelID > 0 {
						id := *mapping.UpstreamChannelID
						upstreamID = &id
					}
				}
			}
			event, buildErr := storage.BuildNewAPIBillingEvent(storage.BillingEventInput{
				EventKey: fmt.Sprintf("log-%d", item.SourceLogID), SourceLogID: item.SourceLogID, CreatedAt: createdAt,
				BucketStart: time.Unix(createdAt.Unix()-createdAt.Unix()%storage.BillingResolutionSeconds, 0).UTC(),
				BucketEnd:   time.Unix(createdAt.Unix()-createdAt.Unix()%storage.BillingResolutionSeconds+storage.BillingResolutionSeconds, 0).UTC(),
				EventType:   item.EventType, ChannelID: item.ChannelID, UpstreamChannelID: upstreamID, MappingStatus: mappingStatus,
				ChannelName: item.ChannelName, Group: item.Group, ModelName: item.ModelName, EffectiveGroupRatio: item.EffectiveGroupRatio,
				RatioSource: item.RatioSource, NormalizationStatus: item.NormalizationStatus, Quota: item.Quota,
				QuotaPerUnit: quotaPerUnit, CreditUSDPerCNY: credit, UserID: item.UserID, TokenName: item.TokenName,
				RequestID: item.RequestID, UpstreamRequestID: item.UpstreamRequestID, CollectedAt: now,
			})
			if buildErr != nil {
				return nil, false, fmt.Errorf("build billing event: %w", buildErr)
			}
			events = append(events, event)
		}
		if !details.HasMore || len(details.Items) == 0 {
			break
		}
	}
	if resolver, ok := s.repo.(historicalEventCreditRateResolver); ok && len(events) > 0 {
		eventKeys := make([]string, 0, len(events))
		for _, event := range events {
			eventKeys = append(eventKeys, event.EventKey)
		}
		historicalRates, resolveErr := resolver.CreditRatesByEventKeys(eventKeys)
		if resolveErr != nil {
			return nil, false, fmt.Errorf("load historical billing event credit rates: %w", resolveErr)
		}
		for i := range events {
			rate, found := historicalRates[events[i].EventKey]
			if !found || rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
				continue
			}
			events[i].CreditUSDPerCNY = rate
			if events[i].NormalizationStatus != storage.BillingStatusUnavailable {
				events[i].SaleCNY = float64(events[i].Quota) / (quotaPerUnit * rate)
			} else {
				events[i].SaleCNY = 0
			}
		}
	}
	return events, true, nil
}

func isUnavailableDetailsEndpoint(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var statusErr *newapi.HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == 404 || statusErr.StatusCode == 405
	}
	// Keep compatibility with clients/tests that predate HTTPStatusError and
	// expose the status only in the established error text.
	message := err.Error()
	return strings.Contains(message, "status 404") || strings.Contains(message, "status 405")
}

// SaveMappings persists the channel-to-upstream observations produced by a
// rates scan. It is optional so deployments can upgrade the scheduler before
// applying the mapping migration.
func (s *Service) SaveMappings(identities []newapi.Identity, metrics []newapi.Metric, observedAt time.Time) error {
	saver, ok := s.repo.(mappingSnapshotSaver)
	if !ok {
		return nil
	}
	return saver.SaveMappingSnapshots(buildMappingInputs(identities, metrics), observedAt)
}

func buildMappingInputs(identities []newapi.Identity, metrics []newapi.Metric) []storage.BillingMappingInput {
	channels := make(map[int]struct{}, len(identities)+len(metrics))
	candidates := make(map[int]map[uint]map[string]struct{})
	for _, identity := range identities {
		if identity.ChannelID > 0 {
			channels[identity.ChannelID] = struct{}{}
		}
	}
	for _, metric := range metrics {
		if metric.ChannelID <= 0 {
			continue
		}
		channels[metric.ChannelID] = struct{}{}
		if metric.UpstreamChannelID == 0 {
			continue
		}
		byUpstream := candidates[metric.ChannelID]
		if byUpstream == nil {
			byUpstream = make(map[uint]map[string]struct{})
			candidates[metric.ChannelID] = byUpstream
		}
		groups := byUpstream[metric.UpstreamChannelID]
		if groups == nil {
			groups = make(map[string]struct{})
			byUpstream[metric.UpstreamChannelID] = groups
		}
		groups[strings.TrimSpace(metric.Group)] = struct{}{}
	}
	ids := make([]int, 0, len(channels))
	for channelID := range channels {
		ids = append(ids, channelID)
	}
	sort.Ints(ids)
	items := make([]storage.BillingMappingInput, 0, len(ids))
	for _, channelID := range ids {
		item := storage.BillingMappingInput{NewAPIChannelID: channelID, MappingStatus: "unmapped"}
		byUpstream := candidates[channelID]
		if len(byUpstream) == 1 {
			var upstreamID uint
			for id := range byUpstream {
				upstreamID = id
			}
			item.MappingStatus = "mapped"
			item.UpstreamChannelID = &upstreamID
			groups := make([]string, 0, len(byUpstream[upstreamID]))
			for group := range byUpstream[upstreamID] {
				if group != "" {
					groups = append(groups, group)
				}
			}
			sort.Strings(groups)
			if len(groups) > 0 {
				item.UpstreamGroup = groups[0]
			}
		} else if len(byUpstream) > 1 {
			item.MappingStatus = "ambiguous"
		}
		items = append(items, item)
	}
	return items
}

func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.client != nil && s.repo != nil
}

func (s *Service) String() string {
	if s == nil {
		return "billing(nil)"
	}
	return "billing(" + strings.TrimSpace(billingSource) + ")"
}
