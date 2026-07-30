package newapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

func TestGetUsageUsesConfiguredTimezoneForToday(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 12, 34, 0, time.UTC)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	wantTodayStart := time.Date(2026, 7, 29, 0, 0, 0, 0, shanghai).UTC()
	seenToday := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota_per_unit": 500000}})
		case "/api/user/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"used_quota": 5000000}})
		case "/api/log/self/stat":
			start, _ := strconv.ParseInt(r.URL.Query().Get("start_timestamp"), 10, 64)
			end, _ := strconv.ParseInt(r.URL.Query().Get("end_timestamp"), 10, 64)
			quota := 50000
			if start == wantTodayStart.Unix() {
				seenToday = true
				if end != now.Unix()-1 {
					t.Errorf("today end_timestamp = %d, want %d", end, now.Unix()-1)
				}
				quota = 1250000
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota": quota}})
		case "/api/data/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		UserID: "42",
		Cookie: "session=test",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if !seenToday {
		t.Fatal("GetUsage did not query from Asia/Shanghai midnight")
	}
	if result.TotalAmount == nil || *result.TotalAmount != 10 {
		t.Fatalf("TotalAmount = %#v, want 10", result.TotalAmount)
	}
	if result.TodayAmount == nil || *result.TodayAmount != 2.5 {
		t.Fatalf("TodayAmount = %#v, want 2.5", result.TodayAmount)
	}
}

func TestGetUsageReturnsLastCompletedFiveMinuteBucket(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 12, 34, 0, time.UTC)
	wantStart := time.Date(2026, 7, 29, 2, 5, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 7, 29, 2, 10, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota_per_unit": 500000}})
		case "/api/user/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"used_quota": 5000000}})
		case "/api/log/self/stat":
			start, _ := strconv.ParseInt(r.URL.Query().Get("start_timestamp"), 10, 64)
			quota := 1250000
			if start == wantStart.Unix() {
				if got := r.URL.Query().Get("end_timestamp"); got != strconv.FormatInt(wantEnd.Unix()-1, 10) {
					t.Errorf("five-minute end_timestamp = %s, want %d", got, wantEnd.Unix()-1)
				}
				quota = 50000
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota": quota}})
		case "/api/data/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		UserID: "42",
		Cookie: "session=test",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if len(result.Buckets) != 1 {
		t.Fatalf("Buckets length = %d, want 1: %#v", len(result.Buckets), result.Buckets)
	}
	bucket := result.Buckets[0]
	if !bucket.StartAt.Equal(wantStart) || !bucket.EndAt.Equal(wantEnd) || bucket.Amount != 0.1 {
		t.Fatalf("unexpected bucket: %#v", bucket)
	}
	if bucket.ResolutionSeconds != 300 || bucket.Quality != connector.UsageQualityExact {
		t.Fatalf("unexpected bucket metadata: %#v", bucket)
	}
}

func TestGetUsageAggregatesHourlyHistoryRows(t *testing.T) {
	now := time.Date(2026, 7, 29, 4, 12, 34, 0, time.UTC)
	hourOne := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	hourTwo := hourOne.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota_per_unit": 500000}})
		case "/api/user/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"used_quota": 5000000}})
		case "/api/log/self/stat":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota": 50000}})
		case "/api/data/self":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": 1, "user_id": 42, "username": "user", "model_name": "gpt-a", "created_at": hourOne.Unix(), "use_group": "default", "token_id": 1, "channel_id": 1, "node_name": "", "token_used": 100, "count": 1, "quota": 50000},
					{"id": 2, "user_id": 42, "username": "user", "model_name": "gpt-b", "created_at": hourOne.Unix(), "use_group": "default", "token_id": 1, "channel_id": 1, "node_name": "", "token_used": 200, "count": 1, "quota": 100000},
					{"id": 3, "user_id": 42, "username": "user", "model_name": "gpt-a", "created_at": hourTwo.Unix(), "use_group": "default", "token_id": 1, "channel_id": 1, "node_name": "", "token_used": 300, "count": 1, "quota": 250000},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := New().GetUsage(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		UserID: "42",
		Cookie: "session=test",
	}, connector.UsageQuery{Now: now, Timezone: "Asia/Shanghai", HistorySince: hourOne})
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}

	hourly := make([]connector.UsageBucketResult, 0)
	for _, bucket := range result.Buckets {
		if bucket.ResolutionSeconds == 3600 {
			hourly = append(hourly, bucket)
		}
	}
	if len(hourly) != 2 {
		t.Fatalf("hourly buckets = %#v, want 2 buckets", hourly)
	}
	if !hourly[0].StartAt.Equal(hourOne) || hourly[0].Amount != 0.3 {
		t.Fatalf("first hourly bucket = %#v, want amount 0.3", hourly[0])
	}
	if !hourly[1].StartAt.Equal(hourTwo) || hourly[1].Amount != 0.5 {
		t.Fatalf("second hourly bucket = %#v, want amount 0.5", hourly[1])
	}
}
