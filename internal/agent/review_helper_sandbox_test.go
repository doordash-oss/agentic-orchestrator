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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type sandboxCapturingSessionManager struct {
	ports.SessionManager
	t        *testing.T
	mu       sync.Mutex
	wrapped  int
	denyDirs []string
}

func (m *sandboxCapturingSessionManager) StartSession(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
	if dirs, ok := helperSandboxDenyDirs(m.t, command); ok {
		m.mu.Lock()
		m.wrapped++
		m.denyDirs = append(m.denyDirs, dirs...)
		m.mu.Unlock()
	}
	return m.SessionManager.StartSession(id, featureID, phase, command, workdir, env, opts...)
}

func TestPerPhaseReviewHelperUsesStateRootPaths(t *testing.T) {
	for _, tc := range []struct {
		name         string
		setStateRoot bool
	}{
		{name: "explicit state root", setStateRoot: true},
		{name: "state root derived from feature state dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			stateRoot := filepath.Join(tmp, "features")
			worktreesDir := feature.WorktreesDir(stateRoot)
			workDir := filepath.Join(worktreesDir, "feat", "repo")
			artifactDir := filepath.Join(tmp, "artifacts")
			iterDir := filepath.Join(artifactDir, "iteration-01")
			scriptsDir := filepath.Join(tmp, "scripts")
			for _, dir := range []string{workDir, iterDir, scriptsDir, stateRoot} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", dir, err)
				}
			}
			progressPath := filepath.Join(artifactDir, "progress.md")
			if err := os.WriteFile(progressPath, []byte("# Iteration Progress\n\n## Iteration State\n\nSUCCESS\n"), 0o644); err != nil {
				t.Fatalf("write progress: %v", err)
			}
			planPath := filepath.Join(artifactDir, "phase-plan.md")
			if err := os.WriteFile(planPath, []byte("# Plan\n"), 0o644); err != nil {
				t.Fatalf("write plan: %v", err)
			}

			reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
				testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
			capture, captured := capturingBuildSession("", reviewScript)
			buildSession := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
				cmd, env, sessOpts, err := capture(opts)
				if sessOpts != nil {
					sessOpts.UsesBoundedHelperSandbox = true
				}
				return cmd, env, sessOpts, err
			}

			eventCh := make(chan interface{}, 100)
			inner := session.NewManager(eventCh)
			defer inner.Shutdown()
			sm := &sandboxCapturingSessionManager{SessionManager: inner, t: t}

			f := newTestFeature(t, workDir)
			f.ID = "per-phase-sandbox"
			f.Pipeline = feature.PipelineMoonshot
			f.CurrentRoadmapPhase = 1
			store := feature.NewStore(stateRoot)
			if err := store.Save(f); err != nil {
				t.Fatalf("save feature: %v", err)
			}
			cfg := ImplementConfig{
				Feature:      f,
				FeatureStore: store,
				WorkDir:      workDir,
				PlanPath:     planPath,
				ExitCriteria: "Relevant tests pass",
				ReviewModel:  "reviewer",
				ArtifactDir:  artifactDir,
				StateDir:     filepath.Join(stateRoot, f.ID),
				BuildSession: buildSession,
				Observer:     observerFromModule(t, false, "", "test"),
			}
			if tc.setStateRoot {
				cfg.StateRoot = stateRoot
			}

			status, feedback, _, err := runImplementationReviewAxes(cfg, sm, 1, iterDir, filepath.Join(iterDir, "review"),
				observe.SpanContext{TraceID: f.TraceID, SpanID: "review-span", FeatureID: f.ID, RunNumber: 1},
				implementationReviewInput{ProgressPath: progressPath})
			if err != nil {
				t.Fatalf("runImplementationReviewAxes() error = %v", err)
			}
			if status != ReviewApproved {
				t.Fatalf("status = %s, want APPROVED; feedback:\n%s", status, feedback)
			}

			var reviewers int
			for _, opts := range *captured {
				if opts.Model != "reviewer" {
					continue
				}
				reviewers++
				if want := filepath.Join(stateRoot, f.ID); opts.PIDDir != want {
					t.Errorf("reviewer PIDDir = %q, want %q", opts.PIDDir, want)
				}
			}
			if reviewers == 0 {
				t.Fatal("no reviewer sessions were built")
			}
			if helperSandboxExpected() && sm.wrapped != reviewers {
				t.Fatalf("sandboxed reviewer launches = %d, want %d", sm.wrapped, reviewers)
			}
			realWorktrees, err := filepath.EvalSymlinks(worktreesDir)
			if err != nil {
				t.Fatalf("resolve worktrees dir: %v", err)
			}
			for _, dir := range sm.denyDirs {
				if dir != realWorktrees {
					t.Errorf("sandbox deny dir = %q, want %q", dir, realWorktrees)
				}
			}
			if run := mustLoadFeature(t, store, f.ID).Run(); helperSandboxExpected() && run.Sandbox != nil {
				t.Errorf("run sandbox record = %+v, want none when the sandbox applied", run.Sandbox)
			}
		})
	}
}

func mustLoadFeature(t *testing.T, store *feature.Store, id string) *feature.Feature {
	t.Helper()
	f, err := store.Load(id)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	return f
}

func TestMaybeWrapHelperSandbox(t *testing.T) {
	cmd := []string{"helper", "run"}
	for _, tc := range []struct {
		name          string
		enabled       bool
		makeWorktrees bool
		wantReason    string
	}{
		{name: "disabled leaves the command unchanged", makeWorktrees: true},
		{name: "missing worktrees directory launches unsandboxed", enabled: true, wantReason: "does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateRoot := filepath.Join(t.TempDir(), "features")
			if tc.makeWorktrees {
				if err := os.MkdirAll(feature.WorktreesDir(stateRoot), 0o755); err != nil {
					t.Fatalf("mkdir worktrees: %v", err)
				}
			}
			got, sandboxed, reason, cleanup := maybeWrapHelperSandbox(cmd, tc.enabled, stateRoot)
			defer cleanup()
			if sandboxed || strings.Join(got, " ") != "helper run" {
				t.Fatalf("got=%v sandboxed=%v, want unchanged [helper run]", got, sandboxed)
			}
			if tc.wantReason == "" && reason != "" {
				t.Fatalf("reason = %q, want none", reason)
			}
			if !strings.Contains(reason, tc.wantReason) || (tc.wantReason != "" && !strings.Contains(reason, feature.WorktreesDir(stateRoot))) {
				t.Fatalf("reason = %q, want it to name %s and contain %q", reason, feature.WorktreesDir(stateRoot), tc.wantReason)
			}
		})
	}
}
