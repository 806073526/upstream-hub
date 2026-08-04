// Package monitor 周期性扫描渠道，采集余额 / 倍率并写入快照、变化日志和通知。
package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/worryzyy/upstream-hub/internal/businessclock"
	"github.com/worryzyy/upstream-hub/internal/channel"
	"github.com/worryzyy/upstream-hub/internal/connector"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/progress"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

// Service 监控扫描服务。
type Service struct {
	channels    *storage.Channels
	rates       *storage.Rates
	usage       *storage.Usage
	monitorLogs *storage.MonitorLogs
	channelSvc  *channel.Service
	dispatcher  *notify.Dispatcher
	log         *slog.Logger
}

type rateSnapshotStore interface {
	Upsert(snapshot *storage.RateSnapshot) (*storage.RateSnapshot, error)
	AppendChange(log *storage.RateChangeLog) error
	DeleteExcept(channelID uint, modelNames []string) error
}

type RateScan struct {
	Channel storage.Channel
	Results []connector.RateResult
}

func NewService(
	channels *storage.Channels,
	rates *storage.Rates,
	usage *storage.Usage,
	monitorLogs *storage.MonitorLogs,
	channelSvc *channel.Service,
	dispatcher *notify.Dispatcher,
	log *slog.Logger,
) *Service {
	return &Service{
		channels:    channels,
		rates:       rates,
		usage:       usage,
		monitorLogs: monitorLogs,
		channelSvc:  channelSvc,
		dispatcher:  dispatcher,
		log:         log,
	}
}

// ScanAllUsage 扫描所有启用监控的渠道用量。
func (s *Service) ScanAllUsage(ctx context.Context) {
	list, err := s.channels.ListMonitorEnabled()
	if err != nil {
		s.log.Error("list channels", "err", err)
		return
	}
	for i := range list {
		c := list[i]
		if err := s.RefreshUsage(ctx, &c); err != nil {
			s.log.Warn("refresh usage failed", "channel", c.Name, "err", err)
		}
	}
}

// RefreshUsage 拉取渠道累计、今日和阶段用量并持久化。
func (s *Service) RefreshUsage(ctx context.Context, c *storage.Channel) error {
	resolved, conn, session, err := s.prepare(ctx, c)
	if err != nil {
		s.notifyError(ctx, c, storage.EventLoginFailed, "登录失败", err)
		return err
	}

	progress.Start(ctx, progress.StageUsage, "拉取用量…")
	started := time.Now()
	latestHour, err := s.usage.LatestBucketStart(c.ID, 3600)
	if err != nil {
		finished := time.Now()
		_ = s.monitorLogs.Append(&storage.MonitorLog{
			ChannelID: c.ID, Job: storage.MonitorJobUsage, Success: false,
			ErrorMessage: err.Error(), StartedAt: started, FinishedAt: finished,
		})
		progress.Fail(ctx, progress.StageUsage, err.Error())
		return err
	}
	result, err := conn.GetUsage(ctx, resolved, session, connector.UsageQuery{
		Now:          started,
		Timezone:     businessclock.Timezone,
		HistorySince: usageHistorySince(started, latestHour),
	})
	finished := time.Now()
	_ = s.monitorLogs.Append(&storage.MonitorLog{
		ChannelID: c.ID, Job: storage.MonitorJobUsage, Success: err == nil,
		ErrorMessage: errString(err), StartedAt: started, FinishedAt: finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageUsage, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "用量采集失败", err)
		return err
	}
	if err := s.usage.Save(usageSampleFromResult(c, result)); err != nil {
		progress.Fail(ctx, progress.StageUsage, err.Error())
		return err
	}
	progress.OK(ctx, progress.StageUsage, fmt.Sprintf("累计用量 %.4f，今日 %.4f", valueOrZero(result.TotalAmount), valueOrZero(result.TodayAmount)), map[string]any{
		"total": result.TotalAmount, "today": result.TodayAmount, "currency": result.Currency,
	})
	return nil
}

func usageHistorySince(now time.Time, latestHour *time.Time) time.Time {
	if latestHour != nil {
		return *latestHour
	}
	return now.Add(-30 * 24 * time.Hour)
}

func usageSampleFromResult(channel *storage.Channel, result *connector.UsageResult) storage.UsageSample {
	sample := storage.UsageSample{
		ChannelID: channel.ID, TotalAmount: result.TotalAmount, TodayAmount: result.TodayAmount,
		Currency: result.Currency, ObservedAt: result.ObservedAt,
		Buckets: make([]storage.UsageBucket, 0, len(result.Buckets)+1),
	}
	for _, bucket := range result.Buckets {
		sample.Buckets = append(sample.Buckets, storage.UsageBucket{
			ChannelID: channel.ID, BucketStart: bucket.StartAt, BucketEnd: bucket.EndAt,
			ResolutionSeconds: bucket.ResolutionSeconds, Amount: bucket.Amount, Currency: bucket.Currency,
			Source: bucket.Source, Quality: string(bucket.Quality), Complete: bucket.Complete, CollectedAt: result.ObservedAt,
		})
	}
	if channel.Type == storage.ChannelTypeSub2API && channel.LastUsageTotal != nil && channel.LastUsageAt != nil && result.TotalAmount != nil {
		interval := result.ObservedAt.Sub(*channel.LastUsageAt)
		delta := *result.TotalAmount - *channel.LastUsageTotal
		if interval >= 4*time.Minute && interval <= 10*time.Minute && delta >= 0 {
			endAt := result.ObservedAt.UTC().Truncate(5 * time.Minute)
			sample.Buckets = append(sample.Buckets, storage.UsageBucket{
				ChannelID: channel.ID, BucketStart: endAt.Add(-5 * time.Minute), BucketEnd: endAt,
				ResolutionSeconds: 300, Amount: math.Round(delta*1e8) / 1e8, Currency: result.Currency,
				Source: "sub2api_counter_delta", Quality: string(connector.UsageQualityObserved), Complete: true, CollectedAt: result.ObservedAt,
			})
		}
	}
	return sample
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// ScanAllBalances 扫描所有启用监控的渠道余额。单个失败不影响其他。
func (s *Service) ScanAllBalances(ctx context.Context) {
	list, err := s.channels.ListMonitorEnabled()
	if err != nil {
		s.log.Error("list channels", "err", err)
		return
	}
	for i := range list {
		c := list[i]
		if err := s.RefreshBalance(ctx, &c); err != nil {
			s.log.Warn("refresh balance failed", "channel", c.Name, "err", err)
		}
	}
}

// ScanAllRates 扫描所有启用监控的渠道倍率。
func (s *Service) ScanAllRates(ctx context.Context) {
	_ = s.ScanAllRatesWithResults(ctx)
}

func (s *Service) ScanAllRatesWithResults(ctx context.Context) []RateScan {
	list, err := s.channels.ListMonitorEnabled()
	if err != nil {
		s.log.Error("list channels", "err", err)
		return nil
	}
	scans := make([]RateScan, 0, len(list))
	for i := range list {
		c := list[i]
		results, err := s.RefreshRatesWithResults(ctx, &c)
		if err != nil {
			s.log.Warn("refresh rates failed", "channel", c.Name, "err", err)
			continue
		}
		scans = append(scans, RateScan{Channel: c, Results: results})
	}
	return scans
}

// RefreshBalance 单个渠道余额刷新，可被 API 手动触发。
func (s *Service) RefreshBalance(ctx context.Context, c *storage.Channel) error {
	resolved, conn, session, err := s.prepare(ctx, c)
	if err != nil {
		s.notifyError(ctx, c, storage.EventLoginFailed, "登录失败", err)
		return err
	}

	progress.Start(ctx, progress.StageBalance, "拉取余额…")
	started := time.Now()
	res, err := conn.GetBalance(ctx, resolved, session)
	finished := time.Now()
	_ = s.monitorLogs.Append(&storage.MonitorLog{
		ChannelID:    c.ID,
		Job:          storage.MonitorJobBalance,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageBalance, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "余额采集失败", err)
		return err
	}

	sampledAt := res.SampledAt
	if sampledAt.IsZero() {
		sampledAt = time.Now()
	}
	if err := s.channels.UpdateBalance(c.ID, res.Balance, &sampledAt, ""); err != nil {
		return err
	}
	_ = s.rates.AppendBalance(&storage.BalanceSnapshot{
		ChannelID: c.ID,
		Balance:   res.Balance,
		SampledAt: sampledAt,
	})
	progress.OK(ctx, progress.StageBalance, fmt.Sprintf("当前余额 %.4f", res.Balance),
		map[string]any{"balance": res.Balance})

	if c.BalanceThreshold > 0 && res.Balance < c.BalanceThreshold {
		body := fmt.Sprintf("当前余额: %.4f，阈值: %.4f", res.Balance, c.BalanceThreshold)
		_ = s.dispatcher.Dispatch(ctx, notify.Message{
			Event:     storage.EventBalanceLow,
			ChannelID: c.ID,
			Subject:   fmt.Sprintf("[upstream-hub] %s 余额低于阈值", c.Name),
			Body:      body,
		})
	}
	return nil
}

// RefreshRates 单个渠道倍率刷新，可被 API 手动触发。
func (s *Service) RefreshRates(ctx context.Context, c *storage.Channel) error {
	_, err := s.RefreshRatesWithResults(ctx, c)
	return err
}

func (s *Service) RefreshRatesWithResults(ctx context.Context, c *storage.Channel) ([]connector.RateResult, error) {
	resolved, conn, session, err := s.prepare(ctx, c)
	if err != nil {
		s.notifyError(ctx, c, storage.EventLoginFailed, "登录失败", err)
		return nil, err
	}

	progress.Start(ctx, progress.StageRates, "拉取分组倍率…")
	started := time.Now()
	results, err := conn.GetRates(ctx, resolved, session)
	finished := time.Now()
	_ = s.monitorLogs.Append(&storage.MonitorLog{
		ChannelID:    c.ID,
		Job:          storage.MonitorJobRates,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageRates, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "倍率采集失败", err)
		return nil, err
	}

	now := time.Now()
	changes, err := reconcileRates(s.rates, c.ID, results, now)
	if err != nil {
		progress.Fail(ctx, progress.StageRates, err.Error())
		return nil, err
	}
	// 一次扫描的所有变化打包推送：去抖策略（合并 / 涨跌幅过滤）由 Dispatcher.Policy 决定。
	if len(changes) > 0 {
		_ = s.dispatcher.DispatchRateBatch(ctx, c, changes)
	}
	progress.OK(ctx, progress.StageRates, fmt.Sprintf("拉到 %d 个分组", len(results)),
		map[string]any{"count": len(results)})
	return results, nil
}

func reconcileRates(store rateSnapshotStore, channelID uint, results []connector.RateResult, now time.Time) ([]notify.RateChange, error) {
	changes := make([]notify.RateChange, 0, len(results))
	currentNames := make([]string, 0, len(results))
	seenNames := make(map[string]struct{}, len(results))
	for _, r := range results {
		if _, seen := seenNames[r.ModelName]; !seen {
			seenNames[r.ModelName] = struct{}{}
			currentNames = append(currentNames, r.ModelName)
		}
		prev, err := store.Upsert(&storage.RateSnapshot{
			ChannelID:       channelID,
			ModelName:       r.ModelName,
			Description:     r.Description,
			Ratio:           r.Ratio,
			CompletionRatio: r.CompletionRatio,
			LastSeenAt:      now,
		})
		if err != nil {
			return nil, fmt.Errorf("upsert rate %q: %w", r.ModelName, err)
		}
		if prev == nil || prev.Ratio == r.Ratio && prev.CompletionRatio == r.CompletionRatio {
			continue
		}
		oldRatio := prev.Ratio
		oldComp := prev.CompletionRatio
		if err := store.AppendChange(&storage.RateChangeLog{
			ChannelID:          channelID,
			ModelName:          r.ModelName,
			OldRatio:           &oldRatio,
			NewRatio:           r.Ratio,
			OldCompletionRatio: &oldComp,
			NewCompletionRatio: r.CompletionRatio,
			ChangedAt:          now,
		}); err != nil {
			return nil, fmt.Errorf("append rate change %q: %w", r.ModelName, err)
		}
		changes = append(changes, notify.RateChange{
			GroupName: r.ModelName,
			OldRatio:  oldRatio,
			NewRatio:  r.Ratio,
			OldComp:   oldComp,
			NewComp:   r.CompletionRatio,
			ChangedAt: now,
		})
	}
	if err := store.DeleteExcept(channelID, currentNames); err != nil {
		return nil, fmt.Errorf("remove unlinked rate snapshots: %w", err)
	}
	return changes, nil
}

func (s *Service) prepare(ctx context.Context, c *storage.Channel) (*connector.Channel, connector.Connector, *connector.AuthSession, error) {
	resolved, err := s.channelSvc.Resolve(ctx, c)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return nil, nil, nil, err
	}
	session, err := s.channelSvc.EnsureSession(ctx, c, resolved, conn)
	if err != nil {
		return nil, nil, nil, err
	}
	return resolved, conn, session, nil
}

func (s *Service) notifyError(ctx context.Context, c *storage.Channel, event storage.NotificationEvent, subject string, err error) {
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event:     event,
		ChannelID: c.ID,
		Subject:   fmt.Sprintf("[upstream-hub] %s %s", c.Name, subject),
		Body:      err.Error(),
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
