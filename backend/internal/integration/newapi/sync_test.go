package newapi

import (
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
