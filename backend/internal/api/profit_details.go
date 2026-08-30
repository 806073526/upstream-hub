package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/businessclock"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

type profitSaleDetail struct {
	Source              string    `json:"source"`
	SourceLogID         *int64    `json:"source_log_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	BucketStart         time.Time `json:"bucket_start"`
	BucketEnd           time.Time `json:"bucket_end"`
	ChannelID           int       `json:"channel_id"`
	ChannelName         string    `json:"channel_name,omitempty"`
	NewAPIChannelID     int       `json:"newapi_channel_id,omitempty"`
	NewAPIChannelName   string    `json:"newapi_channel_name,omitempty"`
	UpstreamChannelID   *uint     `json:"upstream_channel_id,omitempty"`
	UpstreamChannelName string    `json:"upstream_channel_name,omitempty"`
	MappingStatus       string    `json:"mapping_status"`
	EventType           string    `json:"event_type"`
	Group               string    `json:"group"`
	ModelName           string    `json:"model_name"`
	EffectiveGroupRatio float64   `json:"effective_group_ratio"`
	RatioSource         string    `json:"ratio_source"`
	NormalizationStatus string    `json:"normalization_status"`
	Quota               int64     `json:"quota"`
	ChargedUSD          float64   `json:"charged_usd"`
	NormalizedUSD       float64   `json:"normalized_usd"`
	CreditUSDPerCNY     float64   `json:"credit_usd_per_cny"`
	SaleCNY             float64   `json:"sale_cny"`
	CostUSD             float64   `json:"cost_usd"`
	CostCNY             float64   `json:"cost_cny"`
	ProfitCNY           float64   `json:"profit_cny"`
	EventCount          int64     `json:"event_count,omitempty"`
	UserID              int       `json:"user_id,omitempty"`
	TokenName           string    `json:"token_name,omitempty"`
	RequestID           string    `json:"request_id,omitempty"`
	UpstreamRequestID   string    `json:"upstream_request_id,omitempty"`
}

type profitCostDetail struct {
	ChannelID         uint      `json:"channel_id"`
	ChannelName       string    `json:"channel_name,omitempty"`
	BucketStart       time.Time `json:"bucket_start"`
	BucketEnd         time.Time `json:"bucket_end"`
	ResolutionSeconds int       `json:"resolution_seconds"`
	Amount            float64   `json:"amount"`
	Currency          string    `json:"currency"`
	CostCNY           float64   `json:"cost_cny"`
	Source            string    `json:"source"`
	Quality           string    `json:"quality"`
	Complete          bool      `json:"complete"`
	CollectedAt       time.Time `json:"collected_at"`
}

type profitReconciliation struct {
	StartAt               time.Time `json:"start_at"`
	EndAt                 time.Time `json:"end_at"`
	SalesCNY              float64   `json:"sales_cny"`
	SalesDetailCNY        float64   `json:"sales_detail_cny"`
	StageUsageCostUSD     float64   `json:"stage_usage_cost_usd"`
	StageUsageCostCNY     float64   `json:"stage_usage_cost_cny"`
	CostDetailCNY         float64   `json:"cost_detail_cny"`
	AllocatedCostCNY      float64   `json:"allocated_cost_cny"`
	UnmatchedCostCNY      float64   `json:"unmatched_cost_cny"`
	UnmappedSalesCNY      float64   `json:"unmapped_sales_cny"`
	ExternalSalesCNY      float64   `json:"external_sales_cny"`
	ProfitCNY             float64   `json:"profit_cny"`
	OperatingProfitCNY    float64   `json:"operating_profit_cny"`
	PersonalUsageCNY      float64   `json:"personal_usage_cny"`
	NetProfitCNY          float64   `json:"net_profit_cny"`
	PersonalUsageComplete bool      `json:"personal_usage_complete"`
	NetProfitComplete     bool      `json:"net_profit_complete"`
	ReconciliationDelta   float64   `json:"reconciliation_delta_cny"`
	Currency              string    `json:"currency"`
	Complete              bool      `json:"complete"`
	DetailsAvailable      bool      `json:"details_available"`
}

func dashboardProfitDetails(c *gin.Context, d *Deps) {
	location, err := businessclock.Location()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	spec, err := profitDetailsSpec(c, location)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if d.Profit == nil {
		fail(c, http.StatusServiceUnavailable, errProfitRepositoryUnavailable)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(c.DefaultQuery("kind", "reconciliation")))
	if kind == "" {
		kind = "reconciliation"
	}
	if kind != "sales" && kind != "cost" && kind != "unmapped" && kind != "reconciliation" {
		fail(c, http.StatusBadRequest, errProfitDetailKind)
		return
	}
	page, pageSize := parseProfitPagination(c)
	channelNames := profitChannelNames(d)
	costRate := storage.DefaultUpstreamCostCNYPerUSD
	if d.Billing != nil {
		costRate = d.Billing.CostRate()
	}

	profits, err := d.Profit.ListBuckets(spec.StartAt, spec.EndAt)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	var usage []storage.UsageBucket
	if d.Usage != nil {
		usage, err = d.Usage.ListBuckets(spec.StartAt, spec.EndAt, spec.UsageResolutionSeconds)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		if usage == nil {
			usage = []storage.UsageBucket{}
		}
	}
	var personal []storage.NewAPIPersonalUsageBucket
	personalComplete := false
	if d.PersonalUsage != nil {
		personal, personalComplete, err = listPersonalUsageBucketsWithStatus(d.PersonalUsage, spec.StartAt, spec.EndAt, 0)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
	}
	summary := storage.SummarizeProfitWithUsage(profits, usage, spec.StartAt, spec.EndAt, spec.UsageResolutionSeconds, costRate)
	if d.PersonalUsage != nil {
		summary = storage.SummarizeProfitWithPersonalUsage(profits, usage, personal, spec.StartAt, spec.EndAt, spec.UsageResolutionSeconds, costRate, personalComplete)
	}
	reconciliation := profitReconciliation{
		StartAt: spec.StartAt, EndAt: spec.EndAt, SalesCNY: summary.SaleCNY,
		SalesDetailCNY: summary.SalesDetailCNY, StageUsageCostUSD: summary.CostUSD,
		StageUsageCostCNY: summary.StageUsageCostCNY, CostDetailCNY: summary.CostDetailCNY,
		AllocatedCostCNY: summary.AllocatedCostCNY, UnmatchedCostCNY: summary.UnmatchedCostCNY,
		UnmappedSalesCNY: summary.UnmappedSaleCNY, ExternalSalesCNY: summary.ExternalSalesCNY,
		ProfitCNY: summary.ProfitCNY, OperatingProfitCNY: summary.OperatingProfitCNY,
		PersonalUsageCNY: summary.PersonalUsageCNY, NetProfitCNY: summary.NetProfitCNY,
		PersonalUsageComplete: summary.PersonalUsageComplete, NetProfitComplete: summary.NetProfitComplete,
		ReconciliationDelta: summary.ReconciliationDelta, Currency: "CNY", Complete: summary.Complete,
	}

	var items any
	var total int
	hasDetails := false
	switch kind {
	case "sales", "unmapped":
		details, detailsTotal, detailAvailable := loadProfitSaleDetails(d, profits, spec.StartAt, spec.EndAt, kind, page, pageSize, channelNames, usage, costRate)
		items, total, hasDetails = details, detailsTotal, detailAvailable
	case "cost":
		details := make([]profitCostDetail, 0, len(usage))
		for _, bucket := range usage {
			cost, ok := storage.UsageCostCNY(bucket.Amount, bucket.Currency, costRate)
			if !ok {
				cost = 0
			}
			details = append(details, profitCostDetail{
				ChannelID: bucket.ChannelID, ChannelName: channelNames[bucket.ChannelID],
				BucketStart: bucket.BucketStart, BucketEnd: bucket.BucketEnd,
				ResolutionSeconds: bucket.ResolutionSeconds, Amount: bucket.Amount,
				Currency: bucket.Currency, CostCNY: cost, Source: bucket.Source,
				Quality: bucket.Quality, Complete: bucket.Complete, CollectedAt: bucket.CollectedAt,
			})
		}
		sort.Slice(details, func(i, j int) bool {
			if details[i].BucketStart.Equal(details[j].BucketStart) {
				return details[i].ChannelID > details[j].ChannelID
			}
			return details[i].BucketStart.After(details[j].BucketStart)
		})
		total = len(details)
		items = paginateProfitDetails(details, page, pageSize)
		hasDetails = total > 0
	case "reconciliation":
		items = []profitReconciliation{reconciliation}
		total = 1
		hasDetails = true
	}
	reconciliation.DetailsAvailable = hasDetails
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"kind": kind, "start_at": spec.StartAt, "end_at": spec.EndAt,
		"usage_resolution_seconds": spec.UsageResolutionSeconds,
		"page":                     page, "page_size": pageSize, "total": total,
		"has_more": page*pageSize < total, "items": items,
		"reconciliation": reconciliation,
	}})
}

var errProfitRepositoryUnavailable = &apiError{"profit repository is not configured"}
var errProfitDetailKind = &apiError{"unsupported profit detail kind"}

type apiError struct{ message string }

func (e *apiError) Error() string { return e.message }

func profitDetailsSpec(c *gin.Context, location *time.Location) (storage.ProfitTrendSpec, error) {
	if rawStart, rawEnd := strings.TrimSpace(c.Query("start_at")), strings.TrimSpace(c.Query("end_at")); rawStart != "" || rawEnd != "" {
		start, err := parseProfitTime(rawStart)
		if err != nil {
			return storage.ProfitTrendSpec{}, err
		}
		end, err := parseProfitTime(rawEnd)
		if err != nil {
			return storage.ProfitTrendSpec{}, err
		}
		if !end.After(start) {
			return storage.ProfitTrendSpec{}, &apiError{"end_at must be after start_at"}
		}
		return storage.ProfitTrendSpec{StartAt: start.UTC(), EndAt: end.UTC(), UsageResolutionSeconds: 3600, OutputResolutionSeconds: 3600}, nil
	}
	return storage.ResolveProfitTrendSpec(c.DefaultQuery("range", "24h"), profitTrendNow(), location)
}

func parseProfitTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, &apiError{"start_at and end_at are required together"}
	}
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(unix, 0).UTC(), nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, &apiError{"invalid profit detail time"}
	}
	return value.UTC(), nil
}

func parseProfitPagination(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	return page, pageSize
}

func profitChannelNames(d *Deps) map[uint]string {
	names := make(map[uint]string)
	if d.Channels == nil {
		return names
	}
	channels, err := d.Channels.List()
	if err != nil {
		return names
	}
	for _, channel := range channels {
		names[channel.ID] = channel.Name
	}
	return names
}

func loadProfitSaleDetails(d *Deps, rows []storage.ProfitBucket, start, end time.Time, kind string, page, pageSize int, channelNames map[uint]string, usage []storage.UsageBucket, costRate float64) ([]profitSaleDetail, int, bool) {
	if d == nil {
		return nil, 0, false
	}
	if kind == "unmapped" {
		items := buildUnmappedProfitSaleDetails(rows, channelNames)
		// Older databases may contain only source events because the profit
		// read model was introduced later. Keep that upgrade path readable, but
		// aggregate the events before returning them to the UI.
		if len(items) == 0 && d.Billing != nil {
			events, eventErr := listAllProfitEvents(d.Billing, start, end, "unmapped", pageSize)
			if eventErr == nil && len(events) > 0 {
				items = aggregateProfitEventDetails(events, channelNames)
			}
		}
		return paginateProfitDetails(items, page, pageSize), len(items), len(items) > 0
	}
	allItems := buildProfitSaleDetails(rows, usage, costRate, channelNames)
	return paginateProfitDetails(allItems, page, pageSize), len(allItems), len(allItems) > 0
}

func listAllProfitEvents(billing *storage.Billing, start, end time.Time, mappingFilter string, pageSize int) ([]storage.NewAPIBillingEvent, error) {
	if billing == nil {
		return nil, nil
	}
	if pageSize <= 0 {
		pageSize = 500
	}
	if pageSize > 500 {
		pageSize = 500
	}
	events := make([]storage.NewAPIBillingEvent, 0)
	for page := 1; ; page++ {
		pageItems, total, err := billing.ListEventsFiltered(start, end, mappingFilter, page, pageSize)
		if err != nil {
			return nil, err
		}
		events = append(events, pageItems...)
		if len(pageItems) == 0 || int64(len(events)) >= total {
			return events, nil
		}
	}
}

// buildProfitSaleDetails creates the reconciliation view from the two source
// ledgers. Every upstream usage bucket is retained as a row, while NewAPI sale
// buckets are assigned to overlapping cost buckets by time share. This keeps
// the table aligned with the cost ledger instead of exposing one row per log.
func buildProfitSaleDetails(profits []storage.ProfitBucket, usage []storage.UsageBucket, costRate float64, channelNames map[uint]string) []profitSaleDetail {
	if costRate <= 0 {
		costRate = storage.DefaultUpstreamCostCNYPerUSD
	}
	type aggregate struct {
		item           profitSaleDetail
		saleCount      int
		firstSale      *storage.ProfitBucket
		modelNames     map[string]struct{}
		groups         map[string]struct{}
		newAPIChannels map[string]struct{}
	}
	aggregates := make([]*aggregate, len(usage))
	for i, bucket := range usage {
		costCNY, ok := storage.UsageCostCNY(bucket.Amount, bucket.Currency, costRate)
		if !ok {
			costCNY = 0
		}
		name := channelNames[bucket.ChannelID]
		aggregates[i] = &aggregate{item: profitSaleDetail{
			Source: "usage-bucket", CreatedAt: bucket.BucketStart,
			BucketStart: bucket.BucketStart, BucketEnd: bucket.BucketEnd,
			ChannelID: int(bucket.ChannelID), ChannelName: name,
			UpstreamChannelName: name, MappingStatus: "unmapped",
			CostUSD: usageAmountUSDForDetail(bucket.Amount, bucket.Currency, costRate), CostCNY: costCNY,
			ProfitCNY: -costCNY, CreditUSDPerCNY: storage.DefaultCreditUSDPerCNY,
		}, modelNames: make(map[string]struct{}), groups: make(map[string]struct{}), newAPIChannels: make(map[string]struct{})}
	}

	matched := make([]bool, len(profits))
	for profitIndex := range profits {
		profit := profits[profitIndex]
		if profit.MappingStatus != "mapped" || profit.UpstreamChannelID == nil || *profit.UpstreamChannelID == 0 {
			continue
		}
		for usageIndex, costBucket := range usage {
			overlapStart := laterTime(profit.BucketStart, costBucket.BucketStart)
			overlapEnd := earlierTime(profit.BucketEnd, costBucket.BucketEnd)
			if costBucket.ChannelID != *profit.UpstreamChannelID || !overlapEnd.After(overlapStart) || !profit.BucketEnd.After(profit.BucketStart) {
				continue
			}
			fraction := overlapEnd.Sub(overlapStart).Seconds() / profit.BucketEnd.Sub(profit.BucketStart).Seconds()
			if fraction <= 0 {
				continue
			}
			if fraction > 1 {
				fraction = 1
			}
			row := aggregates[usageIndex]
			matched[profitIndex] = true
			row.saleCount++
			row.item.ChargedUSD += profit.ChargedUSD * fraction
			row.item.SaleCNY += profit.SaleCNY * fraction
			row.item.EventCount++
			row.item.ProfitCNY += profit.SaleCNY * fraction
			if row.item.NewAPIChannelID == 0 {
				row.item.NewAPIChannelID = profit.NewAPIChannelID
			}
			if name := strings.TrimSpace(profit.NewAPIChannelName); name != "" {
				row.newAPIChannels[name] = struct{}{}
			}
			if row.item.UpstreamChannelID == nil {
				id := *profit.UpstreamChannelID
				row.item.UpstreamChannelID = &id
			}
			if row.item.MappingStatus == "unmapped" {
				row.item.MappingStatus = "mapped"
			}
			if profit.CreditUSDPerCNY > 0 {
				row.item.CreditUSDPerCNY = profit.CreditUSDPerCNY
			}
			row.groups[profit.Group] = struct{}{}
			row.modelNames[profit.ModelName] = struct{}{}
		}
	}

	items := make([]profitSaleDetail, 0, len(usage)+len(profits))
	for _, row := range aggregates {
		row.item.Group = joinDetailValues(row.groups)
		row.item.ModelName = joinDetailValues(row.modelNames)
		row.item.NewAPIChannelName = joinDetailValues(row.newAPIChannels)
		row.item.ProfitCNY = row.item.SaleCNY - row.item.CostCNY
		items = append(items, row.item)
	}
	for profitIndex, profit := range profits {
		if matched[profitIndex] {
			continue
		}
		id := profit.NewAPIChannelID
		name := profit.NewAPIChannelName
		if name == "" {
			name = channelNames[uint(id)]
		}
		items = append(items, profitSaleDetail{
			Source: "new-api-bucket", CreatedAt: profit.BucketStart,
			BucketStart: profit.BucketStart, BucketEnd: profit.BucketEnd,
			ChannelID: id, ChannelName: name, NewAPIChannelID: id, NewAPIChannelName: name,
			UpstreamChannelID: profit.UpstreamChannelID, MappingStatus: profit.MappingStatus,
			Group: profit.Group, ModelName: profit.ModelName, NormalizationStatus: profit.NormalizationStatus,
			ChargedUSD:      profit.ChargedUSD,
			CreditUSDPerCNY: profit.CreditUSDPerCNY, SaleCNY: profit.SaleCNY,
			CostUSD: profit.CostUSD, CostCNY: profit.CostCNY, ProfitCNY: profit.ProfitCNY,
			EventCount: 1,
		})
	}
	sortProfitSaleDetails(items)
	return items
}

func buildProfitSaleDetailsPage(profits []storage.ProfitBucket, usage []storage.UsageBucket, costRate float64, channelNames map[uint]string, page, pageSize int) []profitSaleDetail {
	return paginateProfitDetails(buildProfitSaleDetails(profits, usage, costRate, channelNames), page, pageSize)
}

func buildUnmappedProfitSaleDetails(profits []storage.ProfitBucket, channelNames map[uint]string) []profitSaleDetail {
	items := make([]profitSaleDetail, 0)
	for _, profit := range profits {
		if profit.MappingStatus == "mapped" {
			continue
		}
		id := profit.NewAPIChannelID
		name := profit.NewAPIChannelName
		if name == "" {
			name = channelNames[uint(id)]
		}
		items = append(items, profitSaleDetail{
			Source: "new-api-bucket", CreatedAt: profit.BucketStart,
			BucketStart: profit.BucketStart, BucketEnd: profit.BucketEnd,
			ChannelID: id, ChannelName: name, NewAPIChannelID: id, NewAPIChannelName: name,
			UpstreamChannelID: profit.UpstreamChannelID, MappingStatus: profit.MappingStatus,
			Group: profit.Group, ModelName: profit.ModelName, NormalizationStatus: profit.NormalizationStatus,
			ChargedUSD:      profit.ChargedUSD,
			CreditUSDPerCNY: profit.CreditUSDPerCNY, SaleCNY: profit.SaleCNY,
			CostUSD: profit.CostUSD, CostCNY: profit.CostCNY, ProfitCNY: profit.ProfitCNY,
			EventCount: 1,
		})
	}
	sortProfitSaleDetails(items)
	return items
}

func aggregateProfitEventDetails(events []storage.NewAPIBillingEvent, channelNames map[uint]string) []profitSaleDetail {
	type key struct {
		start, end       int64
		channelID        int
		group, modelName string
		status           string
	}
	byKey := make(map[key]*profitSaleDetail)
	for _, event := range events {
		bucketKey := key{event.BucketStart.Unix(), event.BucketEnd.Unix(), event.ChannelID, event.Group, event.ModelName, event.MappingStatus}
		row := byKey[bucketKey]
		if row == nil {
			name := event.ChannelName
			if name == "" {
				name = channelNames[uint(event.ChannelID)]
			}
			row = &profitSaleDetail{
				Source: "new-api-bucket", CreatedAt: event.BucketStart,
				BucketStart: event.BucketStart, BucketEnd: event.BucketEnd,
				ChannelID: event.ChannelID, ChannelName: name,
				NewAPIChannelID: event.ChannelID, NewAPIChannelName: name,
				UpstreamChannelID: event.UpstreamChannelID, MappingStatus: event.MappingStatus,
				Group: event.Group, ModelName: event.ModelName,
				NormalizationStatus: event.NormalizationStatus,
				CreditUSDPerCNY:     event.CreditUSDPerCNY,
			}
			byKey[bucketKey] = row
		}
		row.ChargedUSD += event.ChargedUSD
		row.SaleCNY += event.SaleCNY
		row.EventCount++
	}
	items := make([]profitSaleDetail, 0, len(byKey))
	for _, row := range byKey {
		items = append(items, *row)
	}
	sortProfitSaleDetails(items)
	return items
}

func sortProfitSaleDetails(items []profitSaleDetail) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].BucketStart.Equal(items[j].BucketStart) {
			return items[i].BucketStart.After(items[j].BucketStart)
		}
		if !items[i].BucketEnd.Equal(items[j].BucketEnd) {
			return items[i].BucketEnd.After(items[j].BucketEnd)
		}
		if items[i].ChannelID != items[j].ChannelID {
			return items[i].ChannelID > items[j].ChannelID
		}
		return items[i].NewAPIChannelID > items[j].NewAPIChannelID
	})
}

func usageAmountUSDForDetail(amount float64, currency string, costRate float64) float64 {
	value, ok := storage.UsageAmountUSD(amount, currency, costRate)
	if !ok {
		return 0
	}
	return value
}

func joinDetailValues(values map[string]struct{}) string {
	if len(values) == 0 {
		return ""
	}
	items := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			items = append(items, value)
		}
	}
	sort.Strings(items)
	return strings.Join(items, ", ")
}

func laterTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func earlierTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func formatProfitSaleEvents(events []storage.NewAPIBillingEvent, channelNames map[uint]string) []profitSaleDetail {
	items := make([]profitSaleDetail, 0, len(events))
	for _, event := range events {
		sourceID := event.SourceLogID
		items = append(items, profitSaleDetail{
			Source: "new-api-log", SourceLogID: &sourceID, CreatedAt: event.CreatedAt,
			BucketStart: event.BucketStart, BucketEnd: event.BucketEnd, ChannelID: event.ChannelID,
			ChannelName: channelNames[uint(event.ChannelID)], UpstreamChannelID: event.UpstreamChannelID,
			MappingStatus: event.MappingStatus, EventType: event.EventType, Group: event.Group,
			ModelName: event.ModelName, EffectiveGroupRatio: event.EffectiveGroupRatio,
			RatioSource: event.RatioSource, NormalizationStatus: event.NormalizationStatus,
			Quota: event.Quota, ChargedUSD: event.ChargedUSD, NormalizedUSD: event.NormalizedUSD,
			CreditUSDPerCNY: event.CreditUSDPerCNY, SaleCNY: event.SaleCNY, UserID: event.UserID,
			TokenName: event.TokenName, RequestID: event.RequestID, UpstreamRequestID: event.UpstreamRequestID,
		})
	}
	return items
}

func paginateProfitDetails[T any](items []T, page, pageSize int) []T {
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []T{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}
