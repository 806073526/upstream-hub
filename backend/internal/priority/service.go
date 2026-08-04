package priority

import (
	"math"
	"sort"
	"time"
)

type Candidate struct {
	ChannelID       int
	Ratio           float64
	ObservedAt      time.Time
	Excluded        bool
	CurrentPriority int64
}

type Options struct {
	BasePriority int64
	Step         int64
	BucketWidth  float64
	MaxAge       time.Duration
	Now          time.Time
}

type PriorityUpdate struct {
	ChannelID int
	Priority  int64
}

func BuildPriorityUpdates(candidates []Candidate, options Options) []PriorityUpdate {
	base := options.BasePriority
	if base == 0 {
		base = 500
	}
	step := options.Step
	if step == 0 {
		step = 10
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}

	valid := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ChannelID <= 0 || candidate.Excluded || candidate.Ratio <= 0 ||
			math.IsNaN(candidate.Ratio) || math.IsInf(candidate.Ratio, 0) {
			continue
		}
		if options.MaxAge > 0 && (candidate.ObservedAt.IsZero() || now.Sub(candidate.ObservedAt) > options.MaxAge) {
			continue
		}
		valid = append(valid, candidate)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].Ratio == valid[j].Ratio {
			return valid[i].ChannelID < valid[j].ChannelID
		}
		return valid[i].Ratio < valid[j].Ratio
	})

	updates := make([]PriorityUpdate, 0, len(valid))
	lastBucket := math.NaN()
	priority := base
	for _, candidate := range valid {
		bucket := candidate.Ratio
		if options.BucketWidth > 0 {
			bucket = math.Floor(candidate.Ratio/options.BucketWidth + 1e-9)
		}
		if !math.IsNaN(lastBucket) && bucket != lastBucket {
			priority -= step
		}
		if candidate.CurrentPriority != 0 && candidate.CurrentPriority == priority {
			lastBucket = bucket
			continue
		}
		updates = append(updates, PriorityUpdate{ChannelID: candidate.ChannelID, Priority: priority})
		lastBucket = bucket
	}
	return updates
}
