package storage

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBuildNewAPIBillingBucketSnapshotsRatioAndCreditRate(t *testing.T) {
	bucket, err := BuildNewAPIBillingBucket(BillingFactInput{
		BucketStart:         time.Unix(1704067200, 0),
		BucketEnd:           time.Unix(1704067500, 0),
		NewAPIChannelID:     12,
		Group:               "vip",
		ModelName:           "gpt-4o",
		EffectiveGroupRatio: 1.4,
		RatioSource:         "group_ratio",
		NormalizationStatus: "exact",
		ConsumeQuota:        1400000,
		RefundQuota:         140000,
		EventCount:          2,
		QuotaPerUnit:        500000,
		Complete:            true,
	}, 12, time.Unix(1704067600, 0))
	if err != nil {
		t.Fatalf("BuildNewAPIBillingBucket returned error: %v", err)
	}
	if bucket.NetQuota != 1260000 {
		t.Fatalf("net quota = %d, want 1260000", bucket.NetQuota)
	}
	if bucket.ChargedUSD != 2.52 {
		t.Fatalf("charged USD = %v, want 2.52", bucket.ChargedUSD)
	}
	if bucket.NormalizedUSD != 1.8 {
		t.Fatalf("normalized USD = %v, want 1.8", bucket.NormalizedUSD)
	}
	if bucket.CreditUSDPerCNY != 12 || bucket.SaleCNY != 0.21 {
		t.Fatalf("credit/sale = %v/%v, want 12/0.21", bucket.CreditUSDPerCNY, bucket.SaleCNY)
	}
	if bucket.FactKey == "" {
		t.Fatal("fact key is empty")
	}
}

func TestBuildNewAPIBillingEventUsesChargedUSDForSale(t *testing.T) {
	event, err := BuildNewAPIBillingEvent(BillingEventInput{
		SourceLogID:         40,
		CreatedAt:           time.Unix(1704067212, 0),
		EventType:           "consume",
		ChannelID:           40,
		Group:               "vip",
		ModelName:           "gpt-4o",
		EffectiveGroupRatio: 1.4,
		NormalizationStatus: BillingStatusExact,
		Quota:               1400000,
		QuotaPerUnit:        500000,
		CreditUSDPerCNY:     12,
	})
	if err != nil {
		t.Fatalf("BuildNewAPIBillingEvent returned error: %v", err)
	}
	if event.ChargedUSD != 2.8 {
		t.Fatalf("charged USD = %v, want 2.8", event.ChargedUSD)
	}
	if event.SaleCNY != 2.8/12 {
		t.Fatalf("sale CNY = %v, want %v", event.SaleCNY, 2.8/12)
	}
}

func TestBuildNewAPIBillingBucketDoesNotSettleUnavailableRatio(t *testing.T) {
	bucket, err := BuildNewAPIBillingBucket(BillingFactInput{
		BucketStart:         time.Unix(1704067200, 0),
		BucketEnd:           time.Unix(1704067500, 0),
		NewAPIChannelID:     7,
		Group:               "free",
		ModelName:           "gpt-4o-mini",
		NormalizationStatus: "unavailable",
		ConsumeQuota:        500000,
		QuotaPerUnit:        500000,
		Complete:            true,
	}, 12, time.Unix(1704067600, 0))
	if err != nil {
		t.Fatalf("BuildNewAPIBillingBucket returned error: %v", err)
	}
	if bucket.ChargedUSD != 1 {
		t.Fatalf("charged USD = %v, want 1", bucket.ChargedUSD)
	}
	if bucket.NormalizedUSD != 0 || bucket.SaleCNY != 0 {
		t.Fatalf("unavailable ratio produced sale: normalized=%v sale=%v", bucket.NormalizedUSD, bucket.SaleCNY)
	}
}

func TestStableMappingFromSnapshotsUsesUniqueMatchForHistoricalBilling(t *testing.T) {
	upstreamID := uint(4)
	mapping := stableMappingFromSnapshots(31, []BillingMappingSnapshot{
		{NewAPIChannelID: 31, MappingStatus: "unmapped", ObservedAt: time.Date(2026, 8, 27, 17, 40, 0, 0, time.UTC)},
		{NewAPIChannelID: 31, UpstreamChannelID: &upstreamID, MappingStatus: "mapped", ObservedAt: time.Date(2026, 8, 27, 17, 51, 0, 0, time.UTC)},
	})
	if mapping == nil || mapping.MappingStatus != "mapped" || mapping.UpstreamChannelID == nil || *mapping.UpstreamChannelID != upstreamID {
		t.Fatalf("mapping = %#v, want stable mapped upstream %d", mapping, upstreamID)
	}
}

func TestStableMappingFromSnapshotsRejectsConflictingMatches(t *testing.T) {
	first := uint(1)
	second := uint(4)
	mapping := stableMappingFromSnapshots(30, []BillingMappingSnapshot{
		{NewAPIChannelID: 30, UpstreamChannelID: &first, MappingStatus: "mapped", ObservedAt: time.Date(2026, 8, 27, 17, 51, 0, 0, time.UTC)},
		{NewAPIChannelID: 30, UpstreamChannelID: &second, MappingStatus: "mapped", ObservedAt: time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC)},
	})
	if mapping == nil || mapping.MappingStatus != "ambiguous" || mapping.UpstreamChannelID != nil {
		t.Fatalf("mapping = %#v, want ambiguous", mapping)
	}
}

func TestBillingEventDetailCarriesSourceLogIdentity(t *testing.T) {
	event := NewAPIBillingEvent{
		SourceLogID:         42,
		EventType:           "consume",
		ChannelID:           12,
		Quota:               1400000,
		EffectiveGroupRatio: 1.4,
		SaleCNY:             1,
	}
	if event.SourceLogID != 42 || event.EventType != "consume" || event.SaleCNY != 1 {
		t.Fatalf("event detail = %#v", event)
	}
}

func TestReplaceBillingWindowUsesGORMNewAPIChannelNameColumn(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 8, 27, 2, 40, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	now := end.Add(time.Hour)
	bucket := NewAPIBillingBucket{
		FactKey: "billing-column-name-test", BucketStart: start, BucketEnd: end,
		ResolutionSeconds: BillingResolutionSeconds, NewAPIChannelID: 46,
		NewAPIChannelName: "channel-46", MappingStatus: "mapped", Group: "stable",
		ModelName: "gpt-5.6-terra", EffectiveGroupRatio: 1.4,
		RatioSource: "group_ratio", NormalizationStatus: BillingStatusExact,
		ConsumeQuota: 500000, NetQuota: 500000, QuotaPerUnit: 500000,
		ChargedUSD: 1, CreditUSDPerCNY: 12, SaleCNY: 1.0 / 12, Complete: true,
		CollectedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM "newapi_billing_buckets" WHERE bucket_start >= $1 AND bucket_start < $2 AND resolution_seconds = $3`)).
		WithArgs(start, end, BillingResolutionSeconds).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`(?s)INSERT INTO "newapi_billing_buckets".*ON CONFLICT \("fact_key"\) DO UPDATE SET.*"new_api_channel_name"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	if err := NewBilling(db).ReplaceWindow(start, end, BillingResolutionSeconds, []NewAPIBillingBucket{bucket}, now); err != nil {
		t.Fatalf("ReplaceWindow returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
