// Package scheduler 用 robfig/cron 触发周期性扫描。
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/worryzyy/upstream-hub/internal/billing"
	"github.com/worryzyy/upstream-hub/internal/config"
	newapiintegration "github.com/worryzyy/upstream-hub/internal/integration/newapi"
	"github.com/worryzyy/upstream-hub/internal/monitor"
	"github.com/worryzyy/upstream-hub/internal/priority"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

type Scheduler struct {
	cfg         config.SchedulerConfig
	log         *slog.Logger
	cron        *cron.Cron
	monitor     *monitor.Service
	monLogs     *storage.MonitorLogs
	rates       *storage.Rates
	usage       *storage.Usage
	notifies    *storage.Notifications
	newAPI      *newapiintegration.Client
	newAPICfg   config.NewAPIIntegrationConfig
	billing     *billing.Service
	billingCron string
	scanLocks   sync.Map // map[string]*sync.Mutex; each scheduled scan type has its own lock.
}

func (s *Scheduler) SetNewAPIIntegration(client *newapiintegration.Client, cfg config.NewAPIIntegrationConfig) {
	s.newAPI = client
	s.newAPICfg = cfg
}

func (s *Scheduler) SetBillingService(service *billing.Service, cronSpec string) {
	s.billing = service
	s.billingCron = cronSpec
}

func New(
	cfg config.SchedulerConfig,
	m *monitor.Service,
	monLogs *storage.MonitorLogs,
	rates *storage.Rates,
	usage *storage.Usage,
	notifies *storage.Notifications,
	log *slog.Logger,
) *Scheduler {
	return &Scheduler{
		cfg:      cfg,
		log:      log,
		cron:     cron.New(cron.WithSeconds()),
		monitor:  m,
		monLogs:  monLogs,
		rates:    rates,
		usage:    usage,
		notifies: notifies,
	}
}

func (s *Scheduler) Start() error {
	if s.cfg.BalanceCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.BalanceCron, s.runBalance); err != nil {
			return err
		}
	}
	if s.cfg.RateCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.RateCron, s.runRates); err != nil {
			return err
		}
	}
	if s.cfg.UsageCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.UsageCron, s.runUsage); err != nil {
			return err
		}
	}
	if s.billing != nil && s.billing.Enabled() && s.billingCron != "" {
		if _, err := s.cron.AddFunc(s.billingCron, s.runBilling); err != nil {
			return err
		}
	}
	if s.cfg.Retention.Cron != "" && s.hasRetention() {
		if _, err := s.cron.AddFunc(s.cfg.Retention.Cron, s.runRetention); err != nil {
			return err
		}
	}
	s.cron.Start()
	s.log.Info("scheduler started",
		"balanceCron", s.cfg.BalanceCron,
		"rateCron", s.cfg.RateCron,
		"usageCron", s.cfg.UsageCron,
		"billingCron", s.billingCron,
		"retentionCron", s.cfg.Retention.Cron,
		"concurrency", s.cfg.Concurrency,
	)
	if s.newAPI != nil {
		go s.runRates()
	}
	return nil
}

func (s *Scheduler) Stop() {
	if s.cron != nil {
		<-s.cron.Stop().Done()
	}
}

func (s *Scheduler) runBalance() {
	s.runScan("balance", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s.monitor.ScanAllBalances(ctx)
	})
}

func (s *Scheduler) runRates() {
	s.runScan("rates", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		scans := s.monitor.ScanAllRatesWithResults(ctx)
		if s.newAPI == nil || len(scans) == 0 {
			return
		}
		identities, err := s.newAPI.FetchIdentities(ctx)
		if err != nil {
			s.log.Warn("new-api identities sync failed", "err", err)
			return
		}
		converted := make([]newapiintegration.Scan, 0, len(scans))
		for _, scan := range scans {
			converted = append(converted, newapiintegration.Scan{
				ChannelID: scan.Channel.ID,
				SiteURL:   scan.Channel.SiteURL,
				Balance:   scan.Channel.LastBalance,
				BalanceAt: derefTime(scan.Channel.LastBalanceAt),
				Results:   scan.Results,
			})
		}
		metrics := newapiintegration.BuildMatchedMetrics(identities, converted, time.Now())
		if s.billing != nil {
			if err := s.billing.SaveMappings(identities, metrics, time.Now().UTC()); err != nil {
				s.log.Warn("billing mapping snapshot failed", "err", err)
			}
		}
		if err := s.newAPI.PushMetrics(ctx, metrics); err != nil {
			s.log.Warn("new-api metrics sync failed", "err", err)
		}
		if !s.newAPICfg.AutoPriority {
			return
		}
		updates := s.buildPriorityUpdates(identities, metrics)
		if err := s.newAPI.ApplyPriorities(ctx, updates); err != nil {
			s.log.Warn("new-api priority sync failed", "err", err)
		}
	})
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func (s *Scheduler) buildPriorityUpdates(identities []newapiintegration.Identity, metrics []newapiintegration.Metric) []priority.PriorityUpdate {
	excluded := make(map[uint]struct{}, len(s.newAPICfg.ExcludedChannelIDs))
	for _, id := range s.newAPICfg.ExcludedChannelIDs {
		excluded[id] = struct{}{}
	}
	current := make(map[int]int64, len(identities))
	for _, identity := range identities {
		current[identity.ChannelID] = identity.Priority
	}
	byChannel := make(map[int]priority.Candidate)
	for _, metric := range metrics {
		candidate, exists := byChannel[metric.ChannelID]
		observedAt := time.Unix(metric.RatioAt, 0)
		if !exists || metric.Ratio < candidate.Ratio {
			candidate = priority.Candidate{
				ChannelID: metric.ChannelID, Ratio: metric.Ratio, ObservedAt: observedAt,
				CurrentPriority: current[metric.ChannelID],
			}
		}
		if _, ok := excluded[uint(metric.ChannelID)]; ok {
			candidate.Excluded = true
		}
		byChannel[metric.ChannelID] = candidate
	}
	candidates := make([]priority.Candidate, 0, len(byChannel))
	for _, candidate := range byChannel {
		candidates = append(candidates, candidate)
	}
	return priority.BuildPriorityUpdates(candidates, priority.Options{
		BasePriority: s.newAPICfg.BasePriority,
		Step:         s.newAPICfg.PriorityStep,
		BucketWidth:  s.newAPICfg.PriorityBucketWidth,
		MaxAge:       time.Duration(s.newAPICfg.PriorityMaxAgeHours) * time.Hour,
	})
}

func (s *Scheduler) runUsage() {
	s.runScan("usage", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s.monitor.ScanAllUsage(ctx)
	})
}

func (s *Scheduler) runBilling() {
	s.runScan("billing", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.billing.Sync(ctx); err != nil {
			s.log.Warn("billing sync failed", "err", err)
		}
	})
}

// runScan prevents repeated runs of the same scan type from multiplying its
// configured per-scan worker count when a cron interval overlaps a slow run.
// Different scan types remain independent so a slow usage request cannot block
// balance or rate refreshes.
func (s *Scheduler) runScan(name string, fn func()) {
	value, _ := s.scanLocks.LoadOrStore(name, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		s.log.Warn("scheduled scan skipped because another scan is running", "scan", name)
		return
	}
	defer lock.Unlock()
	fn()
}

func (s *Scheduler) hasRetention() bool {
	r := s.cfg.Retention
	return r.MonitorLogsDays > 0 || r.BalanceSnapshotsDays > 0 || r.NotificationLogsDays > 0 ||
		r.UsageFiveMinuteHours > 0 || r.UsageHourlyDays > 0
}

// runRetention 按配置删除过期历史。任一表失败不影响其它，全部错误写日志。
func (s *Scheduler) runRetention() {
	r := s.cfg.Retention
	now := time.Now()

	if r.MonitorLogsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.MonitorLogsDays)
		n, err := s.monLogs.DeleteBefore(cutoff)
		if err != nil {
			s.log.Warn("retention monitor_logs failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention monitor_logs deleted", "rows", n, "before", cutoff)
		}
	}

	if r.BalanceSnapshotsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.BalanceSnapshotsDays)
		n, err := s.rates.DeleteBalanceSnapshotsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention balance_snapshots failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention balance_snapshots deleted", "rows", n, "before", cutoff)
		}
	}

	if r.NotificationLogsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.NotificationLogsDays)
		n, err := s.notifies.DeleteLogsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention notification_logs failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention notification_logs deleted", "rows", n, "before", cutoff)
		}
	}

	if r.UsageFiveMinuteHours > 0 {
		cutoff := now.Add(-time.Duration(r.UsageFiveMinuteHours) * time.Hour)
		n, err := s.usage.DeleteBefore(300, cutoff)
		if err != nil {
			s.log.Warn("retention five-minute usage failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention five-minute usage deleted", "rows", n, "before", cutoff)
		}
	}
	if r.UsageHourlyDays > 0 {
		cutoff := now.AddDate(0, 0, -r.UsageHourlyDays)
		n, err := s.usage.DeleteBefore(3600, cutoff)
		if err != nil {
			s.log.Warn("retention hourly usage failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention hourly usage deleted", "rows", n, "before", cutoff)
		}
	}
}
