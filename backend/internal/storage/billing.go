package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BillingResolutionSeconds = 300
	DefaultCreditUSDPerCNY   = 12.0
	BillingStatusExact       = "exact"
	BillingStatusEstimated   = "estimated"
	BillingStatusUnavailable = "unavailable"
)

// NewAPIBillingBucket stores NewAPI consumption facts and the settlement
// inputs/outputs captured at collection time. It is intentionally separate
// from UsageBucket, which remains the upstream-cost ledger.
type NewAPIBillingBucket struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	FactKey             string    `gorm:"size:128;not null;uniqueIndex" json:"fact_key"`
	BucketStart         time.Time `gorm:"not null;index:idx_newapi_billing_window;index" json:"bucket_start"`
	BucketEnd           time.Time `gorm:"not null" json:"bucket_end"`
	ResolutionSeconds   int       `gorm:"not null;index:idx_newapi_billing_window" json:"resolution_seconds"`
	NewAPIChannelID     int       `gorm:"not null;index:idx_newapi_billing_window" json:"newapi_channel_id"`
	NewAPIChannelName   string    `gorm:"size:256" json:"newapi_channel_name,omitempty"`
	UpstreamChannelID   *uint     `gorm:"index" json:"upstream_channel_id,omitempty"`
	MappingStatus       string    `gorm:"size:16;not null;default:'unmapped';index" json:"mapping_status"`
	Group               string    `gorm:"size:256;not null;index:idx_newapi_billing_window" json:"group"`
	ModelName           string    `gorm:"size:256;not null;index:idx_newapi_billing_window" json:"model_name"`
	EffectiveGroupRatio float64   `gorm:"not null" json:"effective_group_ratio"`
	RatioSource         string    `gorm:"size:32;not null" json:"ratio_source"`
	NormalizationStatus string    `gorm:"size:16;not null;index" json:"normalization_status"`
	ConsumeQuota        int64     `gorm:"not null" json:"consume_quota"`
	RefundQuota         int64     `gorm:"not null" json:"refund_quota"`
	NetQuota            int64     `gorm:"not null" json:"net_quota"`
	EventCount          int64     `gorm:"not null" json:"event_count"`
	QuotaPerUnit        float64   `gorm:"not null" json:"quota_per_unit"`
	ChargedUSD          float64   `gorm:"type:numeric(20,8);not null" json:"charged_usd"`
	NormalizedUSD       float64   `gorm:"type:numeric(20,8);not null" json:"normalized_usd"`
	CreditUSDPerCNY     float64   `gorm:"type:numeric(20,8);not null" json:"credit_usd_per_cny"`
	SaleCNY             float64   `gorm:"type:numeric(20,8);not null" json:"sale_cny"`
	Complete            bool      `gorm:"not null;default:true" json:"complete"`
	CollectedAt         time.Time `gorm:"not null;index" json:"collected_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (NewAPIBillingBucket) TableName() string { return "newapi_billing_buckets" }

// NewAPIBillingEvent preserves the source log facts behind an aggregated
// billing bucket. Aggregates are convenient for settlement, while events make
// every sales figure auditable without exposing the request body.
type NewAPIBillingEvent struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	EventKey            string    `gorm:"size:160;not null;uniqueIndex" json:"event_key"`
	SourceLogID         int64     `gorm:"not null;index" json:"source_log_id"`
	CreatedAt           time.Time `gorm:"not null;index" json:"created_at"`
	BucketStart         time.Time `gorm:"not null;index" json:"bucket_start"`
	BucketEnd           time.Time `gorm:"not null" json:"bucket_end"`
	EventType           string    `gorm:"size:16;not null;index" json:"event_type"`
	ChannelID           int       `gorm:"not null;index" json:"channel_id"`
	ChannelName         string    `gorm:"size:256" json:"channel_name,omitempty"`
	UpstreamChannelID   *uint     `gorm:"index" json:"upstream_channel_id,omitempty"`
	MappingStatus       string    `gorm:"size:16;not null;index" json:"mapping_status"`
	Group               string    `gorm:"size:256;not null" json:"group"`
	ModelName           string    `gorm:"size:256;not null" json:"model_name"`
	EffectiveGroupRatio float64   `gorm:"not null" json:"effective_group_ratio"`
	RatioSource         string    `gorm:"size:32;not null" json:"ratio_source"`
	NormalizationStatus string    `gorm:"size:16;not null" json:"normalization_status"`
	Quota               int64     `gorm:"not null" json:"quota"`
	ChargedUSD          float64   `gorm:"type:numeric(20,8);not null" json:"charged_usd"`
	NormalizedUSD       float64   `gorm:"type:numeric(20,8);not null" json:"normalized_usd"`
	CreditUSDPerCNY     float64   `gorm:"type:numeric(20,8);not null" json:"credit_usd_per_cny"`
	SaleCNY             float64   `gorm:"type:numeric(20,8);not null" json:"sale_cny"`
	UserID              int       `gorm:"index" json:"user_id"`
	TokenName           string    `gorm:"size:256" json:"token_name,omitempty"`
	RequestID           string    `gorm:"size:128;index" json:"request_id,omitempty"`
	UpstreamRequestID   string    `gorm:"size:128;index" json:"upstream_request_id,omitempty"`
	CollectedAt         time.Time `gorm:"not null" json:"collected_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (NewAPIBillingEvent) TableName() string { return "newapi_billing_events" }

// BillingSyncState is a singleton-per-source watermark. The end watermark is
// advanced only in the same transaction as the corresponding bucket rewrite.
type BillingSyncState struct {
	Source              string     `gorm:"primaryKey;size:64" json:"source"`
	LastSuccessfulEndAt *time.Time `json:"last_successful_end_at,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	Status              string     `gorm:"size:16;not null" json:"status"`
	LastError           string     `gorm:"type:text" json:"last_error,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (BillingSyncState) TableName() string { return "billing_sync_states" }

type BillingFactInput struct {
	BucketStart         time.Time
	BucketEnd           time.Time
	NewAPIChannelID     int
	NewAPIChannelName   string
	UpstreamChannelID   *uint
	MappingStatus       string
	Group               string
	ModelName           string
	EffectiveGroupRatio float64
	RatioSource         string
	NormalizationStatus string
	ConsumeQuota        int64
	RefundQuota         int64
	EventCount          int64
	QuotaPerUnit        float64
	Complete            bool
}

type BillingEventInput struct {
	EventKey            string
	SourceLogID         int64
	CreatedAt           time.Time
	BucketStart         time.Time
	BucketEnd           time.Time
	EventType           string
	ChannelID           int
	ChannelName         string
	UpstreamChannelID   *uint
	MappingStatus       string
	Group               string
	ModelName           string
	EffectiveGroupRatio float64
	RatioSource         string
	NormalizationStatus string
	Quota               int64
	QuotaPerUnit        float64
	CreditUSDPerCNY     float64
	UserID              int
	TokenName           string
	RequestID           string
	UpstreamRequestID   string
	CollectedAt         time.Time
}

func BuildNewAPIBillingBucket(input BillingFactInput, creditUSDPerCNY float64, collectedAt time.Time) (NewAPIBillingBucket, error) {
	if input.BucketStart.IsZero() || !input.BucketEnd.After(input.BucketStart) {
		return NewAPIBillingBucket{}, errors.New("invalid billing bucket window")
	}
	if input.NewAPIChannelID <= 0 || input.QuotaPerUnit <= 0 || math.IsNaN(input.QuotaPerUnit) || math.IsInf(input.QuotaPerUnit, 0) {
		return NewAPIBillingBucket{}, errors.New("invalid billing bucket identity or quota_per_unit")
	}
	if creditUSDPerCNY <= 0 || math.IsNaN(creditUSDPerCNY) || math.IsInf(creditUSDPerCNY, 0) {
		return NewAPIBillingBucket{}, errors.New("invalid credit_usd_per_cny")
	}
	if input.ConsumeQuota < 0 || input.RefundQuota < 0 {
		return NewAPIBillingBucket{}, errors.New("billing quota cannot be negative")
	}
	if input.MappingStatus == "" {
		input.MappingStatus = "unmapped"
	}
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}

	netQuota := input.ConsumeQuota - input.RefundQuota
	chargedUSD := float64(netQuota) / input.QuotaPerUnit
	normalizedUSD := 0.0
	saleCNY := 0.0
	if input.NormalizationStatus != BillingStatusUnavailable && input.EffectiveGroupRatio > 0 &&
		!math.IsNaN(input.EffectiveGroupRatio) && !math.IsInf(input.EffectiveGroupRatio, 0) {
		normalizedUSD = chargedUSD / input.EffectiveGroupRatio
	}
	// NewAPI quota already includes the model/group multipliers. Sales are
	// therefore converted from chargedUSD directly; normalizedUSD is diagnostic.
	if input.NormalizationStatus != BillingStatusUnavailable {
		// Keep the rational calculation in one division. It avoids introducing
		// binary rounding error between chargedUSD and the configured credit rate.
		saleCNY = float64(netQuota) / (input.QuotaPerUnit * creditUSDPerCNY)
	}

	bucket := NewAPIBillingBucket{
		BucketStart:         input.BucketStart.UTC(),
		BucketEnd:           input.BucketEnd.UTC(),
		ResolutionSeconds:   int(input.BucketEnd.Sub(input.BucketStart) / time.Second),
		NewAPIChannelID:     input.NewAPIChannelID,
		NewAPIChannelName:   input.NewAPIChannelName,
		UpstreamChannelID:   input.UpstreamChannelID,
		MappingStatus:       input.MappingStatus,
		Group:               input.Group,
		ModelName:           input.ModelName,
		EffectiveGroupRatio: input.EffectiveGroupRatio,
		RatioSource:         input.RatioSource,
		NormalizationStatus: input.NormalizationStatus,
		ConsumeQuota:        input.ConsumeQuota,
		RefundQuota:         input.RefundQuota,
		NetQuota:            netQuota,
		EventCount:          input.EventCount,
		QuotaPerUnit:        input.QuotaPerUnit,
		ChargedUSD:          chargedUSD,
		NormalizedUSD:       normalizedUSD,
		CreditUSDPerCNY:     creditUSDPerCNY,
		SaleCNY:             saleCNY,
		Complete:            input.Complete,
		CollectedAt:         collectedAt.UTC(),
	}
	bucket.FactKey = billingFactKey(bucket)
	return bucket, nil
}

func BuildNewAPIBillingEvent(input BillingEventInput) (NewAPIBillingEvent, error) {
	if input.CreatedAt.IsZero() || input.ChannelID <= 0 || input.QuotaPerUnit <= 0 {
		return NewAPIBillingEvent{}, errors.New("invalid billing event identity or quota_per_unit")
	}
	if input.EventType != "consume" && input.EventType != "refund" {
		return NewAPIBillingEvent{}, errors.New("invalid billing event type")
	}
	if input.MappingStatus == "" {
		input.MappingStatus = "unmapped"
	}
	if input.MappingStatus == "mapped" && (input.UpstreamChannelID == nil || *input.UpstreamChannelID == 0) {
		input.MappingStatus = "unmapped"
	}
	if input.CreditUSDPerCNY <= 0 || math.IsNaN(input.CreditUSDPerCNY) || math.IsInf(input.CreditUSDPerCNY, 0) {
		return NewAPIBillingEvent{}, errors.New("invalid credit_usd_per_cny")
	}
	if input.BucketEnd.IsZero() {
		input.BucketStart = input.CreatedAt.UTC().Truncate(time.Duration(BillingResolutionSeconds) * time.Second)
		input.BucketEnd = input.BucketStart.Add(BillingResolutionSeconds * time.Second)
	}
	if input.BucketStart.IsZero() || !input.BucketEnd.After(input.BucketStart) {
		return NewAPIBillingEvent{}, errors.New("invalid billing event bucket window")
	}
	if input.CollectedAt.IsZero() {
		input.CollectedAt = time.Now().UTC()
	}
	quota := input.Quota
	if quota < 0 {
		quota = -quota
	}
	if input.EventType == "refund" {
		quota = -quota
	}
	chargedUSD := float64(quota) / input.QuotaPerUnit
	normalizedUSD := 0.0
	saleCNY := 0.0
	if input.NormalizationStatus != BillingStatusUnavailable && input.EffectiveGroupRatio > 0 &&
		!math.IsNaN(input.EffectiveGroupRatio) && !math.IsInf(input.EffectiveGroupRatio, 0) {
		normalizedUSD = chargedUSD / input.EffectiveGroupRatio
	}
	if input.NormalizationStatus != BillingStatusUnavailable {
		saleCNY = float64(quota) / (input.QuotaPerUnit * input.CreditUSDPerCNY)
	}
	if input.EventKey == "" {
		input.EventKey = fmt.Sprintf("log-%d-%d-%s", input.SourceLogID, input.CreatedAt.UnixNano(), input.EventType)
	}
	return NewAPIBillingEvent{
		EventKey: input.EventKey, SourceLogID: input.SourceLogID, CreatedAt: input.CreatedAt.UTC(),
		BucketStart: input.BucketStart.UTC(), BucketEnd: input.BucketEnd.UTC(), EventType: input.EventType,
		ChannelID: input.ChannelID, ChannelName: input.ChannelName, UpstreamChannelID: cloneUint(input.UpstreamChannelID), MappingStatus: input.MappingStatus,
		Group: input.Group, ModelName: input.ModelName,
		EffectiveGroupRatio: input.EffectiveGroupRatio, RatioSource: input.RatioSource,
		NormalizationStatus: input.NormalizationStatus, Quota: quota, ChargedUSD: chargedUSD,
		NormalizedUSD: normalizedUSD, CreditUSDPerCNY: input.CreditUSDPerCNY, SaleCNY: saleCNY,
		UserID: input.UserID, TokenName: input.TokenName, RequestID: input.RequestID,
		UpstreamRequestID: input.UpstreamRequestID, CollectedAt: input.CollectedAt.UTC(),
	}, nil
}

func billingFactKey(bucket NewAPIBillingBucket) string {
	raw := fmt.Sprintf("%d|%d|%d|%d|%s|%s|%s|%s|%x", bucket.BucketStart.Unix(), bucket.BucketEnd.Unix(), bucket.ResolutionSeconds, bucket.NewAPIChannelID, bucket.Group, bucket.ModelName, bucket.RatioSource, bucket.NormalizationStatus, math.Float64bits(bucket.EffectiveGroupRatio))
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type Billing struct {
	db                    *gorm.DB
	upstreamCostCNYPerUSD float64
}

func NewBilling(db *gorm.DB) *Billing {
	return &Billing{db: db, upstreamCostCNYPerUSD: DefaultUpstreamCostCNYPerUSD}
}

func NewBillingWithCostRate(db *gorm.DB, upstreamCostCNYPerUSD float64) *Billing {
	if upstreamCostCNYPerUSD <= 0 || math.IsNaN(upstreamCostCNYPerUSD) || math.IsInf(upstreamCostCNYPerUSD, 0) {
		upstreamCostCNYPerUSD = DefaultUpstreamCostCNYPerUSD
	}
	return &Billing{db: db, upstreamCostCNYPerUSD: upstreamCostCNYPerUSD}
}

func (r *Billing) SetUpstreamCostRate(upstreamCostCNYPerUSD float64) {
	if r == nil {
		return
	}
	if upstreamCostCNYPerUSD <= 0 || math.IsNaN(upstreamCostCNYPerUSD) || math.IsInf(upstreamCostCNYPerUSD, 0) {
		upstreamCostCNYPerUSD = DefaultUpstreamCostCNYPerUSD
	}
	r.upstreamCostCNYPerUSD = upstreamCostCNYPerUSD
}

func (r *Billing) upstreamCostRate() float64 {
	if r == nil || r.upstreamCostCNYPerUSD <= 0 || math.IsNaN(r.upstreamCostCNYPerUSD) || math.IsInf(r.upstreamCostCNYPerUSD, 0) {
		return DefaultUpstreamCostCNYPerUSD
	}
	return r.upstreamCostCNYPerUSD
}

func (r *Billing) CostRate() float64 {
	return r.upstreamCostRate()
}

// ReplaceWindow rewrites one complete source window. Empty responses delete
// stale local rows too, which is important when refunds or late corrections
// change a previously non-empty bucket.
func (r *Billing) ReplaceWindow(windowStart, windowEnd time.Time, resolutionSeconds int, buckets []NewAPIBillingBucket, now time.Time) error {
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
		return replaceBillingWindowTx(tx, windowStart, windowEnd, resolutionSeconds, buckets, now)
	})
}

// SettleAndReplaceWindowWithEvents atomically replaces billing facts, source
// event details, and their profit rows. It is used when the NewAPI deployment
// supports the detail endpoint; older deployments can continue using the
// aggregate-only method without losing the existing ledger.
func (r *Billing) SettleAndReplaceWindowWithEvents(windowStart, windowEnd time.Time, resolutionSeconds int, buckets []NewAPIBillingBucket, events []NewAPIBillingEvent, now time.Time) error {
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
		if err := replaceBillingEventsWindowTx(tx, windowStart, windowEnd, events, now); err != nil {
			return err
		}
		return replaceProfitWindowTx(tx, windowStart, windowEnd, profits, now)
	})
}

func replaceBillingWindowTx(tx *gorm.DB, windowStart, windowEnd time.Time, resolutionSeconds int, buckets []NewAPIBillingBucket, now time.Time) error {
	if err := tx.Where("bucket_start >= ? AND bucket_start < ? AND resolution_seconds = ?", windowStart, windowEnd, resolutionSeconds).Delete(&NewAPIBillingBucket{}).Error; err != nil {
		return err
	}
	for i := range buckets {
		buckets[i].UpdatedAt = now.UTC()
		if buckets[i].CollectedAt.IsZero() {
			buckets[i].CollectedAt = now.UTC()
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "fact_key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"bucket_end", "resolution_seconds", "consume_quota", "refund_quota", "net_quota", "event_count",
				"new_api_channel_name", "upstream_channel_id", "mapping_status", "quota_per_unit", "charged_usd", "normalized_usd", "credit_usd_per_cny", "sale_cny", "complete", "collected_at", "updated_at",
			}),
		}).Create(&buckets[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

func replaceBillingEventsWindowTx(tx *gorm.DB, windowStart, windowEnd time.Time, events []NewAPIBillingEvent, now time.Time) error {
	if err := tx.Where("bucket_start >= ? AND bucket_start < ?", windowStart, windowEnd).Delete(&NewAPIBillingEvent{}).Error; err != nil {
		return err
	}
	for i := range events {
		events[i].UpdatedAt = now.UTC()
		if events[i].CollectedAt.IsZero() {
			events[i].CollectedAt = now.UTC()
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "event_key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"source_log_id", "created_at", "bucket_start", "bucket_end", "event_type", "channel_id", "channel_name", "upstream_channel_id", "mapping_status", "group", "model_name",
				"effective_group_ratio", "ratio_source", "normalization_status", "quota", "charged_usd", "normalized_usd", "credit_usd_per_cny", "sale_cny",
				"user_id", "token_name", "request_id", "upstream_request_id", "collected_at", "updated_at",
			}),
		}).Create(&events[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// BillingMappingSnapshot records the upstream identity observed for a NewAPI
// channel. Snapshots remain append-only so a conflicting later identity can be
// detected instead of silently restating the billing ledger.
type BillingMappingSnapshot struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	MappingKey        string    `gorm:"size:128;not null;uniqueIndex" json:"mapping_key"`
	NewAPIChannelID   int       `gorm:"not null;index:idx_billing_mapping_lookup" json:"newapi_channel_id"`
	UpstreamChannelID *uint     `gorm:"index" json:"upstream_channel_id,omitempty"`
	MappingStatus     string    `gorm:"size:16;not null;index" json:"mapping_status"`
	UpstreamGroup     string    `gorm:"size:256;not null" json:"upstream_group"`
	ObservedAt        time.Time `gorm:"not null;index:idx_billing_mapping_lookup" json:"observed_at"`
	CreatedAt         time.Time `json:"created_at"`
}

func (BillingMappingSnapshot) TableName() string { return "billing_mapping_snapshots" }

type BillingMappingInput struct {
	NewAPIChannelID   int
	UpstreamChannelID *uint
	MappingStatus     string
	UpstreamGroup     string
	ObservedAt        time.Time
}

func (r *Billing) SaveMappingSnapshots(items []BillingMappingInput, observedAt time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("billing database is not initialized")
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			if item.NewAPIChannelID <= 0 {
				continue
			}
			status := item.MappingStatus
			if status == "" {
				status = "unmapped"
			}
			upstreamID := item.UpstreamChannelID
			if upstreamID != nil && *upstreamID == 0 {
				upstreamID = nil
			}
			if status == "mapped" && upstreamID == nil {
				status = "unmapped"
			}
			if status != "mapped" {
				upstreamID = nil
			}
			at := item.ObservedAt
			if at.IsZero() {
				at = observedAt
			}
			at = at.UTC()
			var upstreamIDValue uint
			if upstreamID != nil {
				upstreamIDValue = *upstreamID
			}
			key := billingMappingKey(item.NewAPIChannelID, upstreamIDValue, status, item.UpstreamGroup, at)
			row := BillingMappingSnapshot{
				MappingKey: key, NewAPIChannelID: item.NewAPIChannelID, UpstreamChannelID: upstreamID,
				MappingStatus: status, UpstreamGroup: item.UpstreamGroup, ObservedAt: at,
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "mapping_key"}}, DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func billingMappingKey(channelID int, upstreamID uint, status, group string, observedAt time.Time) string {
	raw := fmt.Sprintf("%d|%d|%s|%s|%d", channelID, upstreamID, status, group, observedAt.UTC().Truncate(5*time.Minute).Unix())
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ResolveMapping treats a unique observed upstream identity as stable across a
// channel's history. This lets a newly discovered fingerprint mapping settle
// earlier logs too. Multiple distinct mapped identities remain ambiguous and
// must not be allocated automatically.
func (r *Billing) ResolveMapping(channelID int, _ time.Time) (*BillingMappingSnapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("billing database is not initialized")
	}
	var rows []BillingMappingSnapshot
	if err := r.db.Where("new_api_channel_id = ?", channelID).Order("observed_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return stableMappingFromSnapshots(channelID, rows), nil
}

func stableMappingFromSnapshots(channelID int, snapshots []BillingMappingSnapshot) *BillingMappingSnapshot {
	mapped := make(map[uint]BillingMappingSnapshot)
	for _, snapshot := range snapshots {
		if snapshot.NewAPIChannelID != channelID || snapshot.MappingStatus != "mapped" || snapshot.UpstreamChannelID == nil || *snapshot.UpstreamChannelID == 0 {
			continue
		}
		upstreamID := *snapshot.UpstreamChannelID
		current, found := mapped[upstreamID]
		if !found || snapshot.ObservedAt.After(current.ObservedAt) || (snapshot.ObservedAt.Equal(current.ObservedAt) && snapshot.ID > current.ID) {
			mapped[upstreamID] = snapshot
		}
	}
	if len(mapped) != 1 {
		if len(mapped) > 1 {
			return &BillingMappingSnapshot{NewAPIChannelID: channelID, MappingStatus: "ambiguous"}
		}
		return nil
	}
	for _, snapshot := range mapped {
		copy := snapshot
		upstreamID := *snapshot.UpstreamChannelID
		copy.UpstreamChannelID = &upstreamID
		return &copy
	}
	return nil
}

// ReplaceWindowAndAdvance atomically replaces a source window and advances its
// successful watermark. A failed write therefore leaves both facts and the
// previous watermark untouched.
func (r *Billing) ReplaceWindowAndAdvance(source string, windowStart, windowEnd time.Time, resolutionSeconds int, buckets []NewAPIBillingBucket, state BillingSyncState, now time.Time) error {
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
		if err := replaceBillingWindowTx(tx, windowStart, windowEnd, resolutionSeconds, buckets, now); err != nil {
			return err
		}
		return saveBillingSyncStateTx(tx, state)
	})
}

func (r *Billing) SettleAndReplaceWindowAndAdvanceWithEvents(source string, windowStart, windowEnd time.Time, resolutionSeconds int, buckets []NewAPIBillingBucket, events []NewAPIBillingEvent, state BillingSyncState, now time.Time) error {
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
		if err := replaceBillingEventsWindowTx(tx, windowStart, windowEnd, events, now); err != nil {
			return err
		}
		if err := replaceProfitWindowTx(tx, windowStart, windowEnd, profits, now); err != nil {
			return err
		}
		return saveBillingSyncStateTx(tx, state)
	})
}

func saveBillingSyncStateTx(tx *gorm.DB, state BillingSyncState) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"last_successful_end_at", "last_attempt_at", "last_success_at", "status", "last_error", "updated_at",
		}),
	}).Create(&state).Error
}

func (r *Billing) GetSyncState(source string) (BillingSyncState, error) {
	if r == nil || r.db == nil {
		return BillingSyncState{}, errors.New("billing database is not initialized")
	}
	var state BillingSyncState
	err := r.db.Where("source = ?", source).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BillingSyncState{Source: source, Status: "never"}, nil
	}
	return state, err
}

// CreditRatesByFactKeys returns the sale conversion snapshots for facts that
// already exist locally. Replayed source windows use these values so a later
// configuration change cannot restate historical revenue.
func (r *Billing) CreditRatesByFactKeys(factKeys []string) (map[string]float64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("billing database is not initialized")
	}
	unique := make([]string, 0, len(factKeys))
	seen := make(map[string]struct{}, len(factKeys))
	for _, factKey := range factKeys {
		if factKey == "" {
			continue
		}
		if _, exists := seen[factKey]; exists {
			continue
		}
		seen[factKey] = struct{}{}
		unique = append(unique, factKey)
	}
	rates := make(map[string]float64, len(unique))
	if len(unique) == 0 {
		return rates, nil
	}
	var rows []struct {
		FactKey         string
		CreditUSDPerCNY float64
	}
	if err := r.db.Model(&NewAPIBillingBucket{}).Select("fact_key", "credit_usd_per_cny").Where("fact_key IN ?", unique).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		rates[row.FactKey] = row.CreditUSDPerCNY
	}
	return rates, nil
}

// CreditRatesByEventKeys returns the sale conversion snapshots stored with
// source log events. Replayed overlapping windows use these values so a later
// configuration change cannot restate historical event revenue.
func (r *Billing) CreditRatesByEventKeys(eventKeys []string) (map[string]float64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("billing database is not initialized")
	}
	unique := make([]string, 0, len(eventKeys))
	seen := make(map[string]struct{}, len(eventKeys))
	for _, eventKey := range eventKeys {
		if eventKey == "" {
			continue
		}
		if _, exists := seen[eventKey]; exists {
			continue
		}
		seen[eventKey] = struct{}{}
		unique = append(unique, eventKey)
	}
	rates := make(map[string]float64, len(unique))
	if len(unique) == 0 {
		return rates, nil
	}
	var rows []struct {
		EventKey        string
		CreditUSDPerCNY float64
	}
	if err := r.db.Model(&NewAPIBillingEvent{}).Select("event_key", "credit_usd_per_cny").Where("event_key IN ?", unique).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		rates[row.EventKey] = row.CreditUSDPerCNY
	}
	return rates, nil
}

func (r *Billing) ListEvents(start, end time.Time, page, pageSize int) ([]NewAPIBillingEvent, int64, error) {
	return r.ListEventsFiltered(start, end, "", page, pageSize)
}

// ListEventsFiltered returns source events with an optional mapping filter. The
// filter is applied in SQL before COUNT/OFFSET so the unmapped view remains
// correct when mapped and unmapped events are interleaved across pages.
func (r *Billing) ListEventsFiltered(start, end time.Time, mappingFilter string, page, pageSize int) ([]NewAPIBillingEvent, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, errors.New("billing database is not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 500 {
		pageSize = 500
	}
	query := r.db.Model(&NewAPIBillingEvent{}).Where("created_at >= ? AND created_at < ?", start, end)
	switch mappingFilter {
	case "", "all":
	case "mapped":
		query = query.Where("mapping_status = ?", "mapped")
	case "unmapped":
		query = query.Where("mapping_status <> ?", "mapped")
	default:
		return nil, 0, errors.New("invalid billing mapping filter")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []NewAPIBillingEvent
	err := query.Order("created_at DESC, source_log_id DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

func (r *Billing) CountEvents(start, end time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("billing database is not initialized")
	}
	var total int64
	err := r.db.Model(&NewAPIBillingEvent{}).Where("created_at >= ? AND created_at < ?", start, end).Count(&total).Error
	return total, err
}

func (r *Billing) SaveSyncState(state BillingSyncState) error {
	if r == nil || r.db == nil {
		return errors.New("billing database is not initialized")
	}
	if state.Source == "" {
		return errors.New("billing sync source is required")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	return saveBillingSyncStateTx(r.db, state)
}
