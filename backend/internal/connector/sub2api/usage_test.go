package sub2api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

func TestGetUsageReturnsActualCostAndHourlyTrend(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 12, 34, 0, time.UTC)
	seenTimezone := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"total_actual_cost": 12.5,
					"total_cost":        20.0,
				},
			})
		case "/api/v1/usage/stats":
			if r.URL.Query().Get("period") == "today" && r.URL.Query().Get("timezone") == "Asia/Shanghai" {
				seenTimezone = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"actual_cost": 1.25,
					"cost":        2.0,
				},
			})
		case "/api/v1/usage/dashboard/trend":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"trend": []map[string]any{{
						"date":                  "2026-07-29 09:00",
						"requests":              3,
						"input_tokens":          100,
						"output_tokens":         50,
						"cache_creation_tokens": 0,
						"cache_read_tokens":     0,
						"total_tokens":          150,
						"cost":                  0.5,
						"actual_cost":           0.3,
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		AccessToken: "test-token",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if !seenTimezone {
		t.Fatal("GetUsage did not request today's usage in Asia/Shanghai")
	}
	if result.TotalAmount == nil || *result.TotalAmount != 12.5 {
		t.Fatalf("TotalAmount = %#v, want 12.5", result.TotalAmount)
	}
	if result.TodayAmount == nil || *result.TodayAmount != 1.25 {
		t.Fatalf("TodayAmount = %#v, want 1.25", result.TodayAmount)
	}
	if len(result.Buckets) != 1 {
		t.Fatalf("Buckets length = %d, want 1: %#v", len(result.Buckets), result.Buckets)
	}
	bucket := result.Buckets[0]
	wantStart := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	if !bucket.StartAt.Equal(wantStart) || !bucket.EndAt.Equal(wantStart.Add(time.Hour)) || bucket.Amount != 0.3 {
		t.Fatalf("unexpected bucket: %#v", bucket)
	}
	if bucket.ResolutionSeconds != 3600 || bucket.Quality != connector.UsageQualityExact {
		t.Fatalf("unexpected bucket metadata: %#v", bucket)
	}
}

func TestGetUsageReadsPeriodTotalActualCost(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"total_actual_cost": 12.5},
			})
		case "/api/v1/usage/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"total_actual_cost": 1.25},
			})
		case "/api/v1/usage/dashboard/trend":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"trend": []any{}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		AccessToken: "test-token",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if result.TodayAmount == nil || *result.TodayAmount != 1.25 {
		t.Fatalf("TodayAmount = %#v, want 1.25 from total_actual_cost", result.TodayAmount)
	}
}

func TestGetUsageFallsBackToDashboardTodayActualCost(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"total_actual_cost": 12.5,
					"today_actual_cost": 0.75,
				},
			})
		case "/api/v1/usage/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"total_requests": 3},
			})
		case "/api/v1/usage/dashboard/trend":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"trend": []any{}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		AccessToken: "test-token",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if result.TodayAmount == nil || *result.TodayAmount != 0.75 {
		t.Fatalf("TodayAmount = %#v, want 0.75 from dashboard today_actual_cost", result.TodayAmount)
	}
}

func TestGetUsageDefaultsToZeroWithoutTodayCost(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"total_actual_cost": 12.5},
			})
		case "/api/v1/usage/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"total_requests": 0},
			})
		case "/api/v1/usage/dashboard/trend":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"trend": []any{}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		AccessToken: "test-token",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if result.TodayAmount == nil || *result.TodayAmount != 0 {
		t.Fatalf("TodayAmount = %#v, want silent zero fallback", result.TodayAmount)
	}
}

func TestGetUsageKeepsSummaryWhenTrendFails(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"total_actual_cost": 12.5},
			})
		case "/api/v1/usage/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"total_actual_cost": 1.25},
			})
		case "/api/v1/usage/dashboard/trend":
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		AccessToken: "test-token",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if result.TotalAmount == nil || *result.TotalAmount != 12.5 {
		t.Fatalf("TotalAmount = %#v, want 12.5", result.TotalAmount)
	}
	if result.TodayAmount == nil || *result.TodayAmount != 1.25 {
		t.Fatalf("TodayAmount = %#v, want 1.25", result.TodayAmount)
	}
	if len(result.Buckets) != 0 {
		t.Fatalf("Buckets = %#v, want no trend buckets after trend failure", result.Buckets)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("Warnings = %#v, want one trend warning", result.Warnings)
	}
}

func TestGetUsageDefaultsTrendWindowToOneDay(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 30, 0, 0, time.UTC)
	var startDate string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"total_actual_cost": 1}})
		case "/api/v1/usage/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"total_actual_cost": 1}})
		case "/api/v1/usage/dashboard/trend":
			startDate = r.URL.Query().Get("start_date")
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"trend": []any{}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if _, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		AccessToken: "test-token",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai"}); err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if startDate != "2026-07-28" {
		t.Fatalf("trend start_date = %q, want one-day window", startDate)
	}
}
