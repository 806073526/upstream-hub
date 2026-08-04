package storage

import (
	"reflect"
	"testing"
	"time"
)

func TestResolveUsageTrendSpecUsesExpectedBuckets(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 12, 34, 0, time.UTC)
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rangeID    string
		wantSource int
		wantOutput int
		wantStart  time.Time
		wantEnd    time.Time
	}{
		{"1h", 300, 300, now.Truncate(5 * time.Minute).Add(-time.Hour), now.Truncate(5 * time.Minute)},
		{"today", 3600, 3600, time.Date(2026, 7, 29, 0, 0, 0, 0, location).UTC(), now},
		{"24h", 3600, 3600, now.Truncate(time.Hour).Add(-24 * time.Hour), now.Truncate(time.Hour)},
		{"7d", 3600, 3600, now.Truncate(time.Hour).Add(-7 * 24 * time.Hour), now.Truncate(time.Hour)},
		{"30d", 3600, 86400, time.Date(2026, 6, 30, 0, 0, 0, 0, location).UTC(), now},
		{"6m", 3600, 2592000, time.Date(2026, 2, 1, 0, 0, 0, 0, location).UTC(), time.Date(2026, 8, 1, 0, 0, 0, 0, location).UTC()},
		{"1y", 3600, 2592000, time.Date(2025, 8, 1, 0, 0, 0, 0, location).UTC(), time.Date(2026, 8, 1, 0, 0, 0, 0, location).UTC()},
	}

	for _, tt := range tests {
		t.Run(tt.rangeID, func(t *testing.T) {
			spec, err := ResolveUsageTrendSpec(tt.rangeID, now, location)
			if err != nil {
				t.Fatalf("ResolveUsageTrendSpec returned error: %v", err)
			}
			if spec.SourceResolutionSeconds != tt.wantSource || spec.OutputResolutionSeconds != tt.wantOutput {
				t.Fatalf("unexpected resolutions: %#v", spec)
			}
			if !spec.StartAt.Equal(tt.wantStart) || !spec.EndAt.Equal(tt.wantEnd) {
				t.Fatalf("unexpected window: %#v", spec)
			}
			points := BuildUsageTrend(nil, spec, location, nil)
			if tt.rangeID == "1h" && len(points) != 12 || tt.rangeID == "24h" && len(points) != 24 || tt.rangeID == "7d" && len(points) != 168 || tt.rangeID == "6m" && len(points) != 6 || tt.rangeID == "1y" && len(points) != 12 {
				t.Fatalf("%s points length = %d", tt.rangeID, len(points))
			}
		})
	}
}

func TestBuildUsageTrendAggregatesHourlyBucketsByCalendarMonth(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 31, 16, 0, 0, 0, time.UTC)
	buckets := []UsageBucket{
		{ChannelID: 1, BucketStart: start.Add(2 * time.Hour), BucketEnd: start.Add(3 * time.Hour), ResolutionSeconds: 3600, Amount: 0.2, Quality: "exact", Complete: true},
		{ChannelID: 1, BucketStart: time.Date(2026, 2, 28, 16, 0, 0, 0, time.UTC), BucketEnd: time.Date(2026, 2, 28, 17, 0, 0, 0, time.UTC), ResolutionSeconds: 3600, Amount: 0.3, Quality: "exact", Complete: true},
	}
	spec := UsageTrendSpec{
		StartAt:                 start,
		EndAt:                   time.Date(2026, 3, 31, 16, 0, 0, 0, time.UTC),
		SourceResolutionSeconds: 3600,
		OutputResolutionSeconds: 2592000,
	}
	points := BuildUsageTrend(buckets, spec, location, []uint{1})
	if len(points) != 2 {
		t.Fatalf("points length = %d, want 2", len(points))
	}
	if !points[0].StartAt.Equal(start) || points[0].TotalAmount != 0.2 {
		t.Fatalf("first month = %#v", points[0])
	}
	if !points[1].StartAt.Equal(time.Date(2026, 2, 28, 16, 0, 0, 0, time.UTC)) || points[1].TotalAmount != 0.3 {
		t.Fatalf("second month = %#v", points[1])
	}
}

func TestBuildUsageTrendIncludesMissingIntervalsWithoutInventingZero(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	buckets := []UsageBucket{{
		ChannelID: 1, BucketStart: start, BucketEnd: start.Add(time.Hour),
		ResolutionSeconds: 3600, Amount: 0.5, Quality: "exact", Complete: true,
	}}

	points := BuildUsageTrend(buckets, UsageTrendSpec{
		StartAt: start, EndAt: start.Add(2 * time.Hour),
		SourceResolutionSeconds: 3600, OutputResolutionSeconds: 3600,
	}, time.UTC, []uint{1, 2})
	if len(points) != 2 {
		t.Fatalf("points length = %d, want complete two-point timeline", len(points))
	}
	if !points[0].HasData || points[0].TotalAmount != 0.5 || points[0].Quality != "exact" || !reflect.DeepEqual(points[0].MissingChannelIDs, []uint{2}) {
		t.Fatalf("first point = %#v", points[0])
	}
	if points[1].HasData || points[1].Quality != "missing" || !reflect.DeepEqual(points[1].MissingChannelIDs, []uint{1, 2}) {
		t.Fatalf("missing point = %#v", points[1])
	}
}

func TestBuildUsageTrendMarksMixedQuality(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	buckets := []UsageBucket{
		{ChannelID: 1, BucketStart: start, BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600, Amount: 0.2, Quality: "exact", Complete: true},
		{ChannelID: 2, BucketStart: start, BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600, Amount: 0.3, Quality: "observed", Complete: true},
	}
	points := BuildUsageTrend(buckets, UsageTrendSpec{
		StartAt: start, EndAt: start.Add(time.Hour),
		SourceResolutionSeconds: 3600, OutputResolutionSeconds: 3600,
	}, time.UTC, []uint{1, 2})
	if len(points) != 1 || points[0].Quality != "mixed" || !points[0].Complete {
		t.Fatalf("point = %#v", points)
	}
}

func TestAggregateUsageBucketsReturnsIntervalAmounts(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	buckets := []UsageBucket{
		{ChannelID: 1, BucketStart: start, BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600, Amount: 0.2},
		{ChannelID: 2, BucketStart: start, BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600, Amount: 0.3},
		{ChannelID: 1, BucketStart: start.Add(time.Hour), BucketEnd: start.Add(2 * time.Hour), ResolutionSeconds: 3600, Amount: 0.4},
	}

	points := AggregateUsageBuckets(buckets, UsageTrendSpec{
		StartAt:                 start,
		EndAt:                   start.Add(2 * time.Hour),
		SourceResolutionSeconds: 3600,
		OutputResolutionSeconds: 3600,
	}, time.UTC)
	if len(points) != 2 {
		t.Fatalf("points length = %d, want 2: %#v", len(points), points)
	}
	if points[0].TotalAmount != 0.5 || points[0].ChannelAmounts[1] != 0.2 || points[0].ChannelAmounts[2] != 0.3 {
		t.Fatalf("first point = %#v", points[0])
	}
	if points[1].TotalAmount != 0.4 {
		t.Fatalf("second point total = %v, want interval-only 0.4", points[1].TotalAmount)
	}
}
