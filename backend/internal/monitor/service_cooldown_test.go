package monitor

import (
	"testing"

	"github.com/worryzyy/upstream-hub/internal/channel"
)

func TestCooldownFailuresAreNotNotifiable(t *testing.T) {
	if shouldNotifyFailure(&channel.CooldownError{}) {
		t.Fatal("cooldown failures should not send repeated notifications")
	}
	if !shouldNotifyFailure(assertionError("status 500")) {
		t.Fatal("real monitor failures should remain notifiable")
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }
