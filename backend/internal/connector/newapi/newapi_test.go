package newapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

func TestLoginRc23AccessTokenAuthorizesRates(t *testing.T) {
	const accessToken = "dashboard-access-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			if got := r.URL.Query().Get("turnstile"); got != "captcha-solution" {
				t.Fatalf("login turnstile query = %q, want captcha-solution", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"access_token": accessToken,
					"token_type":   "Bearer",
					"user":         map[string]any{"id": 42},
				},
			})
		case "/api/token/":
			if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
				http.Error(w, fmt.Sprintf("authorization = %q", got), http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"items": []map[string]any{{"id": 7, "name": "active-key", "group": "linked", "status": 1}},
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
				"data":    map[string]any{"linked": map[string]any{"ratio": 0.5, "desc": "used"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New()
	channel := &connector.Channel{
		SiteURL:        server.URL,
		TurnstileToken: "captcha-solution",
	}
	session, err := client.Login(context.Background(), channel)
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if session.AccessToken != accessToken {
		t.Fatalf("Login access token = %q, want %q", session.AccessToken, accessToken)
	}

	if _, err := client.GetRates(context.Background(), channel, session); err != nil {
		t.Fatalf("GetRates returned error after successful login: %v", err)
	}
}

func TestLoginAvoidsUserAgentRejectedByUpstreamGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got == "upstream-hub/0.1" {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"access_token": "dashboard-access-token",
				"user":         map[string]any{"id": 42},
			},
		})
	}))
	defer server.Close()

	if _, err := New().Login(context.Background(), &connector.Channel{SiteURL: server.URL}); err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
}

func TestRefreshRc23RotatesAccessTokenAndCookie(t *testing.T) {
	const refreshedAccessToken = "refreshed-access-token"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/user/auth/refresh" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Cookie"); got != "new_api_refresh=old-refresh" {
			t.Errorf("refresh Cookie = %q, want old refresh cookie", got)
		}
		if got := r.Header.Get("Origin"); got != server.URL {
			t.Errorf("refresh Origin = %q, want %q", got, server.URL)
		}
		w.Header().Add("Set-Cookie", "new_api_refresh=rotated-refresh; Path=/api/user/auth")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"access_token":      refreshedAccessToken,
				"access_expires_at": time.Now().Add(10 * time.Minute).Unix(),
				"user":              map[string]any{"id": 42},
			},
		})
	}))
	defer server.Close()

	refreshed, err := New().Refresh(context.Background(), &connector.Channel{SiteURL: server.URL}, &connector.AuthSession{
		UserID:      "42",
		AccessToken: "expired-access-token",
		Cookie:      "new_api_refresh=old-refresh",
	})
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if refreshed.AccessToken != refreshedAccessToken {
		t.Fatalf("Refresh access token = %q, want %q", refreshed.AccessToken, refreshedAccessToken)
	}
	if refreshed.Cookie != "new_api_refresh=rotated-refresh" {
		t.Fatalf("Refresh cookie = %q, want rotated refresh cookie", refreshed.Cookie)
	}
}

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

func TestJoinCookiesKeepsLatestValueForDuplicateNames(t *testing.T) {
	got := joinCookies([]*http.Cookie{
		{Name: "session", Value: "stale"},
		{Name: "theme", Value: "dark"},
		{Name: "session", Value: "active"},
	})
	if want := "session=active; theme=dark"; got != want {
		t.Fatalf("joinCookies() = %q, want %q", got, want)
	}
}
