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

func TestProfitListBucketsUsesGORMNewAPIColumnName(t *testing.T) {
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

	start := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT * FROM "profit_buckets" WHERE bucket_start >= $1 AND bucket_start < $2 ORDER BY bucket_start ASC, new_api_channel_id ASC, id ASC`,
	)).WithArgs(start, end).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	if _, err := NewProfit(db).ListBuckets(start, end); err != nil {
		t.Fatalf("ListBuckets returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
