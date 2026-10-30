// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package feature

import (
	"errors"
	"fmt"
	"time"
)

// ErrNoAutoResume is returned when a feature has no pending usage-limit
// auto-resume to take or cancel.
var ErrNoAutoResume = errors.New("feature has no pending auto-resume")

// AutoResume is a scheduled retry of a run that failed on a provider usage
// limit. The orchestrator retries the feature at ResumeAt, shortly after the
// provider's reported reset.
type AutoResume struct {
	// Provider is the CLI that reported the limit ("claude" or "codex").
	Provider string `yaml:"provider"`
	// ResetAt is the reset moment the provider reported.
	ResetAt time.Time `yaml:"reset_at"`
	// ResumeAt is when the retry fires.
	ResumeAt time.Time `yaml:"resume_at"`
	// ScheduledAt is when the schedule was recorded.
	ScheduledAt time.Time `yaml:"scheduled_at"`
}

// PendingAutoResume returns the active run's usage-limit auto-resume while
// the feature is Failed; nil otherwise.
func (f *Feature) PendingAutoResume() *AutoResume {
	if f == nil || f.Status != StatusFailed {
		return nil
	}
	return f.Run().AutoResume
}

// ScheduleAutoResume records a pending auto-resume on a Failed feature.
func (m *Manager) ScheduleAutoResume(featureID string, schedule AutoResume) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.Status != StatusFailed {
			return fmt.Errorf("%w: feature is %s, not Failed", ErrInvalidTransition, f.Status)
		}
		f.Run().AutoResume = &schedule
		return nil
	})
}

// TakeAutoResume clears the pending auto-resume when it still fires at
// resumeAt and the feature is still Failed, reporting whether the caller now
// owns the resume. A cancelled, replaced, or superseded schedule returns
// false so a stale timer never retries.
func (m *Manager) TakeAutoResume(featureID string, resumeAt time.Time) (bool, error) {
	taken := false
	err := m.Store.Modify(featureID, func(f *Feature) error {
		pending := f.PendingAutoResume()
		if pending == nil || !pending.ResumeAt.Equal(resumeAt) {
			return nil
		}
		f.Run().AutoResume = nil
		taken = true
		return nil
	})
	return taken, err
}

// CancelAutoResume drops a pending auto-resume and parks the feature as
// Interrupted so Resume or Restart picks it back up on the user's terms.
// The usage-limit failure record is cleared with it: the run did not fail
// on its own work, it was paused for quota.
func (m *Manager) CancelAutoResume(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.PendingAutoResume() == nil {
			return ErrNoAutoResume
		}
		run := f.Run()
		run.AutoResume = nil
		run.Failure = nil
		f.Status = StatusInterrupted
		return nil
	})
}
