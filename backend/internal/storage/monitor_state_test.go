package storage

import (
	"errors"
	"testing"
	"time"
)

func TestFailureKeyFitsDatabaseColumnForLongErrors(t *testing.T) {
	err := errors.New("upstream failure: " + string(make([]byte, 200)))
	key := failureKey(err)
	if len(key) > 128 {
		t.Fatalf("failure key length = %d, want <= 128", len(key))
	}
}

func TestMonitorStateManualFailureDoesNotChangeAutomaticCooldown(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)
	state := MonitorState{
		FailureCount:    3,
		NextAttemptAt:   &next,
		LastFailureType: "transient",
		LastError:       "status 522",
	}

	state.RecordManualFailure("status 522")

	if state.FailureCount != 3 {
		t.Fatalf("manual failure changed failure count to %d", state.FailureCount)
	}
	if state.NextAttemptAt == nil || !state.NextAttemptAt.Equal(next) {
		t.Fatalf("manual failure changed next attempt time to %v", state.NextAttemptAt)
	}
}

func TestMonitorStateSuccessClearsCooldown(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)
	state := MonitorState{
		FailureCount:    2,
		NextAttemptAt:   &next,
		LastFailureType: "auth",
		LastError:       "status 401",
	}

	state.RecordSuccess(now)

	if state.FailureCount != 0 || state.NextAttemptAt != nil || state.LastFailureType != "" || state.LastError != "" {
		t.Fatalf("success did not clear state: %#v", state)
	}
	if state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(now) {
		t.Fatalf("success time = %v, want %v", state.LastSuccessAt, now)
	}
}
