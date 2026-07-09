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

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// TestResearch_RateLimitRetry_EndToEnd wires a real config, feature.Manager,
// orchestrator, PhaseRunner, and session.Manager and drives the research
// artifact-phase completion path against an on-disk output.txt that carries a
// 429. The observable contract: the config-derived exponential-backoff budget
// lets the feature retry past the default cap of 3, and it only fails
// terminally with protocol_violation once the configured MaxRetries is
// exhausted, with the retry sidecar flagged rate-limited.
func TestResearch_RateLimitRetry_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)

	cfg := config.NewDefault()
	cfg.Defaults.RateLimitRetry = config.RateLimitRetryConfig{
		Enabled:    true,
		MaxRetries: 4, // small budget: still larger than the default cap of 3
		BaseDelay:  "1ms",
		MaxDelay:   "2ms",
		Multiplier: 2.0,
		Jitter:     0,
	}
	mgr := feature.NewManager(store, cfg)

	f := &feature.Feature{
		ID:            "research-rate-limit",
		Name:          "Research rate limit retry",
		Slug:          "research-rate-limit",
		Status:        feature.StatusResearching,
		CurrentPhase:  feature.PhaseResearch,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Artifacts:     map[string]string{},
	}

	// Research requires the inquire artifact to build its prompt.
	inquireDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "inquire")
	if err := os.MkdirAll(inquireDir, 0o755); err != nil {
		t.Fatalf("mkdir inquire dir: %v", err)
	}
	inquirePath := filepath.Join(inquireDir, "inquire.md")
	if err := os.WriteFile(inquirePath, []byte("# questions\n"), 0o644); err != nil {
		t.Fatalf("write inquire artifact: %v", err)
	}
	f.Artifacts["inquire"] = inquirePath
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &agent.PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
	}
	// The re-run session writes nothing to stdout (so it never clobbers the
	// output.txt we re-seed each round). runInteractivePhase truncates
	// output.txt via os.Create on every restart, mirroring production.
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		return []string{"true"}, nil, &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
		}, nil
	}

	policy := orchestrator.RateLimitRetryPolicyFromConfig(cfg.Defaults.RateLimitRetry)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:      mgr,
		Store:          store,
		Sessions:       sm,
		PhaseRunner:    pr,
		RateLimitRetry: &policy,
	}, orchestrator.Hooks{})
	defer func() { _ = o.Shutdown() }()

	researchDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "research")
	seedRateLimitOutput := func() {
		if err := os.MkdirAll(researchDir, 0o755); err != nil {
			t.Fatalf("mkdir research dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(researchDir, "output.txt"),
			[]byte("assistant: working...\nAPI rate limit exceeded (429)\n"), 0o644); err != nil {
			t.Fatalf("write output.txt: %v", err)
		}
	}

	// Drive completions up to the configured budget. Each round re-seeds the
	// rate-limit transcript (the restart truncates it) and completes with no
	// phase_complete / markdown, so every attempt is a rate-limit violation.
	for attempt := 1; attempt <= cfg.Defaults.RateLimitRetry.MaxRetries; attempt++ {
		seedRateLimitOutput()

		reloaded, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if reloaded.Status == feature.StatusFailed {
			t.Fatalf("attempt %d: feature already failed before reaching MaxRetries", attempt)
		}

		if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
			Phase:   feature.PhaseResearch,
			Success: true,
		}); err != nil {
			t.Fatalf("attempt %d: HandlePhaseCompletion() error = %v", attempt, err)
		}
		o.WaitForCycles()
	}

	final, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load final feature: %v", err)
	}
	if final.FailureType != feature.FailureProtocolViolation {
		t.Fatalf("FailureType = %q, want %q", final.FailureType, feature.FailureProtocolViolation)
	}

	sidecar, err := agent.ReadProtocolRetrySidecar(researchDir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if sidecar == nil {
		t.Fatal("sidecar = nil, want terminal retry state")
	}
	if sidecar.Consecutive != cfg.Defaults.RateLimitRetry.MaxRetries {
		t.Errorf("sidecar.Consecutive = %d, want %d", sidecar.Consecutive, cfg.Defaults.RateLimitRetry.MaxRetries)
	}
	if !sidecar.RateLimited {
		t.Error("sidecar.RateLimited = false, want true")
	}
}
