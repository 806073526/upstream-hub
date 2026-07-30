package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/storage"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDashboardUsageTrendReturnsNullForMissingIntervals(t *testing.T) {
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

	now := time.Date(2026, 7, 29, 3, 5, 0, 0, time.UTC)
	oldNow := usageTrendNow
	usageTrendNow = func() time.Time { return now }
	t.Cleanup(func() { usageTrendNow = oldNow })

	mock.ExpectQuery(`SELECT channels\.\* FROM "channels" LEFT JOIN .* ORDER BY channel_min_rates\.min_ratio ASC NULLS LAST, channels\.id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "type", "site_url", "username", "password_cipher", "credential_mode", "monitor_enabled", "created_at", "updated_at", "deleted_at"}).
			AddRow(1, "one", "newapi", "https://one.example", "user", "cipher", "token", true, now, now, nil))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "usage_buckets" WHERE bucket_start >= $1 AND bucket_start < $2 AND resolution_seconds = $3 ORDER BY bucket_start ASC, channel_id ASC`)).
		WithArgs(now.Add(-time.Hour), now, 300).
		WillReturnRows(sqlmock.NewRows([]string{"id", "channel_id", "bucket_start", "bucket_end", "resolution_seconds", "amount", "currency", "source", "quality", "complete", "collected_at", "updated_at"}).
			AddRow(1, 1, now.Add(-time.Hour), now.Add(-55*time.Minute), 300, 0.2, "USD", "newapi_stat", "exact", true, now, now))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api")
	registerDashboard(group, &Deps{Channels: storage.NewChannels(db), Usage: storage.NewUsage(db)})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard/usage-trend?range=1h", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Points []struct {
				TotalAmount *float64 `json:"total_amount"`
				Quality     string   `json:"quality"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Points) != 12 {
		t.Fatalf("points length = %d, want 12", len(response.Data.Points))
	}
	if response.Data.Points[0].TotalAmount == nil || *response.Data.Points[0].TotalAmount != 0.2 {
		t.Fatalf("first point = %#v", response.Data.Points[0])
	}
	if response.Data.Points[1].TotalAmount != nil || response.Data.Points[1].Quality != "missing" {
		t.Fatalf("missing point invented a zero: %#v", response.Data.Points[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardUsageTrendRejectsUnknownRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api")
	registerDashboard(group, &Deps{})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/dashboard/usage-trend?range=year", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestChannelRoutesIncludeUsageRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerChannels(router.Group("/api"), &Deps{})
	want := "/api/channels/:id/refresh-usage"
	for _, route := range router.Routes() {
		if route.Method == http.MethodPost && route.Path == want {
			return
		}
	}
	t.Fatalf("POST %s is not registered", want)
}
