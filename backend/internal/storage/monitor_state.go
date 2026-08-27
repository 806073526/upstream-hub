package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
)

// MonitorState stores channel-wide monitoring backoff. All scheduled jobs for
// a channel share this row, so a failed login cannot be retried by every job.
type MonitorState struct {
	ChannelID       uint       `gorm:"primaryKey" json:"channel_id"`
	FailureCount    int        `gorm:"not null;default:0" json:"failure_count"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
	LastFailureType string     `gorm:"size:32" json:"last_failure_type,omitempty"`
	LastError       string     `gorm:"type:text" json:"last_error,omitempty"`
	LastErrorKey    string     `gorm:"size:128" json:"last_error_key,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (MonitorState) TableName() string { return "channel_monitor_states" }

// RecordManualFailure intentionally leaves automatic state unchanged. Manual
// retries are diagnostic actions and must not extend a scheduled backoff.
func (s *MonitorState) RecordManualFailure(_ string) {}

func (s *MonitorState) RecordSuccess(now time.Time) {
	s.FailureCount = 0
	s.NextAttemptAt = nil
	s.LastFailureType = ""
	s.LastError = ""
	s.LastErrorKey = ""
	s.LastCheckedAt = &now
	s.LastSuccessAt = &now
}

func (s *MonitorState) RecordCheckSuccess(now time.Time) {
	s.LastCheckedAt = &now
	s.NextAttemptAt = nil
}

func (s *MonitorState) RecordFailure(kind string, err error, next, now time.Time) {
	s.FailureCount++
	s.NextAttemptAt = &next
	s.LastFailureType = kind
	s.LastError = ""
	if err != nil {
		s.LastError = err.Error()
	}
	s.LastErrorKey = failureKey(err)
	s.UpdatedAt = now
}

type MonitorStates struct{ db *gorm.DB }

func NewMonitorStates(db *gorm.DB) *MonitorStates { return &MonitorStates{db: db} }

func (r *MonitorStates) FindByChannel(channelID uint) (*MonitorState, error) {
	var state MonitorState
	err := r.db.First(&state, "channel_id = ?", channelID).Error
	if err == nil {
		return &state, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &MonitorState{ChannelID: channelID}, nil
	}
	return nil, err
}

func (r *MonitorStates) Save(state *MonitorState) error {
	return r.db.Save(state).Error
}

func (r *MonitorStates) Delete(channelID uint) error {
	return r.db.Delete(&MonitorState{}, "channel_id = ?", channelID).Error
}

func (r *MonitorStates) RecordFailure(channelID uint, kind string, err error, next, now time.Time) error {
	state, findErr := r.FindByChannel(channelID)
	if findErr != nil {
		return findErr
	}
	state.RecordFailure(kind, err, next, now)
	return r.Save(state)
}

func (r *MonitorStates) RecordCheckSuccess(channelID uint, now time.Time) error {
	state, err := r.FindByChannel(channelID)
	if err != nil {
		return err
	}
	state.RecordCheckSuccess(now)
	return r.Save(state)
}

func (r *MonitorStates) RecordSuccess(channelID uint, now time.Time) (bool, error) {
	state, err := r.FindByChannel(channelID)
	if err != nil {
		return false, err
	}
	recovered := state.LastFailureType != ""
	state.RecordSuccess(now)
	return recovered, r.Save(state)
}

func failureKey(err error) string {
	if err == nil {
		return ""
	}
	digest := sha256.Sum256([]byte(err.Error()))
	return hex.EncodeToString(digest[:])
}
