package newapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

func TestGetRatesReturnsOnlyGroupsLinkedToActiveTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/token/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"items": []map[string]any{
						{"id": 7, "name": "active-key", "group": "linked", "status": 1},
						{"name": "invalid-key", "group": "invalid", "status": 0},
						{"name": "disabled-key", "group": "disabled", "status": 2},
					},
				},
			})
		case "/api/token/batch/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"keys": map[string]string{"7": "sk-active-key"}},
			})
		case "/api/user/self/groups":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"linked":   map[string]any{"ratio": 0.5, "desc": "used"},
					"invalid":  map[string]any{"ratio": 0.55, "desc": "invalid token"},
					"disabled": map[string]any{"ratio": 0.6, "desc": "disabled token"},
					"unlinked": map[string]any{"ratio": 0.7, "desc": "unused"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rates, err := New().GetRates(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		UserID: "42",
		Cookie: "session=test",
	})
	if err != nil {
		t.Fatalf("GetRates returned error: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("GetRates returned %d groups, want 1: %#v", len(rates), rates)
	}
	if rates[0].ModelName != "linked" || rates[0].Ratio != 0.5 {
		t.Fatalf("GetRates returned unexpected group: %#v", rates[0])
	}
	if len(rates[0].Keys) != 1 || rates[0].Keys[0].Fingerprint == "" {
		t.Fatalf("GetRates did not return the active key identity: %#v", rates[0].Keys)
	}
	if rates[0].Keys[0].Name != "active-key" || rates[0].Keys[0].TokenID != "7" {
		t.Fatalf("GetRates returned incomplete key identity: %#v", rates[0].Keys[0])
	}
}

func TestDecodeLinkedTokenGroupsRejectsUnknownPayload(t *testing.T) {
	if _, err := decodeLinkedTokenGroups([]byte(`{"unexpected":[]}`)); err == nil {
		t.Fatal("decodeLinkedTokenGroups returned nil error for an unknown payload")
	}
}
