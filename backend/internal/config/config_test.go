package config

import "testing"

func TestSchedulerUsageDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Scheduler.UsageCron != "23 */5 * * * *" {
		t.Fatalf("usage cron = %q", cfg.Scheduler.UsageCron)
	}
	if cfg.Scheduler.Retention.UsageFiveMinuteHours != 48 {
		t.Fatalf("five-minute retention = %d", cfg.Scheduler.Retention.UsageFiveMinuteHours)
	}
	if cfg.Scheduler.Retention.UsageHourlyDays != 90 {
		t.Fatalf("hourly retention = %d", cfg.Scheduler.Retention.UsageHourlyDays)
	}
}
