package newapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
	"github.com/worryzyy/upstream-hub/internal/priority"
)

type Identity struct {
	ChannelID      int    `json:"channel_id"`
	ChannelName    string `json:"channel_name"`
	BaseURL        string `json:"base_url"`
	KeyFingerprint string `json:"key_fingerprint"`
	Priority       int64  `json:"priority"`
}

type Scan struct {
	ChannelID uint
	SiteURL   string
	Balance   *float64
	BalanceAt time.Time
	Results   []connector.RateResult
}

type Metric struct {
	ChannelID         int      `json:"channel_id"`
	UpstreamChannelID uint     `json:"upstream_channel_id"`
	Group             string   `json:"upstream_group"`
	Ratio             float64  `json:"upstream_ratio"`
	Balance           *float64 `json:"upstream_balance,omitempty"`
	RatioAt           int64    `json:"ratio_updated_time"`
	BalanceAt         int64    `json:"balance_updated_time,omitempty"`
	SyncStatus        string   `json:"sync_status"`
	SyncError         string   `json:"sync_error,omitempty"`
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// HTTPStatusError keeps the response status available to callers that need to
// distinguish an unsupported optional endpoint from a transient source error.
type HTTPStatusError struct {
	Path       string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "new-api request failed"
	}
	return fmt.Sprintf("new-api request %s: status %d", e.Path, e.StatusCode)
}

func NewClient(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), Token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: timeout}}
}

func BuildMatchedMetrics(identities []Identity, scans []Scan, now time.Time) []Metric {
	if now.IsZero() {
		now = time.Now()
	}
	type identityKey struct {
		fingerprint string
		baseURL     string
	}
	byKey := make(map[identityKey][]Identity, len(identities))
	for _, identity := range identities {
		fingerprint := strings.TrimSpace(identity.KeyFingerprint)
		if identity.ChannelID <= 0 || fingerprint == "" {
			continue
		}
		baseURL := normalizeURL(identity.BaseURL)
		if baseURL != "" {
			key := identityKey{fingerprint: fingerprint, baseURL: baseURL}
			byKey[key] = append(byKey[key], identity)
		}
		byKey[identityKey{fingerprint: fingerprint}] = append(byKey[identityKey{fingerprint: fingerprint}], identity)
	}
	metrics := make([]Metric, 0)
	seen := make(map[string]struct{})
	for _, scan := range scans {
		for _, result := range scan.Results {
			for _, key := range result.Keys {
				fingerprint := strings.TrimSpace(key.Fingerprint)
				if fingerprint == "" {
					continue
				}
				matches := byKey[identityKey{fingerprint: fingerprint, baseURL: normalizeURL(scan.SiteURL)}]
				if len(matches) == 0 {
					matches = byKey[identityKey{fingerprint: fingerprint}]
				}
				for _, identity := range matches {
					key := fmt.Sprintf("%d:%d:%s", identity.ChannelID, scan.ChannelID, result.ModelName)
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					metric := Metric{ChannelID: identity.ChannelID, UpstreamChannelID: scan.ChannelID, Group: result.ModelName, Ratio: result.Ratio, RatioAt: now.Unix(), SyncStatus: "matched"}
					if scan.Balance != nil {
						metric.Balance = scan.Balance
						if !scan.BalanceAt.IsZero() {
							metric.BalanceAt = scan.BalanceAt.Unix()
						}
					}
					metrics = append(metrics, metric)
				}
			}
		}
	}
	return metrics
}

func normalizeURL(value string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), "/")
}

func (c *Client) FetchIdentities(ctx context.Context) ([]Identity, error) {
	var response struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/internal/upstream-hub/identities", nil, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, fmt.Errorf("new-api identities: %s", response.Message)
	}
	var list []Identity
	if err := json.Unmarshal(response.Data, &list); err != nil {
		return nil, fmt.Errorf("decode new-api identities: %w", err)
	}
	return list, nil
}

func (c *Client) PushMetrics(ctx context.Context, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/internal/upstream-hub/metrics", map[string]any{"items": metrics}, &response); err != nil {
		return err
	}
	if !response.Success {
		return fmt.Errorf("new-api metrics: %s", response.Message)
	}
	return nil
}

func (c *Client) ApplyPriorities(ctx context.Context, updates []priority.PriorityUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(updates))
	for _, update := range updates {
		items = append(items, map[string]any{"channel_id": update.ChannelID, "priority": update.Priority})
	}
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/internal/upstream-hub/priority/apply", map[string]any{"items": items}, &response); err != nil {
		return err
	}
	if !response.Success {
		return fmt.Errorf("new-api priorities: %s", response.Message)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, target any) error {
	if c == nil || c.HTTP == nil || c.BaseURL == "" {
		return fmt.Errorf("new-api integration is not configured")
	}
	var body *strings.Reader
	if payload == nil {
		body = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPStatusError{Path: path, StatusCode: resp.StatusCode}
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return err
	}
	return nil
}
