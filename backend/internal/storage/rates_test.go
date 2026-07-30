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

func TestAggregateBalanceTrendForChannelsFiltersSnapshots(t *testing.T) {
	rates, mock := newMockRates(t)
	mock.ExpectQuery(`(?s)FROM balance_snapshots.*WHERE sampled_at >= \$1.*AND channel_id IN \(\$2,\$3\).*ORDER BY pd.day ASC`).
		WithArgs(sqlmock.AnyArg(), uint(2), uint(5)).
		WillReturnRows(sqlmock.NewRows([]string{"day", "balance"}).
			AddRow(time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC), 12.5))

	got, err := rates.AggregateBalanceTrendForChannels(7, []uint{2, 5})
	if err != nil {
		t.Fatalf("AggregateBalanceTrendForChannels returned error: %v", err)
	}
	if len(got) != 1 || got[0].Balance != 12.5 {
		t.Fatalf("unexpected trend: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateBalanceTrendHourlyForChannelsFiltersSnapshots(t *testing.T) {
	rates, mock := newMockRates(t)
	mock.ExpectQuery(`(?s)FROM balance_snapshots.*WHERE sampled_at >= \$1.*AND channel_id IN \(\$2\).*ORDER BY ph.hour ASC`).
		WithArgs(sqlmock.AnyArg(), uint(2)).
		WillReturnRows(sqlmock.NewRows([]string{"day", "balance"}).
			AddRow(time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC), 8.25))

	got, err := rates.AggregateBalanceTrendHourlyForChannels(24, []uint{2})
	if err != nil {
		t.Fatalf("AggregateBalanceTrendHourlyForChannels returned error: %v", err)
	}
	if len(got) != 1 || got[0].Balance != 8.25 {
		t.Fatalf("unexpected trend: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func newMockRates(t *testing.T) (*Rates, sqlmock.Sqlmock) {
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
	return NewRates(db), mock
}

func TestDeleteExceptRemovesSnapshotsOutsideCurrentGroups(t *testing.T) {
	rates, mock := newMockRates(t)
	mock.ExpectExec(regexp.QuoteMeta(
		`DELETE FROM "rate_snapshots" WHERE channel_id = $1 AND model_name NOT IN ($2,$3)`,
	)).WithArgs(uint(7), "linked-a", "linked-b").
		WillReturnResult(sqlmock.NewResult(0, 2))

	if err := rates.DeleteExcept(7, []string{"linked-a", "linked-b"}); err != nil {
		t.Fatalf("DeleteExcept returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteExceptRemovesAllSnapshotsWhenNoGroupsRemain(t *testing.T) {
	rates, mock := newMockRates(t)
	mock.ExpectExec(regexp.QuoteMeta(
		`DELETE FROM "rate_snapshots" WHERE channel_id = $1`,
	)).WithArgs(uint(7)).WillReturnResult(sqlmock.NewResult(0, 3))

	if err := rates.DeleteExcept(7, nil); err != nil {
		t.Fatalf("DeleteExcept returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
