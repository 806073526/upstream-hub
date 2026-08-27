// Package newapi 实现对 NewAPI 风格上游站点的 connector，参考 docs/USER_BALANCE_GROUP_RATE_AUTH_API_CN-newapi.md。
package newapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/worryzyy/upstream-hub/internal/connector"
)

func init() {
	connector.Register(connector.TypeNewAPI, func() connector.Connector { return New() })
}

// Client NewAPI connector 实现。
type Client struct {
	http *resty.Client
}

func New() *Client {
	c := resty.New().
		SetTimeout(30*time.Second).
		SetHeader("Accept", "application/json")
	return &Client{http: c}
}

// newapiResp NewAPI 统一响应外壳：{ success, message, data }。
type newapiResp struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) GetTurnstileSiteKey(ctx context.Context, ch *connector.Channel) (string, error) {
	body, err := c.getJSON(ctx, strings.TrimRight(ch.SiteURL, "/")+"/api/status", nil)
	if err != nil {
		return "", fmt.Errorf("newapi status: %w", err)
	}
	var status struct {
		TurnstileCheck   bool   `json:"turnstile_check"`
		TurnstileSiteKey string `json:"turnstile_site_key"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return "", fmt.Errorf("newapi status decode: %w", err)
	}
	if !status.TurnstileCheck {
		return "", nil
	}
	return status.TurnstileSiteKey, nil
}

func (c *Client) Login(ctx context.Context, ch *connector.Channel) (*connector.AuthSession, error) {
	site := strings.TrimRight(ch.SiteURL, "/")
	req := c.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]string{
			"username": ch.Username,
			"password": ch.Password,
		})
	if ch.TurnstileToken != "" {
		req.SetQueryParam("turnstile", ch.TurnstileToken)
	}

	resp, err := req.Post(site + "/api/user/login")
	if err != nil {
		return nil, fmt.Errorf("newapi login http: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("newapi login: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	var wrapped newapiResp
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("newapi login decode: %w", err)
	}
	if !wrapped.Success {
		return nil, fmt.Errorf("newapi login: %s", wrapped.Message)
	}

	var data struct {
		Require2FA      bool   `json:"require_2fa"`
		ID              int64  `json:"id"`
		AccessToken     string `json:"access_token"`
		AccessExpiresAt int64  `json:"access_expires_at"`
		User            struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	_ = json.Unmarshal(wrapped.Data, &data)
	if data.Require2FA {
		return nil, errors.New("newapi account requires 2FA; please disable it for monitoring accounts")
	}

	cookie := joinCookies(resp.Cookies())
	if cookie == "" && strings.TrimSpace(data.AccessToken) == "" {
		return nil, errors.New("newapi login: no session cookie returned")
	}
	if data.ID == 0 {
		data.ID = data.User.ID
	}
	if data.ID == 0 {
		// 新版 Bearer 鉴权不需要用户 ID；旧版 Cookie 鉴权仍会尽量带上它。
		if strings.TrimSpace(data.AccessToken) == "" {
			return nil, errors.New("newapi login: missing user id in response")
		}
	}
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	if data.AccessExpiresAt > 0 {
		expiresAt = time.Unix(data.AccessExpiresAt, 0)
	}
	return &connector.AuthSession{
		UserID:      strconv.FormatInt(data.ID, 10),
		AccessToken: strings.TrimSpace(data.AccessToken),
		Cookie:      cookie,
		ExpiresAt:   expiresAt,
	}, nil
}

func (c *Client) CheckAuth(ctx context.Context, ch *connector.Channel, session *connector.AuthSession) error {
	if session == nil || (session.Cookie == "" && session.AccessToken == "") {
		return errors.New("missing newapi session credentials")
	}
	_, err := c.getJSON(ctx, strings.TrimRight(ch.SiteURL, "/")+"/api/user/self", session)
	return err
}

// Refresh exchanges the rc.23 refresh cookie for a short-lived dashboard
// access token. The refresh cookie is rotated by NewAPI and must be persisted
// together with the new access token by the channel service.
func (c *Client) Refresh(ctx context.Context, ch *connector.Channel, session *connector.AuthSession) (*connector.AuthSession, error) {
	if session == nil || strings.TrimSpace(session.Cookie) == "" {
		return nil, errors.New("missing newapi refresh cookie")
	}
	site := strings.TrimRight(ch.SiteURL, "/")
	req := c.http.R().SetContext(ctx).SetHeader("Cookie", session.Cookie)
	if origin, err := requestOrigin(site); err == nil {
		req.SetHeader("Origin", origin)
	}
	resp, err := req.Post(site + "/api/user/auth/refresh")
	if err != nil {
		return nil, fmt.Errorf("newapi refresh http: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("newapi refresh: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	var wrapped newapiResp
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("newapi refresh decode: %w", err)
	}
	if !wrapped.Success {
		return nil, fmt.Errorf("newapi refresh: %s", wrapped.Message)
	}
	var data struct {
		AccessToken     string `json:"access_token"`
		AccessExpiresAt int64  `json:"access_expires_at"`
		User            struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(wrapped.Data, &data); err != nil {
		return nil, fmt.Errorf("newapi refresh data decode: %w", err)
	}
	accessToken := strings.TrimSpace(data.AccessToken)
	if accessToken == "" {
		return nil, errors.New("newapi refresh: missing access token")
	}
	userID := session.UserID
	if userID == "" && data.User.ID > 0 {
		userID = strconv.FormatInt(data.User.ID, 10)
	}
	cookie := joinCookies(resp.Cookies())
	if cookie == "" {
		cookie = session.Cookie
	}
	expiresAt := time.Now().Add(15 * time.Minute)
	if data.AccessExpiresAt > 0 {
		expiresAt = time.Unix(data.AccessExpiresAt, 0)
	}
	return &connector.AuthSession{
		UserID:      userID,
		AccessToken: accessToken,
		Cookie:      cookie,
		ExpiresAt:   expiresAt,
	}, nil
}

func (c *Client) GetBalance(ctx context.Context, ch *connector.Channel, session *connector.AuthSession) (*connector.BalanceResult, error) {
	site := strings.TrimRight(ch.SiteURL, "/")
	statusBody, err := c.getJSON(ctx, site+"/api/status", nil)
	if err != nil {
		return nil, fmt.Errorf("newapi status: %w", err)
	}
	var status struct {
		QuotaPerUnit float64 `json:"quota_per_unit"`
	}
	if err := json.Unmarshal(statusBody, &status); err != nil {
		return nil, fmt.Errorf("newapi status decode: %w", err)
	}
	if status.QuotaPerUnit <= 0 {
		status.QuotaPerUnit = 500000
	}

	selfBody, err := c.getJSON(ctx, site+"/api/user/self", session)
	if err != nil {
		return nil, fmt.Errorf("newapi self: %w", err)
	}
	var self struct {
		Quota float64 `json:"quota"`
	}
	if err := json.Unmarshal(selfBody, &self); err != nil {
		return nil, fmt.Errorf("newapi self decode: %w", err)
	}
	return &connector.BalanceResult{
		Balance:   self.Quota / status.QuotaPerUnit,
		SampledAt: time.Now(),
	}, nil
}

func (c *Client) GetUsage(ctx context.Context, ch *connector.Channel, session *connector.AuthSession, query connector.UsageQuery) (*connector.UsageResult, error) {
	now := query.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	location, err := time.LoadLocation(query.Timezone)
	if err != nil {
		return nil, fmt.Errorf("newapi usage timezone: %w", err)
	}

	site := strings.TrimRight(ch.SiteURL, "/")
	statusBody, err := c.getJSON(ctx, site+"/api/status", nil)
	if err != nil {
		return nil, fmt.Errorf("newapi status: %w", err)
	}
	var status struct {
		QuotaPerUnit float64 `json:"quota_per_unit"`
	}
	if err := json.Unmarshal(statusBody, &status); err != nil {
		return nil, fmt.Errorf("newapi status decode: %w", err)
	}
	if status.QuotaPerUnit <= 0 {
		status.QuotaPerUnit = 500000
	}

	selfBody, err := c.getJSON(ctx, site+"/api/user/self", session)
	if err != nil {
		return nil, fmt.Errorf("newapi self: %w", err)
	}
	var self struct {
		UsedQuota float64 `json:"used_quota"`
	}
	if err := json.Unmarshal(selfBody, &self); err != nil {
		return nil, fmt.Errorf("newapi self decode: %w", err)
	}
	total := self.UsedQuota / status.QuotaPerUnit

	localNow := now.In(location)
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).UTC()
	todayQuota, err := c.getUsageQuota(ctx, site, session, todayStart, now)
	if err != nil {
		return nil, fmt.Errorf("newapi today usage: %w", err)
	}
	today := todayQuota / status.QuotaPerUnit

	bucketEnd := now.Truncate(5 * time.Minute)
	bucketStart := bucketEnd.Add(-5 * time.Minute)
	bucketQuota, err := c.getUsageQuota(ctx, site, session, bucketStart, bucketEnd)
	if err != nil {
		return nil, fmt.Errorf("newapi five-minute usage: %w", err)
	}

	buckets := []connector.UsageBucketResult{{
		StartAt:           bucketStart,
		EndAt:             bucketEnd,
		ResolutionSeconds: 300,
		Amount:            bucketQuota / status.QuotaPerUnit,
		Currency:          "USD",
		Source:            "newapi_stat",
		Quality:           connector.UsageQualityExact,
		Complete:          true,
	}}
	if !query.HistorySince.IsZero() {
		historyBody, historyErr := c.getJSON(ctx, site+"/api/data/self?start_timestamp="+
			strconv.FormatInt(query.HistorySince.UTC().Unix(), 10)+"&end_timestamp="+
			strconv.FormatInt(now.Unix()-1, 10), session)
		if historyErr == nil {
			hourly, decodeErr := decodeHourlyUsage(historyBody, status.QuotaPerUnit, query.HistorySince.UTC(), now)
			if decodeErr != nil {
				return nil, fmt.Errorf("newapi hourly usage decode: %w", decodeErr)
			}
			buckets = append(buckets, hourly...)
		}
	}

	return &connector.UsageResult{
		TotalAmount: &total,
		TodayAmount: &today,
		Currency:    "USD",
		ObservedAt:  now,
		Buckets:     buckets,
	}, nil
}

func decodeHourlyUsage(body []byte, quotaPerUnit float64, since, now time.Time) ([]connector.UsageBucketResult, error) {
	var rows []struct {
		CreatedAt int64   `json:"created_at"`
		Quota     float64 `json:"quota"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	amounts := make(map[int64]float64)
	for _, row := range rows {
		startAt := time.Unix(row.CreatedAt, 0).UTC().Truncate(time.Hour)
		if startAt.Before(since) || !startAt.Before(now) {
			continue
		}
		amounts[startAt.Unix()] += row.Quota / quotaPerUnit
	}
	starts := make([]int64, 0, len(amounts))
	for start := range amounts {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	buckets := make([]connector.UsageBucketResult, 0, len(starts))
	for _, start := range starts {
		startAt := time.Unix(start, 0).UTC()
		endAt := startAt.Add(time.Hour)
		buckets = append(buckets, connector.UsageBucketResult{
			StartAt:           startAt,
			EndAt:             endAt,
			ResolutionSeconds: 3600,
			Amount:            math.Round(amounts[start]*1e8) / 1e8,
			Currency:          "USD",
			Source:            "newapi_data",
			Quality:           connector.UsageQualityExact,
			Complete:          !endAt.After(now),
		})
	}
	return buckets, nil
}

func (c *Client) getUsageQuota(ctx context.Context, site string, session *connector.AuthSession, start, end time.Time) (float64, error) {
	if !end.After(start) {
		return 0, errors.New("usage end must be after start")
	}
	url := site + "/api/log/self/stat?type=2&start_timestamp=" + strconv.FormatInt(start.UTC().Unix(), 10) +
		"&end_timestamp=" + strconv.FormatInt(end.UTC().Unix()-1, 10)
	body, err := c.getJSON(ctx, url, session)
	if err != nil {
		return 0, err
	}
	var stat struct {
		Quota float64 `json:"quota"`
	}
	if err := json.Unmarshal(body, &stat); err != nil {
		return 0, err
	}
	return stat.Quota, nil
}

func (c *Client) GetRates(ctx context.Context, ch *connector.Channel, session *connector.AuthSession) ([]connector.RateResult, error) {
	site := strings.TrimRight(ch.SiteURL, "/")
	tokenBody, err := c.getJSON(ctx, site+"/api/token/?p=0&size=100", session)
	if err != nil {
		return nil, fmt.Errorf("newapi tokens: %w", err)
	}
	linkedTokens, err := decodeActiveTokens(tokenBody)
	if err != nil {
		return nil, fmt.Errorf("newapi tokens decode: %w", err)
	}
	if len(linkedTokens) == 0 {
		return []connector.RateResult{}, nil
	}
	keys, err := c.getTokenKeys(ctx, site, session, linkedTokens)
	if err != nil {
		// Older NewAPI deployments may not expose the batch key endpoint. Keep
		// collecting ratios in that case; identities simply remain unmatched.
		keys = map[int64]string{}
	}
	linkedGroups := make(map[string][]connector.KeyIdentity)
	for _, token := range linkedTokens {
		identity := connector.KeyIdentity{Name: token.Name}
		if token.ID > 0 {
			identity.TokenID = strconv.FormatInt(token.ID, 10)
		}
		if key := keys[token.ID]; key != "" {
			identity.Fingerprint = connector.KeyFingerprint(key)
		}
		linkedGroups[token.Group] = append(linkedGroups[token.Group], identity)
	}

	body, err := c.getJSON(ctx, site+"/api/user/self/groups", session)
	if err != nil {
		return nil, fmt.Errorf("newapi groups: %w", err)
	}
	// data: { "default": { "ratio": 1, "desc": "..." }, "auto": { "ratio": "自动", ... } }
	raw := map[string]struct {
		Ratio json.RawMessage `json:"ratio"`
		Desc  string          `json:"desc"`
	}{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("newapi groups decode: %w", err)
	}
	out := make([]connector.RateResult, 0, len(raw))
	for name, v := range raw {
		identities, ok := linkedGroups[name]
		if !ok {
			continue
		}
		var ratio float64
		if err := json.Unmarshal(v.Ratio, &ratio); err != nil {
			// "auto" 组的 ratio 是字符串 "自动"，跳过。
			continue
		}
		out = append(out, connector.RateResult{
			ModelName:   name,
			Description: v.Desc,
			Ratio:       ratio,
			Keys:        identities,
		})
	}
	return out, nil
}

type activeToken struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Group  string `json:"group"`
	Status *int   `json:"status"`
}

func decodeActiveTokens(body []byte) ([]activeToken, error) {
	var items []activeToken
	if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, err
		}
	} else {
		var page struct {
			Items *[]activeToken `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		if page.Items == nil {
			return nil, errors.New("missing token items")
		}
		items = *page.Items
	}
	active := make([]activeToken, 0, len(items))
	for _, item := range items {
		item.Group = strings.TrimSpace(item.Group)
		if item.Group == "" || (item.Status != nil && *item.Status != 1) {
			continue
		}
		active = append(active, item)
	}
	return active, nil
}

func (c *Client) getTokenKeys(ctx context.Context, site string, session *connector.AuthSession, tokens []activeToken) (map[int64]string, error) {
	ids := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.ID > 0 {
			ids = append(ids, token.ID)
		}
	}
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	body, err := c.postJSON(ctx, site+"/api/token/batch/keys", session, map[string]any{"ids": ids})
	if err != nil {
		return nil, err
	}
	var data struct {
		Keys map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	result := make(map[int64]string, len(data.Keys))
	for id, key := range data.Keys {
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err == nil {
			result[parsed] = key
		}
	}
	return result, nil
}

func decodeLinkedTokenGroups(body []byte) (map[string]struct{}, error) {
	groups := make(map[string]struct{})
	items, err := decodeActiveTokens(body)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		groups[item.Group] = struct{}{}
	}
	return groups, nil
}

func (c *Client) getJSON(ctx context.Context, url string, session *connector.AuthSession) ([]byte, error) {
	req := c.http.R().SetContext(ctx)
	if session != nil {
		if session.AccessToken != "" {
			req.SetHeader("Authorization", "Bearer "+session.AccessToken)
		}
		if session.Cookie != "" {
			req.SetHeader("Cookie", session.Cookie)
		}
		// NewAPI 即便用 session 鉴权也要求带 New-Api-User 头（"unauthorized, New-Api-User header not provided"）。
		if session.UserID != "" {
			req.SetHeader("New-Api-User", session.UserID)
		}
	}
	resp, err := req.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, connector.HTTPStatusError(resp.StatusCode(), resp.Body())
	}
	var wrapped newapiResp
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if !wrapped.Success {
		return nil, errors.New(wrapped.Message)
	}
	return wrapped.Data, nil
}

func (c *Client) postJSON(ctx context.Context, url string, session *connector.AuthSession, payload any) ([]byte, error) {
	req := c.http.R().SetContext(ctx).SetHeader("Content-Type", "application/json").SetBody(payload)
	if session != nil {
		if session.AccessToken != "" {
			req.SetHeader("Authorization", "Bearer "+session.AccessToken)
		}
		if session.Cookie != "" {
			req.SetHeader("Cookie", session.Cookie)
		}
		if session.UserID != "" {
			req.SetHeader("New-Api-User", session.UserID)
		}
	}
	resp, err := req.Post(url)
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, connector.HTTPStatusError(resp.StatusCode(), resp.Body())
	}
	var wrapped newapiResp
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if !wrapped.Success {
		return nil, errors.New(wrapped.Message)
	}
	return wrapped.Data, nil
}

func requestOrigin(site string) (string, error) {
	parsed, err := url.Parse(site)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err == nil {
			err = errors.New("missing scheme or host")
		}
		return "", err
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func joinCookies(cookies []*http.Cookie) string {
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	indexes := make(map[string]int, len(cookies))
	for _, c := range cookies {
		part := c.Name + "=" + c.Value
		if index, ok := indexes[c.Name]; ok {
			parts[index] = part
			continue
		}
		indexes[c.Name] = len(parts)
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}
