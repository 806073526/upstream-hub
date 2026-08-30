package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/businessclock"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

var usageTrendNow = time.Now
var profitTrendNow = time.Now

// registerDashboard 提供首页所需聚合视图。
func registerDashboard(g *gin.RouterGroup, d *Deps) {
	g.GET("/dashboard/summary", func(c *gin.Context) { dashboardSummary(c, d) })
	g.GET("/dashboard/balance-trend", func(c *gin.Context) { dashboardBalanceTrend(c, d) })
	g.GET("/dashboard/usage-trend", func(c *gin.Context) { dashboardUsageTrend(c, d) })
	g.GET("/dashboard/profit-trend", func(c *gin.Context) { dashboardProfitTrend(c, d) })
	g.GET("/dashboard/profit-details", func(c *gin.Context) { dashboardProfitDetails(c, d) })
}

type dashboardUsageChannel struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
}

type dashboardUsagePoint struct {
	StartAt           time.Time        `json:"start_at"`
	EndAt             time.Time        `json:"end_at"`
	TotalAmount       *float64         `json:"total_amount"`
	ChannelAmounts    map[uint]float64 `json:"channel_amounts"`
	Quality           string           `json:"quality"`
	Complete          bool             `json:"complete"`
	MissingChannelIDs []uint           `json:"missing_channel_ids"`
}

type dashboardLowest struct {
	ChannelID uint     `json:"channel_id"`
	Name      string   `json:"name"`
	Balance   *float64 `json:"balance"`
}

type dashboardChannelStat struct {
	ID             uint     `json:"id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	MonitorEnabled bool     `json:"monitor_enabled"`
	LastBalance    *float64 `json:"last_balance,omitempty"`
	LastError      string   `json:"last_error,omitempty"`
}

func sumChannelUsage(channels []storage.Channel) (float64, bool) {
	var total float64
	hasData := false
	for _, channel := range channels {
		if channel.LastUsageTotal == nil {
			continue
		}
		total += *channel.LastUsageTotal
		hasData = true
	}
	return total, hasData
}

func dashboardSummary(c *gin.Context, d *Deps) {
	channels, err := d.Channels.List()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}

	stats := make([]dashboardChannelStat, 0, len(channels))
	var totalBalance float64
	totalUsage, _ := sumChannelUsage(channels)
	var lowest *dashboardLowest
	var activeCount, failedCount int

	for _, ch := range channels {
		stat := dashboardChannelStat{
			ID:             ch.ID,
			Name:           ch.Name,
			Type:           string(ch.Type),
			MonitorEnabled: ch.MonitorEnabled,
			LastBalance:    ch.LastBalance,
			LastError:      ch.LastError,
		}
		stats = append(stats, stat)
		if ch.LastError != "" {
			failedCount++
		} else if ch.MonitorEnabled {
			activeCount++
		}
		if ch.LastBalance != nil {
			totalBalance += *ch.LastBalance
			if lowest == nil || (lowest.Balance == nil) || (*ch.LastBalance < *lowest.Balance) {
				bal := *ch.LastBalance
				lowest = &dashboardLowest{ChannelID: ch.ID, Name: ch.Name, Balance: &bal}
			}
		}
	}

	recentChanges, err := d.Rates.ListChanges(0, 10)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	recentNotifs, err := d.Notifies.ListLogs(10)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	var profit any
	if d.Profit != nil {
		location, locationErr := businessclock.Location()
		if locationErr != nil {
			fail(c, http.StatusInternalServerError, locationErr)
			return
		}
		now := profitTrendNow().UTC()
		localNow := now.In(location)
		dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).UTC()
		rows, rowsErr := d.Profit.ListBuckets(dayStart, now)
		if rowsErr != nil {
			fail(c, http.StatusInternalServerError, rowsErr)
			return
		}
		var usage []storage.UsageBucket
		summary := storage.SummarizeProfit(rows)
		if d.Usage != nil {
			usageRows, usageErr := d.Usage.ListBuckets(dayStart, now, 3600)
			if usageErr != nil {
				fail(c, http.StatusInternalServerError, usageErr)
				return
			}
			if usageRows == nil {
				usageRows = []storage.UsageBucket{}
			}
			usage = usageRows
			costRate := storage.DefaultUpstreamCostCNYPerUSD
			if d.Billing != nil {
				costRate = d.Billing.CostRate()
			}
			summary = storage.SummarizeProfitWithUsage(rows, usage, dayStart, now, 3600, costRate)
		}
		if d.PersonalUsage != nil {
			personal, personalComplete, personalErr := listPersonalUsageBucketsWithStatus(d.PersonalUsage, dayStart, now, 0)
			if personalErr != nil {
				fail(c, http.StatusInternalServerError, personalErr)
				return
			}
			costRate := storage.DefaultUpstreamCostCNYPerUSD
			if d.Billing != nil {
				costRate = d.Billing.CostRate()
			}
			summary = storage.SummarizeProfitWithPersonalUsage(rows, usage, personal, dayStart, now, 3600, costRate, personalComplete)
		}
		profit = gin.H{"start_at": dayStart, "end_at": now, "currency": "CNY", "summary": summary}
	}

	response := gin.H{
		"data": gin.H{
			"total_channels":           len(channels),
			"active_channels":          activeCount,
			"failed_channels":          failedCount,
			"total_balance":            totalBalance,
			"total_usage":              totalUsage,
			"lowest_balance":           lowest,
			"channels":                 stats,
			"recent_rate_changes":      recentChanges,
			"recent_notification_logs": recentNotifs,
		},
	}
	if profit != nil {
		response["data"].(gin.H)["profit"] = profit
	}
	c.JSON(http.StatusOK, response)
}

func dashboardBalanceTrend(c *gin.Context, d *Deps) {
	channelIDs, filtered, err := parseBalanceChannelIDs(c.GetQuery("channel_ids"))
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if c.DefaultQuery("bucket", "day") == "hour" {
		hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
		if hours <= 0 {
			hours = 24
		}
		var trend []storage.DailyAggregate
		if filtered {
			trend, err = d.Rates.AggregateBalanceTrendHourlyForChannels(hours, channelIDs)
		} else {
			trend, err = d.Rates.AggregateBalanceTrendHourly(hours)
		}
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": trend})
		return
	}

	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 {
		days = 7
	}
	var trend []storage.DailyAggregate
	if filtered {
		trend, err = d.Rates.AggregateBalanceTrendForChannels(days, channelIDs)
	} else {
		trend, err = d.Rates.AggregateBalanceTrend(days)
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": trend})
}

func parseBalanceChannelIDs(raw string, provided bool) ([]uint, bool, error) {
	if !provided {
		return nil, false, nil
	}
	if strings.TrimSpace(raw) == "" {
		return []uint{}, true, nil
	}

	ids := make([]uint, 0)
	seen := make(map[uint]struct{})
	for _, value := range strings.Split(raw, ",") {
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed == 0 {
			return nil, false, fmt.Errorf("invalid channel_ids value %q", value)
		}
		id := uint(parsed)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true, nil
}

func dashboardUsageTrend(c *gin.Context, d *Deps) {
	location, err := businessclock.Location()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	rangeID := c.DefaultQuery("range", "24h")
	spec, err := storage.ResolveUsageTrendSpec(rangeID, usageTrendNow(), location)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	channels, err := d.Channels.List()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	channelIDs := make([]uint, 0, len(channels))
	channelMeta := make([]dashboardUsageChannel, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
		currency := channel.UsageCurrency
		if currency == "" {
			currency = "USD"
		}
		channelMeta = append(channelMeta, dashboardUsageChannel{
			ID: channel.ID, Name: channel.Name, Type: string(channel.Type), Currency: currency,
		})
	}
	buckets, err := d.Usage.ListBuckets(spec.StartAt, spec.EndAt, spec.SourceResolutionSeconds)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	trend := storage.BuildUsageTrend(buckets, spec, location, channelIDs)
	points := make([]dashboardUsagePoint, 0, len(trend))
	channelTotals := make(map[uint]float64)
	var rangeTotal float64
	hasRangeData := false
	rangeComplete := len(trend) > 0
	for _, point := range trend {
		var totalAmount *float64
		if point.HasData {
			amount := point.TotalAmount
			totalAmount = &amount
			rangeTotal += point.TotalAmount
			hasRangeData = true
			for channelID, amount := range point.ChannelAmounts {
				channelTotals[channelID] += amount
			}
		}
		if !point.Complete {
			rangeComplete = false
		}
		points = append(points, dashboardUsagePoint{
			StartAt: point.StartAt, EndAt: point.EndAt, TotalAmount: totalAmount,
			ChannelAmounts: point.ChannelAmounts, Quality: point.Quality, Complete: point.Complete,
			MissingChannelIDs: point.MissingChannelIDs,
		})
	}
	var rangeTotalAmount *float64
	if hasRangeData {
		rangeTotalAmount = &rangeTotal
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"range": rangeID, "start_at": spec.StartAt, "end_at": spec.EndAt,
		"source_resolution_seconds": spec.SourceResolutionSeconds,
		"output_resolution_seconds": spec.OutputResolutionSeconds,
		"currency":                  "USD", "channels": channelMeta, "points": points,
		"range_total_amount": rangeTotalAmount, "channel_totals": channelTotals,
		"complete": rangeComplete,
	}})
}

func dashboardProfitTrend(c *gin.Context, d *Deps) {
	location, err := businessclock.Location()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	rangeID := c.DefaultQuery("range", "24h")
	spec, err := storage.ResolveProfitTrendSpec(rangeID, profitTrendNow(), location)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if d.Profit == nil {
		fail(c, http.StatusServiceUnavailable, fmt.Errorf("profit repository is not configured"))
		return
	}
	rows, err := d.Profit.ListBuckets(spec.StartAt, spec.EndAt)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	var usage []storage.UsageBucket
	summary := storage.SummarizeProfit(rows)
	points := storage.BuildProfitTrend(rows, spec, location)
	if d.Usage != nil {
		usageRows, usageErr := d.Usage.ListBuckets(spec.StartAt, spec.EndAt, spec.UsageResolutionSeconds)
		if usageErr != nil {
			fail(c, http.StatusInternalServerError, usageErr)
			return
		}
		if usageRows == nil {
			usageRows = []storage.UsageBucket{}
		}
		usage = usageRows
		costRate := storage.DefaultUpstreamCostCNYPerUSD
		if d.Billing != nil {
			costRate = d.Billing.CostRate()
		}
		summary = storage.SummarizeProfitWithUsage(rows, usage, spec.StartAt, spec.EndAt, spec.UsageResolutionSeconds, costRate)
		points = storage.BuildProfitTrendWithUsage(rows, usage, spec, location, costRate)
	}
	complete := summary.Complete
	if d.PersonalUsage != nil {
		personal, personalComplete, personalErr := listPersonalUsageBucketsWithStatus(d.PersonalUsage, spec.StartAt, spec.EndAt, 0)
		if personalErr != nil {
			fail(c, http.StatusInternalServerError, personalErr)
			return
		}
		costRate := storage.DefaultUpstreamCostCNYPerUSD
		if d.Billing != nil {
			costRate = d.Billing.CostRate()
		}
		summary = storage.SummarizeProfitWithPersonalUsage(rows, usage, personal, spec.StartAt, spec.EndAt, spec.UsageResolutionSeconds, costRate, personalComplete)
		points = storage.BuildProfitTrendWithPersonalUsageStatus(rows, usage, personal, spec, location, costRate, personalComplete)
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"range": rangeID, "start_at": spec.StartAt, "end_at": spec.EndAt,
		"output_resolution_seconds": spec.OutputResolutionSeconds,
		"currency":                  "CNY", "points": points, "summary": summary,
		"complete": complete,
	}})
}
