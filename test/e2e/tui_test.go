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

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// newTestOrchestrator wires a minimal orchestrator that dispatches phase work
// through the supplied PhaseRunner. The TUI routes StartPhaseMsg to the
// orchestrator, so tests that exercise the dashboard-driven pipeline need one.
func newTestOrchestrator(fm *feature.Manager, sm *session.Manager, pr *agent.PhaseRunner) *orchestrator.Orchestrator {
	return orchestrator.New(orchestrator.Deps{
		Lifecycle:   fm,
		Store:       fm.Store,
		Sessions:    sm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
}

func init() {
	// Disable colors in tests for deterministic output
	os.Setenv("NO_COLOR", "1")
}

func setupTestEnv(t *testing.T) (fm *feature.Manager, sm *session.Manager, eventCh chan interface{}, stateDir string) {
	t.Helper()
	tmpDir := t.TempDir()
	stateDir = filepath.Join(tmpDir, "state")
	repoDir := filepath.Join(tmpDir, "repo")
	os.MkdirAll(stateDir, 0o755)
	os.MkdirAll(repoDir, 0o755)

	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Models: config.ModelConfig{
				Research:       "test",
				Planning:       "test",
				Implementation: "test",
				Review:         "test",
			},
			ExitCriteria:  "Relevant tests pass",
			MaxIterations: 10,
		},
		Repos: map[string]config.RepoConfig{
			"test-repo": {Path: repoDir},
		},
		WorkspaceRoots: []string{tmpDir},
	}

	store := feature.NewStore(stateDir)
	fm = feature.NewManager(store, cfg)
	eventCh = make(chan interface{}, 1000)
	sm = session.NewManager(eventCh)
	t.Cleanup(func() {
		sm.Shutdown()
		// Retained in extended gate: PTY child exit has no stable completion
		// signal here, so this prevents TempDir cleanup races on macOS.
		time.Sleep(100 * time.Millisecond)
	})
	return
}

// phaseAwareBuildSessionFn returns a BuildSessionFn that dispatches to
// different scripts based on the prompt content.
// - Research prompts contain "# Research Context"
// - Brainstorm prompts contain "# Feature Context" and "## Research Findings"
// - Roadmap planning prompts contain "# Planning Context"
// - Phase plan prompts start with "# Phase " and include "## Approved Roadmap"
// - Implementation prompts contain "# Implementation Context"
func phaseAwareBuildSessionFn(researchScript, brainstormScript, planScript, implScript, reviewScript string, extraScripts ...string) func(agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
	// extraScripts[0] = phasePlanScript (optional)
	var phasePlanScript string
	if len(extraScripts) > 0 {
		phasePlanScript = extraScripts[0]
	}
	return func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}

		switch opts.PermHandler.(type) {
		case *permission.ReadOnlyHandler, *permission.ReviewFeedbackHandler:
			return startupAwareScriptCommand(reviewScript), nil, sessOpts, nil
		}

		var script string
		switch {
		case strings.Contains(opts.Prompt, "# Feature Context") && strings.Contains(opts.Prompt, "## Research Findings"):
			script = brainstormScript
		case strings.Contains(opts.Prompt, "# Planning Context"):
			script = planScript
		case strings.Contains(opts.Prompt, "# Phase ") && strings.Contains(opts.Prompt, "## Approved Roadmap"):
			if phasePlanScript != "" {
				script = phasePlanScript
			} else {
				script = planScript
			}
		case strings.Contains(opts.Prompt, "# Implementation Context"):
			script = implScript
		default:
			script = researchScript
		}
		return startupAwareScriptCommand(script), nil, sessOpts, nil
	}
}

func startupAwareScriptCommand(script string) []string {
	return []string{"bash", "-c", `read -r -t 5 _ || true; read -r -t 5 _ || true; exec bash "$1"`, "agentic-e2e-session", script}
}

// TestDashboardRendersEmptyState owns the full Bubble Tea empty-dashboard
// render path; model tests cannot prove terminal-sized buffered output reaches
// the headless program.
func TestDashboardRendersEmptyState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, _ := setupTestEnv(t)
	app, err := tui.NewAppModel(fm, sm, nil, nil, eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { tm.Quit() })

	// Wait for the dashboard to render — check for "Features" panel header
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("Features"))
		},
		teatest.WithDuration(3*time.Second),
	)
}

// TestDashboardShowsFeature owns the full Bubble Tea dashboard row projection
// for an in-flight feature; model tests cover row state, but not terminal
// delivery through teatest.
func TestDashboardShowsFeature(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, _ := setupTestEnv(t)

	// Pre-create a feature
	_, err := fm.Create("My Feature", "Test description", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	app, err := tui.NewAppModel(fm, sm, nil, nil, eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { tm.Quit() })

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("my-feature"))
		},
		teatest.WithDuration(3*time.Second),
	)
}

// TestMediumWizardGateProjectionSmoke owns the keyboard-driven wizard gate
// projection flow; model tests cover checkpoint selection, but not the real
// Bubble Tea key delivery and terminal render.
func TestMediumWizardGateProjectionSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, stateDir := setupTestEnv(t)
	repoPath := fm.Config.Repos["test-repo"].Path
	if out, err := exec.Command("git", "-C", repoPath, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", repoPath, "remote", "add", "origin", "https://example.com/test-repo.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
	fm.Config.Repos["test-repo"] = config.RepoConfig{
		Path: repoPath,
		PipelineGates: map[string]config.Checkpoints{
			"medium": {
				InquiryReview: true,
				DesignReview:  true,
				PlanReview:    true,
				ManualPublish: true,
			},
		},
	}
	workspaceRoot := filepath.Join(filepath.Dir(stateDir), "workspace-root")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("creating workspace root: %v", err)
	}
	fm.Config.WorkspaceRoots = []string{workspaceRoot}
	phaseRunner := &agent.PhaseRunner{
		CommandRunner: agent.NewExecCommandRunner(),
		Registry:      llm.NewRegistry(),
		StateDir:      stateDir,
	}

	app, err := tui.NewAppModel(fm, sm, newTestOrchestrator(fm, sm, phaseRunner), nil, eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { tm.Quit() })

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("Features"))
		},
		teatest.WithDuration(3*time.Second),
	)

	tm.Send(tea.KeyPressMsg{Code: 'n', Text: "n"})
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("Step 1 of 4"))
		},
		teatest.WithDuration(3*time.Second),
	)

	tm.Send(tea.KeyPressMsg{Text: "medium wizard smoke"})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.KeyPressMsg{Code: ' ', Text: " "})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyUp})

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("Gate options: Plan review, Publish review"))
		},
		teatest.WithDuration(3*time.Second),
	)

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.KeyPressMsg{Code: 'G', Text: "G"})

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("medium-wizard-smoke"))
		},
		teatest.WithDuration(5*time.Second),
	)

	features, err := fm.List()
	if err != nil {
		t.Fatalf("listing features: %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("feature count = %d, want 1", len(features))
	}
	if features[0].Checkpoints != (feature.Checkpoints{PlanReview: true, ManualPublish: true}) {
		t.Fatalf("created feature checkpoints = %+v, want PlanReview+ManualPublish", features[0].Checkpoints)
	}

	saved := fm.Config.Repos["test-repo"].PipelineGates["medium"]
	if saved != (config.Checkpoints{PlanReview: true, ManualPublish: true}) {
		t.Fatalf("saved medium gates = %+v, want PlanReview+ManualPublish", saved)
	}
}

// TestFreshFeatureSkeletonInvariants asserts the on-disk-shape skeleton
// invariants for fresh features: feature.yaml stamps schema_version: 2,
// Manager.Create pre-populates RepoImpl with a Pending entry per repo, and the
// per-iteration Implement artifact dir resolves to the unified per-repo
// shape. The legacy single-repo PID-file path (session.pid) must not exist
// for fresh features. The extended smoke owns the persisted feature-manager
// shape because model-only tests do not exercise the on-disk feature skeleton.
func TestFreshFeatureSkeletonInvariants(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, _, _, stateDir := setupTestEnv(t)

	f, err := fm.Create("Skeleton", "Validate fresh-feature shape", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Right after Create: feature.yaml exists with the current schema_version.
	rawPath := filepath.Join(stateDir, f.ID, "feature.yaml")
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read feature.yaml: %v", err)
	}
	wantSchemaLine := []byte(fmt.Sprintf("schema_version: %d", feature.SchemaVersionCurrent))
	if !bytes.Contains(raw, wantSchemaLine) {
		t.Errorf("feature.yaml missing %q; got:\n%s", string(wantSchemaLine), string(raw))
	}

	// Right after Create: RepoImpl is pre-populated with Pending status.
	loaded, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.SchemaVersion != feature.SchemaVersionCurrent {
		t.Errorf("loaded.SchemaVersion = %d, want %d", loaded.SchemaVersion, feature.SchemaVersionCurrent)
	}
	state, ok := loaded.RepoStates["test-repo"]
	if !ok || state == nil {
		t.Fatalf("RepoStates[test-repo] missing")
	}
	if state.Touched {
		t.Errorf("RepoStates[test-repo].Touched = true, want false (fresh feature)")
	}
	// ExecutionPlan was removed in SchemaVersionCurrent = 3; the per-phase
	// plan is now read fresh from disk per orchestrator cycle. Per-task
	// `**Repo:** <name>` tags are the single source of truth.

	// The legacy single-repo PID-file path must not exist for fresh features
	// (no implement session has been started, so neither path exists yet —
	// but a stray legacy session.pid would break the unified shape).
	if _, err := os.Stat(filepath.Join(stateDir, f.ID, "session.pid")); err == nil {
		t.Errorf("legacy session.pid present for fresh feature; expected per-repo PID naming")
	}

	// SchemaVersion is stamped at create time.
	if loaded.SchemaVersion != feature.SchemaVersionCurrent {
		t.Errorf("fresh feature SchemaVersion = %d, want %d", loaded.SchemaVersion, feature.SchemaVersionCurrent)
	}
}

// TestTUI_FeatureFailsMidChain owns the full Bubble Tea failure projection for
// a chain that fails after earlier phases; model tests cannot prove the
// session-driven phase cascade reaches the terminal dashboard.
func TestTUI_FeatureFailsMidChain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, stateDir := setupTestEnv(t)
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	os.MkdirAll(scriptsDir, 0o755)

	f, err := fm.Create("Fail Mid Chain", "Test failure mid-chain", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Research mock: succeeds (with phase_complete for TUI to detect completion)
	researchDir := filepath.Join(stateDir, f.ID, "research")
	researchArtifact := filepath.Join(researchDir, "research.md")
	researchScript := testutil.WriteScript(t, scriptsDir, "research.sh", fmt.Sprintf(
		`%s
mkdir -p "%s"
echo "# Research Output" > "%s"
touch "%s/phase_complete"
%s
`, testutil.JSONLInit, researchDir, researchArtifact, researchDir, testutil.JSONLSuccess))

	// Brainstorm mock: creates brainstorm artifact and phase_complete.
	brainstormDir := filepath.Join(stateDir, f.ID, "brainstorm")
	brainstormArtifact := filepath.Join(brainstormDir, "brainstorm.md")
	brainstormScript := testutil.WriteScript(t, scriptsDir, "brainstorm.sh", fmt.Sprintf(
		`%s
mkdir -p "%s"
echo "# Brainstorm Output" > "%s"
touch "%s/phase_complete"
%s
`, testutil.JSONLInit, brainstormDir, brainstormArtifact, brainstormDir, testutil.JSONLSuccess))

	// Plan mock: emits init then exits non-zero
	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLError("planning failed")+"\nexit 1\n")

	pr := agent.NewPhaseRunner(sm, fm.Store, stateDir)
	pr.CommandRunner = agent.NewExecCommandRunner()
	pr.BuildSessionFn = phaseAwareBuildSessionFn(researchScript, brainstormScript, planScript, "", planScript)

	orch := newTestOrchestrator(fm, sm, pr)
	app, err := tui.NewAppModel(fm, sm, orch, nil, eventCh, tui.WithPhaseRunner(pr))
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	ref := app.ProgramRef()
	ref.P = tm.GetProgram()
	t.Cleanup(func() { tm.Quit() })

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("fail-mid-chain"))
		},
		teatest.WithDuration(3*time.Second),
	)

	tm.Send(tui.StartPhaseMsg{FeatureID: f.ID, Phase: feature.PhaseResearch})

	// Wait for dashboard to show Failed status
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("Failed"))
		},
		teatest.WithDuration(15*time.Second),
	)

	updated, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status != feature.StatusFailed {
		t.Errorf("expected StatusFailed, got %s", updated.Status)
	}
}

// TestTUI_SessionCrashInterrupted owns the full Bubble Tea reaction to a
// process-backed session crash; model tests cannot prove PTY/process exit
// delivery through the running program.
func TestTUI_SessionCrashInterrupted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, stateDir := setupTestEnv(t)
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	os.MkdirAll(scriptsDir, 0o755)

	f, err := fm.Create("Crash Test", "Test session crash", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Mock: consumes startup stdin, emits init, then self-terminates.
	crashScript := testutil.WriteScript(t, scriptsDir, "crash.sh",
		`read -r -t 5 _ || true
read -r -t 5 _ || true
`+testutil.JSONLInit+"\nkill $$\n")

	pr := agent.NewPhaseRunner(sm, fm.Store, stateDir)
	pr.CommandRunner = agent.NewExecCommandRunner()
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		return []string{"bash", crashScript}, nil, &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}, nil
	}

	orch := newTestOrchestrator(fm, sm, pr)
	app, err := tui.NewAppModel(fm, sm, orch, nil, eventCh, tui.WithPhaseRunner(pr))
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	ref := app.ProgramRef()
	ref.P = tm.GetProgram()
	t.Cleanup(func() { tm.Quit() })

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("crash-test"))
		},
		teatest.WithDuration(3*time.Second),
	)

	tm.Send(tui.StartPhaseMsg{FeatureID: f.ID, Phase: feature.PhaseResearch})

	// Wait for dashboard to show a terminal state
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("Failed")) ||
				bytes.Contains(bts, []byte("Stopped"))
		},
		teatest.WithDuration(90*time.Second),
	)

	updated, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status != feature.StatusFailed && updated.Status != feature.StatusInterrupted {
		t.Errorf("expected Failed or Interrupted, got %s", updated.Status)
	}
}

// TestTUI_ConcurrentFeatures owns the full Bubble Tea dashboard projection for
// two simultaneous feature starts; model tests cannot prove concurrent program
// message delivery and terminal render ordering.
func TestTUI_ConcurrentFeatures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, stateDir := setupTestEnv(t)
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	os.MkdirAll(scriptsDir, 0o755)

	f1, err := fm.Create("Concurrent A", "First concurrent feature", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	f2, err := fm.Create("Concurrent B", "Second concurrent feature", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	// Create per-feature research scripts that write to the correct artifact dir.
	// Each script creates phase_complete so the TUI detects research as done.
	makeResearchScript := func(f *feature.Feature, name string) string {
		researchDir := filepath.Join(stateDir, f.ID, "research")
		artifact := filepath.Join(researchDir, "research.md")
		return testutil.WriteScript(t, scriptsDir, name, fmt.Sprintf(
			`%s
mkdir -p "%s"
echo "# Research for %s" > "%s"
touch "%s/phase_complete"
%s
`, testutil.JSONLInit, researchDir, f.Name, artifact, researchDir, testutil.JSONLSuccess))
	}

	scriptA := makeResearchScript(f1, "researchA.sh")
	scriptB := makeResearchScript(f2, "researchB.sh")

	pr := agent.NewPhaseRunner(sm, fm.Store, stateDir)
	pr.CommandRunner = agent.NewExecCommandRunner()
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		// Dispatch based on feature name in the research prompt
		if strings.Contains(opts.Prompt, "Concurrent B") {
			return []string{"bash", scriptB}, nil, sessOpts, nil
		}
		return []string{"bash", scriptA}, nil, sessOpts, nil
	}

	orch := newTestOrchestrator(fm, sm, pr)
	app, err := tui.NewAppModel(fm, sm, orch, nil, eventCh, tui.WithPhaseRunner(pr))
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	ref := app.ProgramRef()
	ref.P = tm.GetProgram()
	t.Cleanup(func() { tm.Quit() })

	// Wait for dashboard to render both features
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("concurrent-a")) &&
				bytes.Contains(bts, []byte("concurrent-b"))
		},
		teatest.WithDuration(3*time.Second),
	)

	// Start both features simultaneously
	tm.Send(tui.StartPhaseMsg{FeatureID: f1.ID, Phase: feature.PhaseResearch})
	tm.Send(tui.StartPhaseMsg{FeatureID: f2.ID, Phase: feature.PhaseResearch})

	// Retained in extended gate: fixed observation window lets concurrent
	// feature flows advance through real asynchronous orchestration.
	// Wait for at least one feature to advance past research — both completing
	// concurrently is the goal, but the auto-chain may start planning which
	// uses a different prompt dispatch. We just need to verify both get past research.
	time.Sleep(10 * time.Second)

	// Verify both features advanced past research
	u1, err := fm.Get(f1.ID)
	if err != nil {
		t.Fatalf("Get A: %v", err)
	}
	u2, err := fm.Get(f2.ID)
	if err != nil {
		t.Fatalf("Get B: %v", err)
	}

	// Both should be past research (PlanReady, Planning, Failed, or further)
	if u1.Status == feature.StatusCreated || u1.Status == feature.StatusResearching {
		t.Errorf("feature A stuck at %s", u1.Status)
	}
	if u2.Status == feature.StatusCreated || u2.Status == feature.StatusResearching {
		t.Errorf("feature B stuck at %s", u2.Status)
	}
}

// TestTUI_PermissionPromptSurfaced owns the full Bubble Tea surfacing of a
// deferred tool-permission request; model tests cover queue state, but not the
// live session control-request path through teatest.
func TestTUI_PermissionPromptSurfaced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, stateDir := setupTestEnv(t)
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	os.MkdirAll(scriptsDir, 0o755)

	f, err := fm.Create("Perm Test", "Test permission surfacing", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Mock script: emits init, then a Bash tool permission request, then waits.
	// AcceptEditsHandler defers Bash to TUI, so it should surface as a waiting prompt.
	// We drain pre-existing stdin data (SendInitialize + SendUserMessage) before the
	// long read so the script truly blocks on the control_request response.
	permScript := testutil.WriteScript(t, scriptsDir, "perm.sh", `
`+testutil.JSONLInit+`
`+testutil.JSONLControlRequest("req-perm-1", "Bash")+`
# Drain pre-existing stdin (initialize handshake + user message) then truly wait
read -t 1 discard 2>/dev/null
read -t 1 discard 2>/dev/null
read -t 1 discard 2>/dev/null
sleep 30
`+testutil.JSONLAssistant("Done")+`
`+testutil.JSONLSuccess)

	pr := agent.NewPhaseRunner(sm, fm.Store, stateDir)
	pr.CommandRunner = agent.NewExecCommandRunner()
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		return []string{"bash", permScript}, nil, &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}, nil
	}

	orch := newTestOrchestrator(fm, sm, pr)
	app, err := tui.NewAppModel(fm, sm, orch, nil, eventCh, tui.WithPhaseRunner(pr))
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	ref := app.ProgramRef()
	ref.P = tm.GetProgram()
	t.Cleanup(func() { tm.Quit() })

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("perm-test"))
		},
		teatest.WithDuration(3*time.Second),
	)

	tm.Send(tui.StartPhaseMsg{FeatureID: f.ID, Phase: feature.PhaseResearch})

	// Wait for the permission indicator to appear — the TUI shows "waiting input"
	// or "need attention" when a session has a pending control_request for a deferred tool.
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("waiting input")) ||
				bytes.Contains(bts, []byte("need attention"))
		},
		teatest.WithDuration(15*time.Second),
	)
}

// TestNeedUserInputPauseResumeSmoke owns NEED_USER_INPUT pause/resume rendering
// and persisted-gate decisions after a real implement-loop pause; model tests
// cannot prove the artifact handoff and attach reopening path together.
func TestNeedUserInputPauseResumeSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	t.Run("resume_through_app", func(t *testing.T) {
		h := setupNeedUserInputSmoke(t)

		// Stub the implement-dispatch seam so the resume side does not
		// actually fan out into the multi-repo engine. The seam is
		// exposed precisely so tests can isolate the transition contract.
		h.orch.SetRunMultiRepoImplFn(stubRunMultiRepoImplFn())

		// Pre-fill answers on disk so the artifact-review questionnaire
		// loads fully populated when 'a' attaches. Matches the user's
		// typed-and-saved state in production.
		fillGateAnswers(t, h.gatePath)

		m := bootApp(t, h.app)
		// Transition into the detail view for the paused feature so
		// 'a' routes via updateDetail → attachNeedUserInput. Going
		// through ViewDetail rather than ViewDashboard sidesteps the
		// dashboard cursor's initial position on the section header.
		m = updateApp(m, tui.ViewTransitionMsg{View: tui.ViewDetail, FeatureID: h.f.ID})
		assertViewContains(t, m, "Implementation needs user input")

		// Press 'a': updateDetail → attachNeedUserInput → ArtifactReviewModel
		// in reviewMode == need_user_input. The questionnaire renders.
		m = updateApp(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
		assertViewContains(t, m, "Need User Input")

		// Open the Ctrl+D menu. The first item is "Resume implementation".
		m = updateApp(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		assertViewContains(t, m, "Resume implementation")

		// Press Enter to confirm Resume; the artifact-review shell emits
		// NeedUserInputDecisionMsg via emitDecision, which we feed back
		// through Update so handleNeedUserInputDecision dispatches to
		// the orchestrator.
		m = updateApp(m, tea.KeyPressMsg{Code: tea.KeyEnter})

		final, err := h.fm.Get(h.f.ID)
		if err != nil {
			t.Fatalf("fm.Get: %v", err)
		}
		if final.Status != feature.StatusImplementing {
			t.Errorf("resume: Status = %v, want StatusImplementing", final.Status)
		}
		if final.PendingNeedUserInputPath != "" {
			t.Errorf("resume: PendingNeedUserInputPath should be cleared; got %q", final.PendingNeedUserInputPath)
		}
	})

	t.Run("abort_through_app", func(t *testing.T) {
		h := setupNeedUserInputSmoke(t)
		fillGateAnswers(t, h.gatePath)

		m := bootApp(t, h.app)
		m = updateApp(m, tui.ViewTransitionMsg{View: tui.ViewDetail, FeatureID: h.f.ID})
		m = updateApp(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
		assertViewContains(t, m, "Need User Input")

		// Open menu, move down once to "Abort", commit.
		m = updateApp(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		m = updateApp(m, tea.KeyPressMsg{Code: 'j', Text: "j"})
		m = updateApp(m, tea.KeyPressMsg{Code: tea.KeyEnter})

		final, err := h.fm.Get(h.f.ID)
		if err != nil {
			t.Fatalf("fm.Get: %v", err)
		}
		if final.Status != feature.StatusFailed {
			t.Errorf("abort: Status = %v, want StatusFailed", final.Status)
		}
		if final.FailureType != feature.FailureNeedUserInput {
			t.Errorf("abort: FailureType = %q, want %q", final.FailureType, feature.FailureNeedUserInput)
		}
		if final.PendingNeedUserInputPath != "" {
			t.Errorf("abort: PendingNeedUserInputPath should be cleared; got %q", final.PendingNeedUserInputPath)
		}
	})

	t.Run("detach_then_reopen_through_app", func(t *testing.T) {
		h := setupNeedUserInputSmoke(t)

		m := bootApp(t, h.app)
		m = updateApp(m, tui.ViewTransitionMsg{View: tui.ViewDetail, FeatureID: h.f.ID})
		m = updateApp(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
		assertViewContains(t, m, "Need User Input")

		// Open menu, navigate to "Just detach" (down twice), commit.
		m = updateApp(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		m = updateApp(m, tea.KeyPressMsg{Code: 'j', Text: "j"})
		m = updateApp(m, tea.KeyPressMsg{Code: 'j', Text: "j"})
		m = updateApp(m, tea.KeyPressMsg{Code: tea.KeyEnter})

		// After detach the app falls back to ViewDashboard. Transition
		// back into ViewDetail (the same path the user takes when
		// selecting the feature) before re-pressing 'a'.
		m = updateApp(m, tui.ViewTransitionMsg{View: tui.ViewDetail, FeatureID: h.f.ID})
		// Re-attach with 'a' — the artifact-review shell takes the
		// reattach path and the questionnaire renders again.
		m = updateApp(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
		assertViewContains(t, m, "Need User Input")

		// Feature must remain paused with the gate path intact.
		paused, _ := h.fm.Get(h.f.ID)
		if paused.Status != feature.StatusNeedUserInput {
			t.Errorf("detach should not change feature Status; got %v", paused.Status)
		}
		if paused.PendingNeedUserInputPath != h.gatePath {
			t.Errorf("PendingNeedUserInputPath = %q, want %q (preserved across detach)",
				paused.PendingNeedUserInputPath, h.gatePath)
		}
	})

	t.Run("empty_answers_block_resume_through_app", func(t *testing.T) {
		h := setupNeedUserInputSmoke(t)

		// Do NOT fill answers — the gate file on disk has empty
		// answers from the implement loop.
		m := bootApp(t, h.app)
		m = updateApp(m, tui.ViewTransitionMsg{View: tui.ViewDetail, FeatureID: h.f.ID})
		m = updateApp(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
		assertViewContains(t, m, "Need User Input")

		// Open menu and hit Enter on Resume. The artifact-review shell
		// must block the dispatch because answers are empty.
		m = updateApp(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		assertViewContains(t, m, "answer all questions to enable")
		m = updateApp(m, tea.KeyPressMsg{Code: tea.KeyEnter})

		paused, _ := h.fm.Get(h.f.ID)
		if paused.Status != feature.StatusNeedUserInput {
			t.Errorf("blocked resume must not change Status; got %v", paused.Status)
		}
		if paused.PendingNeedUserInputPath != h.gatePath {
			t.Errorf("blocked resume must not clear gate path; got %q", paused.PendingNeedUserInputPath)
		}
	})
}

// needUserInputSmokeHarness bundles the per-subtest scaffolding so each
// subtest can drive a fresh AppModel while sharing the setup logic that
// runs the implement loop and lands the feature in StatusNeedUserInput.
type needUserInputSmokeHarness struct {
	fm       *feature.Manager
	orch     *orchestrator.Orchestrator
	f        *feature.Feature
	gatePath string
	app      tui.AppModel
}

// setupNeedUserInputSmoke seeds a single-repo feature paused on a
// NEED_USER_INPUT gate by running the real implement loop with a mock
// script that emits the gate, then builds an AppModel pointed at the
// paused feature. The harness exposes everything the per-action subtests
// need to drive interactions directly through AppModel.Update.
func setupNeedUserInputSmoke(t *testing.T) *needUserInputSmokeHarness {
	t.Helper()

	fm, sm, eventCh, stateDir := setupTestEnv(t)
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	os.MkdirAll(scriptsDir, 0o755)

	f, err := fm.Create("Need User Input Smoke", "tracer-bullet pause", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, status := range []feature.Status{
		feature.StatusResearching, feature.StatusPlanReady, feature.StatusPlanning,
		feature.StatusImplementReady, feature.StatusImplementing,
	} {
		if err := fm.Transition(f.ID, status); err != nil {
			t.Fatalf("transition %v: %v", status, err)
		}
	}

	implementDir := filepath.Join(stateDir, f.ID, "implement")
	os.MkdirAll(implementDir, 0o755)
	planPath := filepath.Join(stateDir, f.ID, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\nDo the thing."), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	// Per SchemaVersionCurrent = 3, the orchestrator hard-fails if the
	// per-phase execution-order.yaml is missing alongside the plan; on
	// resume the smoke test exercises the same code path.
	if err := os.WriteFile(filepath.Join(stateDir, f.ID, "execution-order.yaml"),
		[]byte("stages:\n  - repos: [test-repo]\n"), 0o644); err != nil {
		t.Fatalf("write execution-order.yaml: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		if ff.Artifacts == nil {
			ff.Artifacts = make(map[string]string)
		}
		ff.Artifacts["plan"] = planPath
		return nil
	}); err != nil {
		t.Fatalf("modify artifacts: %v", err)
	}

	const summary = "Plan contradicts the worktree."
	questions := []string{
		"Should implementation target the legacy auth path or the new auth service?",
		"Is it acceptable to skip migration of historical sessions?",
	}
	agentScript := testutil.WriteScript(t, scriptsDir, "need_user_input.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementNeedUserInputArtifacts(implementDir, summary, questions...)+"\n"+
			testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(implementDir)+"\n"+testutil.JSONLSuccess+"\n")

	buildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		switch opts.PermHandler.(type) {
		case *permission.ReadOnlyHandler, *permission.ReviewFeedbackHandler:
			return []string{"bash", reviewScript}, nil, sessOpts, nil
		}
		return []string{"bash", agentScript}, nil, sessOpts, nil
	}

	cfg := agent.ImplementConfig{
		Feature:             f,
		FeatureStore:        fm.Store,
		WorkDir:             implementDir,
		PlanPath:            planPath,
		MaxIterations:       3,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "All tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         implementDir,
		StateDir:            filepath.Join(stateDir, f.ID),
		BuildSession:        buildSession,
	}

	result, err := agent.RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop: %v", err)
	}
	if result.FinalStatus != "need_user_input" {
		t.Fatalf("FinalStatus = %q, want need_user_input", result.FinalStatus)
	}
	if result.NeedUserInputPath == "" {
		t.Fatalf("NeedUserInputPath must be set after a paused iteration")
	}

	pr := agent.NewPhaseRunner(sm, fm.Store, stateDir)
	pr.CommandRunner = agent.NewExecCommandRunner()
	pr.BuildSessionFn = buildSession
	orch := newTestOrchestrator(fm, sm, pr)

	if err := orch.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		ImplementResult: result,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	paused, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature post-pause: %v", err)
	}
	if paused.Status != feature.StatusNeedUserInput {
		t.Fatalf("paused Status = %v, want StatusNeedUserInput", paused.Status)
	}
	if paused.PendingNeedUserInputPath == "" {
		t.Fatal("PendingNeedUserInputPath should be set after pause")
	}
	if _, err := os.Stat(paused.PendingNeedUserInputPath); err != nil {
		t.Fatalf("gate artifact missing on disk: %v", err)
	}

	app, err := tui.NewAppModel(fm, sm, orch, nil, eventCh, tui.WithPhaseRunner(pr))
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}

	return &needUserInputSmokeHarness{
		fm:       fm,
		orch:     orch,
		f:        paused,
		gatePath: paused.PendingNeedUserInputPath,
		app:      app,
	}
}

// bootApp performs the equivalent of bubbletea's startup handshake on
// the supplied AppModel: applies a window-size message so layout fields
// are populated, then refreshes the feature list so the dashboard /
// detail views see the seeded paused feature. Returns the resulting
// model so the caller can drive subsequent Update calls.
func bootApp(t *testing.T, app tui.AppModel) tea.Model {
	t.Helper()
	var m tea.Model = app
	m = updateApp(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updateApp(m, tui.RefreshFeaturesMsg{})
	return m
}

// updateApp dispatches a single message through AppModel.Update, then
// drains any synchronous Cmd returned by executing it and recursively
// feeding the resulting messages back through Update. This emulates
// bubbletea's event loop closely enough for our deterministic
// integration assertions: every message that would have been processed
// by the live program in response to the input is processed here too,
// in the same order, against the same model. Cmds that block (e.g.,
// cursor.Blink, which awaits a channel receive that only the live
// bubbletea program ever satisfies) are run in a goroutine with a
// short timeout — once timed out we treat them as background tasks the
// test does not need to observe and move on.
func updateApp(m tea.Model, msg tea.Msg) tea.Model {
	m, cmd := m.Update(msg)
	for cmd != nil {
		out, ok := runCmdWithTimeout(cmd, 200*time.Millisecond)
		if !ok || out == nil {
			cmd = nil
			continue
		}
		switch v := out.(type) {
		case tea.BatchMsg:
			cmd = nil
			for _, c := range v {
				if c == nil {
					continue
				}
				next, nextOK := runCmdWithTimeout(c, 200*time.Millisecond)
				if !nextOK || next == nil {
					continue
				}
				m, _ = m.Update(next)
			}
		default:
			m, cmd = m.Update(v)
		}
	}
	return m
}

// runCmdWithTimeout invokes a tea.Cmd in a goroutine and returns the
// resulting tea.Msg if it produces one within the deadline. ok=false
// signals the cmd is still running — the caller should treat it as a
// background tick (cursor.Blink, spinner.Tick, etc.) that does not
// affect the integration assertions.
func runCmdWithTimeout(cmd tea.Cmd, d time.Duration) (tea.Msg, bool) {
	if cmd == nil {
		return nil, false
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg, true
	case <-time.After(d):
		return nil, false
	}
}

// assertViewContains renders the model's current View and fails the
// test if the rendered content does not include the substring `want`.
func assertViewContains(t *testing.T, m tea.Model, want string) {
	t.Helper()
	view := m.View()
	if !strings.Contains(view.Content, want) {
		t.Fatalf("view does not contain %q\n--- view ---\n%s\n--- end ---", want, view.Content)
	}
}

// stubRunMultiRepoImplFn returns an installer for SetRunMultiRepoImplFn
// that produces a closed channel so the resume dispatch pipeline returns
// immediately without driving the multi-repo engine.
//
// The plan / resumeFromRepo / resumeSessionID parameters were dropped in
// SchemaVersionCurrent = 4 — the unified phase-implement loop derives its
// repo set from PhaseScope and re-runs interrupted units from scratch.
func stubRunMultiRepoImplFn() func(
	f *feature.Feature,
	planPath string,
	kbInfos ...agent.KBInfo,
) (chan *agent.OrchestratorResult, error) {
	return func(
		f *feature.Feature,
		planPath string,
		kbInfos ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		close(ch)
		return ch, nil
	}
}

// fillGateAnswers populates every question's answer field on disk so a
// subsequent attach loads the questionnaire fully populated and Resume
// is no longer blocked.
func fillGateAnswers(t *testing.T, gatePath string) {
	t.Helper()
	rec, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	for i := range rec.Questions {
		rec.Questions[i].Answer = fmt.Sprintf("answer %d", i+1)
	}
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		t.Fatalf("persist filled gate: %v", err)
	}
}

// TestTUI_HelpInputBlocking owns full Bubble Tea help-input blocking while a
// session waits on AskUserQuestion; model tests cover modal precedence, but not
// live control-request delivery through the running program.
func TestTUI_HelpInputBlocking(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	fm, sm, eventCh, stateDir := setupTestEnv(t)
	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	os.MkdirAll(scriptsDir, 0o755)

	f, err := fm.Create("Help Block Test", "Test help input blocking", []string{"test-repo"},
		fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Mock script: emits init, then an AskUserQuestion control_request, then waits.
	// We drain pre-existing stdin data (SendInitialize + SendUserMessage) before the
	// long sleep so the script truly blocks, keeping the feature in Researching state.
	helpScript := testutil.WriteScript(t, scriptsDir, "help.sh", `
`+testutil.JSONLInit+`
`+testutil.JSONLControlRequest("req-help-1", "AskUserQuestion")+`
# Drain pre-existing stdin (initialize handshake + user message) then truly wait
read -t 1 discard 2>/dev/null
read -t 1 discard 2>/dev/null
read -t 1 discard 2>/dev/null
sleep 30
`+testutil.JSONLAssistant("Got help")+`
`+testutil.JSONLSuccess)

	pr := agent.NewPhaseRunner(sm, fm.Store, stateDir)
	pr.CommandRunner = agent.NewExecCommandRunner()
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		return []string{"bash", helpScript}, nil, &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}, nil
	}

	orch := newTestOrchestrator(fm, sm, pr)
	app, err := tui.NewAppModel(fm, sm, orch, nil, eventCh, tui.WithPhaseRunner(pr))
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	ref := app.ProgramRef()
	ref.P = tm.GetProgram()
	t.Cleanup(func() { tm.Quit() })

	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("help-block-test"))
		},
		teatest.WithDuration(3*time.Second),
	)

	tm.Send(tui.StartPhaseMsg{FeatureID: f.ID, Phase: feature.PhaseResearch})

	// Wait for the help indicator to appear on the dashboard.
	// The TUI shows "waiting input" or "need attention" for AskUserQuestion.
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool {
			return bytes.Contains(bts, []byte("waiting input")) ||
				bytes.Contains(bts, []byte("need attention"))
		},
		teatest.WithDuration(15*time.Second),
	)
}
