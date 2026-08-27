package scheduler

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/worryzyy/upstream-hub/internal/config"
)

func TestStartRegistersUsageCron(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(config.SchedulerConfig{
		BalanceCron: "0 0 0 1 1 *",
		RateCron:    "1 0 0 1 1 *",
		UsageCron:   "2 0 0 1 1 *",
	}, nil, nil, nil, nil, nil, logger)
	if err := s.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	t.Cleanup(s.Stop)
	if got := len(s.cron.Entries()); got != 3 {
		t.Fatalf("registered cron jobs = %d, want 3", got)
	}
}

func TestRunScanAllowsDifferentScheduledScanTypesToOverlap(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(config.SchedulerConfig{}, nil, nil, nil, nil, nil, logger)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	go s.runScan("usage", func() {
		calls.Add(1)
		close(started)
		<-release
	})
	<-started
	s.runScan("balance", func() { calls.Add(1) })
	close(release)

	if got := calls.Load(); got != 2 {
		t.Fatalf("scan callbacks = %d, want different scan types to run independently", got)
	}
}

func TestRunScanSkipsOverlappingRunsOfSameScanType(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(config.SchedulerConfig{}, nil, nil, nil, nil, nil, logger)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	go s.runScan("balance", func() {
		calls.Add(1)
		close(started)
		<-release
	})
	<-started
	s.runScan("balance", func() { calls.Add(1) })
	close(release)

	if got := calls.Load(); got != 1 {
		t.Fatalf("scan callbacks = %d, want overlapping same-type scan skipped", got)
	}
}
