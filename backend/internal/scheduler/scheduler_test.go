package scheduler

import (
	"io"
	"log/slog"
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
