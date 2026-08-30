package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildProfitBucketsSplitsSharedUpstreamCostByNormalizedConsumption(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	upstreamID := uint(7)
	billing := []NewAPIBillingBucket{
		{
			FactKey: "sale-a", BucketStart: start, BucketEnd: end, ResolutionSeconds: 300,
			NewAPIChannelID: 1, UpstreamChannelID: &upstreamID, MappingStatus: "mapped",
			Group: "vip", ModelName: "gpt-4o", NormalizationStatus: BillingStatusExact,
			NetQuota: 100, NormalizedUSD: 1, CreditUSDPerCNY: 12, SaleCNY: 1.0 / 12, Complete: true,
		},
		{
			FactKey: "sale-b", BucketStart: start, BucketEnd: end, ResolutionSeconds: 300,
			NewAPIChannelID: 2, UpstreamChannelID: &upstreamID, MappingStatus: "mapped",
			Group: "vip", ModelName: "gpt-4o", NormalizationStatus: BillingStatusExact,
			NetQuota: 300, NormalizedUSD: 3, CreditUSDPerCNY: 12, SaleCNY: 3.0 / 12, Complete: true,
		},
	}
	usage := []UsageBucket{{
		ChannelID: 7, BucketStart: start, BucketEnd: end, ResolutionSeconds: 300,
		Amount: 8, Currency: "USD", Complete: true,
	}}

	profits, err := BuildProfitBuckets(billing, usage)
	if err != nil {
		t.Fatalf("BuildProfitBuckets returned error: %v", err)
	}
	if len(profits) != 2 {
		t.Fatalf("profit buckets = %d, want 2", len(profits))
	}
	if profits[0].CostUSD != 2 || profits[1].CostUSD != 6 {
		t.Fatalf("allocated costs = %v and %v, want 2 and 6", profits[0].CostUSD, profits[1].CostUSD)
	}
	if profits[0].AllocationStatus != ProfitAllocationSettled || profits[1].AllocationStatus != ProfitAllocationSettled {
		t.Fatalf("allocation statuses = %q/%q", profits[0].AllocationStatus, profits[1].AllocationStatus)
	}
}

func TestBuildProfitBucketsPrefersFineUsageBucketsWithoutDoubleCounting(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	upstreamID := uint(7)
	billing := []NewAPIBillingBucket{{
		FactKey: "sale-fine", BucketStart: start, BucketEnd: end, ResolutionSeconds: 300,
		NewAPIChannelID: 1, UpstreamChannelID: &upstreamID, MappingStatus: "mapped",
		Group: "default", ModelName: "gpt-5", NormalizationStatus: BillingStatusExact,
		NormalizedUSD: 1, CreditUSDPerCNY: 12, SaleCNY: 1.0 / 12, Complete: true,
	}}
	usage := []UsageBucket{
		{ChannelID: 7, BucketStart: start, BucketEnd: end, ResolutionSeconds: 300, Amount: 2, Currency: "USD", Complete: true},
		{ChannelID: 7, BucketStart: start.Add(-time.Hour), BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600, Amount: 12, Currency: "USD", Complete: true},
	}

	profits, err := BuildProfitBuckets(billing, usage)
	if err != nil {
		t.Fatalf("BuildProfitBuckets returned error: %v", err)
	}
	if len(profits) != 1 || profits[0].CostUSD != 2 {
		t.Fatalf("cost = %#v, want fine bucket cost 2 without hourly double count", profits)
	}
}

func TestBuildProfitBucketsDoesNotClaimProfitForUnmappedSales(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	billing := []NewAPIBillingBucket{{
		FactKey: "unmapped", BucketStart: start, BucketEnd: start.Add(5 * time.Minute), ResolutionSeconds: 300,
		NewAPIChannelID: 3, MappingStatus: "unmapped", NormalizationStatus: BillingStatusExact,
		NormalizedUSD: 2, CreditUSDPerCNY: 12, SaleCNY: 2.0 / 12,
	}}
	profits, err := BuildProfitBuckets(billing, nil)
	if err != nil {
		t.Fatalf("BuildProfitBuckets returned error: %v", err)
	}
	if len(profits) != 1 {
		t.Fatalf("profit buckets = %d, want 1", len(profits))
	}
	if profits[0].AllocationStatus != ProfitAllocationUnmapped || profits[0].ProfitCNY != 0 || profits[0].Complete {
		t.Fatalf("unmapped profit = %#v", profits[0])
	}
}

func TestBuildProfitDailySnapshotsUsesShanghaiDayBoundary(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 8, 27, 15, 55, 0, 0, time.UTC) // 23:55 local
	second := time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC) // 00:00 local
	profits := []ProfitBucket{
		{FactKey: "one", BucketStart: first, BucketEnd: first.Add(5 * time.Minute), SaleCNY: 1, CostCNY: .2, ProfitCNY: .8, AllocationStatus: ProfitAllocationSettled, Complete: true},
		{FactKey: "two", BucketStart: second, BucketEnd: second.Add(5 * time.Minute), SaleCNY: 2, CostCNY: .4, ProfitCNY: 1.6, AllocationStatus: ProfitAllocationUnmapped, Complete: false},
	}
	snapshots := BuildProfitDailySnapshots(profits, location, time.Now())
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snapshots))
	}
	if snapshots[0].SaleCNY != 1 || snapshots[1].SaleCNY != 2 {
		t.Fatalf("daily sales = %v/%v", snapshots[0].SaleCNY, snapshots[1].SaleCNY)
	}
	if snapshots[1].UnmappedSaleCNY != 2 || snapshots[1].Complete {
		t.Fatalf("unmapped day = %#v", snapshots[1])
	}
}

func TestSummarizeProfitSeparatesSettledAndUnmappedSales(t *testing.T) {
	profits := []ProfitBucket{
		{SaleCNY: 2, CostCNY: .5, ProfitCNY: 1.5, AllocationStatus: ProfitAllocationSettled, Complete: true},
		{SaleCNY: 3, AllocationStatus: ProfitAllocationUnmapped, Complete: false},
		{SaleCNY: 1, AllocationStatus: ProfitAllocationCostMissing, Complete: false},
	}
	summary := SummarizeProfit(profits)
	if summary.SaleCNY != 6 || summary.CostCNY != .5 || summary.ProfitCNY != 1.5 {
		t.Fatalf("summary totals = %#v", summary)
	}
	if summary.SettledSaleCNY != 2 || summary.UnmappedSaleCNY != 3 || summary.UnsettledSaleCNY != 1 || summary.Complete {
		t.Fatalf("summary status totals = %#v", summary)
	}
}

func TestBuildProfitTrendReturnsCompleteIntervalsAndStatusTotals(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	spec, err := ResolveProfitTrendSpec("24h", now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	profits := []ProfitBucket{
		{BucketStart: base, BucketEnd: base.Add(time.Hour), SaleCNY: 2, CostCNY: 1, ProfitCNY: 1, AllocationStatus: ProfitAllocationSettled, Complete: true},
		{BucketStart: base.Add(time.Hour), BucketEnd: base.Add(2 * time.Hour), SaleCNY: 3, CostCNY: 1, ProfitCNY: 2, AllocationStatus: ProfitAllocationUnmapped, Complete: false},
	}
	points := BuildProfitTrend(profits, spec, time.UTC)
	if len(points) != 24 {
		t.Fatalf("points = %d, want 24", len(points))
	}
	if points[22].SaleCNY != 2 || !points[22].Complete {
		t.Fatalf("settled point = %#v", points[22])
	}
	if points[23].SaleCNY != 3 || points[23].UnmappedSaleCNY != 3 || points[23].Complete {
		t.Fatalf("unmapped point = %#v", points[23])
	}
}

func TestResolveProfitTrendSpecUsesShanghaiMidnightForDailyRanges(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 16, 17, 0, 0, time.UTC) // 00:17 on Aug 28 local
	spec, err := ResolveProfitTrendSpec("7d", now, location)
	if err != nil {
		t.Fatal(err)
	}
	wantEnd := time.Date(2026, 8, 28, 16, 0, 0, 0, time.UTC)
	if !spec.EndAt.Equal(wantEnd) || spec.OutputResolutionSeconds != 86400 {
		t.Fatalf("spec = %#v, want end %v/day", spec, wantEnd)
	}
}

func TestBuildProfitBucketsUsesIndependentUpstreamCostRate(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	upstreamID := uint(7)
	billing := []NewAPIBillingBucket{{
		FactKey: "sale-independent-cost", BucketStart: start, BucketEnd: end, ResolutionSeconds: 300,
		NewAPIChannelID: 1, UpstreamChannelID: &upstreamID, MappingStatus: "mapped",
		Group: "vip", ModelName: "gpt-4o", NormalizationStatus: BillingStatusExact,
		NormalizedUSD: 2, CreditUSDPerCNY: 12, SaleCNY: 2.0 / 12, Complete: true,
	}}
	usage := []UsageBucket{{ChannelID: 7, BucketStart: start, BucketEnd: end, ResolutionSeconds: 300, Amount: 8, Currency: "USD", Complete: true}}

	profits, err := BuildProfitBucketsWithRates(billing, usage, 1)
	if err != nil {
		t.Fatalf("BuildProfitBucketsWithRates returned error: %v", err)
	}
	if len(profits) != 1 || profits[0].CostUSD != 8 || profits[0].CostCNY != 8 {
		t.Fatalf("profit = %#v, want cost USD/CNY 8/8", profits)
	}
	if profits[0].ProfitCNY != 2.0/12-8 {
		t.Fatalf("profit cny = %v, want %v", profits[0].ProfitCNY, 2.0/12-8)
	}
}

func TestSummarizeProfitWithUsageIncludesAllUpstreamCost(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	profits := []ProfitBucket{{
		FactKey: "mapped-sale", BucketStart: start, BucketEnd: end,
		SaleCNY: 1, CostUSD: 2, CostCNY: 2, ProfitCNY: -1,
		AllocationStatus: ProfitAllocationSettled, Complete: true,
	}}
	usage := []UsageBucket{
		{ChannelID: 7, BucketStart: start, BucketEnd: end, ResolutionSeconds: 300, Amount: 2, Currency: "USD", Complete: true},
		{ChannelID: 8, BucketStart: start, BucketEnd: end, ResolutionSeconds: 300, Amount: 3, Currency: "USD", Complete: true},
	}
	summary := SummarizeProfitWithUsage(profits, usage, start, end, 300, 1)
	if summary.CostCNY != 5 || summary.ProfitCNY != -4 || summary.UnmatchedCostCNY != 3 {
		t.Fatalf("summary = %#v, want cost=5 profit=-4 unmatched=3", summary)
	}
}

func TestBuildPersonalUsageBucketConvertsNetQuotaToCNY(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	bucket, err := BuildNewAPIPersonalUsageBucket(PersonalUsageFactInput{
		BucketStart: start, BucketEnd: start.Add(5 * time.Minute), ConsumeQuota: 900000,
		RefundQuota: 100000, EventCount: 4, QuotaPerUnit: 500000, Complete: true,
	}, 10, start)
	if err != nil {
		t.Fatalf("BuildNewAPIPersonalUsageBucket returned error: %v", err)
	}
	if bucket.NetQuota != 800000 || bucket.PersonalUsageCNY != 0.16 || !bucket.Complete {
		t.Fatalf("personal usage bucket = %#v, want net=800000 cny=0.16 complete", bucket)
	}
}

func TestSummarizeProfitWithPersonalUsageExposesExternalSalesAndOperatingProfit(t *testing.T) {
	t.Helper()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	profits := []ProfitBucket{{SaleCNY: 10, CostCNY: 4, ProfitCNY: 6, Complete: true, AllocationStatus: ProfitAllocationSettled}}
	usage := []UsageBucket{{BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600, ChannelID: 1, Amount: 4, Currency: "USD", Complete: true}}
	personal := []NewAPIPersonalUsageBucket{{BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600, PersonalUsageCNY: 2, Complete: true}}
	summary := SummarizeProfitWithPersonalUsage(profits, usage, personal, start, end, 3600, 1, true)
	require.Equal(t, 6.0, summary.ProfitCNY)
	require.Equal(t, 2.0, summary.PersonalUsageCNY)
	require.Equal(t, 8.0, summary.ExternalSalesCNY)
	require.Equal(t, 4.0, summary.OperatingProfitCNY)
	require.True(t, summary.NetProfitComplete)
}

func TestSummarizeProfitWithPersonalUsageCalculatesNetProfit(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	profits := []ProfitBucket{{SaleCNY: 10, CostCNY: 4, ProfitCNY: 6, Complete: true, AllocationStatus: ProfitAllocationSettled}}
	usage := []UsageBucket{{BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600, ChannelID: 1, Amount: 4, Currency: "USD", Complete: true}}
	personal := []NewAPIPersonalUsageBucket{{BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600, PersonalUsageCNY: 2, Complete: true}}
	summary := SummarizeProfitWithPersonalUsage(profits, usage, personal, start, end, 3600, 1, true)
	if summary.ProfitCNY != 6 || summary.PersonalUsageCNY != 2 || summary.NetProfitCNY != 4 || !summary.NetProfitComplete {
		t.Fatalf("summary = %#v, want gross=6 personal=2 net=4 complete", summary)
	}
}

func TestSummarizeProfitWithPersonalUsagePreservesProfitBucketCostWithoutUsageLedger(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	profits := []ProfitBucket{{SaleCNY: 10, CostCNY: 4, ProfitCNY: 6, Complete: true, AllocationStatus: ProfitAllocationSettled}}
	personal := []NewAPIPersonalUsageBucket{{BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600, PersonalUsageCNY: 2, Complete: true}}

	summary := SummarizeProfitWithPersonalUsage(profits, nil, personal, start, end, 3600, 1, true)
	if summary.CostCNY != 4 || summary.ProfitCNY != 6 || summary.ExternalSalesCNY != 8 || summary.OperatingProfitCNY != 4 {
		t.Fatalf("summary = %#v, want cost=4 gross=6 external=8 operating=4", summary)
	}
}

func TestBuildProfitTrendWithPersonalUsagePreservesProfitBucketCostWithoutUsageLedger(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	spec := ProfitTrendSpec{StartAt: start, EndAt: end, UsageResolutionSeconds: 3600, OutputResolutionSeconds: 3600}
	profits := []ProfitBucket{{BucketStart: start, BucketEnd: end, SaleCNY: 10, CostCNY: 4, ProfitCNY: 6, Complete: true}}
	personal := []NewAPIPersonalUsageBucket{{BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600, PersonalUsageCNY: 2, Complete: true}}

	points := BuildProfitTrendWithPersonalUsage(profits, nil, personal, spec, time.UTC, 1)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	point := points[0]
	if point.CostCNY != 4 || point.ProfitCNY != 6 || point.ExternalSalesCNY != 8 || point.OperatingProfitCNY != 4 {
		t.Fatalf("point = %#v, want cost=4 gross=6 external=8 operating=4", point)
	}
}

func TestBuildProfitTrendWithPersonalUsageShowsPersonalOnlyBucket(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	spec := ProfitTrendSpec{StartAt: start, EndAt: end, UsageResolutionSeconds: 3600, OutputResolutionSeconds: 3600}
	personal := []NewAPIPersonalUsageBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		PersonalUsageCNY: 3, Complete: true,
	}}

	points := BuildProfitTrendWithPersonalUsage(nil, nil, personal, spec, time.UTC, 1)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	point := points[0]
	if point.PersonalUsageCNY != 3 || point.ExternalSalesCNY != -3 || point.OperatingProfitCNY != -3 || point.NetProfitCNY != -3 || !point.HasData {
		t.Fatalf("personal-only point = %#v, want personal=3 external sales=-3 operating profit=-3 with data", point)
	}
	if !point.PersonalUsageComplete || point.NetProfitComplete {
		t.Fatalf("personal-only completeness = %#v, want personal complete and net incomplete without gross facts", point)
	}
}

func TestBuildProfitTrendWithPersonalUsageMarksMissingBucketsIncomplete(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	spec := ProfitTrendSpec{StartAt: start, EndAt: end, UsageResolutionSeconds: 3600, OutputResolutionSeconds: 3600}
	personal := []NewAPIPersonalUsageBucket{{
		BucketStart: start, BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600,
		PersonalUsageCNY: 1, Complete: true,
	}}

	points := BuildProfitTrendWithPersonalUsage(nil, nil, personal, spec, time.UTC, 1)
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2", len(points))
	}
	if !points[0].PersonalUsageComplete || points[1].PersonalUsageComplete {
		t.Fatalf("personal completeness = %v/%v, want true/false", points[0].PersonalUsageComplete, points[1].PersonalUsageComplete)
	}
}

func TestBuildProfitTrendWithUsagePropagatesCostCompletenessToNetProfit(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	spec := ProfitTrendSpec{StartAt: start, EndAt: end, UsageResolutionSeconds: 3600, OutputResolutionSeconds: 3600}
	profits := []ProfitBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		SaleCNY: 10, ProfitCNY: 6, Complete: true,
	}}
	usage := []UsageBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		ChannelID: 1, Amount: 4, Currency: "USD", Complete: false,
	}}

	points := BuildProfitTrendWithUsage(profits, usage, spec, time.UTC, 1)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	point := points[0]
	if point.Complete {
		t.Fatalf("gross completeness = true, want false for incomplete cost bucket")
	}
	if point.NetProfitComplete {
		t.Fatalf("net completeness = true, want false when cost completeness is false")
	}
}

func TestBuildProfitTrendWithPersonalUsageKeepsGrossCompletenessIndependent(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	spec := ProfitTrendSpec{StartAt: start, EndAt: end, UsageResolutionSeconds: 3600, OutputResolutionSeconds: 3600}
	profits := []ProfitBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		SaleCNY: 10, ProfitCNY: 6, Complete: true,
	}}
	usage := []UsageBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		ChannelID: 1, Amount: 4, Currency: "USD", Complete: true,
	}}
	personal := []NewAPIPersonalUsageBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		PersonalUsageCNY: 2, Complete: false,
	}}

	points := BuildProfitTrendWithPersonalUsage(profits, usage, personal, spec, time.UTC, 1)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	point := points[0]
	if !point.Complete {
		t.Fatalf("gross completeness = false, want true even when personal usage is incomplete")
	}
	if point.PersonalUsageComplete {
		t.Fatalf("personal completeness = true, want false")
	}
	if point.NetProfitComplete {
		t.Fatalf("net completeness = true, want false when personal usage is incomplete")
	}
}

func TestBuildProfitTrendWithPersonalUsageAcceptsExplicitCompleteZeroSource(t *testing.T) {
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	spec := ProfitTrendSpec{StartAt: start, EndAt: end, UsageResolutionSeconds: 3600, OutputResolutionSeconds: 3600}
	profits := []ProfitBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		SaleCNY: 10, ProfitCNY: 6, Complete: true,
	}}
	usage := []UsageBucket{{
		BucketStart: start, BucketEnd: end, ResolutionSeconds: 3600,
		ChannelID: 1, Amount: 4, Currency: "USD", Complete: true,
	}}

	points := BuildProfitTrendWithPersonalUsageStatus(profits, usage, nil, spec, time.UTC, 1, true)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	point := points[0]
	if point.PersonalUsageCNY != 0 || point.ExternalSalesCNY != 10 || point.OperatingProfitCNY != 6 || point.NetProfitCNY != 6 {
		t.Fatalf("zero personal-use point = %#v, want personal=0 external=10 operating=6 net=6", point)
	}
	if !point.PersonalUsageComplete || !point.NetProfitComplete {
		t.Fatalf("zero personal-use completeness = %#v, want both complete", point)
	}
}
