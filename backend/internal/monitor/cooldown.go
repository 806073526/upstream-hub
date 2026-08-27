package monitor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/worryzyy/upstream-hub/internal/connector"
)

type FailureType string

const (
	FailureAuth      FailureType = "auth"
	FailureTransient FailureType = "transient"
)

// ClassifyFailure treats only explicit auth responses as credentials failures.
// Network errors and 5xx/Cloudflare errors must never trigger another login.
func ClassifyFailure(err error) FailureType {
	var statusErr *connector.HTTPError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == 401 || statusErr.StatusCode == 403 {
			return FailureAuth
		}
		return FailureTransient
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureTransient
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return FailureTransient
	}
	// Connector errors may wrap a transport error with additional context.
	message := strings.ToLower(classificationErrorText(err))
	for _, marker := range []string{"timeout", "timed out", "connection", "dial", "dns", "temporary", "unavailable"} {
		if strings.Contains(message, marker) {
			return FailureTransient
		}
	}
	// Unknown monitoring failures are safer to treat as transient than to
	// submit credentials and a new captcha token again.
	return FailureTransient
}

func BackoffDuration(failureCount int, base, max time.Duration) time.Duration {
	if failureCount < 1 {
		failureCount = 1
	}
	if base <= 0 {
		base = 15 * time.Minute
	}
	if max <= 0 {
		max = 2 * time.Hour
	}
	delay := base
	for i := 1; i < failureCount; i++ {
		if delay >= max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}

func classificationErrorText(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}
