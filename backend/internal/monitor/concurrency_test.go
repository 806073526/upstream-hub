package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/worryzyy/upstream-hub/internal/storage"
)

func TestScanChannelsHonorsConcurrencyLimit(t *testing.T) {
	list := []storage.Channel{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}
	started := make(chan uint, len(list))
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		scanChannels(context.Background(), list, 2, func(_ context.Context, c *storage.Channel) error {
			started <- c.ID
			<-release
			return nil
		})
		close(done)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for initial workers")
		}
	}
	select {
	case id := <-started:
		t.Fatalf("channel %d started before a worker was released", id)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scan to finish")
	}
}
