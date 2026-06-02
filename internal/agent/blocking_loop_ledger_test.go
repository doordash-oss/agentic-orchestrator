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

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tunit is a test ledger unit.
type tunit struct {
	id       string
	status   string
	decision string
}

func researchHandoffWithUnits(state string, units []tunit) string {
	var b strings.Builder
	b.WriteString("# Research Progress\n\n## Completed Findings\n- finding\n\n## Remaining Areas\n- area\n\n## Where I Stopped\nnext question\n\n## Gotchas\n- none\n\n## Ledger\n```yaml\nunits:\n")
	for _, u := range units {
		fmt.Fprintf(&b, "  - id: %s\n    status: %s\n", u.id, u.status)
		if u.decision != "" {
			fmt.Fprintf(&b, "    decision: %q\n", u.decision)
		}
	}
	fmt.Fprintf(&b, "```\n\n## Handoff State\n%s\n", state)
	return b.String()
}

func nPending(n int) []tunit {
	units := []tunit{{id: "q-done", status: "done"}}
	for i := range n {
		units = append(units, tunit{id: fmt.Sprintf("q-%03d", i), status: "pending"})
	}
	return units
}

// ledgerLoopConfig builds a research-shaped blocking-loop config wired to the
// ledger ProgressStrategy/ResumeStrategy with a persistent deliverable. capturedPrompts,
// when non-nil, receives each iteration's built prompt (so resume context can be asserted).
func ledgerLoopConfig(t testing.TB, artifactDir string, capturedPrompts *[]string, run func(context.Context, BlockingLoopRunInput) (BlockingLoopRunResult, error)) BlockingLoopConfig {
	t.Helper()
	deliverable := filepath.Join(artifactDir, "research.md")
	ps := &LedgerProgressStrategy{Parse: ParseResearchProgressHandoffMd}
	return BlockingLoopConfig{
		Label:                       "research",
		FeatureID:                   "feat-ledger",
		ArtifactDir:                 artifactDir,
		HandoffFilename:             ResearchProgressHandoffFilename,
		ParseHandoff:                ParseResearchProgressHandoffMd,
		ProgressStrategy:            ps,
		ResumeStrategy:              &LedgerResumeStrategy{Progress: ps, WithDecisions: false},
		PersistentDeliverablePath:   deliverable,
		CanonicalSelector:           func(string) (string, error) { return deliverable, nil },
		MaxConsecNoProgress:         3,
		MaxConsecFailures:           3,
		MaxConsecProtocolViolations: 3,
		BuildPrompt: func(in BlockingLoopPromptInput) (string, error) {
			prompt := "research prompt\n" + in.ResumeContext
			if capturedPrompts != nil {
				*capturedPrompts = append(*capturedPrompts, prompt)
			}
			return prompt, nil
		},
		RunSession: run,
	}
}

// writeLedgerIteration is the common RunSession body: ensure the persistent
// deliverable is non-empty, write the handoff, touch phase_complete.
func writeLedgerIteration(t testing.TB, deliverable, state string, units []tunit, in BlockingLoopRunInput) {
	t.Helper()
	if err := os.WriteFile(deliverable, []byte("# Research\n\n## q-000: answered\nbody\n"), 0o644); err != nil {
		t.Fatalf("write deliverable: %v", err)
	}
	writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, researchHandoffWithUnits(state, units))
	writePhaseComplete(t, in.IterationDir)
}

func TestRunBlockingLoop_AutoCompleteOnPendingZero(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	calls := 0
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		calls++
		// Agent says CONTINUE but reports zero pending units: the engine must
		// override to COMPLETE and end the phase.
		writeLedgerIteration(t, deliverable, "CONTINUE", []tunit{{id: "q-1", status: "done"}}, in)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess {
		t.Fatalf("FinalStatus = %q, want success (auto-complete)", result.FinalStatus)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (auto-complete on first zero-pending)", calls)
	}
}

func TestRunBlockingLoop_NetPendingDecreaseReachesComplete(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		// pending: iter1=2, iter2=1, iter3=0 -> auto-complete.
		pending := 3 - in.Iteration
		writeLedgerIteration(t, deliverable, "CONTINUE", nPending(pending), in)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 3 {
		t.Fatalf("result = %+v, want success after 3 iterations", result)
	}
}

func TestRunBlockingLoop_NetPendingNoDecreaseTripsStallRail(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	calls := 0
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		calls++
		// pending stuck at 2 every iteration: net pending never decreases.
		writeLedgerIteration(t, deliverable, "CONTINUE", nPending(2), in)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSafetyRail {
		t.Fatalf("FinalStatus = %q, want safety_rail", result.FinalStatus)
	}
	// iter1 seeds (progress); iter2..iter4 no-progress; rail at count==3 (iter4).
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
}

func TestRunBlockingLoop_AddOneCompleteOneIsNoProgress(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		// Each iteration completes one unit but discovers a new one: pending
		// holds at 2. This is the open-ended churn case Decision 1 targets.
		done := make([]tunit, 0)
		for i := 0; i < in.Iteration; i++ {
			done = append(done, tunit{id: fmt.Sprintf("done-%d", i), status: "done"})
		}
		units := append(done, tunit{id: "p-a", status: "pending"}, tunit{id: "p-b", status: "pending"})
		writeLedgerIteration(t, deliverable, "CONTINUE", units, in)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSafetyRail {
		t.Fatalf("FinalStatus = %q, want safety_rail (add-1-complete-1 = no net progress)", result.FinalStatus)
	}
}

func TestRunBlockingLoop_CompleteWithPendingIsProtocolViolation(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		// COMPLETE but the ledger still has a pending unit -> contradiction.
		writeLedgerIteration(t, deliverable, "COMPLETE", nPending(1), in)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})
	cfg.MaxConsecProtocolViolations = 1

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
}

func TestRunBlockingLoop_LegacyHandoffWithoutLedgerIsProtocolViolation(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		if err := os.WriteFile(deliverable, []byte("# Research\nbody\n"), 0o644); err != nil {
			t.Fatalf("write deliverable: %v", err)
		}
		// A legacy handoff with no `## Ledger` block.
		legacy := "# Research Progress\n\n## Completed Findings\n- x\n\n## Remaining Areas\n- y\n\n## Where I Stopped\nnext\n\n## Gotchas\n- none\n\n## Handoff State\nCONTINUE\n"
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, legacy)
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})
	cfg.MaxConsecProtocolViolations = 1

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (missing ## Ledger)", result.FinalStatus)
	}
}

func TestRunBlockingLoop_PersistentDeliverableNoCopyForward(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		pending := 2 - in.Iteration // iter1=1, iter2=0 -> complete
		writeLedgerIteration(t, deliverable, "CONTINUE", nPending(pending), in)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess {
		t.Fatalf("FinalStatus = %q, want success", result.FinalStatus)
	}
	// The deliverable lives at the artifact root, never copied into iteration dirs.
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-02", "research.md")); !os.IsNotExist(err) {
		t.Fatalf("iteration-02/research.md should not exist (no copy-forward), stat err = %v", err)
	}
	if _, err := os.Stat(deliverable); err != nil {
		t.Fatalf("persistent deliverable missing at %s: %v", deliverable, err)
	}
}

func TestRunBlockingLoop_MinimalResumePromptContainsPendingIDsNotProse(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	var prompts []string
	cfg := ledgerLoopConfig(t, artifactDir, &prompts, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		if in.Iteration == 1 {
			writeLedgerIteration(t, deliverable, "CONTINUE", []tunit{
				{id: "Q-001", status: "done"},
				{id: "Q-002", status: "pending"},
				{id: "Q-003", status: "pending"},
			}, in)
		} else {
			writeLedgerIteration(t, deliverable, "CONTINUE", []tunit{
				{id: "Q-001", status: "done"},
				{id: "Q-002", status: "done"},
				{id: "Q-003", status: "done"},
			}, in)
		}
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})

	if _, err := RunBlockingLoop(context.Background(), cfg, nil); err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	if len(prompts) < 2 {
		t.Fatalf("expected >=2 prompts, got %d", len(prompts))
	}
	resume := prompts[1] // iteration 2's prompt
	if !strings.Contains(resume, "Q-002") || !strings.Contains(resume, "Q-003") {
		t.Fatalf("iteration-2 prompt missing pending IDs:\n%s", resume)
	}
	if strings.Contains(resume, "Q-001") {
		t.Fatalf("iteration-2 prompt should NOT list the done unit Q-001:\n%s", resume)
	}
	if !strings.Contains(resume, deliverable) {
		t.Fatalf("iteration-2 prompt missing deliverable path pointer:\n%s", resume)
	}
	// Minimal resume: the prompt carries a pointer, not the deliverable's prose body.
	if strings.Contains(resume, "## q-000: answered") {
		t.Fatalf("iteration-2 prompt leaked deliverable prose:\n%s", resume)
	}
}

func TestLatestPriorLedgerHandoff_WalksBackPastInvalid(t *testing.T) {
	artifactDir := t.TempDir()
	am := NewArtifactManager(artifactDir)
	cfg := BlockingLoopConfig{
		ArtifactDir:     artifactDir,
		HandoffFilename: ResearchProgressHandoffFilename,
		ParseHandoff:    ParseResearchProgressHandoffMd,
	}

	// iter1: a valid ledger handoff.
	dir1, _ := am.CreateIterationDir(1)
	writeHelperHandoff(t, dir1, ResearchProgressHandoffFilename, researchHandoffWithUnits("CONTINUE", nPending(2)))
	// iter2: a malformed handoff (missing required sections -> nil ledger).
	dir2, _ := am.CreateIterationDir(2)
	writeHelperHandoff(t, dir2, ResearchProgressHandoffFilename, "# Research Progress\n\n## Handoff State\nCONTINUE\n")

	// Resuming for iteration 3 must walk back past the invalid iter2 to iter1.
	got := latestPriorLedgerHandoff(cfg, 2)
	want := filepath.Join(dir1, ResearchProgressHandoffFilename)
	if got != want {
		t.Fatalf("latestPriorLedgerHandoff = %q, want %q (walk back past invalid iter2)", got, want)
	}

	// With no valid ledger at all, it returns "".
	if got := latestPriorLedgerHandoff(cfg, 0); got != "" {
		t.Fatalf("latestPriorLedgerHandoff(0) = %q, want empty", got)
	}
}

func TestRunBlockingLoop_RecoveryRebuildsNetPendingFromLedger(t *testing.T) {
	artifactDir := t.TempDir()
	deliverable := filepath.Join(artifactDir, "research.md")
	if err := os.WriteFile(deliverable, []byte("# Research\nbody\n"), 0o644); err != nil {
		t.Fatalf("write deliverable: %v", err)
	}
	am := NewArtifactManager(artifactDir)

	// Seed two completed CONTINUE iterations with a stuck pending count of 3.
	for i := 1; i <= 2; i++ {
		dir, err := am.CreateIterationDir(i)
		if err != nil {
			t.Fatalf("CreateIterationDir(%d): %v", i, err)
		}
		writeHelperHandoff(t, dir, ResearchProgressHandoffFilename, researchHandoffWithUnits("CONTINUE", nPending(3)))
		if err := am.WriteMeta(dir, IterationMeta{
			Iteration:    i,
			StartedAt:    time.Now(),
			AgentStatus:  agentStatusSuccess,
			ReviewStatus: HelperHandoffContinue.String(),
			MadeProgress: i == 1, // iter1 seeded progress; iter2 was no-progress
		}); err != nil {
			t.Fatalf("WriteMeta(%d): %v", i, err)
		}
	}

	calls := 0
	cfg := ledgerLoopConfig(t, artifactDir, nil, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		calls++
		writeLedgerIteration(t, deliverable, "CONTINUE", nPending(3), in) // still stuck at 3
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	})
	cfg.MaxConsecNoProgress = 2

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop error = %v", err)
	}
	// Recovery replays iter1 (seed) + iter2 (no-progress, count=1). Live iter3 is
	// no-progress (count=2) -> rail. Without recovery, iter3 would re-seed and not trip.
	if result.FinalStatus != BlockingLoopStatusSafetyRail {
		t.Fatalf("FinalStatus = %q, want safety_rail (recovery rebuilt the stall counter)", result.FinalStatus)
	}
	if result.Iterations != 3 {
		t.Fatalf("Iterations = %d, want 3", result.Iterations)
	}
	if calls != 1 {
		t.Fatalf("live calls = %d, want 1 (iterations 1-2 recovered from disk)", calls)
	}
}
