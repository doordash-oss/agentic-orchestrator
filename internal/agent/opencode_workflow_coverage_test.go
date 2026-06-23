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
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	opencode "github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// These workflow-coverage tests prove that an OpenCode model selection routes
// through the SAME provider-neutral session builder (PhaseRunner.BuildSession)
// that every Agentico workflow reaches, but they exercise that routing through
// the user-reachable workflow ENTRY POINTS — RunKnowledgeBaseForRepo,
// RunInquire, RunResearchFromQuestions, RunDesign, the roadmap/phase planning
// loops, the implementation loop, and RunBoundedHelper — rather than calling
// BuildSession directly. Each entry point selects the model from the feature's
// configuration, renders the role's prompt, computes the working directory and
// mounted context, and threads the marker contract; the tests capture what the
// entry point hands to the session manager and assert the OpenCode launch is
// correct. They never shell out to a live `opencode` CLI, require OpenCode
// authentication, or touch the user's global OpenCode configuration: the real
// OpenCode provider builds the command/protocol, and a fake session manager
// intercepts the launch before any process starts.

const (
	openCodeWorkflowModel   = "opencode:anthropic/claude-sonnet-4-5"
	openCodeWorkflowBackend = "anthropic/claude-sonnet-4-5"
)

// openCodeRoleInstructions parses OPENCODE_CONFIG_CONTENT, follows the
// instructions[] file reference, and returns the role-instructions body that
// OpenCode loads as its system prompt. OpenCode's ACP surface exposes no
// session-time system-prompt method, so a managed instructions file is the only
// channel for the role prompt (Phase 4) — which makes it the place where
// role-prompt identity is observable.
func openCodeRoleInstructions(t *testing.T, env []string) string {
	t.Helper()
	var cfg struct {
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(openCodeManagedConfig(t, env)), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not valid JSON: %v", err)
	}
	if len(cfg.Instructions) == 0 {
		t.Fatalf("managed config carried no instructions file")
	}
	body, err := os.ReadFile(cfg.Instructions[0])
	if err != nil {
		t.Fatalf("reading managed role instructions %q: %v", cfg.Instructions[0], err)
	}
	return string(body)
}

// openCodeManagedConfig returns the raw OPENCODE_CONFIG_CONTENT env value, which
// encodes the managed permission/read-root map and the instructions file
// reference for a launched OpenCode session.
func openCodeManagedConfig(t *testing.T, env []string) string {
	t.Helper()
	for _, e := range env {
		if after, ok := strings.CutPrefix(e, "OPENCODE_CONFIG_CONTENT="); ok {
			return after
		}
	}
	t.Fatalf("env %v missing OPENCODE_CONFIG_CONTENT", env)
	return ""
}

// capturedLaunch records the command, env, working directory, and session opts
// an entry point threads into the session manager. Only the first session is
// retained — for the multi-session planning/implement loops that is the primary
// workflow session (the planner / implementer), not a downstream validator or
// reviewer helper.
type capturedLaunch struct {
	count   int
	cmd     []string
	env     []string
	workdir string
	opts    *ports.SessionOpts
}

func (c *capturedLaunch) record(cmd, env []string, workdir string, opts []*ports.SessionOpts) {
	c.count++
	if c.count > 1 {
		return
	}
	c.cmd = cmd
	c.env = env
	c.workdir = workdir
	if len(opts) > 0 {
		c.opts = opts[0]
	}
}

// startSessionCapture builds a MockSessionManager.StartSessionFn that records the
// launch and returns an unstarted session handle. Async entry points (KB build,
// inquiry, research, design) return immediately after StartSession, so an
// unstarted handle is enough to let them install trackers and return without
// spawning a process.
func (c *capturedLaunch) startSessionFn() func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
	return func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		c.record(command, env, workdir, opts)
		return session.NewSession(id, featureID, phase), nil
	}
}

// loopSessionStartFunc builds a SessionStartFunc for the planning/implement
// loops that records the launch and returns ErrSessionShuttingDown so the loop
// exits cleanly with an "interrupted" result the moment OpenCode would have been
// launched — proving the loop selected and wired OpenCode without running it.
func (c *capturedLaunch) loopSessionStartFunc() func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
	return func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
		c.record(command, env, workdir, opts)
		return nil, ports.ErrSessionShuttingDown
	}
}

// assertOpenCodeLaunch checks the OpenCode launch contract shared by every
// workflow surface: it launched `opencode acp`, selected the OpenCode provider
// and protocol against the expected backend model, ran in the expected working
// directory, mounted the feature state and skills directories as read roots
// (add-dir/context setup), and delivered a non-empty role-instructions body.
// It returns the protocol and the rendered role instructions for surface-specific
// marker and role-identity assertions.
func assertOpenCodeLaunch(t *testing.T, surface string, c *capturedLaunch, wantWorkDir, stateDir, skillsDir string) (*opencode.Protocol, string) {
	t.Helper()
	if c.count == 0 {
		t.Fatalf("%s: entry point never reached the session manager", surface)
	}
	if !slices.Equal(c.cmd, []string{"opencode", "acp"}) {
		t.Fatalf("%s: cmd = %v, want [opencode acp]", surface, c.cmd)
	}
	if c.opts == nil || c.opts.ProviderName != "opencode" {
		t.Fatalf("%s: sessOpts = %#v, want ProviderName opencode", surface, c.opts)
	}
	proto, ok := c.opts.Protocol.(*opencode.Protocol)
	if !ok {
		t.Fatalf("%s: Protocol type = %T, want *opencode.Protocol", surface, c.opts.Protocol)
	}
	if got := proto.BackendModelForTest(); got != openCodeWorkflowBackend {
		t.Fatalf("%s: backend model = %q, want %q", surface, got, openCodeWorkflowBackend)
	}
	if got := proto.WorkDirForTest(); got != wantWorkDir {
		t.Fatalf("%s: protocol work dir = %q, want %q", surface, got, wantWorkDir)
	}
	if c.workdir != wantWorkDir {
		t.Fatalf("%s: StartSession work dir = %q, want %q", surface, c.workdir, wantWorkDir)
	}
	// add-dir / mounted-context setup: the feature state dir and the skills dir
	// reach OpenCode as read roots in the managed permission map.
	cfgContent := openCodeManagedConfig(t, c.env)
	if !strings.Contains(cfgContent, stateDir) {
		t.Fatalf("%s: managed config does not mount state dir %q as a read root:\n%s", surface, stateDir, cfgContent)
	}
	if skillsDir != "" && !strings.Contains(cfgContent, skillsDir) {
		t.Fatalf("%s: managed config does not mount skills dir %q as a read root:\n%s", surface, skillsDir, cfgContent)
	}
	instructions := openCodeRoleInstructions(t, c.env)
	if strings.TrimSpace(instructions) == "" {
		t.Fatalf("%s: managed role instructions are empty", surface)
	}
	return proto, instructions
}

// assertMarkerThreaded asserts a marker-backed surface threaded a real
// phase_complete marker into the OpenCode protocol and that the rendered role
// prompt references that same marker path — the role prompt and the protocol
// agree on the completion contract. The OpenCode marker oracle
// (opencode.TestPromptSuccessRequiresMarkerToCompletePhase) separately proves an
// ACP end_turn success is NOT classified complete unless the marker exists, so
// threading the per-role marker here closes the chain from workflow surface to
// provider completion semantics.
func assertMarkerThreaded(t *testing.T, surface string, proto *opencode.Protocol, instructions string) {
	t.Helper()
	marker := proto.MarkerPathForTest()
	if marker == "" {
		t.Fatalf("%s: marker-backed surface threaded an empty marker path", surface)
	}
	if filepath.Base(marker) != PhaseCompleteFile {
		t.Fatalf("%s: marker path = %q, want a %q file", surface, marker, PhaseCompleteFile)
	}
	if !strings.Contains(instructions, marker) {
		t.Fatalf("%s: role instructions do not reference threaded marker path %q", surface, marker)
	}
}

// TestOpenCodeRoutesThroughEveryMarkerBackedWorkflowEntryPoint drives an OpenCode
// model selection through the real, user-reachable entry point for every primary
// marker-backed workflow surface — KB build, inquiry, research, design, roadmap
// planning, phase planning, and implementation — and asserts each one selects
// the configured OpenCode model, launches `opencode acp`, threads the role's
// phase_complete marker into the OpenCode protocol, runs in the entry point's
// computed working directory with the state/skills dirs mounted, and delivers a
// distinct rendered role prompt to OpenCode as its instructions.
func TestOpenCodeRoutesThroughEveryMarkerBackedWorkflowEntryPoint(t *testing.T) {
	// Rendered role-instruction bodies per surface, used to prove role-prompt
	// identity is distinct per workflow surface (not shared boilerplate).
	rendered := map[string]string{}

	t.Run("kb_build", func(t *testing.T) {
		dir := t.TempDir()
		skillsDir := filepath.Join(dir, "skills")
		repoDir := filepath.Join(dir, "repo")
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("mkdir repo: %v", err)
		}

		cap := &capturedLaunch{}
		sm := mocks.NewMockSessionManager()
		sm.StartSessionFn = cap.startSessionFn()
		pr := NewPhaseRunner(sm, feature.NewStore(dir), dir)
		pr.Registry = newRegistryWithOpenCode()
		pr.SkillsDir = skillsDir

		f := &feature.Feature{
			ID:            "feat-oc-kb",
			Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: repoDir}},
			Models:        config.ModelConfig{KBBuild: openCodeWorkflowModel},
			SchemaVersion: feature.SchemaVersionCurrent,
		}
		if _, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0]); err != nil {
			t.Fatalf("RunKnowledgeBaseForRepo() error: %v", err)
		}

		proto, instructions := assertOpenCodeLaunch(t, "kb_build", cap, repoDir, dir, skillsDir)
		wantMarker := filepath.Join(KBStateDir(dir, "repo-a"), PhaseCompleteFile)
		if got := proto.MarkerPathForTest(); got != wantMarker {
			t.Fatalf("kb_build: marker path = %q, want %q", got, wantMarker)
		}
		assertMarkerThreaded(t, "kb_build", proto, instructions)
		assertContainsSkill(t, "kb_build", instructions, skillsDir, "build-knowledge-base")
		rendered["kb_build"] = instructions
	})

	interactive := []struct {
		name      string
		dirName   string
		skillName string
		run       func(pr *PhaseRunner, f *feature.Feature) (string, error)
	}{
		{
			name: "inquiry", dirName: "inquire", skillName: "inquire",
			run: func(pr *PhaseRunner, f *feature.Feature) (string, error) { return pr.RunInquire(f) },
		},
		{
			name: "research", dirName: "research", skillName: "research-codebase",
			run: func(pr *PhaseRunner, f *feature.Feature) (string, error) {
				qPath := filepath.Join(pr.StateDir, "questions.md")
				if err := os.WriteFile(qPath, []byte("1. Question?\n"), 0o644); err != nil {
					return "", err
				}
				return pr.RunResearchFromQuestions(f, qPath)
			},
		},
		{
			name: "design", dirName: feature.PhaseDesign.DirName(), skillName: "design",
			run: func(pr *PhaseRunner, f *feature.Feature) (string, error) {
				return pr.RunDesign(f, "research output summary", nil)
			},
		},
	}
	for _, tc := range interactive {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			skillsDir := filepath.Join(dir, "skills")
			repoDir := filepath.Join(dir, "repo")
			if err := os.MkdirAll(repoDir, 0o755); err != nil {
				t.Fatalf("mkdir repo: %v", err)
			}

			cap := &capturedLaunch{}
			sm := mocks.NewMockSessionManager()
			sm.StartSessionFn = cap.startSessionFn()
			pr := NewPhaseRunner(sm, feature.NewStore(dir), dir)
			pr.Registry = newRegistryWithOpenCode()
			pr.SkillsDir = skillsDir

			// The interactive phases (inquire, research, design) all select the
			// research-role model, so an OpenCode research selection must route
			// each of them through OpenCode.
			f := &feature.Feature{
				ID:            "feat-oc-" + tc.name,
				Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: repoDir}},
				Models:        config.ModelConfig{Research: openCodeWorkflowModel},
				SchemaVersion: feature.SchemaVersionCurrent,
			}
			if _, err := tc.run(pr, f); err != nil {
				t.Fatalf("Run%s error: %v", tc.name, err)
			}

			proto, instructions := assertOpenCodeLaunch(t, tc.name, cap, repoDir, dir, skillsDir)
			wantMarker := filepath.Join(pr.resolvePhaseArtifactDir(f, tc.dirName), PhaseCompleteFile)
			if got := proto.MarkerPathForTest(); got != wantMarker {
				t.Fatalf("%s: marker path = %q, want %q", tc.name, got, wantMarker)
			}
			assertMarkerThreaded(t, tc.name, proto, instructions)
			assertContainsSkill(t, tc.name, instructions, skillsDir, tc.skillName)
			rendered[tc.name] = instructions
		})
	}

	t.Run("roadmap_planning", func(t *testing.T) {
		dir := t.TempDir()
		skillsDir := filepath.Join(dir, "skills")
		workDir := filepath.Join(dir, "work")
		planDir := filepath.Join(dir, "feat-oc-roadmap", "runs", "run-001", "roadmap")
		for _, d := range []string{skillsDir, workDir, planDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", d, err)
			}
		}

		cap := &capturedLaunch{}
		eventCh := make(chan any, 8)
		sm := session.NewManager(eventCh)
		defer sm.Shutdown()
		store := feature.NewStore(dir)
		pr := NewPhaseRunner(sm, store, dir)
		pr.Registry = newRegistryWithOpenCode()
		pr.SkillsDir = skillsDir

		f := &feature.Feature{
			ID:            "feat-oc-roadmap",
			Name:          "OpenCode Roadmap",
			Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: workDir}},
			Models:        config.ModelConfig{Planning: openCodeWorkflowModel},
			SchemaVersion: feature.SchemaVersionCurrent,
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature: %v", err)
		}

		result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
			Feature:          f,
			FeatureStore:     store,
			StateDir:         dir,
			WorkDir:          workDir,
			SkillsDir:        skillsDir,
			MaxAttempts:      1,
			BuildSession:     pr.BuildSession,
			SessionStartFunc: cap.loopSessionStartFunc(),
		}, sm)
		if err != nil {
			t.Fatalf("RunRoadmapPlanningLoop() error: %v", err)
		}
		if result.FinalStatus != "interrupted" {
			t.Fatalf("roadmap_planning: FinalStatus = %q, want interrupted", result.FinalStatus)
		}

		proto, instructions := assertOpenCodeLaunch(t, "roadmap_planning", cap, workDir, dir, skillsDir)
		assertMarkerThreaded(t, "roadmap_planning", proto, instructions)
		if !strings.HasPrefix(proto.MarkerPathForTest(), dir) {
			t.Fatalf("roadmap_planning: marker %q not under state dir %q", proto.MarkerPathForTest(), dir)
		}
		assertContainsSkill(t, "roadmap_planning", instructions, skillsDir, "create-roadmap")
		rendered["roadmap_planning"] = instructions
	})

	t.Run("phase_planning", func(t *testing.T) {
		dir := t.TempDir()
		skillsDir := filepath.Join(dir, "skills")
		workDir := filepath.Join(dir, "work")
		phasePlanDir := filepath.Join(dir, "feat-oc-phase", "runs", "run-001", "phase-01", "plan")
		for _, d := range []string{skillsDir, workDir, phasePlanDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", d, err)
			}
		}

		cap := &capturedLaunch{}
		eventCh := make(chan any, 8)
		sm := session.NewManager(eventCh)
		defer sm.Shutdown()
		store := feature.NewStore(dir)
		pr := NewPhaseRunner(sm, store, dir)
		pr.Registry = newRegistryWithOpenCode()
		pr.SkillsDir = skillsDir

		f := &feature.Feature{
			ID:            "feat-oc-phase",
			Name:          "OpenCode Phase Plan",
			Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: workDir}},
			Models:        config.ModelConfig{Planning: openCodeWorkflowModel},
			SchemaVersion: feature.SchemaVersionCurrent,
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature: %v", err)
		}

		result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
			PlanLoopConfig: PlanLoopConfig{
				Feature:          f,
				FeatureStore:     store,
				StateDir:         dir,
				WorkDir:          workDir,
				SkillsDir:        skillsDir,
				MaxAttempts:      1,
				BuildSession:     pr.BuildSession,
				SessionStartFunc: cap.loopSessionStartFunc(),
			},
			Phase: RoadmapPhase{Number: 1, Name: "Phase One", Type: "tdd-fill-in", Goal: "Prove OpenCode phase planning"},
		}, sm)
		if err != nil {
			t.Fatalf("RunPhasePlanningLoop() error: %v", err)
		}
		if result.FinalStatus != "interrupted" {
			t.Fatalf("phase_planning: FinalStatus = %q, want interrupted", result.FinalStatus)
		}

		proto, instructions := assertOpenCodeLaunch(t, "phase_planning", cap, workDir, dir, skillsDir)
		assertMarkerThreaded(t, "phase_planning", proto, instructions)
		if !strings.HasPrefix(proto.MarkerPathForTest(), dir) {
			t.Fatalf("phase_planning: marker %q not under state dir %q", proto.MarkerPathForTest(), dir)
		}
		assertContainsSkill(t, "phase_planning", instructions, skillsDir, "plan-phase")
		rendered["phase_planning"] = instructions
	})

	t.Run("implementation", func(t *testing.T) {
		dir := t.TempDir()
		skillsDir := filepath.Join(dir, "skills")
		workDir := filepath.Join(dir, "work")
		artifactDir := filepath.Join(dir, "artifacts")
		stateDir := filepath.Join(dir, "state")
		for _, d := range []string{skillsDir, workDir, artifactDir, stateDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", d, err)
			}
		}
		planPath := filepath.Join(artifactDir, "plan.md")
		if err := os.WriteFile(planPath, []byte("# Plan\n"), 0o644); err != nil {
			t.Fatalf("write plan: %v", err)
		}

		cap := &capturedLaunch{}
		eventCh := make(chan any, 8)
		sm := session.NewManager(eventCh)
		defer sm.Shutdown()
		pr := NewPhaseRunner(sm, feature.NewStore(dir), stateDir)
		pr.Registry = newRegistryWithOpenCode()
		pr.SkillsDir = skillsDir

		f := &feature.Feature{
			ID:            "feat-oc-impl",
			Name:          "OpenCode Implement",
			Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: workDir}},
			SchemaVersion: feature.SchemaVersionCurrent,
		}

		result, err := RunImplementationLoop(ImplementConfig{
			Feature:             f,
			WorkDir:             workDir,
			PlanPath:            planPath,
			MaxIterations:       1,
			MaxConsecFails:      2,
			MaxConsecNoProgress: 2,
			ExitCriteria:        "Relevant tests pass",
			Model:               openCodeWorkflowModel,
			ReviewModel:         openCodeWorkflowModel,
			ArtifactDir:         artifactDir,
			StateDir:            stateDir,
			SkillsDir:           skillsDir,
			BuildSession:        pr.BuildSession,
			SessionStartFunc:    cap.loopSessionStartFunc(),
		}, sm)
		if err != nil {
			t.Fatalf("RunImplementationLoop() error: %v", err)
		}
		if result.FinalStatus != "interrupted" {
			t.Fatalf("implementation: FinalStatus = %q, want interrupted", result.FinalStatus)
		}

		proto, instructions := assertOpenCodeLaunch(t, "implementation", cap, workDir, stateDir, skillsDir)
		wantMarker := filepath.Join(artifactDir, "iteration-01", PhaseCompleteFile)
		if got := proto.MarkerPathForTest(); got != wantMarker {
			t.Fatalf("implementation: marker path = %q, want %q", got, wantMarker)
		}
		assertMarkerThreaded(t, "implementation", proto, instructions)
		assertContainsSkill(t, "implementation", instructions, skillsDir, "implement")
		rendered["implementation"] = instructions
	})

	// Every covered surface delivered a distinct role prompt to OpenCode.
	seen := map[string]string{}
	for name, body := range rendered {
		if other, dup := seen[body]; dup {
			t.Fatalf("workflow surfaces %s and %s delivered identical role prompts to OpenCode", name, other)
		}
		seen[body] = name
	}
	for _, surface := range []string{"kb_build", "inquiry", "research", "design", "roadmap_planning", "phase_planning", "implementation"} {
		if _, ok := rendered[surface]; !ok {
			t.Fatalf("surface %s did not record a rendered role prompt", surface)
		}
	}
}

// assertContainsSkill asserts the rendered role instructions reference the
// surface's distinctive SKILL.md path, anchoring role-prompt identity to the
// specific role the entry point rendered.
func assertContainsSkill(t *testing.T, surface, instructions, skillsDir, skillName string) {
	t.Helper()
	skillPath := filepath.Join(skillsDir, skillName, "SKILL.md")
	if !strings.Contains(instructions, skillPath) {
		t.Fatalf("%s: role instructions missing distinctive skill path %q", surface, skillPath)
	}
}

// TestOpenCodeBoundedHelperRoutesThroughRunBoundedHelper drives a default
// (markerless) bounded helper run through the real RunBoundedHelper entry point
// with an OpenCode model selection. It proves the helper routes OpenCode through
// the normal session builder — launching `opencode acp` against the selected
// backend in the requested working directory — while staying intentionally
// markerless: no phase_complete marker is threaded into the OpenCode protocol,
// so the helper's terminal classification never depends on a marker.
func TestOpenCodeBoundedHelperRoutesThroughRunBoundedHelper(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	workDir := filepath.Join(dir, "work")
	for _, d := range []string{skillsDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	cap := &capturedLaunch{}
	sm := mocks.NewMockSessionManager()
	// Capture the launch, then report shutdown so RunBoundedHelper returns
	// immediately instead of attaching to a session it cannot drive here.
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		cap.record(command, env, workdir, opts)
		return nil, ports.ErrSessionShuttingDown
	}
	pr := NewPhaseRunner(sm, feature.NewStore(dir), dir)
	pr.Registry = newRegistryWithOpenCode()
	pr.SkillsDir = skillsDir

	const helperPrompt = "summarize the provided files"
	_, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:    "feat-oc-helper-scout",
		FeatureID:    "feat-oc-helper",
		Phase:        feature.PhaseResearch,
		Model:        openCodeWorkflowModel,
		Prompt:       helperPrompt,
		SystemPrompt: "You are a bounded research helper.",
		WorkDir:      workDir,
		PermHandler:  &permission.ReadOnlyHandler{},
		// PhaseCompleteDir intentionally empty: a default bounded helper is markerless.
	})
	// The shutdown sentinel surfaces as a start-session error; that is expected
	// and not what this test asserts — the launch capture is.
	if err == nil {
		t.Fatal("RunBoundedHelper() error = nil, want the injected shutdown error")
	}

	proto, instructions := assertOpenCodeLaunch(t, "bounded_helper", cap, workDir, dir, skillsDir)
	if got := proto.MarkerPathForTest(); got != "" {
		t.Fatalf("bounded_helper: marker path = %q, want empty (markerless)", got)
	}
	if got := proto.InitialPromptForTest(); got != helperPrompt {
		t.Fatalf("bounded_helper: initial prompt = %q, want %q", got, helperPrompt)
	}
	if strings.TrimSpace(instructions) == "" {
		t.Fatal("bounded_helper: role instructions are empty")
	}
}
