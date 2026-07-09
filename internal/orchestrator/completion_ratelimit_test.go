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

const rateLimitOutput = "assistant: retrying...\nAPI rate limit exceeded (429)\n"

// writePhaseOutput writes the session transcript file the TUI persists to the
// phase dir immediately before signaling completion. The orchestrator scans it
// to classify rate-limit failures.
func writePhaseOutput(t *testing.T, stateDir string, f *feature.Feature, phaseKey, content string) {
	t.Helper()
	phaseDir := filepath.Join(agent.ActiveRunDir(stateDir, f), phaseKey)
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir phase dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, "output.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write output.txt: %v", err)
	}
}

func withRateLimitPolicy(p agent.RateLimitRetryPolicy) func(*orchestrator.Deps) {
	return func(d *orchestrator.Deps) { d.RateLimitRetry = &p }
}

// fastRateLimitPolicy backs off on sub-millisecond delays with no jitter so
// scheduled retries drain near-instantly under WaitForCycles.
func fastRateLimitPolicy() agent.RateLimitRetryPolicy {
	return agent.RateLimitRetryPolicy{
		Enabled:    true,
		MaxRetries: 6,
		BaseDelay:  time.Millisecond,
		MaxDelay:   2 * time.Millisecond,
		Multiplier: 2.0,
		Jitter:     0,
	}
}

func completeArtifactPhase(t *testing.T, fix artifactPhaseRetryFixture, phase feature.Phase) {
	t.Helper()
	if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
		Phase:   phase,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}
}

func TestOrchestrator_ArtifactPhase_RateLimit_RetriesBeyondThree(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{}, withRateLimitPolicy(fastRateLimitPolicy()))

			const attempts = 4 // past the old default cap of 3
			for i := 0; i < attempts; i++ {
				// Each re-run's session (re)writes output.txt; simulate that
				// the re-run also hit the rate limit. The scheduled retry
				// truncates output.txt via os.Create, so re-seed every round.
				writePhaseOutput(t, fix.store.BaseDir, fix.feature, tc.phaseKey, rateLimitOutput)
				completeArtifactPhase(t, fix, tc.phase)
				fix.orchestrator.WaitForCycles()
				drainEvents(fix.orchestrator)
			}

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.FailureType != "" {
				t.Fatalf("FailureType = %q, want empty (rate-limit budget not exhausted)", reloaded.FailureType)
			}
			sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
			if err != nil {
				t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
			}
			if sidecar == nil || sidecar.Consecutive != attempts {
				t.Fatalf("sidecar = %#v, want Consecutive %d", sidecar, attempts)
			}
			if !sidecar.RateLimited {
				t.Error("sidecar.RateLimited = false, want true")
			}
			if got := len(fix.runner.capturedByPhase(tc.phase)); got != attempts {
				t.Fatalf("starter captures = %d, want %d", got, attempts)
			}
		})
	}
}

func TestOrchestrator_ArtifactPhase_RateLimit_TerminatesAtMaxRetries(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			policy := fastRateLimitPolicy() // MaxRetries=6
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{}, withRateLimitPolicy(policy))

			for i := 0; i < policy.MaxRetries; i++ {
				writePhaseOutput(t, fix.store.BaseDir, fix.feature, tc.phaseKey, rateLimitOutput)
				completeArtifactPhase(t, fix, tc.phase)
				fix.orchestrator.WaitForCycles()
			}

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.FailureType != feature.FailureProtocolViolation {
				t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureProtocolViolation)
			}
			sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
			if err != nil {
				t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
			}
			if sidecar == nil || sidecar.Consecutive != policy.MaxRetries {
				t.Fatalf("sidecar = %#v, want Consecutive %d", sidecar, policy.MaxRetries)
			}
			if !sidecar.RateLimited {
				t.Error("sidecar.RateLimited = false, want true")
			}
			// Retries scheduled for the first MaxRetries-1 attempts; the last
			// hits the cap and fails without scheduling another.
			if got := len(fix.runner.capturedByPhase(tc.phase)); got != policy.MaxRetries-1 {
				t.Fatalf("starter captures = %d, want %d", got, policy.MaxRetries-1)
			}
		})
	}
}

func TestOrchestrator_ArtifactPhase_NonRateLimitOutput_StillTerminatesAtThree(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Default policy (enabled, long delay) is fine: a non-rate-limit
			// classification never reaches the backoff branch.
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{})
			writePhaseOutput(t, fix.store.BaseDir, fix.feature, tc.phaseKey, "assistant: no artifact written this turn\n")

			for i := 0; i < agent.DefaultMaxConsecutiveProtocolViolations; i++ {
				completeArtifactPhase(t, fix, tc.phase)
			}
			fix.orchestrator.WaitForCycles()

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.FailureType != feature.FailureProtocolViolation {
				t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureProtocolViolation)
			}
			sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
			if err != nil {
				t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
			}
			if sidecar == nil || sidecar.Consecutive != agent.DefaultMaxConsecutiveProtocolViolations {
				t.Fatalf("sidecar = %#v, want Consecutive %d", sidecar, agent.DefaultMaxConsecutiveProtocolViolations)
			}
			if sidecar.RateLimited {
				t.Error("sidecar.RateLimited = true, want false for non-rate-limit output")
			}
		})
	}
}

func TestOrchestrator_ArtifactPhase_MissingOutputTxt_TreatedAsNonRateLimit(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{}, withRateLimitPolicy(fastRateLimitPolicy()))
			// No output.txt written.

			for i := 0; i < agent.DefaultMaxConsecutiveProtocolViolations; i++ {
				completeArtifactPhase(t, fix, tc.phase)
			}
			fix.orchestrator.WaitForCycles()

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.FailureType != feature.FailureProtocolViolation {
				t.Fatalf("FailureType = %q, want %q (absent output.txt is not rate-limit)", reloaded.FailureType, feature.FailureProtocolViolation)
			}
		})
	}
}

func TestOrchestrator_ArtifactPhase_RateLimitRetry_IsScheduledNotBlocking(t *testing.T) {
	tc := artifactPhaseCases()[1] // research
	policy := agent.RateLimitRetryPolicy{
		Enabled:    true,
		MaxRetries: 6,
		BaseDelay:  60 * time.Second, // long enough that it cannot run inline
		MaxDelay:   60 * time.Second,
		Multiplier: 2.0,
		Jitter:     0,
	}
	fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{}, withRateLimitPolicy(policy))
	writePhaseOutput(t, fix.store.BaseDir, fix.feature, tc.phaseKey, rateLimitOutput)

	// Returns promptly; the restart is deferred to a background timer.
	completeArtifactPhase(t, fix, tc.phase)

	if got := len(fix.runner.capturedByPhase(tc.phase)); got != 0 {
		t.Fatalf("starter captures = %d, want 0 (retry must be scheduled, not inline)", got)
	}
	reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
	if reloaded.FailureType != "" {
		t.Fatalf("FailureType = %q, want empty", reloaded.FailureType)
	}
	sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if sidecar == nil || !sidecar.RateLimited || sidecar.Consecutive != 1 {
		t.Fatalf("sidecar = %#v, want Consecutive 1 RateLimited true", sidecar)
	}

	// Shutdown cancels the pending timer so WaitForCycles drains without
	// waiting out the 60s delay.
	_ = fix.orchestrator.Shutdown()
	fix.orchestrator.WaitForCycles()
	if got := len(fix.runner.capturedByPhase(tc.phase)); got != 0 {
		t.Fatalf("starter captures after shutdown = %d, want 0 (timer cancelled)", got)
	}
}

func TestOrchestrator_ArtifactPhase_RateLimit_DisabledPolicyImmediate(t *testing.T) {
	tc := artifactPhaseCases()[1] // research
	policy := fastRateLimitPolicy()
	policy.Enabled = false
	fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{}, withRateLimitPolicy(policy))
	writePhaseOutput(t, fix.store.BaseDir, fix.feature, tc.phaseKey, rateLimitOutput)

	// Disabled policy => default cap of 3, immediate retries even though the
	// output is rate-limit-flavored.
	for i := 0; i < agent.DefaultMaxConsecutiveProtocolViolations; i++ {
		completeArtifactPhase(t, fix, tc.phase)
	}
	fix.orchestrator.WaitForCycles()

	reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
	if reloaded.FailureType != feature.FailureProtocolViolation {
		t.Fatalf("FailureType = %q, want %q when policy disabled", reloaded.FailureType, feature.FailureProtocolViolation)
	}
	sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if sidecar == nil || sidecar.RateLimited {
		t.Fatalf("sidecar = %#v, want RateLimited false when disabled", sidecar)
	}
}

func TestOrchestrator_ArtifactPhase_RateLimitThenSuccessAdvances(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, retrySuccessCheckpointForTest(tc.phase), withRateLimitPolicy(fastRateLimitPolicy()))
			writePhaseOutput(t, fix.store.BaseDir, fix.feature, tc.phaseKey, rateLimitOutput)

			// First completion: rate-limit violation -> scheduled retry.
			completeArtifactPhase(t, fix, tc.phase)
			fix.orchestrator.WaitForCycles()
			drainEvents(fix.orchestrator)

			// Second completion: artifacts present -> success + advance.
			writePhaseComplete(t, fix.store.BaseDir, fix.feature, tc.phaseKey)
			artifactPath := writePhaseMarkdown(t, fix.store.BaseDir, fix.feature, tc.phaseKey, tc.phaseKey+".md")
			completeArtifactPhase(t, fix, tc.phase)

			if _, err := os.Stat(filepath.Join(fix.phaseDir, agent.ProtocolRetrySidecarFile)); !os.IsNotExist(err) {
				t.Fatalf("sidecar stat err = %v, want removed after success", err)
			}
			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if got := reloaded.Artifacts[tc.artifactKey]; got != artifactPath {
				t.Fatalf("Artifacts[%q] = %q, want %q", tc.artifactKey, got, artifactPath)
			}
		})
	}
}
