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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agentdef"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/skilldef"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestPhaseRunnerGetPhaseOutput(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	f := &feature.Feature{ID: "test-feat", ActiveRun: 1, RunCount: 1}

	// No output yet
	output := pr.GetPhaseOutput(f, "research")
	if output != "" {
		t.Errorf("expected empty output, got %q", output)
	}

	// Write some output — run-aware path.
	researchDir := filepath.Join(ActiveRunDir(dir, f), "research")
	if err := os.MkdirAll(researchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(researchDir, "output.txt"), []byte("research findings\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output = pr.GetPhaseOutput(f, "research")
	if output != "research findings" {
		t.Errorf("expected 'research findings', got %q", output)
	}
}

func TestNewPhaseRunner(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	if pr.SessionManager != sm {
		t.Error("expected session manager to match")
	}
	if pr.FeatureStore != store {
		t.Error("expected feature store to match")
	}
	if pr.StateDir != dir {
		t.Errorf("state dir = %q, want %q", pr.StateDir, dir)
	}
}

func TestPhaseRunnerRunResearchBuildPrompt(t *testing.T) {
	// Research is driven by an Inquire-produced questions file, not the
	// feature description, so the only assertions worth making at this layer
	// are that the prompt is non-empty and references the questions path.
	f := &feature.Feature{
		ID:          "feat-1",
		Name:        "Test Feature",
		Description: "A test feature",
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: "/nonexistent/path"},
		},
		Models: config.ModelConfig{
			Research: "opus",
		},
	}

	prompt := BuildResearchFromQuestionsPrompt(f, "", "/state/feat-1/inquire/questions.md")

	if prompt == "" {
		t.Error("expected non-empty research prompt")
	}
	if !phaseContains(prompt, "/state/feat-1/inquire/questions.md") {
		t.Error("expected questions path in prompt")
	}
}

func TestBuildResearchPhaseSession_ClaudeProvider(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	// Record what args the mock builder was called with
	var calledModel, calledPrompt string
	var calledAdditionalDirs []string

	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		calledModel = opts.Model
		calledPrompt = opts.Prompt
		calledAdditionalDirs = opts.AdditionalDirs
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		return []string{"claude", "--model", opts.Model}, nil, sessOpts, nil
	}

	prompt := "research this codebase"
	pidDir := filepath.Join(dir, "test-feat")

	cmd, _, opts, err := pr.BuildSession(BuildSessionOpts{
		Model:          "opus",
		Prompt:         prompt,
		AdditionalDirs: []string{dir},
		PIDDir:         pidDir,
		WorkDir:        dir,
	})
	if err != nil {
		t.Fatalf("BuildSession error: %v", err)
	}

	// Verify the mock builder was called with the expected args
	if calledModel != "opus" {
		t.Errorf("expected model %q, got %q", "opus", calledModel)
	}
	if calledPrompt != prompt {
		t.Errorf("expected prompt %q, got %q", prompt, calledPrompt)
	}
	if len(calledAdditionalDirs) != 1 || calledAdditionalDirs[0] != dir {
		t.Errorf("expected additionalDirs [%q], got %v", dir, calledAdditionalDirs)
	}

	// Verify command came from our mock
	if len(cmd) != 3 || cmd[0] != "claude" || cmd[1] != "--model" || cmd[2] != "opus" {
		t.Errorf("expected cmd from mock builder, got %v", cmd)
	}

	if opts.InitialPrompt != prompt {
		t.Errorf("expected InitialPrompt %q, got %q", prompt, opts.InitialPrompt)
	}
}

// TestBuildResearchPhaseSession_MultiRepoAdditionalDirs verifies that
// multi-repo additionalDirs are forwarded to the interactive command builder,
// preventing a regression where multi-repo research phases lose access to
// sibling repos.
func TestBuildResearchPhaseSession_MultiRepoAdditionalDirs(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	var calledAdditionalDirs []string

	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		calledAdditionalDirs = opts.AdditionalDirs
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		return []string{"claude", "--model", opts.Model}, nil, sessOpts, nil
	}

	// Simulate multi-repo additionalDirs as resolveUnifiedWorkDir would return
	multiRepoDirs := []string{dir, "/tmp/worktrees/repo-a", "/tmp/worktrees/repo-b"}

	pr.BuildSession(BuildSessionOpts{
		Model:          "opus",
		Prompt:         "prompt",
		SystemPrompt:   "system",
		AdditionalDirs: multiRepoDirs,
		PIDDir:         filepath.Join(dir, "pid"),
		WorkDir:        dir,
	})

	if len(calledAdditionalDirs) != 3 {
		t.Fatalf("expected 3 additionalDirs, got %d: %v", len(calledAdditionalDirs), calledAdditionalDirs)
	}
	for i, want := range multiRepoDirs {
		if calledAdditionalDirs[i] != want {
			t.Errorf("additionalDirs[%d] = %q, want %q", i, calledAdditionalDirs[i], want)
		}
	}
}

func phaseContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestResolvePhaseArtifactDir(t *testing.T) {
	stateDir := "/tmp/state"
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(stateDir)
	pr := NewPhaseRunner(sm, store, stateDir)

	tests := []struct {
		name      string
		feature   *feature.Feature
		phaseName string
		want      string
	}{
		{
			name: "standard dir when not refactoring",
			feature: &feature.Feature{
				ID:             "feat-1",
				ActiveRun:      1,
				RunCount:       1,
				RefactorPrompt: "",
			},
			phaseName: "research",
			want:      filepath.Join(stateDir, "feat-1", "runs", "run-001", "research"),
		},
		{
			name: "standard dir when refactor count is zero",
			feature: &feature.Feature{
				ID:             "feat-1",
				ActiveRun:      1,
				RunCount:       1,
				RefactorPrompt: "",
			},
			phaseName: "plan",
			want:      filepath.Join(stateDir, "feat-1", "runs", "run-001", "plan"),
		},
		{
			name: "refactor-prefixed dir when refactoring",
			feature: func() *feature.Feature {
				f := &feature.Feature{
					ID:             "feat-1",
					ActiveRun:      1,
					RunCount:       1,
					RefactorPrompt: "refactor the API layer",
				}
				f.SetRefactorCount(1)
				return f
			}(),
			phaseName: "research",
			want:      filepath.Join(stateDir, "feat-1", "runs", "run-001", "refactor-1", "research"),
		},
		{
			name: "refactor-prefixed dir with higher count",
			feature: func() *feature.Feature {
				f := &feature.Feature{
					ID:             "feat-2",
					ActiveRun:      1,
					RunCount:       1,
					RefactorPrompt: "split into microservices",
				}
				f.SetRefactorCount(3)
				return f
			}(),
			phaseName: "implement",
			want:      filepath.Join(stateDir, "feat-2", "runs", "run-001", "refactor-3", "implement"),
		},
		{
			name: "no prefix when refactor count > 0 but prompt is empty",
			feature: func() *feature.Feature {
				f := &feature.Feature{
					ID:             "feat-1",
					ActiveRun:      1,
					RunCount:       1,
					RefactorPrompt: "",
				}
				f.SetRefactorCount(2)
				return f
			}(),
			phaseName: "brainstorm",
			want:      filepath.Join(stateDir, "feat-1", "runs", "run-001", "brainstorm"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pr.resolvePhaseArtifactDir(tt.feature, tt.phaseName)
			if got != tt.want {
				t.Errorf("resolvePhaseArtifactDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveUnifiedWorkDir(t *testing.T) {
	tests := []struct {
		name            string
		feature         *feature.Feature
		stateDir        string
		expectedWorkDir string
		expectedAddDirs []string
	}{
		{
			name: "no_repos",
			feature: &feature.Feature{
				Repos: nil,
			},
			stateDir:        "/tmp/state",
			expectedWorkDir: "/tmp/state",
			expectedAddDirs: []string{"/tmp/state"},
		},
		{
			name: "single_repo",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "repo-a", Path: "/tmp/repos/a"},
				},
			},
			stateDir:        "/tmp/state",
			expectedWorkDir: "/tmp/repos/a",
			expectedAddDirs: []string{"/tmp/state"},
		},
		{
			name: "multi_repo_with_worktrees",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "repo-a", Path: "/tmp/repos/a", WorktreePath: "/tmp/worktrees/a"},
					{Name: "repo-b", Path: "/tmp/repos/b", WorktreePath: "/tmp/worktrees/b"},
				},
			},
			stateDir:        "/tmp/state",
			expectedWorkDir: "/tmp/worktrees",
			expectedAddDirs: []string{"/tmp/state", "/tmp/worktrees/a", "/tmp/worktrees/b"},
		},
		{
			name: "multi_repo_no_worktrees",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "repo-a", Path: "/tmp/repos/a"},
					{Name: "repo-b", Path: "/tmp/repos/b"},
				},
			},
			stateDir:        "/tmp/state",
			expectedWorkDir: "/tmp/repos/a",
			expectedAddDirs: []string{"/tmp/state", "/tmp/repos/b"},
		},
		{
			name: "multi_repo_partial_worktrees",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "repo-a", Path: "/tmp/repos/a", WorktreePath: "/tmp/worktrees/a"},
					{Name: "repo-b", Path: "/tmp/repos/b"},
				},
			},
			stateDir:        "/tmp/state",
			expectedWorkDir: "/tmp/repos/a",
			expectedAddDirs: []string{"/tmp/state", "/tmp/repos/b"},
		},
		{
			name: "single_repo_fresh_with_worktree",
			feature: &feature.Feature{
				SchemaVersion: feature.SchemaVersionCurrent,
				Repos: []feature.FeatureRepo{
					{Name: "repo-a", Path: "/tmp/repos/a", WorktreePath: "/tmp/worktrees/a"},
				},
			},
			stateDir:        "/tmp/state",
			expectedWorkDir: "/tmp/worktrees",
			expectedAddDirs: []string{"/tmp/state", "/tmp/worktrees/a"},
		},
		{
			name: "single_repo_fresh_no_worktree",
			feature: &feature.Feature{
				SchemaVersion: feature.SchemaVersionCurrent,
				Repos: []feature.FeatureRepo{
					{Name: "repo-a", Path: "/tmp/repos/a"},
				},
			},
			stateDir:        "/tmp/state",
			expectedWorkDir: "/tmp/repos/a",
			expectedAddDirs: []string{"/tmp/state"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir, addDirs := resolveUnifiedWorkDir(tt.feature, tt.stateDir)
			if workDir != tt.expectedWorkDir {
				t.Errorf("workDir = %q, want %q", workDir, tt.expectedWorkDir)
			}
			if !reflect.DeepEqual(addDirs, tt.expectedAddDirs) {
				t.Errorf("additionalDirs = %v, want %v", addDirs, tt.expectedAddDirs)
			}
		})
	}
}

// TestRunPhasePlanning_UsesWorktreeParent is a regression test for a bug
// where per-phase planning ran in the base-repo clone (f.Repos[0].Path)
// instead of the feature worktree. That made the Grounding critic report
// it was on master, so every prior-phase symbol looked missing. Under the
// unified shape resolveUnifiedWorkDir returns the worktree's parent dir
// (so multi-repo planning can address every per-repo worktree from one
// CWD); the assertion is that the captured WorkDir is NOT the base repo
// path and IS rooted at the worktree subtree.
func TestRunPhasePlanning_UsesWorktreeParent(t *testing.T) {
	dir := t.TempDir()
	worktreeDir := filepath.Join(dir, "worktrees", "feature-branch", "repo")
	basePath := filepath.Join(dir, "repos", "base")
	for _, d := range []string{worktreeDir, basePath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	eventCh := make(chan any, 10)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()
	store := feature.NewStore(dir)

	// Return an error from BuildSession so the planning goroutine returns
	// quickly — we only care about the opts captured on the first call.
	var capturedWorkDir string
	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       dir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			if capturedWorkDir == "" {
				capturedWorkDir = opts.WorkDir
			}
			return nil, nil, nil, fmt.Errorf("stub: short-circuit")
		},
	}

	f := &feature.Feature{
		ID:            "feat-worktree",
		Name:          "test",
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: basePath, WorktreePath: worktreeDir},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	resultCh, err := pr.RunPhasePlanning(f, "", RoadmapPhase{Number: 1, Type: "tracer-bullet", Name: "Test"}, nil, nil)
	if err != nil {
		t.Fatalf("RunPhasePlanning: %v", err)
	}
	select {
	case <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("planning loop did not return")
	}

	wantWorkDir := filepath.Dir(worktreeDir)
	if capturedWorkDir != wantWorkDir {
		t.Errorf("RunPhasePlanning WorkDir = %q, want %q (worktree parent, not base repo path %q)",
			capturedWorkDir, wantWorkDir, basePath)
	}
	if capturedWorkDir == basePath {
		t.Errorf("RunPhasePlanning WorkDir leaked to base repo path %q", basePath)
	}
}

// capturedArgs holds the arguments captured by a capturing InteractiveCommandBuilder.
type capturedArgs struct {
	model          string
	systemPrompt   string
	prompt         string
	additionalDirs []string
}

func TestRunInteractivePhase_CommandStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tests := []struct {
		name          string
		featureID     string
		sessionSuffix string
		dirName       string
		commandName   string
		usesSkillRead bool // true for phases going through runInteractivePhase
		callPhase     func(pr *PhaseRunner, f *feature.Feature, tmpDir string) (string, error)
	}{
		{
			name:          "RunInquire",
			featureID:     "test-feat-inq",
			sessionSuffix: "-inquire",
			dirName:       "inquire",
			commandName:   "inquire",
			usesSkillRead: true,
			callPhase: func(pr *PhaseRunner, f *feature.Feature, tmpDir string) (string, error) {
				return pr.RunInquire(f)
			},
		},
		{
			name:          "RunResearchFromQuestions",
			featureID:     "test-feat-rfq",
			sessionSuffix: "-research",
			dirName:       "research",
			commandName:   "research-codebase",
			usesSkillRead: true,
			callPhase: func(pr *PhaseRunner, f *feature.Feature, tmpDir string) (string, error) {
				questionsPath := filepath.Join(tmpDir, "questions.md")
				os.WriteFile(questionsPath, []byte("# Questions\n- Q1?"), 0o644)
				return pr.RunResearchFromQuestions(f, questionsPath)
			},
		},
		{
			name:          "RunBrainstorm",
			featureID:     "test-feat-bs",
			sessionSuffix: "-brainstorm",
			dirName:       "brainstorm",
			commandName:   "brainstorm",
			usesSkillRead: true,
			callPhase: func(pr *PhaseRunner, f *feature.Feature, tmpDir string) (string, error) {
				return pr.RunBrainstorm(f, "", nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			stateDir := filepath.Join(tmpDir, "state")
			scriptsDir := filepath.Join(tmpDir, "scripts")
			for _, d := range []string{workDir, stateDir, scriptsDir} {
				os.MkdirAll(d, 0o755)
			}

			// read -r _line keeps the bash process alive until
			// SendInitialize writes the handshake JSON to stdin,
			// preventing a broken-pipe race.
			script := testutil.WriteScript(t, scriptsDir, "phase.sh",
				"read -r _line\n"+testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")

			var mu sync.Mutex
			var captured []capturedArgs
			buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
				mu.Lock()
				dirs := make([]string, len(opts.AdditionalDirs))
				copy(dirs, opts.AdditionalDirs)
				captured = append(captured, capturedArgs{model: opts.Model, systemPrompt: opts.SystemPrompt, prompt: opts.Prompt, additionalDirs: dirs})
				mu.Unlock()
				return []string{"bash", script}, nil, &session.SessionOpts{PIDDir: opts.PIDDir}, nil
			}

			eventCh := make(chan any, 100)
			sm := session.NewManager(eventCh)
			defer sm.Shutdown()

			store := feature.NewStore(stateDir)

			skillsDir := filepath.Join(tmpDir, "skills")
			os.MkdirAll(skillsDir, 0o755)

			pr := &PhaseRunner{
				SessionManager: sm,
				FeatureStore:   store,
				StateDir:       stateDir,
				SkillsDir:      skillsDir,
				BuildSessionFn: buildSessionFn,
			}

			f := &feature.Feature{
				ID:          tt.featureID,
				Name:        "Test Feature",
				Description: "test",
				ActiveRun:   1,
				RunCount:    1,
				Repos: []feature.FeatureRepo{
					{Name: "test-repo", Path: workDir},
				},
				Models: config.ModelConfig{
					Research: "test-model",
				},
			}

			sessionID, err := tt.callPhase(pr, f, tmpDir)
			if err != nil {
				t.Fatalf("phase method error: %v", err)
			}

			// Verify session ID format
			expectedSessionID := tt.featureID + tt.sessionSuffix
			if sessionID != expectedSessionID {
				t.Errorf("session ID = %q, want %q", sessionID, expectedSessionID)
			}

			// All interactive phase methods now route through the active run
			// directory — artifact dir is always runs/run-001/<phase>/.
			artifactDir := filepath.Join(stateDir, tt.featureID, "runs", "run-001", tt.dirName)
			if _, err := os.Stat(artifactDir); os.IsNotExist(err) {
				t.Errorf("artifact dir %q was not created", artifactDir)
			}

			// Wait for session to complete
			sess := sm.GetSession(sessionID)
			if sess == nil {
				t.Fatal("session not found")
			}
			select {
			case <-sess.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("session did not complete within timeout")
			}

			// Verify captured builder arguments
			mu.Lock()
			defer mu.Unlock()
			if len(captured) == 0 {
				t.Fatal("builder was never called")
			}
			cap := captured[0]

			if cap.model != "test-model" {
				t.Errorf("captured model = %q, want %q", cap.model, "test-model")
			}

			if cap.systemPrompt == "" {
				t.Error("captured systemPrompt is empty")
			}
			if !strings.Contains(cap.systemPrompt, "## Output Roots") {
				t.Error("captured systemPrompt does not contain RoleSpec output roots")
			}

			if tt.usesSkillRead {
				// RoleSpec-backed phases put the primary skill path in the
				// system prompt and keep the user prompt to invocation
				// arguments only.
				expectedSkillPath := filepath.Join(skillsDir, tt.commandName, "SKILL.md")
				if !strings.Contains(cap.systemPrompt, expectedSkillPath) {
					t.Errorf("system prompt missing skill-read instruction for %s, want path %s in prompt", tt.commandName, expectedSkillPath)
				}
				if strings.Contains(cap.prompt, expectedSkillPath) {
					t.Errorf("user prompt contains RoleSpec-owned skill path %s", expectedSkillPath)
				}
			}

			if !slices.Contains(cap.additionalDirs, stateDir) {
				t.Errorf("additionalDirs %v does not include stateDir %q", cap.additionalDirs, stateDir)
			}
		})
	}
}

func TestRunInteractivePhase_RemovesStalePhaseCompleteBeforeSession(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	for _, d := range []string{workDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", d, err)
		}
	}

	f := &feature.Feature{
		ID:        "test-stale-inquire-marker",
		Name:      "Test Feature",
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}
	artifactDir := filepath.Join(ActiveRunDir(stateDir, f), "inquire")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", artifactDir, err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, PhaseCompleteFile), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(phase_complete): %v", err)
	}

	var buildCalled bool
	var markerPresentAtBuild bool
	pr := &PhaseRunner{
		StateDir: stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			buildCalled = true
			markerPresentAtBuild = HasPhaseComplete(artifactDir)
			return nil, nil, nil, fmt.Errorf("test: stopping after capture")
		},
	}

	_, err := pr.RunInquire(f)
	if err == nil {
		t.Fatal("RunInquire error = nil, want injected BuildSession error")
	}
	if !buildCalled {
		t.Fatal("BuildSessionFn was not called")
	}
	if markerPresentAtBuild {
		t.Fatal("BuildSessionFn observed stale phase_complete; marker should be removed before session build")
	}
	if HasPhaseComplete(artifactDir) {
		t.Fatal("stale phase_complete still exists after RunInquire")
	}
}

// TestRunInteractivePhase_EmptyConfiguredModel_UsesDefaultAskingClause verifies
// that when f.Models.Research is empty and the registry provides a catalog
// default, the system prompt includes the asking clause for the resolved
// default model rather than an empty/wrong clause. This covers the
// runInteractivePhase path (RunInquire) and the RunResearchFromQuestions path.
func TestRunInteractivePhase_EmptyConfiguredModel_UsesDefaultAskingClause(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	const uniqueClause = "UNIQUE_ASKING_CLAUSE_FOR_DEFAULT_MODEL"
	const defaultModel = "default-research-model"

	tests := []struct {
		name      string
		callPhase func(pr *PhaseRunner, f *feature.Feature) (string, error)
	}{
		{
			name: "RunInquire_via_runInteractivePhase",
			callPhase: func(pr *PhaseRunner, f *feature.Feature) (string, error) {
				return pr.RunInquire(f)
			},
		},
		{
			name: "RunResearchFromQuestions",
			callPhase: func(pr *PhaseRunner, f *feature.Feature) (string, error) {
				return pr.RunResearchFromQuestions(f, "questions-stub.md")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			stateDir := filepath.Join(tmpDir, "state")
			scriptsDir := filepath.Join(tmpDir, "scripts")
			for _, d := range []string{workDir, stateDir, scriptsDir} {
				os.MkdirAll(d, 0o755)
			}

			script := testutil.WriteScript(t, scriptsDir, "phase.sh",
				"read -r _line\n"+testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")

			var mu sync.Mutex
			var captured []capturedArgs
			buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
				mu.Lock()
				captured = append(captured, capturedArgs{model: opts.Model, systemPrompt: opts.SystemPrompt})
				mu.Unlock()
				return []string{"bash", script}, nil, &session.SessionOpts{PIDDir: opts.PIDDir}, nil
			}

			// Set up a registry with a mock provider that has a catalog default
			// for PhaseResearch and a known asking-questions clause.
			reg := llm.NewRegistry()
			reg.Register(&mocks.MockProvider{
				ProviderName: "claude",
				Models:       []string{defaultModel},
				CLIDetected:  true,
				Catalog: []llm.ModelInfo{
					{ID: defaultModel, Category: "capable"},
				},
				QuestionsClause: uniqueClause,
			})

			eventCh := make(chan any, 100)
			sm := session.NewManager(eventCh)
			defer sm.Shutdown()

			store := feature.NewStore(stateDir)

			pr := &PhaseRunner{
				SessionManager: sm,
				FeatureStore:   store,
				StateDir:       stateDir,
				Registry:       reg,
				BuildSessionFn: buildSessionFn,
			}

			// Feature has NO configured research model — must fall back to catalog default.
			f := &feature.Feature{
				ID:          "test-empty-model",
				Name:        "Test Feature",
				Description: "test",
				Repos: []feature.FeatureRepo{
					{Name: "test-repo", Path: workDir},
				},
				Models: config.ModelConfig{
					Research: "", // intentionally empty
				},
			}

			sessionID, err := tt.callPhase(pr, f)
			if err != nil {
				t.Fatalf("phase method error: %v", err)
			}

			// Wait for session to complete
			sess := sm.GetSession(sessionID)
			if sess == nil {
				t.Fatal("session not found")
			}
			select {
			case <-sess.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("session did not complete within timeout")
			}

			mu.Lock()
			defer mu.Unlock()
			if len(captured) == 0 {
				t.Fatal("builder was never called")
			}
			cap := captured[0]

			// The resolved model should be the catalog default, not empty.
			if cap.model != defaultModel {
				t.Errorf("captured model = %q, want catalog default %q", cap.model, defaultModel)
			}

			// The system prompt must include the provider's asking clause.
			if !strings.Contains(cap.systemPrompt, uniqueClause) {
				t.Errorf("system prompt does not contain expected asking clause %q;\ngot: %s", uniqueClause, cap.systemPrompt)
			}
		})
	}
}

func TestRunResearch_SkillReadInstruction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	skillsDir := filepath.Join(tmpDir, "skills")
	for _, d := range []string{workDir, stateDir, scriptsDir, skillsDir} {
		os.MkdirAll(d, 0o755)
	}

	script := testutil.WriteScript(t, scriptsDir, "phase.sh",
		"read -r _line\n"+testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")

	var mu sync.Mutex
	var capturedOpts []BuildSessionOpts
	buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		mu.Lock()
		capturedOpts = append(capturedOpts, opts)
		mu.Unlock()
		return []string{"bash", script}, nil, &session.SessionOpts{PIDDir: opts.PIDDir}, nil
	}

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		SkillsDir:      skillsDir,
		BuildSessionFn: buildSessionFn,
	}

	f := &feature.Feature{
		ID:          "test-research-skill",
		Name:        "Test",
		Description: "test research skill read",
		Repos:       []feature.FeatureRepo{{Name: "test-repo", Path: workDir}},
		Models:      config.ModelConfig{Research: "test-model"},
	}

	sessionID, err := pr.RunResearchFromQuestions(f, "questions-stub.md")
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Wait for session to complete
	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(capturedOpts) == 0 {
		t.Fatal("buildSessionFn never called")
	}
	cap := capturedOpts[0]

	if !strings.Contains(cap.SystemPrompt, "## Output Roots") {
		t.Error("systemPrompt missing RoleSpec output roots")
	}
	expectedSkillPath := filepath.Join(skillsDir, "research-codebase", "SKILL.md")
	if !strings.Contains(cap.SystemPrompt, expectedSkillPath) {
		t.Errorf("systemPrompt missing skill-read instruction, expected path %q in system prompt:\n%s", expectedSkillPath, cap.SystemPrompt)
	}
	if strings.Contains(cap.Prompt, expectedSkillPath) {
		t.Errorf("prompt contains RoleSpec-owned skill-read instruction %q:\n%s", expectedSkillPath, cap.Prompt)
	}
	if strings.Contains(cap.Prompt, "# Useful Resources") {
		t.Errorf("prompt contains RoleSpec-owned Useful Resources section:\n%s", cap.Prompt)
	}
}

func TestRunKnowledgeBaseForRepo_SkillReadInstruction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	skillsDir := filepath.Join(tmpDir, "skills")
	for _, d := range []string{workDir, stateDir, scriptsDir, skillsDir} {
		os.MkdirAll(d, 0o755)
	}

	script := testutil.WriteScript(t, scriptsDir, "phase.sh",
		"read -r _line\n"+testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")

	var mu sync.Mutex
	var capturedOpts []BuildSessionOpts
	buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		mu.Lock()
		capturedOpts = append(capturedOpts, opts)
		mu.Unlock()
		return []string{"bash", script}, nil, &session.SessionOpts{PIDDir: opts.PIDDir}, nil
	}

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		SkillsDir:      skillsDir,
		BuildSessionFn: buildSessionFn,
	}

	repo := feature.FeatureRepo{Name: "test-repo", Path: workDir}
	f := &feature.Feature{
		ID:          "test-kb-skill",
		Name:        "Test KB",
		Description: "test kb skill read",
		Repos:       []feature.FeatureRepo{repo},
		Models:      config.ModelConfig{},
	}

	sessionID, err := pr.RunKnowledgeBaseForRepo(f, repo)
	if err != nil {
		t.Fatalf("RunKnowledgeBaseForRepo error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Wait for session to complete
	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(capturedOpts) == 0 {
		t.Fatal("buildSessionFn never called")
	}
	cap := capturedOpts[0]

	if !strings.Contains(cap.SystemPrompt, "## Output Roots") {
		t.Error("systemPrompt missing RoleSpec output roots")
	}
	expectedSkillPath := filepath.Join(skillsDir, "build-knowledge-base", "SKILL.md")
	if !strings.Contains(cap.SystemPrompt, expectedSkillPath) {
		t.Errorf("systemPrompt missing skill-read instruction, expected path %q in system prompt:\n%s", expectedSkillPath, cap.SystemPrompt)
	}
	if strings.Contains(cap.Prompt, expectedSkillPath) {
		t.Errorf("prompt contains RoleSpec-owned skill-read instruction %q:\n%s", expectedSkillPath, cap.Prompt)
	}
	if strings.Contains(cap.Prompt, "# Useful Resources") {
		t.Errorf("prompt contains RoleSpec-owned Useful Resources section:\n%s", cap.Prompt)
	}
}

func TestRunKnowledgeBaseForRepo_RemovesStalePhaseCompleteBeforeSession(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	for _, d := range []string{workDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", d, err)
		}
	}

	repo := feature.FeatureRepo{Name: "test-repo", Path: workDir}
	f := &feature.Feature{
		ID:     "test-stale-kb-marker",
		Name:   "Test KB",
		Repos:  []feature.FeatureRepo{repo},
		Models: config.ModelConfig{},
	}
	kbDir := KBStateDir(stateDir, repo.Name)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	if err := os.WriteFile(KBPath(kbDir), []byte("# stale kb\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.md): %v", err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, PhaseCompleteFile), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(phase_complete): %v", err)
	}

	var buildCalled bool
	var markerPresentAtBuild bool
	pr := NewPhaseRunner(nil, nil, stateDir)
	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildCalled = true
		markerPresentAtBuild = HasPhaseComplete(kbDir)
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	_, err := pr.RunKnowledgeBaseForRepo(f, repo)
	if err == nil {
		t.Fatal("RunKnowledgeBaseForRepo error = nil, want injected BuildSession error")
	}
	if !buildCalled {
		t.Fatal("BuildSessionFn was not called")
	}
	if markerPresentAtBuild {
		t.Fatal("BuildSessionFn observed stale phase_complete; marker should be removed before session build")
	}
	if HasPhaseComplete(kbDir) {
		t.Fatal("stale phase_complete still exists after RunKnowledgeBaseForRepo")
	}
}

func newRegistryWithProviders() *llm.Registry {
	reg := llm.NewRegistry()
	claudeProvider := &claude.Provider{}
	claudeProvider.SetModelCatalog([]llm.ModelInfo{
		{ID: "opus", Category: "capable"},
		{ID: "sonnet", Category: "balanced"},
		{ID: "haiku", Category: "cheap"},
	})
	codexProvider := &codex.Provider{}
	codexProvider.SetModelCatalog([]llm.ModelInfo{
		{ID: "gpt-5.4", Category: "capable"},
		{ID: "gpt-5.4-mini", Category: "balanced"},
		{ID: "codex", Category: "capable"},
	})
	reg.Register(claudeProvider)
	reg.Register(codexProvider)
	return reg
}

func TestBuildSession_Claude(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "research this",
		SystemPrompt: "you are a researcher",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	// Claude provider returns claude CLI command
	if len(cmd) == 0 || cmd[0] != "claude" {
		t.Errorf("expected claude command, got %v", cmd)
	}

	// Claude provider returns nil env
	if env != nil {
		t.Errorf("expected nil env for claude, got %v", env)
	}

	// Session opts should have protocol set (registry path)
	if sessOpts.Protocol == nil {
		t.Error("expected non-nil Protocol in session opts")
	}
	if sessOpts.ProviderName != "claude" {
		t.Errorf("expected ProviderName 'claude', got %q", sessOpts.ProviderName)
	}
	if sessOpts.InitialPrompt != "research this" {
		t.Errorf("expected InitialPrompt 'research this', got %q", sessOpts.InitialPrompt)
	}
}

// TestBuildSession_PinsPermissionModeForGrillingPhases verifies that
// BuildSession passes `--permission-mode default` to the Claude CLI for
// phases whose prompts use the [grill-me] directive. Without this, the
// user's ~/.claude/settings.json `defaultMode: auto` causes Claude Code to
// inject a "work without stopping for clarifying questions" system reminder
// that silently suppresses grilling and leaves the agent narrating things
// like "Given the global 'no clarifying questions' mode, I'll proceed
// without grilling".
func TestBuildSession_PinsPermissionModeForGrillingPhases(t *testing.T) {
	tests := []struct {
		name          string
		phase         feature.Phase
		wantHasFlag   bool
		wantFlagValue string
	}{
		{name: "inquire pins default", phase: feature.PhaseInquire, wantHasFlag: true, wantFlagValue: "default"},
		{name: "brainstorm pins default", phase: feature.PhaseBrainstorm, wantHasFlag: true, wantFlagValue: "default"},
		{name: "plan pins default", phase: feature.PhasePlan, wantHasFlag: true, wantFlagValue: "default"},
		{name: "research does not pin", phase: feature.PhaseResearch, wantHasFlag: false},
		{name: "implement does not pin", phase: feature.PhaseImplement, wantHasFlag: false},
		{name: "review does not pin", phase: feature.PhaseReview, wantHasFlag: false},
		{name: "knowledge base does not pin", phase: feature.PhaseKnowledgeBase, wantHasFlag: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			eventCh := make(chan any, 100)
			sm := session.NewManager(eventCh)
			store := feature.NewStore(dir)
			pr := NewPhaseRunner(sm, store, dir)
			pr.Registry = newRegistryWithProviders()

			cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
				Model:        "opus",
				Prompt:       "test prompt",
				SystemPrompt: "test system",
				PIDDir:       filepath.Join(dir, "pid"),
				PermHandler:  permHandlerFor(false, nil, ""),
				WorkDir:      dir,
				Phase:        tc.phase,
			})
			if err != nil {
				t.Fatalf("BuildSession() error: %v", err)
			}

			gotHas, gotValue := false, ""
			for i, a := range cmd {
				if a == "--permission-mode" {
					gotHas = true
					if i+1 < len(cmd) {
						gotValue = cmd[i+1]
					}
				}
			}
			if gotHas != tc.wantHasFlag {
				t.Errorf("--permission-mode present = %v, want %v\ncmd=%v", gotHas, tc.wantHasFlag, cmd)
			}
			if tc.wantHasFlag && gotValue != tc.wantFlagValue {
				t.Errorf("--permission-mode value = %q, want %q\ncmd=%v", gotValue, tc.wantFlagValue, cmd)
			}
		})
	}
}

func TestBuildSession_ClaudeSeedsProtocolContextWindow(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	reg := llm.NewRegistry()
	claudeProvider := &claude.Provider{}
	claudeProvider.SetModelCatalog([]llm.ModelInfo{
		{ID: "opus", ContextWindow: 1_000_000, Category: "capable"},
	})
	reg.Register(claudeProvider)
	pr.Registry = reg

	_, _, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "research this",
		SystemPrompt: "you are a researcher",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	if sessOpts == nil || sessOpts.Protocol == nil {
		t.Fatal("expected non-nil session protocol")
	}

	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000,"output_tokens":10}}}`)
	msgs, err := sessOpts.Protocol.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine() error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Assistant == nil || msgs[0].Assistant.Message.Usage == nil {
		t.Fatalf("expected one assistant message with usage, got %+v", msgs)
	}
	if got := msgs[0].Assistant.Message.Usage.ContextWindow; got != 1_000_000 {
		t.Errorf("assistant usage contextWindow = %d, want %d", got, 1_000_000)
	}
}

func TestBuildSession_CodexExplicitWritableRootsDoNotInheritStateOrAdditionalDirs(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	workDir := filepath.Join(dir, "work")
	additionalDir := filepath.Join(dir, "attempt-01")
	feedbackPath := filepath.Join(additionalDir, "validate-scope", "validation-scope-feedback.md")
	markerPath := filepath.Join(additionalDir, "validate-scope", "phase_complete")
	for _, path := range []string{stateDir, workDir, filepath.Dir(feedbackPath)} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	// Codex provider needs writable HOME/CODEX_HOME for command construction.
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, ".codex"))

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(stateDir)
	pr := NewPhaseRunner(sm, store, stateDir)
	pr.Registry = newRegistryWithProviders()

	wantRoots := []string{feedbackPath, markerPath}
	_, _, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:          "gpt-5.4",
		Prompt:         "validate this plan",
		SystemPrompt:   "you are a validator",
		AdditionalDirs: []string{additionalDir},
		WritableRoots:  wantRoots,
		PIDDir:         filepath.Join(stateDir, "pid"),
		PermHandler:    permHandlerFor(false, nil, ""),
		WorkDir:        workDir,
		Phase:          feature.PhasePlan,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	if sessOpts == nil || sessOpts.Protocol == nil {
		t.Fatal("expected non-nil sessOpts.Protocol")
	}
	cp, ok := sessOpts.Protocol.(*codex.Protocol)
	if !ok {
		t.Fatalf("expected *codex.Protocol, got %T", sessOpts.Protocol)
	}
	gotRoots := cp.WritableRootsForTest()
	if !reflect.DeepEqual(gotRoots, wantRoots) {
		t.Fatalf("WritableRoots = %#v, want %#v", gotRoots, wantRoots)
	}
	for _, forbidden := range []string{stateDir, additionalDir, filepath.Dir(feedbackPath), workDir} {
		if stringSliceContains(gotRoots, forbidden) {
			t.Fatalf("WritableRoots includes forbidden directory %q: %#v", forbidden, gotRoots)
		}
	}
}

func TestBuildSession_Claude_AgentSelection(t *testing.T) {
	tests := []struct {
		name       string
		agentNames []string
		wantAgents bool
		wantErrHas string
	}{
		{
			name:       "empty selection omits --agents",
			agentNames: []string{},
			wantAgents: false,
		},
		{
			name:       "nil selection omits --agents",
			agentNames: nil,
			wantAgents: false,
		},
		{
			name:       "subset selection includes --agents",
			agentNames: []string{"web-search-researcher", "codebase-locator"},
			wantAgents: true,
		},
		{
			name:       "unknown name fails fast",
			agentNames: []string{"missing-agent"},
			wantErrHas: `unknown embedded agent "missing-agent"`,
		},
		{
			name:       "duplicate name fails fast",
			agentNames: []string{"codebase-locator", "codebase-locator"},
			wantErrHas: `duplicate embedded agent "codebase-locator"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			eventCh := make(chan any, 100)
			sm := session.NewManager(eventCh)
			store := feature.NewStore(dir)
			pr := NewPhaseRunner(sm, store, dir)
			pr.Registry = newRegistryWithProviders()

			cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
				Model:       "opus",
				Prompt:      "research this",
				AgentNames:  tt.agentNames,
				PIDDir:      filepath.Join(dir, "pid"),
				PermHandler: permHandlerFor(false, nil, ""),
				WorkDir:     dir,
			})
			if tt.wantErrHas != "" {
				if err == nil {
					t.Fatal("BuildSession() error = nil, want failure")
				}
				if !strings.Contains(err.Error(), tt.wantErrHas) {
					t.Fatalf("BuildSession() error = %q, want substring %q", err, tt.wantErrHas)
				}
				if cmd != nil {
					t.Fatalf("BuildSession() cmd = %v, want nil", cmd)
				}
				if env != nil {
					t.Fatalf("BuildSession() env = %v, want nil", env)
				}
				if sessOpts != nil {
					t.Fatalf("BuildSession() sessOpts = %#v, want nil", sessOpts)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildSession() error: %v", err)
			}

			got := findArgValue(cmd, "--agents")
			if !tt.wantAgents {
				if got != "" {
					t.Fatalf("BuildSession() emitted --agents=%q for AgentNames=%v", got, tt.agentNames)
				}
				return
			}
			if got == "" {
				t.Fatal("BuildSession() omitted --agents for non-empty AgentNames")
			}

			var parsed map[string]agentdef.AgentDef
			if err := json.Unmarshal([]byte(got), &parsed); err != nil {
				t.Fatalf("--agents payload is invalid JSON: %v", err)
			}
			if len(parsed) != len(tt.agentNames) {
				t.Fatalf("--agents payload contained %d agents, want %d", len(parsed), len(tt.agentNames))
			}

			prevIndex := -1
			for _, name := range tt.agentNames {
				if _, ok := parsed[name]; !ok {
					t.Fatalf("--agents payload missing %q in %q", name, got)
				}
				idx := strings.Index(got, `"`+name+`"`)
				if idx < 0 {
					t.Fatalf("--agents payload missing ordered key %q in %q", name, got)
				}
				if idx <= prevIndex {
					t.Fatalf("--agents payload did not preserve caller order in %q", got)
				}
				prevIndex = idx
			}
		})
	}
}

func TestBuildSession_Claude_InvalidAgentNamesFailFast(t *testing.T) {
	tests := []struct {
		name       string
		agentNames []string
		wantErrHas string
	}{
		{
			name:       "unknown name",
			agentNames: []string{"missing-agent"},
			wantErrHas: `unknown embedded agent "missing-agent"`,
		},
		{
			name:       "duplicate name",
			agentNames: []string{"codebase-locator", "codebase-locator"},
			wantErrHas: `duplicate embedded agent "codebase-locator"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			eventCh := make(chan any, 100)
			sm := session.NewManager(eventCh)
			store := feature.NewStore(dir)
			pr := NewPhaseRunner(sm, store, dir)

			prov := &mocks.MockProvider{
				ProviderName: "claude",
				Models:       []string{"opus"},
				CLIDetected:  true,
				CommandArgs:  []string{"claude", "--model", "opus"},
			}
			reg := llm.NewRegistry()
			reg.Register(prov)
			pr.Registry = reg

			cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
				Model:       "opus",
				Prompt:      "research this",
				AgentNames:  tt.agentNames,
				PIDDir:      filepath.Join(dir, "pid"),
				PermHandler: permHandlerFor(false, nil, ""),
				WorkDir:     dir,
			})
			if err == nil {
				t.Fatal("BuildSession() error = nil, want failure")
			}
			if !strings.Contains(err.Error(), tt.wantErrHas) {
				t.Fatalf("BuildSession() error = %q, want substring %q", err, tt.wantErrHas)
			}
			if cmd != nil {
				t.Fatalf("BuildSession() cmd = %v, want nil", cmd)
			}
			if env != nil {
				t.Fatalf("BuildSession() env = %v, want nil", env)
			}
			if sessOpts != nil {
				t.Fatalf("BuildSession() sessOpts = %#v, want nil", sessOpts)
			}
			if len(prov.BuildCommandCalls) != 0 {
				t.Fatalf("BuildCommandCalls = %d, want 0", len(prov.BuildCommandCalls))
			}
		})
	}
}

func TestBuildSession_Claude_EffortLevel(t *testing.T) {
	tests := []struct {
		name        string
		effortLevel llm.EffortLevel
		wantFlag    string // expected --effort value in command
	}{
		{"medium effort", llm.EffortMedium, "medium"},
		{"high effort", llm.EffortHigh, "high"},
		{"max effort maps to xhigh", llm.EffortMax, "xhigh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			eventCh := make(chan any, 100)
			sm := session.NewManager(eventCh)
			store := feature.NewStore(dir)
			pr := NewPhaseRunner(sm, store, dir)
			pr.Registry = newRegistryWithProviders()

			cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
				Model:       "opus",
				Prompt:      "test",
				WorkDir:     dir,
				EffortLevel: tt.effortLevel,
			})
			if err != nil {
				t.Fatalf("BuildSession() error: %v", err)
			}

			// Find --effort flag and its value
			foundEffort := false
			for i, arg := range cmd {
				if arg == "--effort" && i+1 < len(cmd) {
					foundEffort = true
					if cmd[i+1] != tt.wantFlag {
						t.Errorf("--effort = %q, want %q", cmd[i+1], tt.wantFlag)
					}
					break
				}
			}
			if !foundEffort {
				t.Errorf("--effort flag not found in command: %v", cmd)
			}
		})
	}
}

func TestBuildSession_Codex_UsesProviderNilEnv(t *testing.T) {
	dir := t.TempDir()
	homeDir := filepath.Join(dir, "home")
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", "~/resolved-codex-home")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "gpt-5.4",
		Prompt:       "research this",
		SystemPrompt: "you are a researcher",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	// Codex provider returns codex app-server command.
	if len(cmd) < 2 || cmd[0] != "codex" || cmd[1] != "app-server" {
		t.Errorf("expected [codex app-server], got %v", cmd)
	}

	// Codex provider should use the nil-env contract.
	if env != nil {
		t.Errorf("expected nil env, got %v", env)
	}

	// Session opts should have protocol set (registry path).
	if sessOpts.Protocol == nil {
		t.Error("expected non-nil Protocol in session opts")
	}
	if sessOpts.ProviderName != "codex" {
		t.Errorf("expected ProviderName 'codex', got %q", sessOpts.ProviderName)
	}
}

func TestBuildSession_Codex_IgnoresAgentNamesSelection(t *testing.T) {
	dir := t.TempDir()
	homeDir := filepath.Join(dir, "home")
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", "~/resolved-codex-home")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:       "gpt-5.4",
		Prompt:      "research this",
		AgentNames:  []string{"codebase-locator", "web-search-researcher"},
		PIDDir:      filepath.Join(dir, "pid"),
		PermHandler: permHandlerFor(false, nil, ""),
		WorkDir:     dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	if got := findArgValue(cmd, "--agents"); got != "" {
		t.Fatalf("BuildSession() emitted --agents=%q for codex command %v", got, cmd)
	}
	if env != nil {
		t.Fatalf("BuildSession() env = %v, want nil", env)
	}
	if sessOpts.ProviderName != "codex" {
		t.Fatalf("BuildSession() ProviderName = %q, want codex", sessOpts.ProviderName)
	}
}

func TestBuildSession_Codex_WrapsResolvedHomeFailures(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing blocking file: %v", err)
	}

	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("CODEX_HOME", blockingFile)

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:       "gpt-5.4",
		Prompt:      "test",
		PermHandler: permHandlerFor(false, nil, ""),
		WorkDir:     dir,
	})
	if err == nil {
		t.Fatal("expected BuildSession() error, got nil")
	}
	if cmd != nil {
		t.Fatalf("expected nil cmd on error, got %v", cmd)
	}
	if env != nil {
		t.Fatalf("expected nil env on error, got %v", env)
	}
	if sessOpts != nil {
		t.Fatalf("expected nil session opts on error, got %#v", sessOpts)
	}
	if !strings.Contains(err.Error(), "building command for codex: preparing codex home:") {
		t.Fatalf("BuildSession() error = %q, want codex home context", err)
	}
	if !strings.Contains(err.Error(), "creating codex home") &&
		!strings.Contains(err.Error(), "creating codex agents dir") {
		t.Fatalf("BuildSession() error = %q, want wrapped reconcile failure", err)
	}
}

func TestBuildSession_TestOverride(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	var calledModel string
	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		calledModel = opts.Model
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		return []string{"mock", "--model", opts.Model}, nil, sessOpts, nil
	}

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "test prompt",
		SystemPrompt: "test system",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		RepoName:     "test-repo",
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	if calledModel != "opus" {
		t.Errorf("expected mock called with model 'opus', got %q", calledModel)
	}
	if len(cmd) != 3 || cmd[0] != "mock" {
		t.Errorf("expected mock command, got %v", cmd)
	}
	// Test override path should return nil env
	if env != nil {
		t.Errorf("expected nil env for test override, got %v", env)
	}
	// Test override path should NOT set protocol
	if sessOpts.Protocol != nil {
		t.Error("expected nil Protocol for test override")
	}
	if sessOpts.RepoName != "test-repo" {
		t.Errorf("expected RepoName 'test-repo', got %q", sessOpts.RepoName)
	}
	if sessOpts.InitialPrompt != "test prompt" {
		t.Errorf("expected InitialPrompt 'test prompt', got %q", sessOpts.InitialPrompt)
	}
}

// TestNoLegacyBuilderCallersInAgentLayer is a compile-time guard ensuring that
// agent-layer source files no longer reference the legacy command-builder
// functions that were replaced by the provider-agnostic BuildSession path.
func TestNoLegacyBuilderCallersInAgentLayer(t *testing.T) {
	legacyFuncs := []string{
		"BuildClaudeCommand",
		"BuildInteractiveClaudeCommand",
		"BuildPrintStreamingClaudeCommand",
		"InsertAfterClaude",
	}
	files := []string{
		"describe.go",
		"summarize.go",
		"research.go",
		"phase.go",
	}

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		content := string(data)
		for _, fn := range legacyFuncs {
			if strings.Contains(content, fn) {
				t.Errorf("%s still references legacy builder %q", file, fn)
			}
		}
	}
}

func TestRunKnowledgeBaseForRepo_PassesKBTracerAgentNames(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 10)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	var captured BuildSessionOpts
	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		captured = opts
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	f := &feature.Feature{
		ID: "test-kb-agent-names",
		Models: config.ModelConfig{
			KBBuild: "custom-kb-model",
		},
		Repos: []feature.FeatureRepo{
			{Name: "testrepo", Path: dir},
		},
	}

	_, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}
	if !reflect.DeepEqual(captured.AgentNames, knowledgeBaseAgentNames()) {
		t.Fatalf("captured AgentNames = %v, want %v", captured.AgentNames, knowledgeBaseAgentNames())
	}
}

func TestRunResearchFromQuestions_PassesResearchTracerAgentNames(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 10)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	var captured BuildSessionOpts
	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		captured = opts
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	f := &feature.Feature{
		ID: "test-research-questions-agent-names",
		Models: config.ModelConfig{
			Research: "custom-research-model",
		},
		Repos: []feature.FeatureRepo{
			{Name: "testrepo", Path: dir},
		},
	}

	_, err := pr.RunResearchFromQuestions(f, filepath.Join(dir, "questions.md"))
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}
	if !reflect.DeepEqual(captured.AgentNames, researchAgentNames()) {
		t.Fatalf("captured AgentNames = %v, want %v", captured.AgentNames, researchAgentNames())
	}
}

func TestInteractiveNonResearchPhases_PassExplicitEmptyAgentNames(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(t *testing.T, pr *PhaseRunner, f *feature.Feature, tmpDir string) error
	}{
		{
			name: "inquire",
			invoke: func(t *testing.T, pr *PhaseRunner, f *feature.Feature, _ string) error {
				_, err := pr.RunInquire(f)
				return err
			},
		},
		{
			name: "brainstorm",
			invoke: func(t *testing.T, pr *PhaseRunner, f *feature.Feature, tmpDir string) error {
				researchPath := filepath.Join(tmpDir, "research.md")
				if err := os.WriteFile(researchPath, []byte("# Research"), 0o644); err != nil {
					t.Fatalf("WriteFile(%s): %v", researchPath, err)
				}
				_, err := pr.RunBrainstorm(f, researchPath, nil)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			eventCh := make(chan any, 10)
			sm := session.NewManager(eventCh)
			store := feature.NewStore(tmpDir)
			pr := NewPhaseRunner(sm, store, tmpDir)

			var captured BuildSessionOpts
			pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
				captured = opts
				return nil, nil, nil, fmt.Errorf("test: stopping after capture")
			}

			f := &feature.Feature{
				ID: "test-explicit-empty-agent-names",
				Models: config.ModelConfig{
					Research: "custom-research-model",
				},
				Repos: []feature.FeatureRepo{
					{Name: "testrepo", Path: tmpDir},
				},
			}

			if err := tt.invoke(t, pr, f, tmpDir); err == nil {
				t.Fatal("expected error from mock BuildSession")
			}
			assertExplicitEmptyAgentNames(t, captured.AgentNames)
		})
	}
}

func TestKBBuild_UsesModelFromConfig(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 10)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)

	var capturedModel string
	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		capturedModel = opts.Model
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	f := &feature.Feature{
		ID: "test-kb",
		Models: config.ModelConfig{
			KBBuild: "custom-kb-model",
		},
		Repos: []feature.FeatureRepo{
			{Name: "testrepo", Path: dir},
		},
	}

	// The function will error from our mock, but we can check the captured model
	_, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}
	if capturedModel != "custom-kb-model" {
		t.Errorf("expected model 'custom-kb-model', got %q", capturedModel)
	}
}

func TestPhaseWiring_AgentNamesTracerBullet(t *testing.T) {
	tests := []struct {
		name      string
		callPhase func(pr *PhaseRunner, f *feature.Feature, tmpDir string) error
	}{
		{
			name: "knowledge base",
			callPhase: func(pr *PhaseRunner, f *feature.Feature, _ string) error {
				_, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
				return err
			},
		},
		{
			name: "research from questions",
			callPhase: func(pr *PhaseRunner, f *feature.Feature, tmpDir string) error {
				questionsPath := filepath.Join(tmpDir, "questions.md")
				if err := os.WriteFile(questionsPath, []byte("# Questions\n- Why?\n"), 0o644); err != nil {
					t.Fatalf("WriteFile(%s): %v", questionsPath, err)
				}
				_, err := pr.RunResearchFromQuestions(f, questionsPath)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			stateDir := filepath.Join(tmpDir, "state")
			for _, d := range []string{workDir, stateDir} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatalf("MkdirAll(%s): %v", d, err)
				}
			}

			var captured BuildSessionOpts
			pr := &PhaseRunner{
				SessionManager: session.NewManager(make(chan any, 10)),
				FeatureStore:   feature.NewStore(stateDir),
				StateDir:       stateDir,
				BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
					captured = opts
					return nil, nil, nil, fmt.Errorf("stop after capture")
				},
			}

			f := &feature.Feature{
				ID:           "feat-agent-names",
				Name:         "Tracer Bullet",
				Description:  "exercise agent selection wiring",
				Status:       feature.StatusResearching,
				CurrentPhase: feature.PhaseResearch,
				Repos: []feature.FeatureRepo{
					{Name: "test-repo", Path: workDir},
				},
				Models: config.ModelConfig{
					Research: "test-model",
					KBBuild:  "test-model",
				},
			}

			err := tt.callPhase(pr, f, tmpDir)
			if err == nil {
				t.Fatal("expected capture error, got nil")
			}
			if len(captured.AgentNames) == 0 {
				t.Fatalf("captured AgentNames = %v, want non-empty", captured.AgentNames)
			}
		})
	}
}

func TestBuildSession_AllowedToolsPropagation(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "test prompt",
		SystemPrompt: "test system",
		AllowedTools: []string{"Read", "Edit"},
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	// The Claude provider should emit --allowedTools Read,Edit,WebSearch,WebFetch
	// (caller-specified tools plus the always-on web tools).
	foundAllowed := false
	for i, arg := range cmd {
		if arg == "--allowedTools" {
			if i+1 >= len(cmd) {
				t.Fatal("--allowedTools flag present but no value follows")
			}
			val := cmd[i+1]
			for _, tool := range []string{"Read", "Edit", "WebSearch", "WebFetch"} {
				if !strings.Contains(val, tool) {
					t.Errorf("expected --allowedTools value to contain %s, got %q", tool, val)
				}
			}
			foundAllowed = true
			break
		}
	}
	if !foundAllowed {
		t.Errorf("expected --allowedTools in command args, got %v", cmd)
	}
}

func TestBuildSession_WebSearchAlwaysAllowed(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	// No explicit AllowedTools — WebSearch and WebFetch should still appear.
	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:       "opus",
		Prompt:      "research this",
		PIDDir:      filepath.Join(dir, "pid"),
		PermHandler: permHandlerFor(false, nil, ""),
		WorkDir:     dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	foundAllowed := false
	for i, arg := range cmd {
		if arg == "--allowedTools" {
			if i+1 >= len(cmd) {
				t.Fatal("--allowedTools flag present but no value follows")
			}
			val := cmd[i+1]
			if !strings.Contains(val, "WebSearch") || !strings.Contains(val, "WebFetch") {
				t.Errorf("expected --allowedTools to contain WebSearch and WebFetch, got %q", val)
			}
			foundAllowed = true
			break
		}
	}
	if !foundAllowed {
		t.Errorf("expected --allowedTools WebSearch,WebFetch in command args, got %v", cmd)
	}
}

func TestKBBuild_FallsBackToOpus(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 10)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()

	var capturedModel string
	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		capturedModel = opts.Model
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	f := &feature.Feature{
		ID:     "test-kb-fallback",
		Models: config.ModelConfig{}, // KBBuild is empty
		Repos: []feature.FeatureRepo{
			{Name: "testrepo", Path: dir},
		},
	}

	_, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}
	if capturedModel != "claude:opus" {
		t.Errorf("expected fallback model 'claude:opus', got %q", capturedModel)
	}
}

// findArgValue returns the value following the given flag in cmd args.
// Returns "" if the flag is not found.
func findArgValue(cmd []string, flag string) string {
	for i, arg := range cmd {
		if arg == flag && i+1 < len(cmd) {
			return cmd[i+1]
		}
	}
	return ""
}

// findAllArgValues returns all values for a repeated flag in cmd args.
func findAllArgValues(cmd []string, flag string) []string {
	var vals []string
	for i, arg := range cmd {
		if arg == flag && i+1 < len(cmd) {
			vals = append(vals, cmd[i+1])
		}
	}
	return vals
}

func TestBuildSession_SkillsInjection(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() setup: %v", err)
	}

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	pr.SkillsDir = skillsDir

	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "implement this",
		SystemPrompt: "you are an implementer",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
		Phase:        feature.PhaseImplement, // has utility skills (frontend-design)
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	// The retired "Available Skills" discovery table must not be appended
	// by BuildSession. RoleSpec-backed system prompts carry the current
	// Useful Resources section before this layer is called.
	systemPrompt := findArgValue(cmd, "--append-system-prompt")
	if strings.Contains(systemPrompt, "## Available Skills") {
		t.Error("soft skills discovery preamble must not appear in system prompt (moved to user prompt)")
	}
	if !strings.Contains(systemPrompt, "you are an implementer") {
		t.Error("original system prompt content was lost")
	}

	// Verify --add-dir includes skillsDir for filesystem access
	addDirs := findAllArgValues(cmd, "--add-dir")
	if !slices.Contains(addDirs, skillsDir) {
		t.Errorf("--add-dir flags %v do not include skillsDir %q", addDirs, skillsDir)
	}
}

// TestBuildSession_FrontendTagPromotesFrontendDesignToMandatory is the
// regression guard for the Native App feature scenario — a frontend feature
// ran 28.7h and never loaded skills/frontend-design/SKILL.md because the
// preamble table was soft advertisement only. With FeatureTags set, the
// system prompt must carry the mandatory-read block naming the skill file.
func TestBuildSession_FrontendTagPromotesFrontendDesignToMandatory(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() setup: %v", err)
	}

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	pr.SkillsDir = skillsDir

	phases := []feature.Phase{
		feature.PhaseBrainstorm,
		feature.PhasePlan,
		feature.PhaseImplement,
		feature.PhaseReview,
	}
	for _, p := range phases {
		t.Run(p.String(), func(t *testing.T) {
			cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
				Model:        "opus",
				Prompt:       "implement this",
				SystemPrompt: "you are an implementer",
				PIDDir:       filepath.Join(dir, "pid"),
				PermHandler:  permHandlerFor(false, nil, ""),
				WorkDir:      dir,
				Phase:        p,
				FeatureTags:  []string{feature.TagFrontend},
			})
			if err != nil {
				t.Fatalf("BuildSession() error: %v", err)
			}
			systemPrompt := findArgValue(cmd, "--append-system-prompt")
			if !strings.Contains(systemPrompt, "Required Skills For This Feature") {
				t.Errorf("phase=%v: mandatory required-skills block absent", p)
			}
			if !strings.Contains(systemPrompt, "MUST read") {
				t.Errorf("phase=%v: mandatory verb missing", p)
			}
			if !strings.Contains(systemPrompt, "frontend-design") {
				t.Errorf("phase=%v: frontend-design not named as mandatory", p)
			}
			expectPath := filepath.Join(skillsDir, "frontend-design", "SKILL.md")
			if !strings.Contains(systemPrompt, expectPath) {
				t.Errorf("phase=%v: mandatory block missing skill file path %q", p, expectPath)
			}
		})
	}
}

// TestBuildSession_NoTagsNoMandatoryBlock ensures the mandatory-read block
// is suppressed when the feature carries no tags (backward compat with
// existing callers that don't classify features yet).
func TestBuildSession_NoTagsNoMandatoryBlock(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() setup: %v", err)
	}

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	pr.SkillsDir = skillsDir

	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "implement this",
		SystemPrompt: "you are an implementer",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
		Phase:        feature.PhaseImplement,
		// FeatureTags deliberately nil
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	systemPrompt := findArgValue(cmd, "--append-system-prompt")
	if strings.Contains(systemPrompt, "Required Skills For This Feature") {
		t.Error("mandatory block appeared for feature with no tags")
	}
	// The retired soft skills preamble must not appear here regardless of
	// FeatureTags.
	if strings.Contains(systemPrompt, "## Available Skills") {
		t.Error("soft skills discovery preamble must not appear in system prompt")
	}
}

// TestBuildSession_BackendTagDoesNotPromoteFrontendDesign ensures that a
// backend-tagged feature does not pull frontend-design into the mandatory
// block, avoiding noise on features unrelated to UI work.
func TestBuildSession_BackendTagDoesNotPromoteFrontendDesign(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() setup: %v", err)
	}

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	pr.SkillsDir = skillsDir

	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "implement this",
		SystemPrompt: "you are an implementer",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
		Phase:        feature.PhaseImplement,
		FeatureTags:  []string{feature.TagBackend},
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	systemPrompt := findArgValue(cmd, "--append-system-prompt")
	// If a mandatory block appears at all, it must not carry frontend-design.
	if strings.Contains(systemPrompt, "Required Skills For This Feature") &&
		strings.Contains(systemPrompt, "frontend-design") {
		idx := strings.Index(systemPrompt, "Required Skills For This Feature")
		tail := systemPrompt[idx:]
		if strings.Contains(tail, "frontend-design") {
			t.Error("backend-tagged feature unexpectedly promoted frontend-design as mandatory")
		}
	}
}

func TestBuildSession_SkillsNoSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() setup: %v", err)
	}

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	pr.SkillsDir = skillsDir

	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:       "opus",
		Prompt:      "research this",
		PIDDir:      filepath.Join(dir, "pid"),
		PermHandler: permHandlerFor(false, nil, ""),
		WorkDir:     dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	// When SystemPrompt is empty, skills preamble should NOT be injected.
	// Prompt-free sessions (e.g. review gates) get directory access only.
	systemPrompt := findArgValue(cmd, "--append-system-prompt")
	if strings.Contains(systemPrompt, "## Available Skills") {
		t.Error("skills preamble should not be injected when SystemPrompt is empty")
	}

	// --add-dir should still include skillsDir for filesystem access
	addDirs := findAllArgValues(cmd, "--add-dir")
	if !slices.Contains(addDirs, skillsDir) {
		t.Errorf("--add-dir flags %v should include skillsDir %q even without SystemPrompt", addDirs, skillsDir)
	}
}

func TestBuildSession_EmptySkillsDir(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	// pr.SkillsDir is deliberately left empty (zero value)

	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "research this",
		SystemPrompt: "you are a researcher",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	// System prompt should NOT contain skills preamble
	systemPrompt := findArgValue(cmd, "--append-system-prompt")
	if strings.Contains(systemPrompt, "## Available Skills") {
		t.Error("skills preamble injected despite empty SkillsDir")
	}
	// Original system prompt should be intact
	if !strings.Contains(systemPrompt, "you are a researcher") {
		t.Error("original system prompt lost when SkillsDir is empty")
	}
}

func TestAvailabilityContract(t *testing.T) {
	dir := t.TempDir()

	// Simulate reconciliation failure by pointing to an unwritable path
	unwritablePath := filepath.Join(dir, "blocked")
	os.WriteFile(unwritablePath, []byte("x"), 0o644) // file, not dir
	skillsDir := filepath.Join(unwritablePath, "skills")

	// Reconciliation fails
	err := skilldef.ReconcileSkills(skillsDir)
	if err == nil {
		t.Fatal("ReconcileSkills() should fail on unwritable path")
	}

	// Contract: SkillsDir remains empty on failure
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	// pr.SkillsDir left empty — simulates startup code NOT setting it after failure

	cmd, _, _, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opus",
		Prompt:       "research this",
		SystemPrompt: "you are a researcher",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	// No skills preamble injected
	systemPrompt := findArgValue(cmd, "--append-system-prompt")
	if strings.Contains(systemPrompt, "## Available Skills") {
		t.Error("skills preamble injected after reconciliation failure")
	}
	if strings.Contains(systemPrompt, "frontend-design") {
		t.Error("skill name appeared in prompt after reconciliation failure")
	}
}

func TestBuildSession_SkillsInjection_Codex(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := skilldef.ReconcileSkills(skillsDir); err != nil {
		t.Fatalf("ReconcileSkills() setup: %v", err)
	}

	// Codex provider needs writable HOME/CODEX_HOME
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, ".codex"))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	pr.SkillsDir = skillsDir

	_, _, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "gpt-5.4",
		Prompt:       "implement this",
		SystemPrompt: "you are an implementer",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
		Phase:        feature.PhaseImplement, // has utility skills (frontend-design)
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	if sessOpts == nil || sessOpts.Protocol == nil {
		t.Fatal("expected non-nil sessOpts.Protocol for Codex provider")
	}

	cp, ok := sessOpts.Protocol.(*codex.Protocol)
	if !ok {
		t.Fatalf("expected *codex.Protocol, got %T", sessOpts.Protocol)
	}

	// Verify WritableRoots contains skillsDir
	roots := cp.WritableRootsForTest()
	if !slices.Contains(roots, skillsDir) {
		t.Errorf("WritableRoots %v does not contain skillsDir %q", roots, skillsDir)
	}

	// The retired "Available Skills" discovery table must not be appended
	// by BuildSession, but the original system prompt content is preserved.
	sysPrompt := cp.SystemPromptForTest()
	if strings.Contains(sysPrompt, "## Available Skills") {
		t.Error("soft skills discovery preamble must not appear in system prompt (moved to user prompt)")
	}
	if !strings.Contains(sysPrompt, "you are an implementer") {
		t.Error("original system prompt content was lost")
	}
}

func TestBuildSession_SkillsInjection_Codex_EmptySkillsDir(t *testing.T) {
	dir := t.TempDir()

	// Codex provider needs writable HOME/CODEX_HOME
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, ".codex"))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithProviders()
	// pr.SkillsDir is deliberately left empty (zero value)

	_, _, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "gpt-5.4",
		Prompt:       "research this",
		SystemPrompt: "you are a researcher",
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	if sessOpts == nil || sessOpts.Protocol == nil {
		t.Fatal("expected non-nil sessOpts.Protocol for Codex provider")
	}

	cp, ok := sessOpts.Protocol.(*codex.Protocol)
	if !ok {
		t.Fatalf("expected *codex.Protocol, got %T", sessOpts.Protocol)
	}

	// WritableRoots should only contain StateDir (no skillsDir)
	roots := cp.WritableRootsForTest()
	for _, r := range roots {
		if strings.Contains(r, "skills") {
			t.Errorf("WritableRoots %v should not contain a skills directory when SkillsDir is empty", roots)
			break
		}
	}

	// System prompt should NOT contain skills preamble
	sysPrompt := cp.SystemPromptForTest()
	if strings.Contains(sysPrompt, "## Available Skills") {
		t.Error("skills preamble injected despite empty SkillsDir")
	}
	// Original system prompt should be intact
	if !strings.Contains(sysPrompt, "you are a researcher") {
		t.Error("original system prompt lost when SkillsDir is empty")
	}
}

func TestBuildTweakPrompt_FullContext(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "feat-123"
	roadmapPath := filepath.Join(stateDir, featureID, "roadmap", "roadmap.md")
	_ = os.MkdirAll(filepath.Dir(roadmapPath), 0o755)
	_ = os.WriteFile(roadmapPath, []byte("# Roadmap\nDo things."), 0o644)
	skillsDir := t.TempDir()

	f := &feature.Feature{
		ID:    featureID,
		Name:  "Add dark mode",
		Repos: []feature.FeatureRepo{{Name: "repo"}},
		RepoStates: map[string]*feature.RepoState{
			"repo": {PRURL: "https://github.com/org/repo/pull/42"},
		},
		Artifacts: map[string]string{
			"roadmap": roadmapPath,
		},
	}

	prompt := BuildTweakPrompt(f, stateDir, skillsDir, "repo")

	for _, want := range []string{
		"Before starting your tweak session, read the methodology instructions at:",
		filepath.Join(skillsDir, "tweak-session", "SKILL.md"),
		"Tweak Session Context",
		"Add dark mode",
		roadmapPath,
		"pull/42",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// TestBuildTweakPrompt_MultiRepoUsesRepoSpecificPR pins down that when the
// caller passes a per-repo context, the prompt renders that repo's PR URL —
// not an arbitrary one picked out of map iteration order.
func TestBuildTweakPrompt_MultiRepoUsesRepoSpecificPR(t *testing.T) {
	stateDir := t.TempDir()
	skillsDir := t.TempDir()
	f := &feature.Feature{
		ID:   "feat-multi",
		Name: "Cross-repo tweak",
		Repos: []feature.FeatureRepo{
			{Name: "repo-a"},
			{Name: "repo-b"},
		},
		ActiveRun: 1,
		RunCount:  1,
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {PRURL: "https://github.com/org/repo-a/pull/1"},
			"repo-b": {PRURL: "https://github.com/org/repo-b/pull/2"},
		},
	}

	for _, tc := range []struct {
		name        string
		repoName    string
		wantURL     string
		unwantedURL string
	}{
		{"repo-a", "repo-a", "https://github.com/org/repo-a/pull/1", "https://github.com/org/repo-b/pull/2"},
		{"repo-b", "repo-b", "https://github.com/org/repo-b/pull/2", "https://github.com/org/repo-a/pull/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := BuildTweakPrompt(f, stateDir, skillsDir, tc.repoName)
			if !strings.Contains(prompt, tc.wantURL) {
				t.Errorf("prompt missing %q for repo %q", tc.wantURL, tc.repoName)
			}
			if strings.Contains(prompt, tc.unwantedURL) {
				t.Errorf("prompt unexpectedly contains %q when repo %q was selected", tc.unwantedURL, tc.repoName)
			}
		})
	}
}

func TestBuildTweakPrompt_MinimalFeature(t *testing.T) {
	stateDir := t.TempDir()
	skillsDir := t.TempDir()
	f := &feature.Feature{
		Name: "Quick fix",
	}

	prompt := BuildTweakPrompt(f, stateDir, skillsDir, "")

	if !strings.Contains(prompt, "Quick fix") {
		t.Error("prompt should contain feature name")
	}
	for _, absent := range []string{"**Plan**", "**PR**"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("prompt should not contain %q for minimal feature", absent)
		}
	}
}

func TestResolveImplementArtifactDir_CyclePrefix(t *testing.T) {
	stateDir := t.TempDir()

	tests := []struct {
		name    string
		feature *feature.Feature
		wantDir string
	}{
		{
			"no cycle no refactor",
			&feature.Feature{ID: "f1", ActiveRun: 1},
			filepath.Join(stateDir, "f1", "runs", "run-001", "implement"),
		},
		{
			"rebase cycle active",
			func() *feature.Feature {
				f := &feature.Feature{ID: "f1", ActiveRun: 1}
				f.SetActiveCycleType(feature.CycleRebase)
				f.SetRebaseCount(2)
				return f
			}(),
			filepath.Join(stateDir, "f1", "runs", "run-001", "rebase-2", "implement"),
		},
		{
			"tweak cycle active",
			func() *feature.Feature {
				f := &feature.Feature{ID: "f1", ActiveRun: 1}
				f.SetActiveCycleType(feature.CycleTweak)
				f.SetTweakCount(1)
				return f
			}(),
			filepath.Join(stateDir, "f1", "runs", "run-001", "tweak-1", "implement"),
		},
		{
			"review-comments cycle active",
			func() *feature.Feature {
				f := &feature.Feature{ID: "f1", ActiveRun: 1}
				f.SetActiveCycleType(feature.CycleReviewComments)
				return f
			}(),
			filepath.Join(stateDir, "f1", "runs", "run-001", "review-comments", "implement"),
		},
		{
			"refactor active no cycle",
			func() *feature.Feature {
				f := &feature.Feature{
					ID:             "f1",
					ActiveRun:      1,
					RefactorPrompt: "refactor auth",
				}
				f.SetRefactorCount(1)
				return f
			}(),
			filepath.Join(stateDir, "f1", "runs", "run-001", "refactor-1", "implement"),
		},
		{
			"cycle takes precedence over refactor",
			func() *feature.Feature {
				f := &feature.Feature{
					ID:             "f1",
					ActiveRun:      1,
					RefactorPrompt: "refactor auth",
				}
				f.SetActiveCycleType(feature.CycleRebase)
				f.SetRebaseCount(1)
				f.SetRefactorCount(1)
				return f
			}(),
			filepath.Join(stateDir, "f1", "runs", "run-001", "rebase-1", "implement"),
		},
		{
			"roadmap phase with cycle skips phase scoping",
			func() *feature.Feature {
				f := &feature.Feature{
					ID:                  "f1",
					ActiveRun:           1,
					CurrentRoadmapPhase: 2,
				}
				f.SetActiveCycleType(feature.CycleRebase)
				f.SetRebaseCount(1)
				return f
			}(),
			filepath.Join(stateDir, "f1", "runs", "run-001", "rebase-1", "implement"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &PhaseRunner{StateDir: stateDir}
			got := pr.resolveImplementArtifactDir(tt.feature)
			if got != tt.wantDir {
				t.Errorf("resolveImplementArtifactDir() = %q, want %q", got, tt.wantDir)
			}
		})
	}
}

func TestRunTweakSession_BuildsSessionWithoutCompletionProtocol(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)

	// Create a mock BuildSessionFn that captures args and stops early.
	var captured BuildSessionOpts
	buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		captured = opts
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		SkillsDir:      t.TempDir(),
		BuildSessionFn: buildSessionFn,
	}

	f := &feature.Feature{
		ID:   "test-feat-123",
		Name: "Test Feature",
		Models: config.ModelConfig{
			Implementation: "test-impl-model",
			Research:       "test-research-model",
		},
		Repos: []feature.FeatureRepo{{Path: "/tmp/test-repo", WorktreePath: "/tmp/test-wt"}},
	}

	_, err := pr.RunTweakSession(f)
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}

	// Tweak is fully interactive: the user ends the PTY with Ctrl+D, so it
	// must not receive the universal phase_complete completion protocol.
	if captured.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want empty for interactive tweak", captured.SystemPrompt)
	}
	if captured.TurnMode != ports.TurnModeInteractive {
		t.Errorf("TurnMode = %v, want interactive", captured.TurnMode)
	}

	// Verify prompt contains tweak session skill invocation and feature context.
	if !strings.Contains(captured.Prompt, "tweak-session") {
		t.Errorf("Prompt should invoke tweak-session skill, got %q", captured.Prompt)
	}
	if !strings.Contains(captured.Prompt, "Test Feature") {
		t.Errorf("Prompt should contain feature name, got %q", captured.Prompt)
	}
	if strings.Contains(captured.Prompt, "## Handoff Contract") {
		t.Errorf("Prompt should not include the autonomous-loop handoff contract for an interactive tweak; got %q", captured.Prompt)
	}
	if strings.Contains(captured.Prompt, "NEED_USER_INPUT") {
		t.Errorf("Prompt should not mention NEED_USER_INPUT for an interactive tweak; got %q", captured.Prompt)
	}
	if strings.Contains(captured.Prompt, "**Exit Criteria**") {
		t.Errorf("Prompt should not include exit criteria, got %q", captured.Prompt)
	}

	// Verify implementation model (not research model)
	if captured.Model != "test-impl-model" {
		t.Errorf("Model = %q, want %q", captured.Model, "test-impl-model")
	}

	// Verify phase
	if captured.Phase != feature.PhaseImplement {
		t.Errorf("Phase = %v, want PhaseImplement", captured.Phase)
	}

	// Verify no subagents
	assertExplicitEmptyAgentNames(t, captured.AgentNames)
}

func TestRunTweakSession_MultiRepo_WorkDirIsRepoSpecific(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)

	var captured BuildSessionOpts
	buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		captured = opts
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		BuildSessionFn: buildSessionFn,
	}

	f := &feature.Feature{
		ID:   "test-feat",
		Name: "Test Feature",
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: "/tmp/api", WorktreePath: "/tmp/api-wt"},
		},
	}

	_, err := pr.RunTweakSession(f, TweakSessionConfig{
		WorkDir: "/tmp/api-wt",
	})
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}

	if captured.WorkDir != "/tmp/api-wt" {
		t.Errorf("WorkDir = %q, want %q", captured.WorkDir, "/tmp/api-wt")
	}
}

func TestRunTweakSession_MultiRepo_AdditionalDirsIncludeOtherRepos(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)

	var captured BuildSessionOpts
	buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		captured = opts
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		BuildSessionFn: buildSessionFn,
	}

	f := &feature.Feature{
		ID:   "test-feat",
		Name: "Test Feature",
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: "/tmp/api"},
		},
	}

	wantDirs := []string{"/tmp/backend-wt", "/tmp/state-dir"}
	_, err := pr.RunTweakSession(f, TweakSessionConfig{
		WorkDir:        "/tmp/api",
		AdditionalDirs: wantDirs,
	})
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}

	if !reflect.DeepEqual(captured.AdditionalDirs, wantDirs) {
		t.Errorf("AdditionalDirs = %v, want %v", captured.AdditionalDirs, wantDirs)
	}
}

func TestRunTweakSession_DefaultConfig_BackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)

	var captured BuildSessionOpts
	buildSessionFn := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		captured = opts
		return nil, nil, nil, fmt.Errorf("test: stopping after capture")
	}

	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		BuildSessionFn: buildSessionFn,
	}

	f := &feature.Feature{
		ID:   "test-feat",
		Name: "Test Feature",
		Repos: []feature.FeatureRepo{
			{Name: "myrepo", Path: "/tmp/myrepo", WorktreePath: "/tmp/myrepo-wt"},
		},
	}

	// Call without any TweakSessionConfig — backward compatible path
	_, err := pr.RunTweakSession(f)
	if err == nil {
		t.Fatal("expected error from mock BuildSession")
	}

	// Session ID should NOT contain a repo name segment — just "<id>-impl-tweak"
	// We verify indirectly via RepoName being empty in BuildSessionOpts.
	if captured.RepoName != "" {
		t.Errorf("RepoName = %q, want empty (default config)", captured.RepoName)
	}

	// Under the unified shape, resolveUnifiedWorkDir returns the worktree
	// parent dir for any feature where every repo has a worktree (so
	// multi-repo planning can address each per-repo worktree from one
	// CWD). For this single-repo fixture that's "/tmp".
	if captured.WorkDir != "/tmp" {
		t.Errorf("WorkDir = %q, want %q (worktree parent)", captured.WorkDir, "/tmp")
	}

	// AdditionalDirs should include stateDir plus every per-repo worktree.
	wantDirs := []string{stateDir, "/tmp/myrepo-wt"}
	if !reflect.DeepEqual(captured.AdditionalDirs, wantDirs) {
		t.Errorf("AdditionalDirs = %v, want %v", captured.AdditionalDirs, wantDirs)
	}
}
