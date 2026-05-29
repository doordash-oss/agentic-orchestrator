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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestRunBlockingLoop_CompleteWritesMeta(t *testing.T) {
	artifactDir := t.TempDir()

	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		if in.Iteration != 1 {
			t.Fatalf("Iteration = %d, want 1", in.Iteration)
		}
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n")
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "answered all questions"))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusSuccess)
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1", result.Iterations)
	}
	if filepath.Base(result.TerminalIterationDir) != "iteration-01" {
		t.Fatalf("TerminalIterationDir = %q, want iteration-01", result.TerminalIterationDir)
	}
	if filepath.Base(result.CanonicalPath) != "research.md" {
		t.Fatalf("CanonicalPath = %q, want research.md", result.CanonicalPath)
	}
	meta := readBlockingLoopMeta(t, result.TerminalIterationDir)
	if meta.AgentStatus != agentStatusSuccess {
		t.Fatalf("AgentStatus = %q, want %q", meta.AgentStatus, agentStatusSuccess)
	}
	if meta.ReviewStatus != HelperHandoffComplete.String() {
		t.Fatalf("ReviewStatus = %q, want COMPLETE", meta.ReviewStatus)
	}
}

func TestRunBlockingLoop_ContinueSeedsPriorCanonical(t *testing.T) {
	artifactDir := t.TempDir()
	var seen []int

	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		seen = append(seen, in.Iteration)
		switch in.Iteration {
		case 1:
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\nfirst pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "mapped subsystem A"))
		case 2:
			seeded, err := os.ReadFile(filepath.Join(in.IterationDir, "research.md"))
			if err != nil {
				t.Fatalf("read seeded canonical: %v", err)
			}
			if !strings.Contains(string(seeded), "first pass") {
				t.Fatalf("seeded canonical = %q, want prior contents", string(seeded))
			}
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", string(seeded)+"second pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "mapped subsystem B"))
		default:
			t.Fatalf("unexpected iteration %d", in.Iteration)
		}
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("result = %+v, want success after 2 iterations", result)
	}
	if fmt.Sprint(seen) != "[1 2]" {
		t.Fatalf("seen iterations = %v, want [1 2]", seen)
	}
}

func TestRunBlockingLoopSafetyRails(t *testing.T) {
	t.Run("no progress", func(t *testing.T) {
		artifactDir := t.TempDir()
		calls := 0
		cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
			calls++
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "same finding"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
		})
		cfg.MaxConsecNoProgress = 1

		result, err := RunBlockingLoop(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("RunBlockingLoop() error = %v", err)
		}
		if result.FinalStatus != BlockingLoopStatusSafetyRail {
			t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusSafetyRail)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		artifactDir := t.TempDir()
		calls := 0
		cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
			calls++
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("INVALID", "same finding"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
		})
		cfg.MaxConsecProtocolViolations = 2

		result, err := RunBlockingLoop(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("RunBlockingLoop() error = %v", err)
		}
		if result.FinalStatus != BlockingLoopStatusProtocolViolation {
			t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusProtocolViolation)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("session failures", func(t *testing.T) {
		artifactDir := t.TempDir()
		calls := 0
		cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
			calls++
			return BlockingLoopRunResult{Status: agentStatusFailed}, nil
		})
		cfg.MaxConsecFailures = 2

		result, err := RunBlockingLoop(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("RunBlockingLoop() error = %v", err)
		}
		if result.FinalStatus != BlockingLoopStatusSafetyRail {
			t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusSafetyRail)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})
}

func TestRunBlockingLoop_ContinueWithoutFixedCap(t *testing.T) {
	artifactDir := t.TempDir()
	calls := 0

	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		calls++
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", fmt.Sprintf("# Research\n\niteration %d\n", in.Iteration))
		state := "CONTINUE"
		if in.Iteration == 6 {
			state = "COMPLETE"
		}
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff(state, fmt.Sprintf("finding %d", in.Iteration)))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 6 {
		t.Fatalf("result = %+v, want success after 6 iterations", result)
	}
	if calls != 6 {
		t.Fatalf("calls = %d, want 6", calls)
	}
}

func TestRunBlockingLoop_ResumeReplaysIncompleteIteration(t *testing.T) {
	artifactDir := t.TempDir()
	am := NewArtifactManager(artifactDir)
	iter1, err := am.CreateIterationDir(1)
	if err != nil {
		t.Fatalf("CreateIterationDir(1): %v", err)
	}
	writeBlockingLoopCanonical(t, iter1, "research.md", "# Research\n\ncompleted iteration 1\n")
	writeHelperHandoff(t, iter1, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "completed iteration 1"))
	if err := am.WriteMeta(iter1, IterationMeta{
		Iteration:    1,
		StartedAt:    time.Now(),
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: HelperHandoffContinue.String(),
		MadeProgress: true,
	}); err != nil {
		t.Fatalf("WriteMeta(iter1): %v", err)
	}
	iter2, err := am.CreateIterationDir(2)
	if err != nil {
		t.Fatalf("CreateIterationDir(2): %v", err)
	}
	writeBlockingLoopCanonical(t, iter2, "research.md", "# Research\n\nstale interrupted partial\n")

	var seen []int
	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		seen = append(seen, in.Iteration)
		if in.Iteration != 2 {
			t.Fatalf("Iteration = %d, want replay of iteration 2", in.Iteration)
		}
		seeded, err := os.ReadFile(filepath.Join(in.IterationDir, "research.md"))
		if err != nil {
			t.Fatalf("read seeded canonical: %v", err)
		}
		if strings.Contains(string(seeded), "stale interrupted partial") || !strings.Contains(string(seeded), "completed iteration 1") {
			t.Fatalf("seeded canonical = %q, want replay from completed iteration 1 only", string(seeded))
		}
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", string(seeded)+"completed iteration 2\n")
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "completed iteration 2"))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("result = %+v, want success at replayed iteration 2", result)
	}
	if fmt.Sprint(seen) != "[2]" {
		t.Fatalf("seen iterations = %v, want [2]", seen)
	}
}

func TestRunBlockingLoop_InterruptedInFlightIterationLeavesNoMeta(t *testing.T) {
	artifactDir := t.TempDir()
	store := mocks.NewMockFeatureStore()
	current := &feature.Feature{ID: "feat-research", Status: feature.StatusResearching}
	store.LoadFn = func(id string) (*feature.Feature, error) {
		if id != current.ID {
			t.Fatalf("Load(%q), want %q", id, current.ID)
		}
		clone := *current
		return &clone, nil
	}

	cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\npartial interrupted work\n")
		current.Status = feature.StatusInterrupted
		return BlockingLoopRunResult{Status: agentStatusFailed}, nil
	})
	cfg.Feature = current
	cfg.FeatureStore = store

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusInterrupted {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusInterrupted)
	}
	if result.Iterations != 0 {
		t.Fatalf("Iterations = %d, want 0 completed iterations", result.Iterations)
	}
	metaPath := filepath.Join(artifactDir, "iteration-01", "meta.yaml")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", metaPath, err)
	}
}

func TestRunBlockingLoop_SessionManagerShutdownLeavesNoMeta(t *testing.T) {
	artifactDir := t.TempDir()
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
		return nil, ports.ErrSessionShuttingDown
	}

	cfg := blockingLoopTestConfig(artifactDir, nil)
	cfg.RunSession = nil
	cfg.Phase = feature.PhaseResearch
	cfg.Spec = ResearcherRoleSpec()
	cfg.StateDir = t.TempDir()
	cfg.BuildSession = func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		return []string{"agent"}, nil, &ports.SessionOpts{}, nil
	}

	result, err := RunBlockingLoop(context.Background(), cfg, sm)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusInterrupted {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusInterrupted)
	}
	metaPath := filepath.Join(artifactDir, "iteration-01", "meta.yaml")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", metaPath, err)
	}
}

func TestRunBlockingLoop_ShutdownFailedStatusLeavesNoMeta(t *testing.T) {
	artifactDir := t.TempDir()
	sm := mocks.NewMockSessionManager()
	sm.ShuttingDownVal = true

	cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\npartial interrupted work\n")
		return BlockingLoopRunResult{Status: agentStatusFailed}, nil
	})

	result, err := RunBlockingLoop(context.Background(), cfg, sm)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusInterrupted {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusInterrupted)
	}
	metaPath := filepath.Join(artifactDir, "iteration-01", "meta.yaml")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", metaPath, err)
	}
}

func TestBlockingLoopContextHandoffRoleUsesSkillName(t *testing.T) {
	cfg := normalizeBlockingLoopConfig(BlockingLoopConfig{
		Label:         "research",
		TelemetryRole: "research",
		Spec:          ResearcherRoleSpec(),
	})

	if got := blockingLoopContextHandoffRole(cfg); got != "research-codebase" {
		t.Fatalf("blockingLoopContextHandoffRole() = %q, want research-codebase", got)
	}
}

func blockingLoopTestConfig(artifactDir string, run func(context.Context, BlockingLoopRunInput) (BlockingLoopRunResult, error)) BlockingLoopConfig {
	return BlockingLoopConfig{
		Label:                       "research",
		FeatureID:                   "feat-research",
		ArtifactDir:                 artifactDir,
		HandoffFilename:             ResearchProgressHandoffFilename,
		ParseHandoff:                ParseResearchProgressHandoffMd,
		Fingerprint:                 ResearchProgressHandoffFingerprint,
		CanonicalSelector:           SelectNewestNonExcludedMarkdown,
		MaxConsecNoProgress:         3,
		MaxConsecFailures:           3,
		MaxConsecProtocolViolations: 3,
		RunSession:                  run,
	}
}

func writeBlockingLoopCanonical(t testing.TB, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", filepath.Join(dir, name), err)
	}
}

func writePhaseComplete(t testing.TB, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, PhaseCompleteFile), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(phase_complete): %v", err)
	}
}

func readBlockingLoopMeta(t testing.TB, iterDir string) IterationMeta {
	t.Helper()
	meta, err := NewArtifactManager(filepath.Dir(iterDir)).ReadMeta(iterDir)
	if err != nil {
		t.Fatalf("ReadMeta(%q): %v", iterDir, err)
	}
	return meta
}
