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

func newMockUsage(t *testing.T) (*Usage, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return NewUsage(db), mock
}

func TestUsageSaveStoresSummaryAndUpsertsBucketsInOneTransaction(t *testing.T) {
	repo, mock := newMockUsage(t)
	now := time.Date(2026, 7, 29, 3, 5, 0, 0, time.UTC)
	total, today := 12.5, 0.75

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(
		`UPDATE channels SET last_usage_total=$1, last_usage_today=$2, usage_currency=$3, last_usage_at=$4, updated_at=$5 WHERE id=$6 AND deleted_at IS NULL`,
	)).WithArgs(total, today, "USD", now, sqlmock.AnyArg(), uint(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO usage_buckets .* ON CONFLICT \(channel_id, bucket_start, resolution_seconds\) DO UPDATE SET`).
		WithArgs(uint(7), now.Add(-time.Hour), now, 3600, 0.25, "USD", "newapi-history", "exact", true, now, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Save(UsageSample{
		ChannelID:   7,
		TotalAmount: &total,
		TodayAmount: &today,
		Currency:    "USD",
		ObservedAt:  now,
		Buckets: []UsageBucket{{
			ChannelID: 7, BucketStart: now.Add(-time.Hour), BucketEnd: now,
			ResolutionSeconds: 3600, Amount: 0.25, Currency: "USD",
			Source: "newapi-history", Quality: "exact", Complete: true, CollectedAt: now,
		}},
	})
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageListBucketsFiltersByWindowAndResolution(t *testing.T) {
	repo, mock := newMockUsage(t)
	start := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	query := regexp.QuoteMeta(
		`SELECT * FROM "usage_buckets" WHERE bucket_start >= $1 AND bucket_start < $2 AND resolution_seconds = $3 ORDER BY bucket_start ASC, channel_id ASC`,
	)
	mock.ExpectQuery(query).WithArgs(start, end, 3600).WillReturnRows(
		sqlmock.NewRows([]string{"id", "channel_id", "bucket_start", "bucket_end", "resolution_seconds", "amount", "currency", "source", "quality", "complete", "collected_at", "updated_at"}),
	)

	if _, err := repo.ListBuckets(start, end, 3600); err != nil {
		t.Fatalf("ListBuckets returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageLatestBucketStartReturnsPerChannelWatermark(t *testing.T) {
	repo, mock := newMockUsage(t)
	want := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT MAX(bucket_start) FROM "usage_buckets" WHERE channel_id = $1 AND resolution_seconds = $2`,
	)).WithArgs(uint(7), 3600).WillReturnRows(
		sqlmock.NewRows([]string{"max"}).AddRow(want),
	)

	got, err := repo.LatestBucketStart(7, 3600)
	if err != nil {
		t.Fatalf("LatestBucketStart returned error: %v", err)
	}
	if got == nil || !got.Equal(want) {
		t.Fatalf("LatestBucketStart = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
