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
	if cfg.Scheduler.Retention.UsageHourlyDays != 400 {
		t.Fatalf("hourly retention = %d", cfg.Scheduler.Retention.UsageHourlyDays)
	}
}

func TestBillingDefaultsAndCreditRateCanBeConfigured(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Billing.Cron != "47 */5 * * * *" || cfg.Billing.SettlementDelayMinutes != 15 || cfg.Billing.OverlapMinutes != 30 || cfg.Billing.InitialLookbackHours != 24 {
		t.Fatalf("billing defaults = %#v", cfg.Billing)
	}
	if cfg.Billing.CreditUSDPerCNY != 12 {
		t.Fatalf("credit rate = %v, want 12", cfg.Billing.CreditUSDPerCNY)
	}

	t.Setenv("UPSTREAMHUB_BILLING_CREDIT_USD_PER_CNY", "10.5")
	t.Setenv("UPSTREAMHUB_BILLING_ENABLED", "true")
	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load with env returned error: %v", err)
	}
	if cfg.Billing.CreditUSDPerCNY != 10.5 || !cfg.Billing.Enabled {
		t.Fatalf("billing env overrides = %#v", cfg.Billing)
	}
}
