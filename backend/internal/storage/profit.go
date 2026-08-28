package storage

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ProfitAllocationSettled         = "settled"
	ProfitAllocationPartial         = "partial"
	ProfitAllocationUnmapped        = "unmapped"
	ProfitAllocationAmbiguous       = "ambiguous"
	ProfitAllocationUnavailable     = "unavailable"
	ProfitAllocationCostMissing     = "cost_missing"
	ProfitAllocationCostUnavailable = "cost_unavailable"
)

// ProfitBucket is the reconciliation result for one NewAPI billing fact. It
// stays separate from both source ledgers so a cost correction can be applied
// without rewriting the original sales or upstream usage facts.
type ProfitBucket struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	FactKey             string    `gorm:"column:billing_fact_key;size:128;not null;uniqueIndex" json:"billing_fact_key"`
	BucketStart         time.Time `gorm:"not null;index:idx_profit_window" json:"bucket_start"`
	BucketEnd           time.Time `gorm:"not null" json:"bucket_end"`
	ResolutionSeconds   int       `gorm:"not null;index:idx_profit_window" json:"resolution_seconds"`
	NewAPIChannelID     int       `gorm:"not null;index:idx_profit_window" json:"newapi_channel_id"`
	NewAPIChannelName   string    `gorm:"size:256" json:"newapi_channel_name,omitempty"`
	UpstreamChannelID   *uint     `gorm:"index" json:"upstream_channel_id,omitempty"`
	MappingStatus       string    `gorm:"size:16;not null;index" json:"mapping_status"`
	Group               string    `gorm:"size:256;not null" json:"group"`
	ModelName           string    `gorm:"size:256;not null" json:"model_name"`
	NormalizationStatus string    `gorm:"size:16;not null;index" json:"normalization_status"`
	ChargedUSD          float64   `gorm:"type:numeric(20,8);not null;default:0" json:"charged_usd"`
	SaleCNY             float64   `gorm:"type:numeric(20,8);not null" json:"sale_cny"`
	CostUSD             float64   `gorm:"type:numeric(20,8);not null" json:"cost_usd"`
	CostCNY             float64   `gorm:"type:numeric(20,8);not null" json:"cost_cny"`
	ProfitCNY           float64   `gorm:"type:numeric(20,8);not null" json:"profit_cny"`
	CreditUSDPerCNY     float64   `gorm:"type:numeric(20,8);not null" json:"credit_usd_per_cny"`
	AllocationStatus    string    `gorm:"size:24;not null;index" json:"allocation_status"`
	Complete            bool      `gorm:"not null;default:false" json:"complete"`
	CalculatedAt        time.Time `gorm:"not null;index" json:"calculated_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (ProfitBucket) TableName() string { return "profit_buckets" }

// ProfitDailySnapshot is a compact dashboard read model. DayStart is stored
// in UTC but represents midnight in the configured business timezone.
type ProfitDailySnapshot struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	DayStart            time.Time `gorm:"not null;uniqueIndex" json:"day_start"`
	SaleCNY             float64   `gorm:"type:numeric(20,8);not null" json:"sale_cny"`
	CostCNY             float64   `gorm:"type:numeric(20,8);not null" json:"cost_cny"`
	ProfitCNY           float64   `gorm:"type:numeric(20,8);not null" json:"profit_cny"`
	SettledSaleCNY      float64   `gorm:"type:numeric(20,8);not null" json:"settled_sale_cny"`
	UnmappedSaleCNY     float64   `gorm:"type:numeric(20,8);not null" json:"unmapped_sale_cny"`
	UnsettledSaleCNY    float64   `gorm:"type:numeric(20,8);not null" json:"unsettled_sale_cny"`
	BucketCount         int64     `gorm:"not null" json:"bucket_count"`
	SettledBucketCount  int64     `gorm:"not null" json:"settled_bucket_count"`
	UnmappedBucketCount int64     `gorm:"not null" json:"unmapped_bucket_count"`
	Complete            bool      `gorm:"not null;default:false" json:"complete"`
	CalculatedAt        time.Time `gorm:"not null" json:"calculated_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (ProfitDailySnapshot) TableName() string { return "profit_daily_snapshots" }

// BuildProfitBuckets allocates each upstream usage interval once across all
// sales facts that point at that account and share the same time window.
func BuildProfitBuckets(billing []NewAPIBillingBucket, usage []UsageBucket) ([]ProfitBucket, error) {
	return BuildProfitBucketsWithRates(billing, usage, DefaultUpstreamCostCNYPerUSD)
}

// BuildProfitBucketsWithRates allocates upstream usage to mapped sales rows for
// drill-down purposes. The allocation is only a presentation aid: the period
// cost total is always calculated independently from every upstream usage row.
func BuildProfitBucketsWithRates(billing []NewAPIBillingBucket, usage []UsageBucket, upstreamCostCNYPerUSD float64) ([]ProfitBucket, error) {
	if upstreamCostCNYPerUSD <= 0 || math.IsNaN(upstreamCostCNYPerUSD) || math.IsInf(upstreamCostCNYPerUSD, 0) {
		return nil, errors.New("invalid upstream_cost_cny_per_usd")
	}
	profits := make([]ProfitBucket, len(billing))
	type allocationKey struct {
		upstreamID uint
		start      int64
		end        int64
	}
	groups := make(map[allocationKey][]int)
	for i, sale := range billing {
		if sale.BucketStart.IsZero() || !sale.BucketEnd.After(sale.BucketStart) {
			return nil, fmt.Errorf("invalid billing bucket window at index %d", i)
		}
		if sale.NewAPIChannelID <= 0 {
			return nil, fmt.Errorf("invalid new-api channel at index %d", i)
		}
		if sale.FactKey == "" {
			sale.FactKey = fmt.Sprintf("billing-%d-%d-%d", sale.NewAPIChannelID, sale.BucketStart.Unix(), i)
		}
		if sale.MappingStatus == "" {
			sale.MappingStatus = "unmapped"
		}
		profits[i] = ProfitBucket{
			FactKey: sale.FactKey, BucketStart: sale.BucketStart.UTC(), BucketEnd: sale.BucketEnd.UTC(),
			ResolutionSeconds: sale.ResolutionSeconds, NewAPIChannelID: sale.NewAPIChannelID, NewAPIChannelName: sale.NewAPIChannelName,
			UpstreamChannelID: cloneUint(sale.UpstreamChannelID), MappingStatus: sale.MappingStatus,
			Group: sale.Group, ModelName: sale.ModelName, NormalizationStatus: sale.NormalizationStatus,
			ChargedUSD: sale.ChargedUSD, SaleCNY: sale.SaleCNY, CreditUSDPerCNY: validCreditRate(sale.CreditUSDPerCNY),
			AllocationStatus: ProfitAllocationUnmapped,
		}
		if sale.MappingStatus != "mapped" || sale.UpstreamChannelID == nil || *sale.UpstreamChannelID == 0 {
			if sale.MappingStatus == "ambiguous" {
				profits[i].AllocationStatus = ProfitAllocationAmbiguous
			}
			continue
		}
		groups[allocationKey{upstreamID: *sale.UpstreamChannelID, start: sale.BucketStart.UnixNano(), end: sale.BucketEnd.UnixNano()}] = append(groups[allocationKey{upstreamID: *sale.UpstreamChannelID, start: sale.BucketStart.UnixNano(), end: sale.BucketEnd.UnixNano()}], i)
	}

	for key, indexes := range groups {
		costUSD, found, complete, currencyOK := usageCostForWindow(usage, key.upstreamID, time.Unix(0, key.start).UTC(), time.Unix(0, key.end).UTC(), upstreamCostCNYPerUSD)
		bases := make([]float64, len(indexes))
		var totalBase float64
		for i, index := range indexes {
			base := math.Abs(billing[index].ChargedUSD)
			if base == 0 {
				base = math.Abs(billing[index].NormalizedUSD)
			}
			if base == 0 {
				base = math.Abs(billing[index].SaleCNY)
			}
			bases[i] = base
			totalBase += base
		}
		if totalBase == 0 {
			for i := range bases {
				bases[i] = 1
			}
			totalBase = float64(len(bases))
		}
		for i, index := range indexes {
			profit := &profits[index]
			share := bases[i] / totalBase
			profit.CostUSD = costUSD * share
			profit.CostCNY = profit.CostUSD * upstreamCostCNYPerUSD
			normalizationUsable := profit.NormalizationStatus != BillingStatusUnavailable
			switch {
			case !found:
				profit.AllocationStatus = ProfitAllocationCostMissing
			case !currencyOK:
				profit.AllocationStatus = ProfitAllocationCostUnavailable
			case !normalizationUsable:
				profit.AllocationStatus = ProfitAllocationUnavailable
			default:
				profit.AllocationStatus = ProfitAllocationSettled
				if !complete || billing[index].Complete == false || profit.NormalizationStatus != BillingStatusExact {
					profit.AllocationStatus = ProfitAllocationPartial
				}
			}
			if profit.AllocationStatus == ProfitAllocationSettled || profit.AllocationStatus == ProfitAllocationPartial {
				profit.ProfitCNY = profit.SaleCNY - profit.CostCNY
			}
			profit.Complete = profit.AllocationStatus == ProfitAllocationSettled
		}
	}
	return profits, nil
}

func usageCostForWindow(usage []UsageBucket, upstreamID uint, start, end time.Time, upstreamCostCNYPerUSD float64) (float64, bool, bool, bool) {
	start = start.UTC()
	end = end.UTC()
	// Usage is collected at multiple resolutions (for example, a recent
	// five-minute sample alongside an hourly history row). Partition the target
	// interval at every usage boundary and choose the finest bucket for each
	// segment so the same upstream cost is never counted twice.
	candidates := make([]UsageBucket, 0)
	boundaries := []time.Time{start, end}
	for _, bucket := range usage {
		if bucket.ChannelID != upstreamID || bucket.BucketStart.IsZero() || !bucket.BucketEnd.After(bucket.BucketStart) {
			continue
		}
		bucketStart := bucket.BucketStart.UTC()
		bucketEnd := bucket.BucketEnd.UTC()
		overlapStart := maxTime(start, bucketStart)
		overlapEnd := minTime(end, bucketEnd)
		if !overlapEnd.After(overlapStart) {
			continue
		}
		candidates = append(candidates, bucket)
		boundaries = append(boundaries, overlapStart, overlapEnd)
	}
	if len(candidates) == 0 {
		return 0, false, true, true
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Before(boundaries[j]) })
	uniqueBoundaries := boundaries[:0]
	for _, boundary := range boundaries {
		if len(uniqueBoundaries) == 0 || !boundary.Equal(uniqueBoundaries[len(uniqueBoundaries)-1]) {
			uniqueBoundaries = append(uniqueBoundaries, boundary)
		}
	}

	var cost float64
	found := false
	complete := true
	currencyOK := true
	for i := 0; i+1 < len(uniqueBoundaries); i++ {
		segmentStart := uniqueBoundaries[i]
		segmentEnd := uniqueBoundaries[i+1]
		if !segmentEnd.After(segmentStart) {
			continue
		}
		selected := -1
		for j := range candidates {
			bucket := candidates[j]
			if bucket.BucketStart.After(segmentStart) || bucket.BucketEnd.Before(segmentEnd) {
				continue
			}
			if selected < 0 || preferredUsageBucket(bucket, candidates[selected]) {
				selected = j
			}
		}
		if selected < 0 {
			complete = false
			continue
		}
		bucket := candidates[selected]
		found = true
		if !bucket.Complete {
			complete = false
		}
		amountUSD, ok := usageAmountUSD(bucket.Amount, bucket.Currency, upstreamCostCNYPerUSD)
		if !ok {
			currencyOK = false
			continue
		}
		fraction := segmentEnd.Sub(segmentStart).Seconds() / bucket.BucketEnd.Sub(bucket.BucketStart).Seconds()
		cost += amountUSD * fraction
	}
	return cost, found, complete, currencyOK
}

func preferredUsageBucket(candidate, current UsageBucket) bool {
	candidateDuration := candidate.BucketEnd.Sub(candidate.BucketStart)
	currentDuration := current.BucketEnd.Sub(current.BucketStart)
	if candidateDuration != currentDuration {
		return candidateDuration < currentDuration
	}
	if candidate.Complete != current.Complete {
		return candidate.Complete
	}
	return candidate.ID < current.ID
}

func usageAmountUSD(amount float64, currency string, upstreamCostCNYPerUSD float64) (float64, bool) {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "", "USD", "US dollar", "US DOLLAR":
		return amount, true
	case "CNY", "RMB", "￥", "¥":
		if upstreamCostCNYPerUSD <= 0 {
			return 0, false
		}
		return amount / upstreamCostCNYPerUSD, true
	default:
		return 0, false
	}
}

func UsageAmountUSD(amount float64, currency string, upstreamCostCNYPerUSD float64) (float64, bool) {
	return usageAmountUSD(amount, currency, upstreamCostCNYPerUSD)
}

func UsageCostCNY(amount float64, currency string, upstreamCostCNYPerUSD float64) (float64, bool) {
	amountUSD, ok := usageAmountUSD(amount, currency, upstreamCostCNYPerUSD)
	if !ok {
		return 0, false
	}
	return amountUSD * upstreamCostCNYPerUSD, true
}

const DefaultUpstreamCostCNYPerUSD = 1.0

func validCreditRate(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return DefaultCreditUSDPerCNY
	}
	return value
}

func cloneUint(value *uint) *uint {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func maxTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

// BuildProfitDailySnapshots aggregates profit buckets by midnight in location.
func BuildProfitDailySnapshots(profits []ProfitBucket, location *time.Location, calculatedAt time.Time) []ProfitDailySnapshot {
	if location == nil {
		location = time.UTC
	}
	if calculatedAt.IsZero() {
		calculatedAt = time.Now().UTC()
	}
	type state struct {
		snapshot ProfitDailySnapshot
	}
	byDay := make(map[int64]*state)
	for _, profit := range profits {
		if profit.BucketStart.IsZero() || !profit.BucketEnd.After(profit.BucketStart) {
			continue
		}
		start := profit.BucketStart.UTC()
		end := profit.BucketEnd.UTC()
		totalSeconds := end.Sub(start).Seconds()
		for cursor := start; cursor.Before(end); {
			local := cursor.In(location)
			dayLocal := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
			dayStart := dayLocal.UTC()
			nextDay := dayLocal.AddDate(0, 0, 1).UTC()
			segmentEnd := minTime(end, nextDay)
			fraction := segmentEnd.Sub(cursor).Seconds() / totalSeconds
			item := byDay[dayStart.Unix()]
			if item == nil {
				item = &state{snapshot: ProfitDailySnapshot{DayStart: dayStart, Complete: true, CalculatedAt: calculatedAt.UTC()}}
				byDay[dayStart.Unix()] = item
			}
			item.snapshot.SaleCNY += profit.SaleCNY * fraction
			item.snapshot.CostCNY += profit.CostCNY * fraction
			item.snapshot.ProfitCNY += profit.ProfitCNY * fraction
			if profit.AllocationStatus == ProfitAllocationSettled {
				item.snapshot.SettledSaleCNY += profit.SaleCNY * fraction
			} else if profit.AllocationStatus == ProfitAllocationUnmapped || profit.AllocationStatus == ProfitAllocationAmbiguous {
				item.snapshot.UnmappedSaleCNY += profit.SaleCNY * fraction
			} else {
				item.snapshot.UnsettledSaleCNY += profit.SaleCNY * fraction
			}
			if cursor.Equal(start) {
				item.snapshot.BucketCount++
				switch profit.AllocationStatus {
				case ProfitAllocationSettled:
					item.snapshot.SettledBucketCount++
				case ProfitAllocationUnmapped, ProfitAllocationAmbiguous:
					item.snapshot.UnmappedBucketCount++
				}
			}
			if !profit.Complete {
				item.snapshot.Complete = false
			}
			cursor = segmentEnd
		}
	}
	items := make([]ProfitDailySnapshot, 0, len(byDay))
	for _, item := range byDay {
		item.snapshot.SaleCNY = roundProfitAmount(item.snapshot.SaleCNY)
		item.snapshot.CostCNY = roundProfitAmount(item.snapshot.CostCNY)
		item.snapshot.ProfitCNY = roundProfitAmount(item.snapshot.ProfitCNY)
		item.snapshot.SettledSaleCNY = roundProfitAmount(item.snapshot.SettledSaleCNY)
		item.snapshot.UnmappedSaleCNY = roundProfitAmount(item.snapshot.UnmappedSaleCNY)
		item.snapshot.UnsettledSaleCNY = roundProfitAmount(item.snapshot.UnsettledSaleCNY)
		items = append(items, item.snapshot)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DayStart.Before(items[j].DayStart) })
	return items
}

func roundProfitAmount(value float64) float64 {
	return math.Round(value*1e8) / 1e8
}

type ProfitSummary struct {
	SaleCNY float64 `json:"sale_cny"`
	// CostCNY is the complete upstream usage cost for the requested period,
	// independent of whether a NewAPI sale could be mapped to an account.
	CostCNY             float64 `json:"cost_cny"`
	CostUSD             float64 `json:"cost_usd"`
	ProfitCNY           float64 `json:"profit_cny"`
	ProfitMargin        float64 `json:"profit_margin"`
	AllocatedCostCNY    float64 `json:"allocated_cost_cny"`
	UnmatchedCostCNY    float64 `json:"unmatched_cost_cny"`
	StageUsageCostCNY   float64 `json:"stage_usage_cost_cny"`
	SalesDetailCNY      float64 `json:"sales_detail_cny"`
	CostDetailCNY       float64 `json:"cost_detail_cny"`
	ReconciliationDelta float64 `json:"reconciliation_delta_cny"`
	SettledSaleCNY      float64 `json:"settled_sale_cny"`
	UnmappedSaleCNY     float64 `json:"unmapped_sale_cny"`
	UnsettledSaleCNY    float64 `json:"unsettled_sale_cny"`
	BucketCount         int64   `json:"bucket_count"`
	SettledBucketCount  int64   `json:"settled_bucket_count"`
	UnmappedBucketCount int64   `json:"unmapped_bucket_count"`
	Complete            bool    `json:"complete"`
}

func SummarizeProfit(profits []ProfitBucket) ProfitSummary {
	summary := ProfitSummary{Complete: len(profits) > 0}
	for _, profit := range profits {
		summary.BucketCount++
		summary.SaleCNY += profit.SaleCNY
		summary.CostCNY += profit.CostCNY
		summary.CostUSD += profit.CostUSD
		summary.AllocatedCostCNY += profit.CostCNY
		summary.ProfitCNY += profit.ProfitCNY
		switch profit.AllocationStatus {
		case ProfitAllocationSettled:
			summary.SettledSaleCNY += profit.SaleCNY
			summary.SettledBucketCount++
		case ProfitAllocationUnmapped, ProfitAllocationAmbiguous:
			summary.UnmappedSaleCNY += profit.SaleCNY
			summary.UnmappedBucketCount++
		default:
			summary.UnsettledSaleCNY += profit.SaleCNY
		}
		if !profit.Complete {
			summary.Complete = false
		}
	}
	summary.SaleCNY = roundProfitAmount(summary.SaleCNY)
	summary.CostCNY = roundProfitAmount(summary.CostCNY)
	summary.CostUSD = roundProfitAmount(summary.CostUSD)
	summary.AllocatedCostCNY = roundProfitAmount(summary.AllocatedCostCNY)
	summary.StageUsageCostCNY = summary.CostCNY
	summary.SalesDetailCNY = summary.SaleCNY
	summary.CostDetailCNY = summary.CostCNY
	summary.ReconciliationDelta = roundProfitAmount(summary.SaleCNY - summary.CostCNY - summary.ProfitCNY)
	summary.ProfitCNY = roundProfitAmount(summary.ProfitCNY)
	summary.SettledSaleCNY = roundProfitAmount(summary.SettledSaleCNY)
	summary.UnmappedSaleCNY = roundProfitAmount(summary.UnmappedSaleCNY)
	summary.UnsettledSaleCNY = roundProfitAmount(summary.UnsettledSaleCNY)
	if summary.SaleCNY != 0 {
		summary.ProfitMargin = roundProfitAmount(summary.ProfitCNY / summary.SaleCNY)
	}
	return summary
}

// SummarizeProfitWithUsage reconciles two independent ledgers. Sales come from
// NewAPI billing facts; cost comes from every upstream usage bucket in the
// requested interval, including channels without a mapping or a sale.
func SummarizeProfitWithUsage(profits []ProfitBucket, usage []UsageBucket, start, end time.Time, resolutionSeconds int, upstreamCostCNYPerUSD float64) ProfitSummary {
	summary := SummarizeProfit(profits)
	if upstreamCostCNYPerUSD <= 0 || math.IsNaN(upstreamCostCNYPerUSD) || math.IsInf(upstreamCostCNYPerUSD, 0) {
		upstreamCostCNYPerUSD = DefaultUpstreamCostCNYPerUSD
	}
	usageUSD, usageCNY, complete, currencyOK := SumUpstreamUsageCost(usage, start, end, resolutionSeconds, upstreamCostCNYPerUSD)
	summary.CostUSD = roundProfitAmount(usageUSD)
	summary.CostCNY = roundProfitAmount(usageCNY)
	summary.StageUsageCostCNY = summary.CostCNY
	summary.CostDetailCNY = summary.CostCNY
	summary.SalesDetailCNY = summary.SaleCNY
	summary.UnmatchedCostCNY = roundProfitAmount(math.Max(0, summary.CostCNY-summary.AllocatedCostCNY))
	summary.ProfitCNY = roundProfitAmount(summary.SaleCNY - summary.CostCNY)
	summary.ReconciliationDelta = roundProfitAmount(summary.SaleCNY - summary.CostCNY - summary.ProfitCNY)
	if summary.SaleCNY != 0 {
		summary.ProfitMargin = roundProfitAmount(summary.ProfitCNY / summary.SaleCNY)
	}
	if len(usage) == 0 || !complete || !currencyOK {
		summary.Complete = false
	}
	return summary
}

// SumUpstreamUsageCost returns the complete upstream cost for a period. When
// callers pass multiple resolutions, the finest covering bucket wins for each
// segment, preventing a five-minute sample and an hourly history row from
// being counted twice.
func SumUpstreamUsageCost(usage []UsageBucket, start, end time.Time, resolutionSeconds int, upstreamCostCNYPerUSD float64) (float64, float64, bool, bool) {
	if !end.After(start) {
		return 0, 0, false, true
	}
	if upstreamCostCNYPerUSD <= 0 || math.IsNaN(upstreamCostCNYPerUSD) || math.IsInf(upstreamCostCNYPerUSD, 0) {
		return 0, 0, false, false
	}
	start = start.UTC()
	end = end.UTC()
	byChannel := make(map[uint][]UsageBucket)
	for _, bucket := range usage {
		if bucket.BucketStart.IsZero() || !bucket.BucketEnd.After(bucket.BucketStart) || bucket.BucketEnd.Before(start) || !bucket.BucketStart.Before(end) {
			continue
		}
		if resolutionSeconds > 0 && bucket.ResolutionSeconds != resolutionSeconds {
			continue
		}
		byChannel[bucket.ChannelID] = append(byChannel[bucket.ChannelID], bucket)
	}
	var totalUSD, totalCNY float64
	complete := true
	currencyOK := true
	for _, channelBuckets := range byChannel {
		boundaries := []time.Time{start, end}
		for _, bucket := range channelBuckets {
			overlapStart := maxTime(start, bucket.BucketStart.UTC())
			overlapEnd := minTime(end, bucket.BucketEnd.UTC())
			if overlapEnd.After(overlapStart) {
				boundaries = append(boundaries, overlapStart, overlapEnd)
			}
		}
		sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Before(boundaries[j]) })
		unique := boundaries[:0]
		for _, boundary := range boundaries {
			if len(unique) == 0 || !boundary.Equal(unique[len(unique)-1]) {
				unique = append(unique, boundary)
			}
		}
		for i := 0; i+1 < len(unique); i++ {
			segmentStart, segmentEnd := unique[i], unique[i+1]
			if !segmentEnd.After(segmentStart) {
				continue
			}
			selected := -1
			for j, bucket := range channelBuckets {
				if bucket.BucketStart.After(segmentStart) || bucket.BucketEnd.Before(segmentEnd) {
					continue
				}
				if selected < 0 || preferredUsageBucket(bucket, channelBuckets[selected]) {
					selected = j
				}
			}
			if selected < 0 {
				complete = false
				continue
			}
			bucket := channelBuckets[selected]
			if !bucket.Complete {
				complete = false
			}
			amountUSD, ok := usageAmountUSD(bucket.Amount, bucket.Currency, upstreamCostCNYPerUSD)
			if !ok {
				currencyOK = false
				continue
			}
			fraction := segmentEnd.Sub(segmentStart).Seconds() / bucket.BucketEnd.Sub(bucket.BucketStart).Seconds()
			amountUSD *= fraction
			totalUSD += amountUSD
			totalCNY += amountUSD * upstreamCostCNYPerUSD
		}
	}
	return totalUSD, totalCNY, complete, currencyOK
}

type ProfitTrendSpec struct {
	StartAt                 time.Time
	EndAt                   time.Time
	UsageResolutionSeconds  int
	OutputResolutionSeconds int
}

type ProfitTrendPoint struct {
	StartAt          time.Time `json:"start_at"`
	EndAt            time.Time `json:"end_at"`
	SaleCNY          float64   `json:"sale_cny"`
	CostCNY          float64   `json:"cost_cny"`
	ProfitCNY        float64   `json:"profit_cny"`
	SettledSaleCNY   float64   `json:"settled_sale_cny"`
	UnmappedSaleCNY  float64   `json:"unmapped_sale_cny"`
	UnsettledSaleCNY float64   `json:"unsettled_sale_cny"`
	Complete         bool      `json:"complete"`
	HasData          bool      `json:"-"`
}

func ResolveProfitTrendSpec(rangeID string, now time.Time, location *time.Location) (ProfitTrendSpec, error) {
	if location == nil {
		location = time.UTC
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	spec := ProfitTrendSpec{}
	switch rangeID {
	case "24h":
		spec.EndAt = now.Truncate(time.Hour)
		spec.StartAt = spec.EndAt.Add(-24 * time.Hour)
		spec.UsageResolutionSeconds = 3600
		spec.OutputResolutionSeconds = 3600
	case "7d", "30d":
		local := now.In(location)
		dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
		days := 7
		if rangeID == "30d" {
			days = 30
		}
		spec.StartAt = dayStart.AddDate(0, 0, -(days - 1)).UTC()
		spec.EndAt = dayStart.AddDate(0, 0, 1).UTC()
		spec.UsageResolutionSeconds = 3600
		spec.OutputResolutionSeconds = 86400
	default:
		return ProfitTrendSpec{}, errors.New("unsupported profit trend range")
	}
	return spec, nil
}

func BuildProfitTrend(profits []ProfitBucket, spec ProfitTrendSpec, location *time.Location) []ProfitTrendPoint {
	if location == nil {
		location = time.UTC
	}
	if !spec.EndAt.After(spec.StartAt) || spec.OutputResolutionSeconds <= 0 {
		return nil
	}
	points := make(map[int64]*ProfitTrendPoint)
	for cursor := profitOutputBucketStart(spec.StartAt, spec.OutputResolutionSeconds, location); cursor.Before(spec.EndAt); cursor = profitOutputBucketEnd(cursor, spec.OutputResolutionSeconds, location) {
		end := profitOutputBucketEnd(cursor, spec.OutputResolutionSeconds, location)
		points[cursor.Unix()] = &ProfitTrendPoint{StartAt: cursor, EndAt: end}
	}
	for _, profit := range profits {
		if profit.BucketStart.IsZero() || !profit.BucketEnd.After(profit.BucketStart) {
			continue
		}
		start := maxTime(profit.BucketStart.UTC(), spec.StartAt)
		end := minTime(profit.BucketEnd.UTC(), spec.EndAt)
		if !end.After(start) {
			continue
		}
		totalSeconds := profit.BucketEnd.Sub(profit.BucketStart).Seconds()
		for cursor := start; cursor.Before(end); {
			pointStart := profitOutputBucketStart(cursor, spec.OutputResolutionSeconds, location)
			point := points[pointStart.Unix()]
			if point == nil {
				cursor = profitOutputBucketEnd(pointStart, spec.OutputResolutionSeconds, location)
				continue
			}
			segmentEnd := minTime(end, point.EndAt)
			fraction := segmentEnd.Sub(cursor).Seconds() / totalSeconds
			point.HasData = true
			point.SaleCNY += profit.SaleCNY * fraction
			point.CostCNY += profit.CostCNY * fraction
			point.ProfitCNY += profit.ProfitCNY * fraction
			switch profit.AllocationStatus {
			case ProfitAllocationSettled:
				point.SettledSaleCNY += profit.SaleCNY * fraction
			case ProfitAllocationUnmapped, ProfitAllocationAmbiguous:
				point.UnmappedSaleCNY += profit.SaleCNY * fraction
			default:
				point.UnsettledSaleCNY += profit.SaleCNY * fraction
			}
			if !profit.Complete {
				point.Complete = false
			} else if !point.Complete {
				// Complete is initialized below once the point has at least one row.
			}
			cursor = segmentEnd
		}
	}
	items := make([]ProfitTrendPoint, 0, len(points))
	for _, point := range points {
		if point.HasData {
			allComplete := true
			for _, profit := range profits {
				if profit.BucketStart.Before(point.EndAt) && profit.BucketEnd.After(point.StartAt) && !profit.Complete {
					allComplete = false
					break
				}
			}
			point.Complete = allComplete
		}
		point.SaleCNY = roundProfitAmount(point.SaleCNY)
		point.CostCNY = roundProfitAmount(point.CostCNY)
		point.ProfitCNY = roundProfitAmount(point.ProfitCNY)
		point.SettledSaleCNY = roundProfitAmount(point.SettledSaleCNY)
		point.UnmappedSaleCNY = roundProfitAmount(point.UnmappedSaleCNY)
		point.UnsettledSaleCNY = roundProfitAmount(point.UnsettledSaleCNY)
		items = append(items, *point)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].StartAt.Before(items[j].StartAt) })
	return items
}

func BuildProfitTrendWithUsage(profits []ProfitBucket, usage []UsageBucket, spec ProfitTrendSpec, location *time.Location, upstreamCostCNYPerUSD float64) []ProfitTrendPoint {
	points := BuildProfitTrend(profits, spec, location)
	for i := range points {
		costUSD, costCNY, complete, currencyOK := SumUpstreamUsageCost(usage, points[i].StartAt, points[i].EndAt, spec.UsageResolutionSeconds, upstreamCostCNYPerUSD)
		_ = costUSD
		points[i].CostCNY = roundProfitAmount(costCNY)
		points[i].ProfitCNY = roundProfitAmount(points[i].SaleCNY - points[i].CostCNY)
		if costCNY != 0 {
			points[i].HasData = true
		}
		if !complete || !currencyOK {
			points[i].Complete = false
		}
	}
	return points
}

func profitOutputBucketStart(at time.Time, seconds int, location *time.Location) time.Time {
	if seconds == 86400 {
		local := at.In(location)
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC()
	}
	return at.UTC().Truncate(time.Duration(seconds) * time.Second)
}

func profitOutputBucketEnd(start time.Time, seconds int, location *time.Location) time.Time {
	if seconds == 86400 {
		return start.In(location).AddDate(0, 0, 1).UTC()
	}
	return start.Add(time.Duration(seconds) * time.Second)
}

type Profit struct{ db *gorm.DB }

func NewProfit(db *gorm.DB) *Profit { return &Profit{db: db} }

// SettleWindow loads upstream usage for the mapped accounts in a billing
// window, allocates the shared cost, and persists the resulting profit rows.
func (r *Billing) SettleWindow(windowStart, windowEnd time.Time, buckets []NewAPIBillingBucket, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("billing database is not initialized")
	}
	if !windowEnd.After(windowStart) {
		return errors.New("invalid billing settlement window")
	}
	profits, err := r.buildProfitBucketsForWindow(r.db, windowStart, windowEnd, buckets)
	if err != nil {
		return err
	}
	return NewProfit(r.db).ReplaceWindow(windowStart, windowEnd, profits, now)
}

// SettleAndReplaceWindow commits a manually rebuilt billing window and its
// profit read model together. Unlike the scheduler path, it intentionally does
// not write a BillingSyncState watermark.
func (r *Billing) SettleAndReplaceWindow(windowStart, windowEnd time.Time, resolutionSeconds int, buckets []NewAPIBillingBucket, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("billing database is not initialized")
	}
	if !windowEnd.After(windowStart) || resolutionSeconds <= 0 {
		return errors.New("invalid billing replacement window")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		profits, err := r.buildProfitBucketsForWindow(tx, windowStart, windowEnd, buckets)
		if err != nil {
			return err
		}
		if err := replaceBillingWindowTx(tx, windowStart, windowEnd, resolutionSeconds, buckets, now); err != nil {
			return err
		}
		return replaceProfitWindowTx(tx, windowStart, windowEnd, profits, now)
	})
}

func (r *Billing) buildProfitBucketsForWindow(db *gorm.DB, windowStart, windowEnd time.Time, buckets []NewAPIBillingBucket) ([]ProfitBucket, error) {
	channelSet := make(map[uint]struct{})
	for _, bucket := range buckets {
		if bucket.MappingStatus == "mapped" && bucket.UpstreamChannelID != nil && *bucket.UpstreamChannelID > 0 {
			channelSet[*bucket.UpstreamChannelID] = struct{}{}
		}
	}
	channelIDs := make([]uint, 0, len(channelSet))
	for id := range channelSet {
		channelIDs = append(channelIDs, id)
	}
	var usage []UsageBucket
	if len(channelIDs) > 0 {
		if err := db.Where("channel_id IN ? AND bucket_start < ? AND bucket_end > ?", channelIDs, windowEnd, windowStart).Find(&usage).Error; err != nil {
			return nil, fmt.Errorf("load upstream usage for settlement: %w", err)
		}
	}
	profits, err := BuildProfitBucketsWithRates(buckets, usage, r.upstreamCostRate())
	if err != nil {
		return nil, err
	}
	return profits, nil
}

// SettleAndReplaceWindowAndAdvance commits one billing window as a single
// ledger transaction: sales facts, allocated costs, profit read models, and
// the successful source watermark either all change or all remain untouched.
func (r *Billing) SettleAndReplaceWindowAndAdvance(source string, windowStart, windowEnd time.Time, resolutionSeconds int, buckets []NewAPIBillingBucket, state BillingSyncState, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("billing database is not initialized")
	}
	if source == "" || !windowEnd.After(windowStart) || resolutionSeconds <= 0 {
		return errors.New("invalid billing replacement window")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state.Source = source
	state.UpdatedAt = now.UTC()
	return r.db.Transaction(func(tx *gorm.DB) error {
		profits, err := r.buildProfitBucketsForWindow(tx, windowStart, windowEnd, buckets)
		if err != nil {
			return err
		}
		if err := replaceBillingWindowTx(tx, windowStart, windowEnd, resolutionSeconds, buckets, now); err != nil {
			return err
		}
		if err := replaceProfitWindowTx(tx, windowStart, windowEnd, profits, now); err != nil {
			return err
		}
		return saveBillingSyncStateTx(tx, state)
	})
}

// ReplaceWindow rewrites profit rows for one source window and refreshes the
// affected Shanghai-day snapshots. It is idempotent on the billing fact key.
func (r *Profit) ReplaceWindow(windowStart, windowEnd time.Time, profits []ProfitBucket, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("profit database is not initialized")
	}
	if !windowEnd.After(windowStart) {
		return errors.New("invalid profit replacement window")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		return replaceProfitWindowTx(tx, windowStart, windowEnd, profits, now)
	})
}

func replaceProfitWindowTx(tx *gorm.DB, windowStart, windowEnd time.Time, profits []ProfitBucket, now time.Time) error {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	if err := tx.Where("bucket_start >= ? AND bucket_start < ?", windowStart, windowEnd).Delete(&ProfitBucket{}).Error; err != nil {
		return err
	}
	for i := range profits {
		profits[i].CalculatedAt = now.UTC()
		profits[i].UpdatedAt = now.UTC()
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "billing_fact_key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"bucket_start", "bucket_end", "resolution_seconds", "new_api_channel_id", "new_api_channel_name", "upstream_channel_id", "mapping_status",
				"charged_usd", "sale_cny", "cost_usd", "cost_cny", "profit_cny", "credit_usd_per_cny", "allocation_status", "complete", "calculated_at", "updated_at",
			}),
		}).Create(&profits[i]).Error; err != nil {
			return err
		}
	}
	firstDay := dayStartAt(windowStart, location)
	lastDay := dayStartAt(windowEnd.Add(-time.Nanosecond), location)
	dayEnd := lastDay.AddDate(0, 0, 1)
	var rows []ProfitBucket
	if err := tx.Where("bucket_start >= ? AND bucket_start < ?", firstDay, dayEnd).Find(&rows).Error; err != nil {
		return err
	}
	snapshots := BuildProfitDailySnapshots(rows, location, now)
	if err := tx.Where("day_start >= ? AND day_start < ?", firstDay, dayEnd).Delete(&ProfitDailySnapshot{}).Error; err != nil {
		return err
	}
	for i := range snapshots {
		snapshots[i].UpdatedAt = now.UTC()
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "day_start"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"sale_cny", "cost_cny", "profit_cny", "settled_sale_cny", "unmapped_sale_cny", "unsettled_sale_cny",
				"bucket_count", "settled_bucket_count", "unmapped_bucket_count", "complete", "calculated_at", "updated_at",
			}),
		}).Create(&snapshots[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

func dayStartAt(at time.Time, location *time.Location) time.Time {
	local := at.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC()
}

func (r *Profit) ListBuckets(start, end time.Time) ([]ProfitBucket, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("profit database is not initialized")
	}
	var rows []ProfitBucket
	err := r.db.Where("bucket_start >= ? AND bucket_start < ?", start, end).Order("bucket_start ASC, new_api_channel_id ASC, id ASC").Find(&rows).Error
	return rows, err
}

func (r *Profit) ListDailySnapshots(start, end time.Time) ([]ProfitDailySnapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("profit database is not initialized")
	}
	var rows []ProfitDailySnapshot
	err := r.db.Where("day_start >= ? AND day_start < ?", start, end).Order("day_start ASC").Find(&rows).Error
	return rows, err
}
