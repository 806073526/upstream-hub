package sub2api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

func TestGetRatesReturnsOnlyGroupsLinkedToActiveKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{
						"name":   "active-key",
						"key":    "sk-active-key",
						"status": "active",
						"group":  map[string]any{"id": 2, "name": "linked"},
					},
					{
						"name":   "disabled-key",
						"status": "disabled",
						"group":  map[string]any{"id": 3, "name": "disabled"},
					},
					{
						"name":   "invalid-key",
						"status": 0,
						"group":  map[string]any{"id": 4, "name": "invalid"},
					},
				},
				"total": 3,
			})
		case "/api/v1/groups/available":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": []map[string]any{
					{"id": 2, "name": "linked", "rate_multiplier": 1.2},
					{"id": 3, "name": "disabled", "rate_multiplier": 1.3},
					{"id": 4, "name": "invalid", "rate_multiplier": 1.4},
					{"id": 5, "name": "unlinked", "rate_multiplier": 1.5},
				},
			})
		case "/api/v1/groups/rates":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]float64{"2": 0.8},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rates, err := New().GetRates(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		AccessToken: "test-token",
	})
	if err != nil {
		t.Fatalf("GetRates returned error: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("GetRates returned %d groups, want 1: %#v", len(rates), rates)
	}
	if rates[0].ModelName != "linked" || rates[0].Ratio != 0.8 {
		t.Fatalf("GetRates returned unexpected group: %#v", rates[0])
	}
	if len(rates[0].Keys) != 1 || rates[0].Keys[0].Fingerprint == "" {
		t.Fatalf("GetRates did not return the active key identity: %#v", rates[0].Keys)
	}
	if rates[0].Keys[0].Name != "active-key" {
		t.Fatalf("GetRates returned incomplete key identity: %#v", rates[0].Keys[0])
	}
}

func TestDecodeLinkedKeyGroupsRejectsUnknownPayload(t *testing.T) {
	if _, err := decodeLinkedKeyGroups([]byte(`{"unexpected":[]}`)); err == nil {
		t.Fatal("decodeLinkedKeyGroups returned nil error for an unknown payload")
	}
}
