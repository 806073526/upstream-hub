package newapi

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	BillingBucketSeconds   = 300
	BillingMaxRangeSeconds = 7 * 24 * 60 * 60
)

// Setup contains the NewAPI installation metadata needed to determine the
// earliest safe point for a first billing sync.
type Setup struct {
	InitializedAt int64 `json:"initialized_at"`
}

type BillingAggregateRequest struct {
	StartAt       int64 `json:"start_at"`
	EndAt         int64 `json:"end_at"`
	BucketSeconds int   `json:"bucket_seconds"`
}

type BillingBucket struct {
	BucketStart         int64   `json:"bucket_start"`
	BucketEnd           int64   `json:"bucket_end"`
	ChannelID           int     `json:"channel_id"`
	ChannelName         string  `json:"channel_name"`
	Group               string  `json:"group"`
	ModelName           string  `json:"model_name"`
	EffectiveGroupRatio float64 `json:"effective_group_ratio"`
	RatioSource         string  `json:"ratio_source"`
	NormalizationStatus string  `json:"normalization_status"`
	ConsumeQuota        int64   `json:"consume_quota"`
	RefundQuota         int64   `json:"refund_quota"`
	NetQuota            int64   `json:"net_quota"`
	EventCount          int64   `json:"event_count"`
}

type BillingAggregate struct {
	Source                string                `json:"source"`
	StartAt               int64                 `json:"start_at"`
	EndAt                 int64                 `json:"end_at"`
	BucketSeconds         int                   `json:"bucket_seconds"`
	QuotaPerUnit          float64               `json:"quota_per_unit"`
	Complete              bool                  `json:"complete"`
	Items                 []BillingBucket       `json:"items"`
	PersonalUsageItems    []PersonalUsageBucket `json:"personal_usage_items"`
	PersonalUsageComplete bool                  `json:"personal_usage_complete"`
}

type PersonalUsageBucket struct {
	BucketStart  int64 `json:"bucket_start"`
	BucketEnd    int64 `json:"bucket_end"`
	ConsumeQuota int64 `json:"consume_quota"`
	RefundQuota  int64 `json:"refund_quota"`
	NetQuota     int64 `json:"net_quota"`
	EventCount   int64 `json:"event_count"`
}

type BillingDetailRequest struct {
	StartAt  int64 `json:"start_at"`
	EndAt    int64 `json:"end_at"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

type BillingEvent struct {
	SourceLogID         int64   `json:"source_log_id"`
	CreatedAt           int64   `json:"created_at"`
	EventType           string  `json:"event_type"`
	ChannelID           int     `json:"channel_id"`
	ChannelName         string  `json:"channel_name"`
	Group               string  `json:"group"`
	ModelName           string  `json:"model_name"`
	EffectiveGroupRatio float64 `json:"effective_group_ratio"`
	RatioSource         string  `json:"ratio_source"`
	NormalizationStatus string  `json:"normalization_status"`
	Quota               int64   `json:"quota"`
	UserID              int     `json:"user_id"`
	TokenName           string  `json:"token_name,omitempty"`
	RequestID           string  `json:"request_id,omitempty"`
	UpstreamRequestID   string  `json:"upstream_request_id,omitempty"`
}

type BillingDetails struct {
	Source   string         `json:"source"`
	StartAt  int64          `json:"start_at"`
	EndAt    int64          `json:"end_at"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int64          `json:"total"`
	HasMore  bool           `json:"has_more"`
	Complete bool           `json:"complete"`
	Items    []BillingEvent `json:"items"`
}

func (c *Client) FetchSetup(ctx context.Context) (Setup, error) {
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    Setup  `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/internal/upstream-hub/setup", nil, &response); err != nil {
		return Setup{}, err
	}
	if !response.Success {
		return Setup{}, fmt.Errorf("new-api setup: %s", response.Message)
	}
	if response.Data.InitializedAt <= 0 {
		return Setup{}, fmt.Errorf("new-api setup: invalid initialized_at")
	}
	return response.Data, nil
}

func (c *Client) FetchBillingAggregate(ctx context.Context, request BillingAggregateRequest) (BillingAggregate, error) {
	if request.StartAt <= 0 || request.EndAt <= request.StartAt || request.EndAt-request.StartAt > BillingMaxRangeSeconds {
		return BillingAggregate{}, fmt.Errorf("invalid billing aggregation window")
	}
	if request.BucketSeconds != BillingBucketSeconds {
		return BillingAggregate{}, fmt.Errorf("unsupported billing bucket_seconds")
	}
	var response struct {
		Success bool             `json:"success"`
		Message string           `json:"message"`
		Data    BillingAggregate `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/internal/upstream-hub/billing/aggregate", request, &response); err != nil {
		return BillingAggregate{}, err
	}
	if !response.Success {
		return BillingAggregate{}, fmt.Errorf("new-api billing aggregate: %s", response.Message)
	}
	if response.Data.QuotaPerUnit <= 0 {
		return BillingAggregate{}, fmt.Errorf("new-api billing aggregate: invalid quota_per_unit")
	}
	return response.Data, nil
}

func (c *Client) FetchBillingDetails(ctx context.Context, request BillingDetailRequest) (BillingDetails, error) {
	if request.StartAt <= 0 || request.EndAt <= request.StartAt || request.EndAt-request.StartAt > BillingMaxRangeSeconds {
		return BillingDetails{}, fmt.Errorf("invalid billing detail range")
	}
	if request.Page < 1 {
		request.Page = 1
	}
	if request.PageSize <= 0 {
		request.PageSize = 500
	}
	if request.PageSize > 500 {
		request.PageSize = 500
	}
	var response struct {
		Success bool           `json:"success"`
		Message string         `json:"message"`
		Data    BillingDetails `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/internal/upstream-hub/billing/details", request, &response); err != nil {
		return BillingDetails{}, err
	}
	if !response.Success {
		return BillingDetails{}, fmt.Errorf("new-api billing details: %s", response.Message)
	}
	return response.Data, nil
}

func billingTime(unix int64) time.Time {
	return time.Unix(unix, 0).UTC()
}
