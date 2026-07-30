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

func newMockChannels(t *testing.T) (*Channels, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return NewChannels(db), mock
}

func TestListOrdersChannelsByLowestRate(t *testing.T) {
	channels, mock := newMockChannels(t)
	query := regexp.QuoteMeta(
		`SELECT channels.* FROM "channels" LEFT JOIN (SELECT channel_id, MIN(ratio) AS min_ratio FROM rate_snapshots GROUP BY channel_id) AS channel_min_rates ON channel_min_rates.channel_id = channels.id WHERE "channels"."deleted_at" IS NULL ORDER BY channel_min_rates.min_ratio ASC NULLS LAST, channels.id ASC`,
	)
	rows := sqlmock.NewRows([]string{
		"id", "name", "type", "site_url", "username", "password_cipher",
		"credential_mode", "monitor_enabled", "created_at", "updated_at", "deleted_at",
	}).
		AddRow(2, "cheap", "newapi", "https://cheap.example", "user", "cipher", "token", true, time.Now(), time.Now(), nil).
		AddRow(1, "expensive", "newapi", "https://expensive.example", "user", "cipher", "token", true, time.Now(), time.Now(), nil).
		AddRow(3, "unknown", "sub2api", "https://unknown.example", "user", "cipher", "token", true, time.Now(), time.Now(), nil)
	mock.ExpectQuery(query).WillReturnRows(rows)

	got, err := channels.List()
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(got) != 3 || got[0].ID != 2 || got[1].ID != 1 || got[2].ID != 3 {
		t.Fatalf("List returned unexpected order: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
