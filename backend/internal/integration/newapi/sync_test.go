package newapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

func TestBuildMatchedMetricsUsesKeyFingerprintAndCarriesSnapshot(t *testing.T) {
	balance := 12.5
	now := time.Date(2026, 8, 4, 3, 0, 0, 0, time.UTC)
	metrics := BuildMatchedMetrics(
		[]Identity{{ChannelID: 10, BaseURL: "https://upstream.example/", KeyFingerprint: connector.KeyFingerprint("sk-key")}},
		[]Scan{{ChannelID: 7, SiteURL: "https://upstream.example", Balance: &balance, BalanceAt: now.Add(-time.Minute), Results: []connector.RateResult{{
			ModelName: "cheap", Ratio: 0.5, Keys: []connector.KeyIdentity{{Fingerprint: connector.KeyFingerprint("key"), Name: "group-key"}},
		}}}},
		now,
	)
	if len(metrics) != 1 {
		t.Fatalf("matched metrics = %#v, want one", metrics)
	}
	if metrics[0].ChannelID != 10 || metrics[0].Group != "cheap" || metrics[0].Ratio != 0.5 || metrics[0].UpstreamChannelID != 7 {
		t.Fatalf("unexpected metric: %#v", metrics[0])
	}
	if metrics[0].Balance == nil || *metrics[0].Balance != balance || metrics[0].BalanceAt != now.Add(-time.Minute).Unix() {
		t.Fatalf("metric did not carry balance snapshot: %#v", metrics[0])
	}
}

func TestBuildMatchedMetricsSkipsUnmatchedKeys(t *testing.T) {
	metrics := BuildMatchedMetrics(
		[]Identity{{ChannelID: 10, KeyFingerprint: connector.KeyFingerprint("other")}},
		[]Scan{{ChannelID: 7, Results: []connector.RateResult{{ModelName: "cheap", Ratio: 0.5, Keys: []connector.KeyIdentity{{Fingerprint: connector.KeyFingerprint("key")}}}}}},
		time.Now(),
	)
	if len(metrics) != 0 {
		t.Fatalf("unmatched metrics = %#v, want none", metrics)
	}
}

func TestBuildMatchedMetricsKeepsMatchesFromDistinctUpstreamChannels(t *testing.T) {
	metrics := BuildMatchedMetrics(
		[]Identity{{ChannelID: 10, KeyFingerprint: connector.KeyFingerprint("key")}},
		[]Scan{
			{ChannelID: 7, Results: []connector.RateResult{{ModelName: "cheap", Ratio: 0.5, Keys: []connector.KeyIdentity{{Fingerprint: connector.KeyFingerprint("key")}}}}},
			{ChannelID: 8, Results: []connector.RateResult{{ModelName: "cheap", Ratio: 0.6, Keys: []connector.KeyIdentity{{Fingerprint: connector.KeyFingerprint("key")}}}}},
		},
		time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
	)
	if len(metrics) != 2 {
		t.Fatalf("matched metrics = %#v, want both upstream channels", metrics)
	}
	if metrics[0].UpstreamChannelID != 7 || metrics[1].UpstreamChannelID != 8 {
		t.Fatalf("upstream channels = %#v, want 7 and 8", metrics)
	}
}

func TestFetchBillingAggregateSendsWindowAndBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/internal/upstream-hub/billing/aggregate" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request BillingAggregateRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request.StartAt != 1704067200 || request.EndAt != 1704067500 || request.BucketSeconds != 300 {
			t.Fatalf("request = %#v", request)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"source":"new-api","start_at":1704067200,"end_at":1704067500,"bucket_seconds":300,"quota_per_unit":500000,"complete":true,"items":[{"bucket_start":1704067200,"bucket_end":1704067500,"channel_id":12,"group":"vip","model_name":"gpt-4o","effective_group_ratio":1.4,"ratio_source":"group_ratio","normalization_status":"exact","consume_quota":1400000,"refund_quota":140000,"net_quota":1260000,"event_count":2}]}}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", time.Second)
	aggregate, err := client.FetchBillingAggregate(context.Background(), BillingAggregateRequest{
		StartAt: 1704067200, EndAt: 1704067500, BucketSeconds: 300,
	})
	if err != nil {
		t.Fatalf("FetchBillingAggregate returned error: %v", err)
	}
	if aggregate.QuotaPerUnit != 500000 || len(aggregate.Items) != 1 || aggregate.Items[0].NetQuota != 1260000 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
}

func TestFetchBillingAggregateRejectsInvalidWindowBeforeRequest(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", strings.Repeat("x", 8), time.Second)
	_, err := client.FetchBillingAggregate(context.Background(), BillingAggregateRequest{StartAt: 10, EndAt: 9, BucketSeconds: 300})
	if err == nil || !strings.Contains(err.Error(), "invalid billing aggregation window") {
		t.Fatalf("error = %v, want invalid billing aggregation window", err)
	}
}

func TestFetchSetupReturnsInitializationTimeAndSendsBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/internal/upstream-hub/setup" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"initialized_at":1704067200}}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", time.Second)
	setup, err := client.FetchSetup(context.Background())
	if err != nil {
		t.Fatalf("FetchSetup returned error: %v", err)
	}
	if setup.InitializedAt != 1704067200 {
		t.Fatalf("initialized_at = %d, want 1704067200", setup.InitializedAt)
	}
}
