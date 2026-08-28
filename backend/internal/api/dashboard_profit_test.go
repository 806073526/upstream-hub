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

func newProfitTrendTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
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
	return db, mock
}

func TestDashboardProfitTrendMarksEmptyRangeIncomplete(t *testing.T) {
	db, mock := newProfitTrendTestDB(t)
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	oldNow := profitTrendNow
	profitTrendNow = func() time.Time { return now }
	t.Cleanup(func() { profitTrendNow = oldNow })
	end := now.Truncate(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "profit_buckets" WHERE bucket_start >= $1 AND bucket_start < $2 ORDER BY bucket_start ASC, new_api_channel_id ASC, id ASC`)).
		WithArgs(end.Add(-24*time.Hour), end).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerDashboard(router.Group("/api"), &Deps{Profit: storage.NewProfit(db)})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/dashboard/profit-trend?range=24h", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Complete bool `json:"complete"`
			Summary  struct {
				Complete bool `json:"complete"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Complete || response.Data.Summary.Complete {
		t.Fatalf("empty profit range was marked complete: %#v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardProfitTrendRejectsUnknownRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerDashboard(router.Group("/api"), &Deps{})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/dashboard/profit-trend?range=year", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestDashboardProfitTrendKeepsUnmappedSalesOutOfSettledProfit(t *testing.T) {
	db, mock := newProfitTrendTestDB(t)
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	oldNow := profitTrendNow
	profitTrendNow = func() time.Time { return now }
	t.Cleanup(func() { profitTrendNow = oldNow })
	end := now.Truncate(time.Hour)
	bucketStart := end.Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "profit_buckets" WHERE bucket_start >= $1 AND bucket_start < $2 ORDER BY bucket_start ASC, new_api_channel_id ASC, id ASC`)).
		WithArgs(end.Add(-24*time.Hour), end).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "billing_fact_key", "bucket_start", "bucket_end", "resolution_seconds", "new_api_channel_id", "upstream_channel_id", "mapping_status",
			"group", "model_name", "normalization_status", "sale_cny", "cost_usd", "cost_cny", "profit_cny", "credit_usd_per_cny",
			"allocation_status", "complete", "calculated_at", "updated_at",
		}).AddRow(
			1, "unmapped-sale", bucketStart, bucketStart.Add(5*time.Minute), 300, 12, nil, "unmapped",
			"vip", "gpt-4o", "exact", 3.0, 0.0, 0.0, 0.0, 12.0,
			"unmapped", false, now, now,
		))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerDashboard(router.Group("/api"), &Deps{Profit: storage.NewProfit(db)})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/dashboard/profit-trend?range=24h", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Complete bool `json:"complete"`
			Summary  struct {
				SettledSaleCNY  float64 `json:"settled_sale_cny"`
				UnmappedSaleCNY float64 `json:"unmapped_sale_cny"`
				ProfitCNY       float64 `json:"profit_cny"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Complete || response.Data.Summary.SettledSaleCNY != 0 || response.Data.Summary.UnmappedSaleCNY != 3 || response.Data.Summary.ProfitCNY != 0 {
		t.Fatalf("unmapped sale was represented as settled profit: %#v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardProfitDetailsFiltersUnmappedEventsBeforePagination(t *testing.T) {
	db, mock := newProfitTrendTestDB(t)
	now := time.Date(2026, 8, 27, 12, 17, 0, 0, time.UTC)
	oldNow := profitTrendNow
	profitTrendNow = func() time.Time { return now }
	t.Cleanup(func() { profitTrendNow = oldNow })
	end := now.Truncate(time.Hour)
	start := end.Add(-24 * time.Hour)
	created := start.Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "profit_buckets" WHERE bucket_start >= $1 AND bucket_start < $2 ORDER BY bucket_start ASC, new_api_channel_id ASC, id ASC`)).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM "newapi_billing_events" WHERE (created_at >= $1 AND created_at < $2) AND mapping_status <> $3`)).
		WithArgs(start, end, "mapped").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "newapi_billing_events" WHERE (created_at >= $1 AND created_at < $2) AND mapping_status <> $3 ORDER BY created_at DESC, source_log_id DESC, id DESC LIMIT $4`)).
		WithArgs(start, end, "mapped", 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "event_key", "source_log_id", "created_at", "bucket_start", "bucket_end", "event_type", "channel_id", "upstream_channel_id", "mapping_status",
			"group", "model_name", "effective_group_ratio", "ratio_source", "normalization_status", "quota", "charged_usd", "normalized_usd", "credit_usd_per_cny", "sale_cny",
			"user_id", "token_name", "request_id", "upstream_request_id", "collected_at", "updated_at",
		}).AddRow(
			1, "unmapped-event", 101, created, created, created.Add(5*time.Minute), "consume", 12, nil, "unmapped",
			"vip", "gpt-4o", 1.4, "group_ratio", "exact", 1400000, 1.4, 1, 12, 1.0/12,
			8, "token-a", "req-a", "up-a", now, now,
		))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerDashboard(router.Group("/api"), &Deps{Profit: storage.NewProfit(db), Billing: storage.NewBilling(db)})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/dashboard/profit-details?range=24h&kind=unmapped&page=1&page_size=1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Total int `json:"total"`
			Items []struct {
				MappingStatus string `json:"mapping_status"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Total != 1 || len(response.Data.Items) != 1 || response.Data.Items[0].MappingStatus == "mapped" {
		t.Fatalf("unmapped detail page = %#v", response.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildProfitSaleDetailsAggregatesSalesIntoCostBuckets(t *testing.T) {
	start := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	usage := []storage.UsageBucket{
		{ID: 1, ChannelID: 7, BucketStart: start, BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600, Amount: 10, Currency: "USD", Source: "provider", Quality: "exact", Complete: true},
		{ID: 2, ChannelID: 8, BucketStart: start, BucketEnd: start.Add(time.Hour), ResolutionSeconds: 3600, Amount: 3, Currency: "USD", Source: "provider", Quality: "exact", Complete: true},
	}
	upstreamID := uint(7)
	profits := []storage.ProfitBucket{
		{FactKey: "a", BucketStart: start, BucketEnd: start.Add(5 * time.Minute), ResolutionSeconds: 300, NewAPIChannelID: 40, UpstreamChannelID: &upstreamID, MappingStatus: "mapped", Group: "vip", ModelName: "gpt-4o", SaleCNY: 0.1166666667, ChargedUSD: 1.4, Complete: true},
		{FactKey: "b", BucketStart: start.Add(5 * time.Minute), BucketEnd: start.Add(10 * time.Minute), ResolutionSeconds: 300, NewAPIChannelID: 40, UpstreamChannelID: &upstreamID, MappingStatus: "mapped", Group: "vip", ModelName: "gpt-4o", SaleCNY: 0.2333333333, ChargedUSD: 2.8, Complete: true},
	}
	items := buildProfitSaleDetails(profits, usage, 1, map[uint]string{7: "上游七", 8: "上游八"})
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 cost buckets", len(items))
	}
	var matched, unmatched *profitSaleDetail
	for i := range items {
		if items[i].ChannelID == 7 {
			matched = &items[i]
		}
		if items[i].ChannelID == 8 {
			unmatched = &items[i]
		}
	}
	if matched == nil || matched.SaleCNY < 0.3499 || matched.SaleCNY > 0.3501 {
		t.Fatalf("aggregated sale = %#v, want channel 7 sale 0.35", matched)
	}
	if matched.CostCNY != 10 || matched.ChannelName != "上游七" {
		t.Fatalf("matched cost bucket = %#v", matched)
	}
	if unmatched == nil || unmatched.SaleCNY != 0 || unmatched.CostCNY != 3 || unmatched.MappingStatus != "unmapped" {
		t.Fatalf("unmatched cost bucket = %#v", unmatched)
	}
}

func TestBuildProfitSaleDetailsPaginatesAggregatedRows(t *testing.T) {
	start := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	usage := make([]storage.UsageBucket, 3)
	for i := range usage {
		bucketStart := start.Add(time.Duration(i) * time.Hour)
		usage[i] = storage.UsageBucket{ID: uint(i + 1), ChannelID: uint(i + 1), BucketStart: bucketStart, BucketEnd: bucketStart.Add(time.Hour), ResolutionSeconds: 3600, Amount: 1, Currency: "USD", Complete: true}
	}
	items := buildProfitSaleDetailsPage(nil, usage, 1, map[uint]string{}, 1, 2)
	if len(items) != 2 || items[0].ChannelID != 3 || items[1].ChannelID != 2 {
		t.Fatalf("page 1 = %#v, want newest aggregate rows first", items)
	}
	items = buildProfitSaleDetailsPage(nil, usage, 1, map[uint]string{}, 2, 2)
	if len(items) != 1 || items[0].ChannelID != 1 {
		t.Fatalf("page 2 = %#v, want oldest aggregate row", items)
	}
}
