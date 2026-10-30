// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Usage-limit auto-resume. When a run fails because the Claude or Codex CLI
// hit a subscription usage limit and named its reset time, the failure is
// kept (the feature stays Failed) and a retry is scheduled shortly after the
// reset. The schedule is durable on the run so a server restart re-arms it;
// the in-memory timer only decides when to look again. Firing re-reads the
// feature and retries only when the same schedule is still pending, so a
// manual retry, cancel, rewind, or newer failure silently retires the timer.

const (
	// autoResumeGrace delays the retry past the reported reset: providers
	// print reset times to the minute, so the quota can still be exhausted
	// right at the printed time.
	autoResumeGrace = 2 * time.Minute
	// autoResumeMaxWait bounds how far ahead a schedule may be; a parsed
	// reset beyond it is treated as unreadable rather than parking the
	// feature for weeks.
	autoResumeMaxWait = 8 * 24 * time.Hour
	// autoResumeOverdueDelay is how long an overdue schedule found at boot
	// waits before retrying, so the runtime finishes starting first.
	autoResumeOverdueDelay = 10 * time.Second
)

// autoResumeTimers holds the armed in-memory timer of each scheduled feature.
type autoResumeTimers struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

// now is the orchestrator's clock; tests override nowFn.
func (o *Orchestrator) now() time.Time {
	if o.nowFn != nil {
		return o.nowFn()
	}
	return time.Now()
}

// autoResumeEligiblePhases are the agent phases a usage limit can fail and
// a cancel can park as Interrupted for a later Resume.
func autoResumeEligiblePhase(phase feature.Phase) bool {
	switch phase {
	case feature.PhaseKnowledgeBase, feature.PhaseInquire, feature.PhaseResearch,
		feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement,
		feature.PhaseReview, feature.PhaseFinalReview:
		return true
	default:
		return false
	}
}

// scheduleAutoResumeForFailure records and arms an auto-resume when the
// failure just persisted on featureID is a provider usage limit with a
// readable reset time. It runs before the FeatureFailed event so the first
// read model a client loads already carries the schedule.
func (o *Orchestrator) scheduleAutoResumeForFailure(featureID string, failure errcat.FailureRecord) {
	if errcat.IsSetupFailure(failure.Code) {
		return
	}
	now := o.now()
	limit, ok := llm.ParseUsageLimit(failure.Diagnostics, now)
	if !ok || !limit.ResetAt.After(now) || limit.ResetAt.After(now.Add(autoResumeMaxWait)) {
		return
	}
	f, err := o.deps.Store.Load(featureID)
	// Children recover through their own restart and integration paths,
	// never the parent retry this schedules.
	if err != nil || f.Status != feature.StatusFailed || f.IsChild() || !autoResumeEligiblePhase(f.CurrentPhase) {
		return
	}
	schedule := feature.AutoResume{
		Provider:    limit.Provider,
		ResetAt:     limit.ResetAt,
		ResumeAt:    limit.ResetAt.Add(autoResumeGrace),
		ScheduledAt: now,
	}
	if err := o.deps.Manager.ScheduleAutoResume(featureID, schedule); err != nil {
		return
	}
	o.armAutoResume(featureID, schedule.ResumeAt)
}

// armAutoResume (re)arms featureID's timer to fire at resumeAt, replacing
// any timer already armed for it.
func (o *Orchestrator) armAutoResume(featureID string, resumeAt time.Time) {
	select {
	case <-o.doneCh:
		return
	default:
	}
	delay := resumeAt.Sub(o.now())
	if delay < 0 {
		delay = 0
	}
	o.autoResume.mu.Lock()
	defer o.autoResume.mu.Unlock()
	if o.autoResume.timers == nil {
		o.autoResume.timers = make(map[string]*time.Timer)
	}
	if existing := o.autoResume.timers[featureID]; existing != nil {
		existing.Stop()
	}
	o.autoResume.timers[featureID] = time.AfterFunc(delay, func() {
		o.fireAutoResume(featureID, resumeAt)
	})
}

// disarmAutoResume stops featureID's timer, if any.
func (o *Orchestrator) disarmAutoResume(featureID string) {
	o.autoResume.mu.Lock()
	defer o.autoResume.mu.Unlock()
	if t := o.autoResume.timers[featureID]; t != nil {
		t.Stop()
		delete(o.autoResume.timers, featureID)
	}
}

// stopAutoResumeTimers stops every armed timer at shutdown. The durable
// schedules stay on disk for the next boot to re-arm.
func (o *Orchestrator) stopAutoResumeTimers() {
	o.autoResume.mu.Lock()
	defer o.autoResume.mu.Unlock()
	for id, t := range o.autoResume.timers {
		t.Stop()
		delete(o.autoResume.timers, id)
	}
}

// autoResumeArmed reports whether featureID has an armed timer.
func (o *Orchestrator) autoResumeArmed(featureID string) bool {
	o.autoResume.mu.Lock()
	defer o.autoResume.mu.Unlock()
	return o.autoResume.timers[featureID] != nil
}

// fireAutoResume retries featureID when the schedule armed for resumeAt is
// still pending. Taking the schedule clears it before the retry, so a retry
// that fails on a fresh usage limit schedules a new resume of its own, and
// one that fails for any other reason leaves the ordinary failure card.
func (o *Orchestrator) fireAutoResume(featureID string, resumeAt time.Time) {
	o.autoResume.mu.Lock()
	if t := o.autoResume.timers[featureID]; t != nil {
		delete(o.autoResume.timers, featureID)
	}
	o.autoResume.mu.Unlock()
	select {
	case <-o.doneCh:
		return
	default:
	}
	taken, err := o.deps.Manager.TakeAutoResume(featureID, resumeAt)
	if err != nil || !taken {
		return
	}
	if err := o.RetryFeature(featureID); err != nil {
		// The feature is still Failed on its usage-limit record; tell
		// clients the schedule is gone so they show the Retry card.
		o.emitEvent(ports.Event{
			Type:      ports.FeatureAutoResumeChanged,
			FeatureID: featureID,
			Message:   fmt.Sprintf("usage-limit auto-resume could not retry: %v", err),
		})
	}
}

// RearmAutoResumes re-arms the durable auto-resume schedules of every Failed
// feature after a restart. Overdue schedules retry shortly after boot.
func (o *Orchestrator) RearmAutoResumes() error {
	features, err := o.deps.Store.List()
	var partial *feature.PartialLoadError
	if err != nil && !errors.As(err, &partial) {
		return err
	}
	now := o.now()
	for _, f := range features {
		pending := f.PendingAutoResume()
		if pending == nil {
			continue
		}
		resumeAt := pending.ResumeAt
		if !resumeAt.After(now) {
			// Re-stamp the overdue schedule so the fired timer matches it.
			resumeAt = now.Add(autoResumeOverdueDelay)
			updated := *pending
			updated.ResumeAt = resumeAt
			if err := o.deps.Manager.ScheduleAutoResume(f.ID, updated); err != nil {
				continue
			}
		}
		o.armAutoResume(f.ID, resumeAt)
	}
	return nil
}

// cancelAutoResumeLocked is StopFeature for a Failed feature with a pending
// usage-limit auto-resume: it drops the schedule and parks the feature as
// Interrupted. The caller holds the relationship read lock and has passed
// the stop guard. When the timer took the schedule first, the feature is
// already being retried, so the stop falls through to the ordinary
// interruption.
func (o *Orchestrator) cancelAutoResumeLocked(featureID string) error {
	if err := o.deps.Manager.CancelAutoResume(featureID); err != nil {
		if errors.Is(err, feature.ErrNoAutoResume) {
			return o.interruptFeature(featureID)
		}
		return err
	}
	o.disarmAutoResume(featureID)
	if o.hooks.OnFeatureInterrupted != nil {
		o.hooks.OnFeatureInterrupted(featureID)
	}
	o.emitEvent(ports.Event{Type: ports.FeatureInterrupted, FeatureID: featureID})
	return nil
}
