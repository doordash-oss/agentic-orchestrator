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

package orchestrator_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
)

const (
	claudeLimitError = "You've hit your 5-hour limit · resets 11:10am (America/Los_Angeles)"
	codexLimitError  = "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at 11:30 AM."
)

// autoResumeNow is 2026-10-29 08:15 in Los Angeles.
func autoResumeNow(t *testing.T) time.Time {
	t.Helper()
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, time.October, 29, 8, 15, 0, 0, la)
}

// autoResumeRedispatchError is the terminal error the implementation spy
// reports when a resumed or retried Implement phase is dispatched again.
const autoResumeRedispatchError = "re-dispatched implement finished"

// newAutoResumeFixture stores an Implementing feature with a plan artifact
// and returns an orchestrator on a fixed clock, plus the implementation
// spy that records each Implement dispatch. Timers stop at test end.
func newAutoResumeFixture(t *testing.T, featureID string, hooks orchestrator.Hooks) (*orchestrator.Orchestrator, *feature.Store, time.Time, *fakeRunMultiRepoImpl) {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID:           featureID,
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Artifacts:    map[string]string{"plan": planPath},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, hooks)
	spy := &fakeRunMultiRepoImpl{TerminalResult: &agent.OrchestratorResult{
		FinalStatus: finalStatusFailed,
		LastError:   autoResumeRedispatchError,
	}}
	o.SetRunMultiRepoImplFn(spy.Fn())
	now := autoResumeNow(t)
	o.SetNowFn(func() time.Time { return now })
	t.Cleanup(func() { _ = o.Shutdown() })
	return o, fs, now, spy
}

// assertRedispatched checks the Implement phase ran exactly once more and
// its (spy) outcome is now the feature's failure.
func assertRedispatched(t *testing.T, o *orchestrator.Orchestrator, fs *feature.Store, featureID string, spy *fakeRunMultiRepoImpl) {
	t.Helper()
	o.WaitForCycles()
	if got := spy.numCalls(); got != 1 {
		t.Fatalf("implement dispatched %d times, want 1", got)
	}
	f := loadStoredFeature(t, fs, featureID)
	if rec := f.FailureRecord(); f.Status != feature.StatusFailed || rec == nil || rec.Diagnostics != autoResumeRedispatchError {
		t.Errorf("after re-dispatch: status %s failure %+v, want the spy's outcome", f.Status, rec)
	}
}

func failImplement(t *testing.T, o *orchestrator.Orchestrator, featureID, lastError string) {
	t.Helper()
	if err := o.HandlePhaseCompletion(featureID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus: finalStatusFailed,
			LastError:   lastError,
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}
}

func TestAutoResume_UsageLimitFailureSchedulesResume(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lastError string
		provider  string
		resetAt   func(now time.Time) time.Time
	}{
		{"claude", claudeLimitError, "claude", func(now time.Time) time.Time {
			return time.Date(2026, time.October, 29, 11, 10, 0, 0, now.Location())
		}},
		{"codex", codexLimitError, "codex", func(now time.Time) time.Time {
			return time.Date(2026, time.October, 29, 11, 30, 0, 0, now.Location())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, fs, now, _ := newAutoResumeFixture(t, "feat-limit-"+tc.name, orchestrator.Hooks{})
			failImplement(t, o, "feat-limit-"+tc.name, tc.lastError)

			f := loadStoredFeature(t, fs, "feat-limit-"+tc.name)
			if f.Status != feature.StatusFailed {
				t.Fatalf("status = %s, want Failed", f.Status)
			}
			pending := f.PendingAutoResume()
			if pending == nil {
				t.Fatal("PendingAutoResume() = nil, want a schedule")
			}
			wantReset := tc.resetAt(now)
			if pending.Provider != tc.provider || !pending.ResetAt.Equal(wantReset) ||
				!pending.ResumeAt.Equal(wantReset.Add(2*time.Minute)) || !pending.ScheduledAt.Equal(now) {
				t.Errorf("schedule = %+v, want provider %s reset %s resume +2m", pending, tc.provider, wantReset)
			}
			if !o.AutoResumeArmed(f.ID) {
				t.Error("timer not armed")
			}
		})
	}
}

func TestAutoResume_OtherFailuresDoNotSchedule(t *testing.T) {
	o, fs, _, _ := newAutoResumeFixture(t, "feat-capacity", orchestrator.Hooks{})
	failImplement(t, o, "feat-capacity", "Selected model is at capacity. Please try a different model.")

	f := loadStoredFeature(t, fs, "feat-capacity")
	if f.Status != feature.StatusFailed {
		t.Fatalf("status = %s, want Failed", f.Status)
	}
	if f.Run().AutoResume != nil || o.AutoResumeArmed(f.ID) {
		t.Errorf("unexpected auto-resume %+v", f.Run().AutoResume)
	}
}

// TestAutoResume_StopCancelsAndParksFeatureInterrupted pins that pause-stop
// on a feature waiting out a usage limit cancels the schedule and parks it
// Interrupted, and that a repeated stop is the usual idempotent no-op.
func TestAutoResume_StopCancelsAndParksFeatureInterrupted(t *testing.T) {
	interrupted := 0
	o, fs, _, spy := newAutoResumeFixture(t, "feat-cancel", orchestrator.Hooks{
		OnFeatureInterrupted: func(string) { interrupted++ },
	})
	failImplement(t, o, "feat-cancel", claudeLimitError)

	if err := o.StopFeature("feat-cancel"); err != nil {
		t.Fatalf("StopFeature: %v", err)
	}
	f := loadStoredFeature(t, fs, "feat-cancel")
	if f.Status != feature.StatusInterrupted {
		t.Errorf("status = %s, want Interrupted", f.Status)
	}
	if f.Run().AutoResume != nil || f.FailureRecord() != nil {
		t.Errorf("auto-resume %+v / failure %+v not cleared", f.Run().AutoResume, f.FailureRecord())
	}
	if o.AutoResumeArmed("feat-cancel") {
		t.Error("timer still armed after cancel")
	}
	if interrupted != 1 {
		t.Errorf("OnFeatureInterrupted fired %d times, want 1", interrupted)
	}
	if err := o.StopFeature("feat-cancel"); err != nil {
		t.Errorf("second stop: %v", err)
	}
	if interrupted != 1 {
		t.Errorf("second stop fired OnFeatureInterrupted again (%d)", interrupted)
	}

	// The parked feature resumes through the ordinary Resume path.
	if err := o.StartFeature("feat-cancel"); err != nil {
		t.Fatalf("StartFeature after cancel: %v", err)
	}
	assertRedispatched(t, o, fs, "feat-cancel", spy)
}

func TestAutoResume_StaleTimerDoesNothing(t *testing.T) {
	o, fs, now, spy := newAutoResumeFixture(t, "feat-stale", orchestrator.Hooks{})
	failImplement(t, o, "feat-stale", claudeLimitError)

	o.FireAutoResume("feat-stale", now.Add(time.Hour))

	o.WaitForCycles()
	f := loadStoredFeature(t, fs, "feat-stale")
	if f.Status != feature.StatusFailed || f.PendingAutoResume() == nil || spy.numCalls() != 0 {
		t.Errorf("stale fire changed the feature: status %s, schedule %+v, dispatches %d", f.Status, f.Run().AutoResume, spy.numCalls())
	}
}

func TestAutoResume_FireTakesScheduleAndRetries(t *testing.T) {
	o, fs, _, spy := newAutoResumeFixture(t, "feat-fire", orchestrator.Hooks{})
	failImplement(t, o, "feat-fire", claudeLimitError)
	resumeAt := loadStoredFeature(t, fs, "feat-fire").PendingAutoResume().ResumeAt

	o.FireAutoResume("feat-fire", resumeAt)

	// The retry re-dispatched the failed Implement phase.
	assertRedispatched(t, o, fs, "feat-fire", spy)
}

func TestAutoResume_ManualRestartClearsSchedule(t *testing.T) {
	o, fs, _, _ := newAutoResumeFixture(t, "feat-manual", orchestrator.Hooks{})
	failImplement(t, o, "feat-manual", codexLimitError)

	if _, err := o.RestartPhase("feat-manual", 0, 0); err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	f := loadStoredFeature(t, fs, "feat-manual")
	if f.Run().AutoResume != nil || o.AutoResumeArmed("feat-manual") {
		t.Errorf("manual restart left auto-resume %+v armed=%v", f.Run().AutoResume, o.AutoResumeArmed("feat-manual"))
	}
}

func TestAutoResume_RearmAfterRestart(t *testing.T) {
	o, fs, _, _ := newAutoResumeFixture(t, "feat-rearm", orchestrator.Hooks{})
	failImplement(t, o, "feat-rearm", claudeLimitError)
	scheduled := loadStoredFeature(t, fs, "feat-rearm").PendingAutoResume().ResumeAt

	// A fresh orchestrator over the same store stands in for a restarted
	// server, booting after the schedule became overdue.
	lc := feature.NewManager(fs, nil)
	booted := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	later := scheduled.Add(time.Hour)
	booted.SetNowFn(func() time.Time { return later })
	t.Cleanup(func() { _ = booted.Shutdown() })

	if err := booted.RearmAutoResumes(); err != nil {
		t.Fatalf("RearmAutoResumes: %v", err)
	}
	if !booted.AutoResumeArmed("feat-rearm") {
		t.Fatal("overdue schedule not re-armed")
	}
	pending := loadStoredFeature(t, fs, "feat-rearm").PendingAutoResume()
	if pending == nil || !pending.ResumeAt.Equal(later.Add(10*time.Second)) {
		t.Errorf("overdue schedule = %+v, want resume at boot + 10s", pending)
	}
}

func TestAutoResume_RepeatedLimitSchedulesAgain(t *testing.T) {
	o, fs, now, spy := newAutoResumeFixture(t, "feat-repeat", orchestrator.Hooks{})
	failImplement(t, o, "feat-repeat", claudeLimitError)
	first := loadStoredFeature(t, fs, "feat-repeat").PendingAutoResume().ResumeAt

	// The resumed run burns through the next window and stops on Codex.
	spy.TerminalResult = &agent.OrchestratorResult{FinalStatus: finalStatusFailed, LastError: codexLimitError}
	o.FireAutoResume("feat-repeat", first)
	o.WaitForCycles()

	pending := loadStoredFeature(t, fs, "feat-repeat").PendingAutoResume()
	wantReset := time.Date(2026, time.October, 29, 11, 30, 0, 0, now.Location())
	if pending == nil || pending.Provider != "codex" || !pending.ResetAt.Equal(wantReset) {
		t.Fatalf("second schedule = %+v, want codex reset %s", pending, wantReset)
	}
	if !o.AutoResumeArmed("feat-repeat") {
		t.Error("second schedule not armed")
	}
}
