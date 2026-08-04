package priority

import (
	"testing"
	"time"
)

func TestBuildPriorityUpdatesUsesBase500AndStep10(t *testing.T) {
	now := time.Now()
	updates := BuildPriorityUpdates([]Candidate{
		{ChannelID: 1, Ratio: 1.0, ObservedAt: now},
		{ChannelID: 2, Ratio: 0.5, ObservedAt: now},
		{ChannelID: 3, Ratio: 0.5, ObservedAt: now},
	}, Options{BasePriority: 500, Step: 10, BucketWidth: 0})

	want := []PriorityUpdate{
		{ChannelID: 2, Priority: 500},
		{ChannelID: 3, Priority: 500},
		{ChannelID: 1, Priority: 490},
	}
	if len(updates) != len(want) {
		t.Fatalf("got %d updates, want %d: %#v", len(updates), len(want), updates)
	}
	for i := range want {
		if updates[i] != want[i] {
			t.Fatalf("update %d = %#v, want %#v", i, updates[i], want[i])
		}
	}
}

func TestBuildPriorityUpdatesSkipsExcludedAndStaleCandidates(t *testing.T) {
	now := time.Now()
	updates := BuildPriorityUpdates([]Candidate{
		{ChannelID: 1, Ratio: 0.5, ObservedAt: now},
		{ChannelID: 2, Ratio: 0.4, ObservedAt: now.Add(-2 * time.Hour), Excluded: true},
		{ChannelID: 3, Ratio: 0.3, ObservedAt: now.Add(-2 * time.Hour)},
	}, Options{BasePriority: 500, Step: 10, BucketWidth: 0, MaxAge: time.Hour, Now: now})

	if len(updates) != 1 || updates[0] != (PriorityUpdate{ChannelID: 1, Priority: 500}) {
		t.Fatalf("got %#v, want one update for channel 1 at priority 500", updates)
	}
}

func TestBuildPriorityUpdatesSkipsChannelsAlreadyAtSuggestedPriority(t *testing.T) {
	now := time.Now()
	updates := BuildPriorityUpdates([]Candidate{
		{ChannelID: 1, Ratio: 0.5, ObservedAt: now, CurrentPriority: 500},
		{ChannelID: 2, Ratio: 1.0, ObservedAt: now, CurrentPriority: 480},
	}, Options{BasePriority: 500, Step: 10, Now: now})
	if len(updates) != 1 || updates[0] != (PriorityUpdate{ChannelID: 2, Priority: 490}) {
		t.Fatalf("got %#v, want only channel 2 at priority 490", updates)
	}
}
