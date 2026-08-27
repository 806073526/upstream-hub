package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

func TestClassifyFailureSeparatesAuthFromTransientErrors(t *testing.T) {
	if got := ClassifyFailure(connector.HTTPStatusError(401, nil)); got != FailureAuth {
		t.Fatalf("401 classified as %q, want %q", got, FailureAuth)
	}
	if got := ClassifyFailure(connector.HTTPStatusError(403, nil)); got != FailureAuth {
		t.Fatalf("403 classified as %q, want %q", got, FailureAuth)
	}
	if got := ClassifyFailure(connector.HTTPStatusError(522, nil)); got != FailureTransient {
		t.Fatalf("522 classified as %q, want %q", got, FailureTransient)
	}
	if got := ClassifyFailure(context.DeadlineExceeded); got != FailureTransient {
		t.Fatalf("deadline classified as %q, want %q", got, FailureTransient)
	}
	if got := ClassifyFailure(errors.New("temporary dns failure")); got != FailureTransient {
		t.Fatalf("network-like error classified as %q, want %q", got, FailureTransient)
	}
}

func TestBackoffDurationUsesCappedExponentialSchedule(t *testing.T) {
	base := 15 * time.Minute
	max := 2 * time.Hour
	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 2 * time.Hour}
	for failureCount, expected := range want {
		got := BackoffDuration(failureCount+1, base, max)
		if got != expected {
			t.Fatalf("failure count %d: got %s, want %s", failureCount+1, got, expected)
		}
	}
}
