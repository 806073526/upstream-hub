package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/storage"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDashboardBalanceTrendFiltersSelectedChannels(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery(`(?s)FROM balance_snapshots.*WHERE sampled_at >= \$1.*AND channel_id IN \(\$2,\$3\).*ORDER BY pd.day ASC`).
		WithArgs(sqlmock.AnyArg(), uint(2), uint(5)).
		WillReturnRows(sqlmock.NewRows([]string{"day", "balance"}).
			AddRow(time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC), 12.5))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerDashboard(router.Group("/api"), &Deps{Rates: storage.NewRates(db)})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet,
		"/api/dashboard/balance-trend?days=7&channel_ids=2,5",
		nil,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestParseBalanceChannelIDsRejectsInvalidValues(t *testing.T) {
	if _, _, err := parseBalanceChannelIDs("2,nope", true); err == nil {
		t.Fatal("parseBalanceChannelIDs accepted an invalid channel id")
	}
}
