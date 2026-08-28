// Package sub2api 实现 sub2api 风格上游站点的 connector，参考 docs/USER_BALANCE_GROUP_RATE_AUTH_API_CN-sub2api.md。
package sub2api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/worryzyy/upstream-hub/internal/connector"
)

func init() {
	connector.Register(connector.TypeSub2API, func() connector.Connector { return New() })
}

type Client struct {
	http *resty.Client
}

const (
	sub2APIHTTPTimeout  = 60 * time.Second
	sub2APITrendTimeout = 60 * time.Second
)

func New() *Client {
	c := resty.New().
		SetTimeout(sub2APIHTTPTimeout).
		SetHeader("User-Agent", "upstream-hub/0.1").
		SetHeader("Accept", "application/json")
	return &Client{http: c}
}

// sub2Resp sub2api 统一响应外壳：{ code, message, data }。code 0 = 成功。
type sub2Resp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) GetTurnstileSiteKey(ctx context.Context, ch *connector.Channel) (string, error) {
	body, err := c.getJSON(ctx, strings.TrimRight(ch.SiteURL, "/")+"/api/v1/settings/public", nil)
	if err != nil {
		return "", fmt.Errorf("sub2api public settings: %w", err)
	}
	var settings struct {
		TurnstileEnabled bool   `json:"turnstile_enabled"`
		TurnstileSiteKey string `json:"turnstile_site_key"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		return "", fmt.Errorf("sub2api public settings decode: %w", err)
	}
	if !settings.TurnstileEnabled {
		return "", nil
	}
	return settings.TurnstileSiteKey, nil
}

func (c *Client) Login(ctx context.Context, ch *connector.Channel) (*connector.AuthSession, error) {
	site := strings.TrimRight(ch.SiteURL, "/")
	body := map[string]string{
		"email":    ch.Username,
		"password": ch.Password,
	}
	if ch.TurnstileToken != "" {
		body["turnstile_token"] = ch.TurnstileToken
	}

	resp, err := c.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		Post(site + "/api/v1/auth/login")
	if err != nil {
		return nil, fmt.Errorf("sub2api login http: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("sub2api login: %w", connector.HTTPStatusError(resp.StatusCode(), resp.Body()))
	}
	var wrapped sub2Resp
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("sub2api login decode: %w", err)
	}
	if wrapped.Code != 0 {
		return nil, fmt.Errorf("sub2api login: %s", wrapped.Message)
	}

	var data struct {
		Requires2FA bool   `json:"requires_2fa"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(wrapped.Data, &data); err != nil {
		return nil, fmt.Errorf("sub2api login data: %w", err)
	}
	if data.Requires2FA {
		return nil, errors.New("sub2api account requires 2FA; please disable it for monitoring accounts")
	}
	if data.AccessToken == "" {
		return nil, errors.New("sub2api login: empty access_token")
	}

	expires := time.Now().Add(time.Duration(data.ExpiresIn) * time.Second)
	if data.ExpiresIn <= 0 {
		expires = time.Now().Add(time.Hour)
	}
	return &connector.AuthSession{
		AccessToken: data.AccessToken,
		ExpiresAt:   expires,
	}, nil
}

func (c *Client) CheckAuth(ctx context.Context, ch *connector.Channel, session *connector.AuthSession) error {
	if session == nil || session.AccessToken == "" {
		return errors.New("missing sub2api access_token")
	}
	_, err := c.getJSON(ctx, strings.TrimRight(ch.SiteURL, "/")+"/api/v1/auth/me", session)
	return err
}

func (c *Client) GetBalance(ctx context.Context, ch *connector.Channel, session *connector.AuthSession) (*connector.BalanceResult, error) {
	body, err := c.getJSON(ctx, strings.TrimRight(ch.SiteURL, "/")+"/api/v1/auth/me", session)
	if err != nil {
		return nil, fmt.Errorf("sub2api me: %w", err)
	}
	var me struct {
		Balance float64 `json:"balance"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return nil, fmt.Errorf("sub2api me decode: %w", err)
	}
	return &connector.BalanceResult{
		Balance:   me.Balance,
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
		return nil, fmt.Errorf("sub2api usage timezone: %w", err)
	}
	site := strings.TrimRight(ch.SiteURL, "/")

	statsBody, err := c.getJSON(ctx, site+"/api/v1/usage/dashboard/stats", session)
	if err != nil {
		return nil, fmt.Errorf("sub2api usage dashboard stats: %w", err)
	}
	var dashboard struct {
		TotalActualCost float64  `json:"total_actual_cost"`
		TodayActualCost *float64 `json:"today_actual_cost"`
	}
	if err := json.Unmarshal(statsBody, &dashboard); err != nil {
		return nil, fmt.Errorf("sub2api usage dashboard stats decode: %w", err)
	}
	total := dashboard.TotalActualCost

	todayQuery := url.Values{}
	todayQuery.Set("period", "today")
	todayQuery.Set("timezone", query.Timezone)
	todayBody, err := c.getJSON(ctx, site+"/api/v1/usage/stats?"+todayQuery.Encode(), session)
	if err != nil {
		return nil, fmt.Errorf("sub2api today usage: %w", err)
	}
	var todayStats struct {
		TotalActualCost *float64 `json:"total_actual_cost"`
		ActualCost      *float64 `json:"actual_cost"`
	}
	if err := json.Unmarshal(todayBody, &todayStats); err != nil {
		return nil, fmt.Errorf("sub2api today usage decode: %w", err)
	}
	zeroToday := 0.0
	today := &zeroToday
	if todayStats.TotalActualCost != nil {
		today = todayStats.TotalActualCost
	} else if todayStats.ActualCost != nil {
		today = todayStats.ActualCost
	} else if dashboard.TodayActualCost != nil {
		today = dashboard.TodayActualCost
	}

	historySince := query.HistorySince
	if historySince.IsZero() {
		historySince = now.Add(-24 * time.Hour)
	}
	trendQuery := url.Values{}
	trendQuery.Set("start_date", historySince.In(location).Format("2006-01-02"))
	trendQuery.Set("end_date", now.In(location).Format("2006-01-02"))
	trendQuery.Set("granularity", "hour")
	trendQuery.Set("timezone", query.Timezone)
	trendCtx, cancelTrend := context.WithTimeout(ctx, sub2APITrendTimeout)
	trendBody, err := c.getJSON(trendCtx, site+"/api/v1/usage/dashboard/trend?"+trendQuery.Encode(), session)
	cancelTrend()
	result := &connector.UsageResult{
		TotalAmount: &total,
		TodayAmount: today,
		Currency:    "USD",
		ObservedAt:  now,
	}
	if err != nil {
		result.Warnings = []string{fmt.Sprintf("sub2api usage trend: %v", err)}
		return result, nil
	}
	var trend struct {
		Trend []struct {
			Date       string  `json:"date"`
			ActualCost float64 `json:"actual_cost"`
		} `json:"trend"`
	}
	if err := json.Unmarshal(trendBody, &trend); err != nil {
		result.Warnings = []string{fmt.Sprintf("sub2api usage trend decode: %v", err)}
		return result, nil
	}

	buckets := make([]connector.UsageBucketResult, 0, len(trend.Trend))
	for _, point := range trend.Trend {
		startAt, err := time.ParseInLocation("2006-01-02 15:04", point.Date, location)
		if err != nil {
			result.Warnings = []string{fmt.Sprintf("sub2api usage trend date %q: %v", point.Date, err)}
			return result, nil
		}
		startAt = startAt.UTC()
		endAt := startAt.Add(time.Hour)
		buckets = append(buckets, connector.UsageBucketResult{
			StartAt:           startAt,
			EndAt:             endAt,
			ResolutionSeconds: 3600,
			Amount:            point.ActualCost,
			Currency:          "USD",
			Source:            "sub2api_trend",
			Quality:           connector.UsageQualityExact,
			Complete:          !endAt.After(now),
		})
	}

	result.Buckets = buckets
	return result, nil
}

func (c *Client) GetRates(ctx context.Context, ch *connector.Channel, session *connector.AuthSession) ([]connector.RateResult, error) {
	site := strings.TrimRight(ch.SiteURL, "/")
	linkedGroups, err := c.getLinkedKeyGroups(ctx, site, session)
	if err != nil {
		return nil, err
	}
	if len(linkedGroups.names) == 0 && len(linkedGroups.ids) == 0 {
		return []connector.RateResult{}, nil
	}

	availBody, err := c.getJSON(ctx, site+"/api/v1/groups/available", session)
	if err != nil {
		return nil, fmt.Errorf("sub2api groups available: %w", err)
	}
	var groups []struct {
		ID             uint64  `json:"id"`
		Name           string  `json:"name"`
		Description    string  `json:"description"`
		RateMultiplier float64 `json:"rate_multiplier"`
	}
	if err := json.Unmarshal(availBody, &groups); err != nil {
		return nil, fmt.Errorf("sub2api groups available decode: %w", err)
	}

	overrides := map[string]float64{}
	if ratesBody, err := c.getJSON(ctx, site+"/api/v1/groups/rates", session); err == nil {
		_ = json.Unmarshal(ratesBody, &overrides)
	}

	out := make([]connector.RateResult, 0, len(groups))
	for _, g := range groups {
		_, nameLinked := linkedGroups.names[g.Name]
		_, idLinked := linkedGroups.ids[g.ID]
		if !nameLinked && !idLinked {
			continue
		}
		rate := g.RateMultiplier
		if v, ok := overrides[strconv.FormatUint(g.ID, 10)]; ok {
			rate = v
		}
		out = append(out, connector.RateResult{
			ModelName:   g.Name,
			Description: g.Description,
			Ratio:       rate,
			Keys:        linkedGroups.identitiesFor(g.Name, g.ID),
		})
	}
	return out, nil
}

var keyListPaths = []string{
	"/api/v1/keys?page=1&page_size=100",
	"/api/v1/keys",
	"/api/v1/api-keys?page=1&page_size=100",
	"/api/v1/api-keys",
	"/api/v1/user/api-keys",
	"/api/v1/user/keys",
	"/api/keys",
}

type linkedKeyGroups struct {
	names       map[string]struct{}
	ids         map[uint64]struct{}
	identities  map[string][]connector.KeyIdentity
	identityIDs map[uint64][]connector.KeyIdentity
}

func (g linkedKeyGroups) identitiesFor(name string, id uint64) []connector.KeyIdentity {
	if values := g.identities[name]; len(values) > 0 {
		return values
	}
	return g.identityIDs[id]
}

func (c *Client) getLinkedKeyGroups(ctx context.Context, site string, session *connector.AuthSession) (linkedKeyGroups, error) {
	var lastErr error
	for _, path := range keyListPaths {
		body, err := c.getJSON(ctx, site+path, session)
		if err != nil {
			lastErr = err
			continue
		}
		groups, err := decodeLinkedKeyGroups(body)
		if err != nil {
			return linkedKeyGroups{}, fmt.Errorf("sub2api keys decode: %w", err)
		}
		return groups, nil
	}
	return linkedKeyGroups{}, fmt.Errorf("sub2api keys: %w", lastErr)
}

func decodeLinkedKeyGroups(body []byte) (linkedKeyGroups, error) {
	type keyItem struct {
		ID        json.RawMessage `json:"id"`
		Name      string          `json:"name"`
		Key       string          `json:"key"`
		Status    json.RawMessage `json:"status"`
		Enabled   *bool           `json:"enabled"`
		IsActive  *bool           `json:"is_active"`
		Group     json.RawMessage `json:"group"`
		GroupName string          `json:"group_name"`
		GroupID   uint64          `json:"group_id"`
	}

	var items []keyItem
	if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		if err := json.Unmarshal(body, &items); err != nil {
			return linkedKeyGroups{}, err
		}
	} else {
		var page struct {
			Items *[]keyItem      `json:"items"`
			List  *[]keyItem      `json:"list"`
			Keys  *[]keyItem      `json:"keys"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return linkedKeyGroups{}, err
		}
		switch {
		case page.Items != nil:
			items = *page.Items
		case page.List != nil:
			items = *page.List
		case page.Keys != nil:
			items = *page.Keys
		case len(page.Data) > 0 && string(page.Data) != "null":
			return decodeLinkedKeyGroups(page.Data)
		default:
			return linkedKeyGroups{}, errors.New("missing key items")
		}
	}

	groups := linkedKeyGroups{
		names:       make(map[string]struct{}),
		ids:         make(map[uint64]struct{}),
		identities:  make(map[string][]connector.KeyIdentity),
		identityIDs: make(map[uint64][]connector.KeyIdentity),
	}
	for _, item := range items {
		if !activeKey(item.Status, item.Enabled, item.IsActive) {
			continue
		}
		name := strings.TrimSpace(item.GroupName)
		id := item.GroupID
		if len(item.Group) > 0 && string(item.Group) != "null" {
			var group struct {
				ID   uint64 `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(item.Group, &group); err == nil {
				if id == 0 {
					id = group.ID
				}
				if name == "" {
					name = strings.TrimSpace(group.Name)
				}
			} else {
				var groupName string
				if stringErr := json.Unmarshal(item.Group, &groupName); stringErr == nil && name == "" {
					name = strings.TrimSpace(groupName)
				}
			}
		}
		if name != "" {
			groups.names[name] = struct{}{}
		}
		if id != 0 {
			groups.ids[id] = struct{}{}
		}
		identity := connector.KeyIdentity{Name: strings.TrimSpace(item.Name)}
		if len(item.ID) > 0 && string(item.ID) != "null" {
			var numeric uint64
			if json.Unmarshal(item.ID, &numeric) == nil && numeric != 0 {
				identity.TokenID = strconv.FormatUint(numeric, 10)
			}
			if identity.TokenID == "" {
				var text string
				if json.Unmarshal(item.ID, &text) == nil {
					identity.TokenID = strings.TrimSpace(text)
				}
			}
		}
		if strings.TrimSpace(item.Key) != "" {
			identity.Fingerprint = connector.KeyFingerprint(item.Key)
		}
		if name != "" {
			groups.identities[name] = append(groups.identities[name], identity)
		}
		if id != 0 {
			groups.identityIDs[id] = append(groups.identityIDs[id], identity)
		}
	}
	return groups, nil
}

func activeKey(status json.RawMessage, enabled, isActive *bool) bool {
	if (enabled != nil && !*enabled) || (isActive != nil && !*isActive) {
		return false
	}
	if len(status) == 0 || string(status) == "null" {
		return true
	}
	var text string
	if err := json.Unmarshal(status, &text); err == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "disabled", "expired", "revoked", "inactive":
			return false
		default:
			return true
		}
	}
	var number int
	if err := json.Unmarshal(status, &number); err == nil {
		return number == 1
	}
	return true
}

func (c *Client) getJSON(ctx context.Context, url string, session *connector.AuthSession) ([]byte, error) {
	req := c.http.R().SetContext(ctx)
	if session != nil && session.AccessToken != "" {
		req.SetHeader("Authorization", "Bearer "+session.AccessToken)
	}
	resp, err := req.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, connector.HTTPStatusError(resp.StatusCode(), resp.Body())
	}
	var wrapped struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if wrapped.Code == nil {
		return resp.Body(), nil
	}
	if *wrapped.Code != 0 {
		return nil, errors.New(wrapped.Message)
	}
	return wrapped.Data, nil
}
