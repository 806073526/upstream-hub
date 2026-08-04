package storage

import (
	"database/sql"
	"errors"
	"math"
	"sort"
	"time"

	"gorm.io/gorm"
)

type Usage struct{ db *gorm.DB }

func NewUsage(db *gorm.DB) *Usage { return &Usage{db: db} }

type UsageSample struct {
	ChannelID   uint
	TotalAmount *float64
	TodayAmount *float64
	Currency    string
	ObservedAt  time.Time
	Buckets     []UsageBucket
}

func (r *Usage) Save(sample UsageSample) error {
	if sample.ObservedAt.IsZero() {
		sample.ObservedAt = time.Now()
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`UPDATE channels SET last_usage_total=?, last_usage_today=?, usage_currency=?, last_usage_at=?, updated_at=? WHERE id=? AND deleted_at IS NULL`,
			sample.TotalAmount, sample.TodayAmount, sample.Currency, sample.ObservedAt, time.Now(), sample.ChannelID,
		).Error; err != nil {
			return err
		}
		for i := range sample.Buckets {
			bucket := &sample.Buckets[i]
			if bucket.ChannelID == 0 {
				bucket.ChannelID = sample.ChannelID
			}
			if bucket.CollectedAt.IsZero() {
				bucket.CollectedAt = sample.ObservedAt
			}
			if bucket.Currency == "" {
				bucket.Currency = sample.Currency
			}
			if err := tx.Exec(`
				INSERT INTO usage_buckets
					(channel_id, bucket_start, bucket_end, resolution_seconds, amount, currency, source, quality, complete, collected_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (channel_id, bucket_start, resolution_seconds) DO UPDATE SET
					bucket_end=EXCLUDED.bucket_end, amount=EXCLUDED.amount, currency=EXCLUDED.currency,
					source=EXCLUDED.source, quality=EXCLUDED.quality, complete=EXCLUDED.complete,
					collected_at=EXCLUDED.collected_at, updated_at=EXCLUDED.updated_at`,
				bucket.ChannelID, bucket.BucketStart, bucket.BucketEnd, bucket.ResolutionSeconds,
				bucket.Amount, bucket.Currency, bucket.Source, bucket.Quality, bucket.Complete,
				bucket.CollectedAt, time.Now(),
			).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Usage) ListBuckets(start, end time.Time, resolutionSeconds int) ([]UsageBucket, error) {
	var buckets []UsageBucket
	err := r.db.
		Where("bucket_start >= ? AND bucket_start < ? AND resolution_seconds = ?", start, end, resolutionSeconds).
		Order("bucket_start ASC, channel_id ASC").
		Find(&buckets).Error
	return buckets, err
}

func (r *Usage) LatestBucketStart(channelID uint, resolutionSeconds int) (*time.Time, error) {
	var latest sql.NullTime
	err := r.db.Model(&UsageBucket{}).
		Select("MAX(bucket_start)").
		Where("channel_id = ? AND resolution_seconds = ?", channelID, resolutionSeconds).
		Scan(&latest).Error
	if err != nil || !latest.Valid {
		return nil, err
	}
	return &latest.Time, nil
}

func (r *Usage) DeleteBefore(resolutionSeconds int, cutoff time.Time) (int64, error) {
	res := r.db.Where("resolution_seconds = ? AND bucket_start < ?", resolutionSeconds, cutoff).Delete(&UsageBucket{})
	return res.RowsAffected, res.Error
}

type UsageTrendSpec struct {
	StartAt                 time.Time
	EndAt                   time.Time
	SourceResolutionSeconds int
	OutputResolutionSeconds int
}

const usageMonthResolutionSeconds = 2592000

type UsageTrendPoint struct {
	StartAt           time.Time        `json:"start_at"`
	EndAt             time.Time        `json:"end_at"`
	TotalAmount       float64          `json:"total_amount"`
	ChannelAmounts    map[uint]float64 `json:"channel_amounts"`
	HasData           bool             `json:"-"`
	Quality           string           `json:"quality"`
	Complete          bool             `json:"complete"`
	MissingChannelIDs []uint           `json:"missing_channel_ids"`
}

func ResolveUsageTrendSpec(rangeID string, now time.Time, location *time.Location) (UsageTrendSpec, error) {
	if location == nil {
		location = time.UTC
	}
	now = now.UTC()
	spec := UsageTrendSpec{EndAt: now}
	switch rangeID {
	case "1h":
		spec.EndAt = now.Truncate(5 * time.Minute)
		spec.StartAt = spec.EndAt.Add(-time.Hour)
		spec.SourceResolutionSeconds = 300
		spec.OutputResolutionSeconds = 300
	case "today":
		localNow := now.In(location)
		spec.StartAt = time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).UTC()
		spec.SourceResolutionSeconds = 3600
		spec.OutputResolutionSeconds = 3600
	case "24h":
		spec.EndAt = now.Truncate(time.Hour)
		spec.StartAt = spec.EndAt.Add(-24 * time.Hour)
		spec.SourceResolutionSeconds = 3600
		spec.OutputResolutionSeconds = 3600
	case "7d":
		spec.EndAt = now.Truncate(time.Hour)
		spec.StartAt = spec.EndAt.Add(-7 * 24 * time.Hour)
		spec.SourceResolutionSeconds = 3600
		spec.OutputResolutionSeconds = 3600
	case "30d":
		localNow := now.In(location)
		todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location)
		spec.StartAt = todayStart.AddDate(0, 0, -29).UTC()
		spec.SourceResolutionSeconds = 3600
		spec.OutputResolutionSeconds = 86400
	case "6m", "1y":
		localNow := now.In(location)
		currentMonthStart := time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, location)
		months := 6
		if rangeID == "1y" {
			months = 12
		}
		spec.StartAt = currentMonthStart.AddDate(0, -(months - 1), 0).UTC()
		spec.EndAt = currentMonthStart.AddDate(0, 1, 0).UTC()
		spec.SourceResolutionSeconds = 3600
		spec.OutputResolutionSeconds = usageMonthResolutionSeconds
	default:
		return UsageTrendSpec{}, errors.New("unsupported usage trend range")
	}
	return spec, nil
}

// BuildUsageTrend 创建完整时间轴，显式标记没有采样的区间和渠道。
func BuildUsageTrend(buckets []UsageBucket, spec UsageTrendSpec, location *time.Location, channelIDs []uint) []UsageTrendPoint {
	if location == nil {
		location = time.UTC
	}
	type pointState struct {
		point     UsageTrendPoint
		present   map[uint]bool
		qualities map[string]bool
	}
	states := make(map[int64]*pointState)
	for cursor := usageOutputBucketStart(spec.StartAt, spec.OutputResolutionSeconds, location); cursor.Before(spec.EndAt); cursor = usageOutputBucketEnd(cursor, spec.OutputResolutionSeconds, location) {
		endAt := usageOutputBucketEnd(cursor, spec.OutputResolutionSeconds, location)
		states[cursor.Unix()] = &pointState{
			point:   UsageTrendPoint{StartAt: cursor, EndAt: endAt, ChannelAmounts: make(map[uint]float64), Quality: "missing"},
			present: make(map[uint]bool), qualities: make(map[string]bool),
		}
	}
	for _, bucket := range buckets {
		if bucket.ResolutionSeconds != spec.SourceResolutionSeconds || bucket.BucketStart.Before(spec.StartAt) || !bucket.BucketStart.Before(spec.EndAt) {
			continue
		}
		startAt := usageOutputBucketStart(bucket.BucketStart, spec.OutputResolutionSeconds, location)
		state := states[startAt.Unix()]
		if state == nil {
			continue
		}
		state.point.HasData = true
		state.point.TotalAmount += bucket.Amount
		state.point.ChannelAmounts[bucket.ChannelID] += bucket.Amount
		state.present[bucket.ChannelID] = true
		state.qualities[bucket.Quality] = true
		if !bucket.Complete {
			state.point.Complete = false
		}
	}
	starts := make([]int64, 0, len(states))
	for start := range states {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	points := make([]UsageTrendPoint, 0, len(starts))
	for _, start := range starts {
		state := states[start]
		state.point.TotalAmount = roundUsageAmount(state.point.TotalAmount)
		for channelID, amount := range state.point.ChannelAmounts {
			state.point.ChannelAmounts[channelID] = roundUsageAmount(amount)
		}
		for _, channelID := range channelIDs {
			if !state.present[channelID] {
				state.point.MissingChannelIDs = append(state.point.MissingChannelIDs, channelID)
			}
		}
		sort.Slice(state.point.MissingChannelIDs, func(i, j int) bool { return state.point.MissingChannelIDs[i] < state.point.MissingChannelIDs[j] })
		switch {
		case !state.point.HasData:
			state.point.Quality = "missing"
		case len(state.qualities) > 1:
			state.point.Quality = "mixed"
		case state.qualities["observed"]:
			state.point.Quality = "observed"
		default:
			state.point.Quality = "exact"
		}
		state.point.Complete = state.point.HasData && len(state.point.MissingChannelIDs) == 0
		for _, bucket := range buckets {
			if usageOutputBucketStart(bucket.BucketStart, spec.OutputResolutionSeconds, location).Equal(state.point.StartAt) && !bucket.Complete {
				state.point.Complete = false
				break
			}
		}
		points = append(points, state.point)
	}
	return points
}

func AggregateUsageBuckets(buckets []UsageBucket, spec UsageTrendSpec, location *time.Location) []UsageTrendPoint {
	if location == nil {
		location = time.UTC
	}
	pointsByStart := make(map[int64]*UsageTrendPoint)
	for _, bucket := range buckets {
		if bucket.ResolutionSeconds != spec.SourceResolutionSeconds || bucket.BucketStart.Before(spec.StartAt) || !bucket.BucketStart.Before(spec.EndAt) {
			continue
		}
		startAt := usageOutputBucketStart(bucket.BucketStart, spec.OutputResolutionSeconds, location)
		key := startAt.Unix()
		point := pointsByStart[key]
		if point == nil {
			point = &UsageTrendPoint{
				StartAt:        startAt,
				EndAt:          usageOutputBucketEnd(startAt, spec.OutputResolutionSeconds, location),
				ChannelAmounts: make(map[uint]float64),
			}
			pointsByStart[key] = point
		}
		point.TotalAmount += bucket.Amount
		point.ChannelAmounts[bucket.ChannelID] += bucket.Amount
	}
	starts := make([]int64, 0, len(pointsByStart))
	for start := range pointsByStart {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	points := make([]UsageTrendPoint, 0, len(starts))
	for _, start := range starts {
		point := pointsByStart[start]
		point.TotalAmount = roundUsageAmount(point.TotalAmount)
		for channelID, amount := range point.ChannelAmounts {
			point.ChannelAmounts[channelID] = roundUsageAmount(amount)
		}
		points = append(points, *point)
	}
	return points
}

func usageOutputBucketStart(at time.Time, seconds int, location *time.Location) time.Time {
	if seconds == usageMonthResolutionSeconds {
		local := at.In(location)
		return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location).UTC()
	}
	if seconds == 86400 {
		local := at.In(location)
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC()
	}
	return at.UTC().Truncate(time.Duration(seconds) * time.Second)
}

func usageOutputBucketEnd(start time.Time, seconds int, location *time.Location) time.Time {
	if seconds == usageMonthResolutionSeconds {
		return start.In(location).AddDate(0, 1, 0).UTC()
	}
	if seconds == 86400 {
		return start.In(location).AddDate(0, 0, 1).UTC()
	}
	return start.Add(time.Duration(seconds) * time.Second)
}

func roundUsageAmount(amount float64) float64 {
	return math.Round(amount*1e8) / 1e8
}
