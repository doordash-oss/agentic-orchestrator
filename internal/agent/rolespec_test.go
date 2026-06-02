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
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

var updateRoleSpecGenerated = flag.Bool("update", false, "rewrite generated RoleSpec-backed artifacts")

func TestImplementRoleSpecShape(t *testing.T) {
	spec := ImplementRoleSpec()
	if spec.Phase != feature.PhaseImplement {
		t.Fatalf("ImplementRoleSpec().Phase = %v, want %v", spec.Phase, feature.PhaseImplement)
	}
	if spec.Role != RoleImplementer {
		t.Fatalf("ImplementRoleSpec().Role = %q, want %q", spec.Role, RoleImplementer)
	}
	if spec.SkillName != "implement" {
		t.Fatalf("ImplementRoleSpec().SkillName = %q, want implement", spec.SkillName)
	}
	if spec.UserTemplate != "implement.user" {
		t.Fatalf("ImplementRoleSpec().UserTemplate = %q, want implement.user", spec.UserTemplate)
	}
	if spec.MarkerRoot != "iteration_dir" {
		t.Fatalf("ImplementRoleSpec().MarkerRoot = %q, want iteration_dir", spec.MarkerRoot)
	}

	var rootNames []string
	for _, root := range spec.OutputRoots {
		rootNames = append(rootNames, root.Name)
	}
	if !slices.Equal(rootNames, []string{"phase_dir", "iteration_dir"}) {
		t.Fatalf("ImplementRoleSpec() output roots = %v, want [phase_dir iteration_dir]", rootNames)
	}

	artifacts := map[string]RoleArtifactSpec{}
	for _, artifact := range spec.Artifacts {
		artifacts[artifact.Name] = artifact
	}
	checks := []struct {
		name        string
		root        string
		path        string
		presence    ArtifactPresence
		conditional bool
	}{
		{name: "progress", root: "phase_dir", path: "progress.md", presence: ArtifactRequired},
		{name: "verification_report", root: "iteration_dir", path: "verification-report.yaml", presence: ArtifactRequired},
		{name: "need_user_input", root: "iteration_dir", path: "need-user-input.yaml", presence: ArtifactConditional, conditional: true},
	}
	for _, tt := range checks {
		got, ok := artifacts[tt.name]
		if !ok {
			t.Fatalf("ImplementRoleSpec() missing artifact %q", tt.name)
		}
		if got.RootName != tt.root || got.RelativePath != tt.path || got.Presence != tt.presence {
			t.Fatalf("artifact %q = {root:%q path:%q presence:%q}, want {root:%q path:%q presence:%q}",
				tt.name, got.RootName, got.RelativePath, got.Presence, tt.root, tt.path, tt.presence)
		}
		if (got.When != "") != tt.conditional {
			t.Fatalf("artifact %q conditional predicate set = %v, want %v", tt.name, got.When != "", tt.conditional)
		}
		if got.Description == "" {
			t.Fatalf("artifact %q Description is empty", tt.name)
		}
	}
}

func TestImplementRoleSpecDerivesContractPaths(t *testing.T) {
	spec := ImplementRoleSpec()
	contract := spec.Contract()
	if contract.Role != RoleImplementer {
		t.Fatalf("spec.Contract().Role = %q, want %q", contract.Role, RoleImplementer)
	}
	if len(contract.Required) != 2 {
		t.Fatalf("spec.Contract().Required length = %d, want 2", len(contract.Required))
	}
	if len(contract.Conditional) != 1 {
		t.Fatalf("spec.Contract().Conditional length = %d, want 1", len(contract.Conditional))
	}

	iterDir := filepath.Join(t.TempDir(), "phase-01", "implement", "iteration-02")
	wantPaths := map[string]string{
		"progress":            filepath.Join(filepath.Dir(iterDir), "progress.md"),
		"verification_report": filepath.Join(iterDir, "verification-report.yaml"),
	}
	for _, artifact := range contract.Required {
		if got, want := artifact.ResolvePath(iterDir), wantPaths[artifact.Name]; got != want {
			t.Fatalf("required artifact %q path = %q, want %q", artifact.Name, got, want)
		}
	}
	if got, want := contract.Conditional[0].Artifact.ResolvePath(iterDir), filepath.Join(iterDir, "need-user-input.yaml"); got != want {
		t.Fatalf("conditional need_user_input path = %q, want %q", got, want)
	}
}

func TestBuildImplementSystemPromptFromRoleSpec(t *testing.T) {
	got := BuildImplementSystemPrompt(BuildImplementSystemPromptInput{
		IterationDir:  "/state/feat-x/run-001/phase-01/implement/iteration-02",
		SkillsDir:     "/skills",
		GuidelinesDir: "/guidelines",
		KBInfos: []KBInfo{
			{Name: "agentic", IndexPath: "/kb/agentic/index.md", RootDir: "/kb/agentic"},
		},
		AskingClause: "## Asking Questions\n\nAsk one question at a time.",
	})

	for _, want := range []string{
		"## Output Roots",
		"`phase_dir`: /state/feat-x/run-001/phase-01/implement",
		"`iteration_dir`: /state/feat-x/run-001/phase-01/implement/iteration-02",
		"/state/feat-x/run-001/phase-01/implement/iteration-02/phase_complete",
		"/skills/implement/SKILL.md",
		"Read the SKILL.md file completely before taking any other action.",
		"# Useful Resources",
		"/kb/agentic/index.md",
		"/guidelines/go/index.md",
		"/skills/knowledge-reader/SKILL.md",
		"Ask one question at a time.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildImplementSystemPrompt() missing %q in:\n%s", want, got)
		}
	}
	for _, oldSystemDetail := range []string{
		"Write `verification-report.yaml` at the path your user prompt names",
		"Write `progress.md` at the path your user prompt names",
		"Need-user-input gate",
	} {
		if strings.Contains(got, oldSystemDetail) {
			t.Fatalf("BuildImplementSystemPrompt() still contains old implement-only system detail %q:\n%s", oldSystemDetail, got)
		}
	}
}

func TestRoleSpecCarriesRequiredPhasesAndAskingClauseProvider(t *testing.T) {
	spec := ImplementRoleSpec()
	if !slices.Equal(spec.Required, []feature.Phase{feature.PhasePlan}) {
		t.Fatalf("ImplementRoleSpec().Required = %v, want [%s]", spec.Required, feature.PhasePlan)
	}

	spec.AskingClauseFor = func(model string) string {
		return "## Asking Questions\n\nAsk via provider for " + model + "."
	}
	got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:         spec,
		IterationDir: "/state/feat-x/run-001/phase-01/implement/iteration-02",
		SkillsDir:    "/skills",
		Model:        "codex-test",
	})
	if !strings.Contains(got, "Ask via provider for codex-test.") {
		t.Fatalf("BuildRoleSystemPrompt() did not use RoleSpec.AskingClauseFor:\n%s", got)
	}
}

func TestPlanningRoleSpecsDeriveContractPaths(t *testing.T) {
	base := t.TempDir()
	roadmapAttemptDir := filepath.Join(base, "roadmap", "attempt-02")
	phasePlanAttemptDir := filepath.Join(base, "phase-02", "plan", "attempt-03")

	tests := []struct {
		name       string
		spec       RoleSpec
		attemptDir string
		wantRoots  []string
		wantPaths  map[string]string
	}{
		{
			name:       "roadmap creator",
			spec:       RoadmapCreatorRoleSpec(),
			attemptDir: roadmapAttemptDir,
			wantRoots:  []string{"artifact_dir", "attempt_dir"},
			wantPaths: map[string]string{
				"roadmap":           filepath.Join(base, "roadmap", "roadmap.md"),
				"plan_attempt_meta": filepath.Join(base, "roadmap", "attempt-02", "meta.yaml"),
			},
		},
		{
			name:       "roadmap reviser",
			spec:       RoadmapReviserRoleSpec(),
			attemptDir: roadmapAttemptDir,
			wantRoots:  []string{"artifact_dir", "attempt_dir"},
			wantPaths: map[string]string{
				"roadmap":           filepath.Join(base, "roadmap", "roadmap.md"),
				"plan_attempt_meta": filepath.Join(base, "roadmap", "attempt-02", "meta.yaml"),
			},
		},
		{
			name:       "phase plan creator",
			spec:       PhasePlanCreatorRoleSpec(),
			attemptDir: phasePlanAttemptDir,
			wantRoots:  []string{"artifact_dir", "attempt_dir"},
			wantPaths: map[string]string{
				"phase_plan_markdown": filepath.Join(base, "phase-02", "plan", "phase-plan.md"),
				"plan_attempt_meta":   filepath.Join(base, "phase-02", "plan", "attempt-03", "meta.yaml"),
			},
		},
		{
			name:       "phase plan reviser",
			spec:       PhasePlanReviserRoleSpec(),
			attemptDir: phasePlanAttemptDir,
			wantRoots:  []string{"artifact_dir", "attempt_dir"},
			wantPaths: map[string]string{
				"phase_plan_markdown": filepath.Join(base, "phase-02", "plan", "phase-plan.md"),
				"plan_attempt_meta":   filepath.Join(base, "phase-02", "plan", "attempt-03", "meta.yaml"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRootNames []string
			for _, root := range tt.spec.OutputRoots {
				gotRootNames = append(gotRootNames, root.Name)
			}
			if !slices.Equal(gotRootNames, tt.wantRoots) {
				t.Fatalf("%s output roots = %v, want %v", tt.name, gotRootNames, tt.wantRoots)
			}
			if got := tt.spec.MarkerPath(RoleRuntime{IterationDir: tt.attemptDir}); got != filepath.Join(tt.attemptDir, "phase_complete") {
				t.Fatalf("%s marker path = %q, want attempt-local phase_complete", tt.name, got)
			}

			contract := tt.spec.Contract()
			if len(contract.Required) != len(tt.wantPaths) {
				t.Fatalf("%s required artifacts = %d, want %d", tt.name, len(contract.Required), len(tt.wantPaths))
			}
			for _, artifact := range contract.Required {
				want, ok := tt.wantPaths[artifact.Name]
				if !ok {
					t.Fatalf("%s unexpected required artifact %q", tt.name, artifact.Name)
				}
				if got := artifact.ResolvePath(tt.attemptDir); got != want {
					t.Fatalf("%s artifact %q path = %q, want %q", tt.name, artifact.Name, got, want)
				}
			}

			section := RenderRoleSpecOutputFilesSection(tt.spec)
			if strings.Contains(section, "meta.yaml") {
				t.Fatalf("%s Output Files section exposes harness-authored meta.yaml:\n%s", tt.name, section)
			}
		})
	}
}

func TestSingleShotProducerRoleSpecsDeriveContractPaths(t *testing.T) {
	base := t.TempDir()

	tests := []struct {
		name           string
		spec           RoleSpec
		phaseDir       string
		wantPhase      feature.Phase
		wantRole       Role
		wantSkill      string
		wantArtifact   string
		wantPath       string
		wantMarker     string
		wantHidden     []string
		wantMarkerRoot string
		wantRoots      []string
		wantRenderRoot string // output-root the visible artifact renders under
	}{
		{
			name:           "knowledge base",
			spec:           KnowledgeBaseBuilderRoleSpec(),
			phaseDir:       filepath.Join(base, "knowledge-base", "agentic"),
			wantPhase:      feature.PhaseKnowledgeBase,
			wantRole:       RoleKnowledgeBaseBuilder,
			wantSkill:      "build-knowledge-base",
			wantArtifact:   "knowledge_base_index",
			wantPath:       filepath.Join(base, "knowledge-base", "agentic", "index.md"),
			wantMarker:     filepath.Join(base, "knowledge-base", "agentic", "phase_complete"),
			wantHidden:     []string{"kb_progress_handoff", "iteration_meta"},
			wantMarkerRoot: "iteration_dir",
			wantRoots:      []string{"phase_dir", "iteration_dir"},
		},
		{
			name:           "inquire",
			spec:           InquirerRoleSpec(),
			phaseDir:       filepath.Join(base, "runs", "run-001", "inquire", "iteration-02"),
			wantPhase:      feature.PhaseInquire,
			wantRole:       RoleInquirer,
			wantSkill:      "inquire",
			wantArtifact:   "inquire_markdown_artifact",
			wantPath:       filepath.Join(base, "runs", "run-001", "inquire"),
			wantMarker:     filepath.Join(base, "runs", "run-001", "inquire", "iteration-02", "phase_complete"),
			wantHidden:     []string{"inquire_progress_handoff", "iteration_meta"},
			wantMarkerRoot: "phase_dir",
			wantRoots:      []string{"artifact_dir", "phase_dir"},
			wantRenderRoot: "artifact_dir",
		},
		{
			name:         "research",
			spec:         ResearcherRoleSpec(),
			phaseDir:     filepath.Join(base, "runs", "run-001", "research", "iteration-02"),
			wantPhase:    feature.PhaseResearch,
			wantRole:     RoleResearcher,
			wantSkill:    "research-codebase",
			wantArtifact: "research_markdown_artifact",
			// Persistent deliverable resolves to the shared artifact_dir (parent
			// of the iteration dir), where research.md is edited in place.
			wantPath:       filepath.Join(base, "runs", "run-001", "research"),
			wantMarker:     filepath.Join(base, "runs", "run-001", "research", "iteration-02", "phase_complete"),
			wantHidden:     []string{"research_progress_handoff", "iteration_meta"},
			wantMarkerRoot: "phase_dir",
			wantRoots:      []string{"artifact_dir", "phase_dir"},
			wantRenderRoot: "artifact_dir",
		},
		{
			name:           "design",
			spec:           DesignerRoleSpec(),
			phaseDir:       filepath.Join(base, "runs", "run-001", "design", "iteration-02"),
			wantPhase:      feature.PhaseDesign,
			wantRole:       RoleDesigner,
			wantSkill:      "design",
			wantArtifact:   "design_markdown_artifact",
			wantPath:       filepath.Join(base, "runs", "run-001", "design"),
			wantMarker:     filepath.Join(base, "runs", "run-001", "design", "iteration-02", "phase_complete"),
			wantHidden:     []string{"design_progress_handoff", "iteration_meta"},
			wantMarkerRoot: "phase_dir",
			wantRoots:      []string{"artifact_dir", "phase_dir"},
			wantRenderRoot: "artifact_dir",
		},
		{
			// Repeated lookup protects clone stability for the canonical
			// Design role.
			name:           "design",
			spec:           DesignerRoleSpec(),
			phaseDir:       filepath.Join(base, "runs", "run-001", "design", "iteration-02"),
			wantPhase:      feature.PhaseDesign,
			wantRole:       RoleDesigner,
			wantSkill:      "design",
			wantArtifact:   "design_markdown_artifact",
			wantPath:       filepath.Join(base, "runs", "run-001", "design"),
			wantMarker:     filepath.Join(base, "runs", "run-001", "design", "iteration-02", "phase_complete"),
			wantHidden:     []string{"design_progress_handoff", "iteration_meta"},
			wantMarkerRoot: "phase_dir",
			wantRoots:      []string{"artifact_dir", "phase_dir"},
			wantRenderRoot: "artifact_dir",
		},
		{
			name:           "refactor plan",
			spec:           RefactorPlanRoleSpec(),
			phaseDir:       filepath.Join(base, "runs", "run-001", "refactor-1"),
			wantPhase:      feature.PhasePlan,
			wantRole:       RoleRefactorPlanStep,
			wantSkill:      "refactor",
			wantArtifact:   "refactor_plan_markdown",
			wantPath:       filepath.Join(base, "runs", "run-001", "refactor-1", "refactor-plan.md"),
			wantMarker:     filepath.Join(base, "runs", "run-001", "refactor-1", "phase_complete"),
			wantMarkerRoot: "phase_dir",
			wantRoots:      []string{"phase_dir"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.spec.Phase != tt.wantPhase {
				t.Fatalf("%s Phase = %v, want %v", tt.name, tt.spec.Phase, tt.wantPhase)
			}
			if tt.spec.Role != tt.wantRole {
				t.Fatalf("%s Role = %q, want %q", tt.name, tt.spec.Role, tt.wantRole)
			}
			if tt.spec.SkillName != tt.wantSkill {
				t.Fatalf("%s SkillName = %q, want %q", tt.name, tt.spec.SkillName, tt.wantSkill)
			}
			if tt.spec.MarkerRoot != tt.wantMarkerRoot {
				t.Fatalf("%s MarkerRoot = %q, want %s", tt.name, tt.spec.MarkerRoot, tt.wantMarkerRoot)
			}
			if got := rootNames(tt.spec); !slices.Equal(got, tt.wantRoots) {
				t.Fatalf("%s output roots = %v, want %v", tt.name, got, tt.wantRoots)
			}

			contract := tt.spec.Contract()
			if contract.Role != tt.wantRole {
				t.Fatalf("%s contract role = %q, want %q", tt.name, contract.Role, tt.wantRole)
			}
			wantRequired := 1 + len(tt.wantHidden)
			if len(contract.Required) != wantRequired {
				t.Fatalf("%s required artifacts = %d, want %d", tt.name, len(contract.Required), wantRequired)
			}
			requiredByName := make(map[string]RequiredArtifact, len(contract.Required))
			for _, artifact := range contract.Required {
				requiredByName[artifact.Name] = artifact
			}
			artifact, ok := requiredByName[tt.wantArtifact]
			if !ok {
				t.Fatalf("%s missing required artifact %q", tt.name, tt.wantArtifact)
			}
			if got := artifact.ResolvePath(tt.phaseDir); got != tt.wantPath {
				t.Fatalf("%s artifact %q path = %q, want %q", tt.name, tt.wantArtifact, got, tt.wantPath)
			}
			for _, hiddenName := range tt.wantHidden {
				if _, ok := requiredByName[hiddenName]; !ok {
					t.Fatalf("%s missing hidden required artifact %q", tt.name, hiddenName)
				}
			}
			if got := tt.spec.MarkerPath(RoleRuntime{IterationDir: tt.phaseDir}); got != tt.wantMarker {
				t.Fatalf("%s marker path = %q, want %q", tt.name, got, tt.wantMarker)
			}
			renderRoot := tt.wantRenderRoot
			if renderRoot == "" {
				renderRoot = "phase_dir"
			}
			if section := RenderRoleSpecOutputFilesSection(tt.spec); !strings.Contains(section, "{"+renderRoot+"}/") {
				t.Fatalf("%s generated section missing {%s}/ path:\n%s", tt.name, renderRoot, section)
			} else if strings.Contains(section, "research-progress.md") || strings.Contains(section, "inquire-progress.md") || strings.Contains(section, "design-progress.md") || strings.Contains(section, "kb-progress.md") || strings.Contains(section, "meta.yaml") {
				t.Fatalf("%s generated section exposes harness-hidden research artifacts:\n%s", tt.name, section)
			}
		})
	}
}

func TestBuildSingleShotSystemPromptFromRoleSpec(t *testing.T) {
	got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          DesignerRoleSpec(),
		IterationDir:  "/state/feat-x/runs/run-001/design",
		SkillsDir:     "/skills",
		GuidelinesDir: "/guidelines",
		KBInfos: []KBInfo{
			{Name: "agentic", IndexPath: "/kb/agentic/index.md", RootDir: "/kb/agentic"},
		},
		AskingClause: "## Asking Questions\n\nUse numbered alternatives.",
	})

	for _, want := range []string{
		"`phase_dir`: /state/feat-x/runs/run-001/design",
		"/state/feat-x/runs/run-001/design/phase_complete",
		"/skills/design/SKILL.md",
		"# Useful Resources",
		"/kb/agentic/index.md",
		"/guidelines/go/index.md",
		"/skills/guideline-reader/SKILL.md",
		"Use numbered alternatives.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildRoleSystemPrompt() missing %q in:\n%s", want, got)
		}
	}
}

func TestPlanValidatorRoleSpecs(t *testing.T) {
	specs := PlanValidatorRoleSpecs()
	if len(specs) != 8 {
		t.Fatalf("PlanValidatorRoleSpecs() length = %d, want 8", len(specs))
	}

	seen := map[string]bool{}
	for _, spec := range specs {
		if spec.Phase != feature.PhasePlan {
			t.Fatalf("%s Phase = %v, want PhasePlan", spec.SkillName, spec.Phase)
		}
		if spec.SkillName == "" {
			t.Fatalf("%s SkillName is empty", spec.Role)
		}
		seen[spec.SkillName] = true
		if got := spec.MarkerPath(RoleRuntime{IterationDir: "/tmp/attempt-01/validate-scope"}); got != "/tmp/attempt-01/validate-scope/phase_complete" {
			t.Fatalf("%s marker path = %q, want helper-local phase_complete", spec.SkillName, got)
		}
		if len(spec.Artifacts) != 1 {
			t.Fatalf("%s artifact count = %d, want feedback only", spec.SkillName, len(spec.Artifacts))
		}
		if spec.Artifacts[0].RootName != "helper_dir" || spec.Artifacts[0].Presence != ArtifactRequired {
			t.Fatalf("%s feedback artifact = {root:%q presence:%q}, want helper_dir required", spec.SkillName, spec.Artifacts[0].RootName, spec.Artifacts[0].Presence)
		}
	}
	for _, want := range []string{
		"validate-roadmap-architecture",
		"validate-roadmap-scope",
		"validate-phase-plan-structural",
		"validate-phase-plan-scope",
		"validate-phase-plan-grounding",
		"validate-plan-security",
		"validate-plan-performance",
		"validate-plan-testing",
	} {
		if !seen[want] {
			t.Fatalf("PlanValidatorRoleSpecs() missing skill %q", want)
		}
	}
}

func TestReviewFamilyRoleSpecs(t *testing.T) {
	tests := []struct {
		name       string
		phase      feature.Phase
		role       Role
		iterDir    string
		wantSkill  string
		wantRoots  []string
		wantMarker string
		wantPaths  map[string]string
		wantNoOp   bool
	}{
		{
			name:       "iteration reviewer",
			phase:      feature.PhaseReview,
			role:       RoleIterationReviewer,
			iterDir:    "/state/feat/run-001/phase-04/implement/iteration-01/review",
			wantSkill:  "review-implementation",
			wantRoots:  []string{"helper_dir"},
			wantMarker: "/state/feat/run-001/phase-04/implement/iteration-01/review/phase_complete",
			wantPaths: map[string]string{
				"review_feedback": "/state/feat/run-001/phase-04/implement/iteration-01/review/review-feedback.md",
			},
		},
		{
			name:       "final reviewer",
			phase:      feature.PhaseReview,
			role:       RoleFinalReviewer,
			iterDir:    "/state/feat/run-001/review/iteration-01",
			wantSkill:  "final-review",
			wantRoots:  []string{"iteration_dir"},
			wantMarker: "/state/feat/run-001/review/iteration-01/phase_complete",
			wantPaths: map[string]string{
				"review_feedback":     "/state/feat/run-001/review/iteration-01/review-feedback.md",
				"verification_report": "/state/feat/run-001/review/iteration-01/verification-report.yaml",
			},
		},
		{
			name:       "final review fixer",
			phase:      feature.PhaseReview,
			role:       RoleFinalReviewFixer,
			iterDir:    "/state/feat/run-001/review/iteration-02",
			wantSkill:  "final-fix",
			wantRoots:  []string{"iteration_dir"},
			wantMarker: "/state/feat/run-001/review/iteration-02/phase_complete",
			wantPaths: map[string]string{
				"verification_report": "/state/feat/run-001/review/iteration-02/verification-report.yaml",
			},
		},
		{
			name:     "interactive pty",
			phase:    feature.PhaseImplement,
			role:     RoleInteractivePTY,
			iterDir:  "/state/feat/run-001/tweak-1",
			wantNoOp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := lookupRoleSpec(tt.phase, tt.role)
			if !ok {
				t.Fatalf("lookupRoleSpec(%v, %q) ok = false, want true", tt.phase, tt.role)
			}
			contract := spec.Contract()
			if contract.Role != tt.role {
				t.Fatalf("Contract().Role = %q, want %q", contract.Role, tt.role)
			}
			if contract.NoOp != tt.wantNoOp {
				t.Fatalf("Contract().NoOp = %v, want %v", contract.NoOp, tt.wantNoOp)
			}
			if tt.wantNoOp {
				if len(contract.Required) != 0 || len(contract.Optional) != 0 || len(contract.Conditional) != 0 {
					t.Fatalf("NoOp contract has artifacts: %+v", contract)
				}
				return
			}
			if spec.SkillName != tt.wantSkill {
				t.Fatalf("SkillName = %q, want %q", spec.SkillName, tt.wantSkill)
			}
			if got := rootNames(spec); !slices.Equal(got, tt.wantRoots) {
				t.Fatalf("output roots = %v, want %v", got, tt.wantRoots)
			}
			if got := spec.MarkerPath(RoleRuntime{IterationDir: tt.iterDir}); got != tt.wantMarker {
				t.Fatalf("MarkerPath() = %q, want %q", got, tt.wantMarker)
			}
			if len(contract.Required) != len(tt.wantPaths) {
				t.Fatalf("required artifacts = %d, want %d", len(contract.Required), len(tt.wantPaths))
			}
			for _, artifact := range contract.Required {
				want, ok := tt.wantPaths[artifact.Name]
				if !ok {
					t.Fatalf("unexpected artifact %q", artifact.Name)
				}
				if got := artifact.ResolvePath(tt.iterDir); got != want {
					t.Fatalf("artifact %q path = %q, want %q", artifact.Name, got, want)
				}
			}
		})
	}
}

func TestBuildReviewFamilySystemPromptsFromRoleSpec(t *testing.T) {
	tests := []struct {
		name      string
		spec      RoleSpec
		iterDir   string
		wantRoots []string
		wantSkill string
	}{
		{
			name:      "iteration reviewer",
			spec:      mustLookupRoleSpecForTest(t, feature.PhaseReview, RoleIterationReviewer),
			iterDir:   "/state/feat/run-001/phase-04/implement/iteration-01/review",
			wantRoots: []string{"`helper_dir`: /state/feat/run-001/phase-04/implement/iteration-01/review"},
			wantSkill: "/skills/review-implementation/SKILL.md",
		},
		{
			name:      "final reviewer",
			spec:      mustLookupRoleSpecForTest(t, feature.PhaseReview, RoleFinalReviewer),
			iterDir:   "/state/feat/run-001/review/iteration-01",
			wantRoots: []string{"`iteration_dir`: /state/feat/run-001/review/iteration-01"},
			wantSkill: "/skills/final-review/SKILL.md",
		},
		{
			name:      "final review fixer",
			spec:      mustLookupRoleSpecForTest(t, feature.PhaseReview, RoleFinalReviewFixer),
			iterDir:   "/state/feat/run-001/review/iteration-01",
			wantRoots: []string{"`iteration_dir`: /state/feat/run-001/review/iteration-01"},
			wantSkill: "/skills/final-fix/SKILL.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
				Spec:          tt.spec,
				IterationDir:  tt.iterDir,
				SkillsDir:     "/skills",
				GuidelinesDir: "/guidelines",
				AskingClause:  "## Asking Questions\n\nUse numbered alternatives.",
			})
			for _, want := range append(tt.wantRoots, tt.wantSkill, filepath.Join(tt.iterDir, "phase_complete"), "# Useful Resources", "Use numbered alternatives.") {
				if !strings.Contains(got, want) {
					t.Fatalf("BuildRoleSystemPrompt() missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestBuildPlanningSystemPromptFromRoleSpec(t *testing.T) {
	got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          PhasePlanCreatorRoleSpec(),
		IterationDir:  "/state/feat/run-001/phase-02/plan/attempt-03",
		SkillsDir:     "/skills",
		GuidelinesDir: "/guidelines",
		KBInfos: []KBInfo{
			{Name: "agentic", IndexPath: "/kb/agentic/index.md", RootDir: "/kb/agentic"},
		},
		AskingClause: "## Asking Questions\n\nAsk one question at a time.",
	})

	for _, want := range []string{
		"`artifact_dir`: /state/feat/run-001/phase-02/plan",
		"`attempt_dir`: /state/feat/run-001/phase-02/plan/attempt-03",
		"/state/feat/run-001/phase-02/plan/attempt-03/phase_complete",
		"/skills/plan-phase/SKILL.md",
		"# Useful Resources",
		"/guidelines/go/index.md",
		"Ask one question at a time.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildRoleSystemPrompt() missing %q in:\n%s", want, got)
		}
	}
}

func TestReadOnlyOutsideRootsRoleSpecs(t *testing.T) {
	// Only the four planning/research/design role families are document-only
	// agents that must refuse to touch source code. Implementer, validators,
	// reviewers, refactor-plan, KB-builder, and InteractivePTY all have
	// legitimate non-document writes (code, working-tree artifacts, etc.).
	readOnly := []struct {
		name string
		spec RoleSpec
	}{
		{"inquirer", InquirerRoleSpec()},
		{"designer", DesignerRoleSpec()},
		{"roadmap creator", RoadmapCreatorRoleSpec()},
		{"roadmap reviser", RoadmapReviserRoleSpec()},
		{"phase plan creator", PhasePlanCreatorRoleSpec()},
		{"phase plan reviser", PhasePlanReviserRoleSpec()},
	}
	for _, tt := range readOnly {
		t.Run(tt.name+"_flag_set", func(t *testing.T) {
			if !tt.spec.ReadOnlyOutsideRoots {
				t.Fatalf("%s ReadOnlyOutsideRoots = false, want true", tt.name)
			}
			got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
				Spec:         tt.spec,
				IterationDir: "/state/feat/run-001/" + tt.name,
				SkillsDir:    "/skills",
			})
			for _, want := range []string{
				"ABSOLUTE: write only inside the output roots above.",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("%s system prompt missing %q in:\n%s", tt.name, want, got)
				}
			}
		})
	}

	writeAllowed := []struct {
		name string
		spec RoleSpec
	}{
		{"implementer", ImplementRoleSpec()},
		{"final reviewer", FinalReviewerRoleSpec()},
		{"final review fixer", FinalReviewFixerRoleSpec()},
		{"iteration reviewer", IterationReviewerRoleSpec()},
		{"researcher", ResearcherRoleSpec()},
		{"knowledge base builder", KnowledgeBaseBuilderRoleSpec()},
		{"refactor plan", RefactorPlanRoleSpec()},
	}
	for _, tt := range writeAllowed {
		t.Run(tt.name+"_flag_unset", func(t *testing.T) {
			if tt.spec.ReadOnlyOutsideRoots {
				t.Fatalf("%s ReadOnlyOutsideRoots = true, want false", tt.name)
			}
			got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
				Spec:         tt.spec,
				IterationDir: "/state/feat/run-001/" + tt.name,
				SkillsDir:    "/skills",
			})
			if strings.Contains(got, "ABSOLUTE: write only inside the output roots above.") {
				t.Fatalf("%s system prompt contains read-only-outside-roots clause but role legitimately writes outside its output roots:\n%s", tt.name, got)
			}
		})
	}
}

func TestBuildValidatorSystemPromptFromRoleSpec(t *testing.T) {
	role, ok := PlanValidatorRoleForSkill("validate-roadmap-architecture")
	if !ok {
		t.Fatal("PlanValidatorRoleForSkill(validate-roadmap-architecture) ok = false, want true")
	}
	got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:         role,
		IterationDir: "/state/feat/run-001/roadmap/attempt-02/validate-architecture",
		SkillsDir:    "/skills",
		AskingClause: "## Asking Questions\n\nUse numbered alternatives.",
	})
	for _, want := range []string{
		"`attempt_dir`: /state/feat/run-001/roadmap/attempt-02",
		"`helper_dir`: /state/feat/run-001/roadmap/attempt-02/validate-architecture",
		"/state/feat/run-001/roadmap/attempt-02/validate-architecture/phase_complete",
		"/skills/validate-roadmap-architecture/SKILL.md",
		"Use numbered alternatives.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("validator system prompt missing %q in:\n%s", want, got)
		}
	}
}

func rootNames(spec RoleSpec) []string {
	names := make([]string, 0, len(spec.OutputRoots))
	for _, root := range spec.OutputRoots {
		names = append(names, root.Name)
	}
	return names
}

func mustLookupRoleSpecForTest(t testing.TB, phase feature.Phase, role Role) RoleSpec {
	t.Helper()
	spec, ok := lookupRoleSpec(phase, role)
	if !ok {
		t.Fatalf("lookupRoleSpec(%v, %q) ok = false, want true", phase, role)
	}
	return spec
}

func TestSkillOutputFilesMatchRoleSpec(t *testing.T) {
	for _, spec := range SkillOutputRoleSpecs() {
		t.Run(spec.SkillName, func(t *testing.T) {
			path := repoRootPath(t, "skills", spec.SkillName, "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			want := RenderRoleSpecOutputFilesSection(spec)
			got, ok := extractOutputFilesSection(string(data))
			if *updateRoleSpecGenerated {
				updated, err := replaceOutputFilesSection(string(data), want)
				if err != nil {
					t.Fatalf("updating Output Files section: %v", err)
				}
				if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
					t.Fatalf("writing %s: %v", path, err)
				}
				return
			}
			if !ok || got != want {
				t.Fatalf("the '## Output Files' section in skills/%s/SKILL.md does not match RoleSpec.Artifacts. Run 'go test ./internal/agent/... -update' to refresh it.\n--- WANT ---\n%s\n--- GOT ---\n%s", spec.SkillName, want, got)
			}
		})
	}
}

func TestImplementSkillDiscoversTestingContractFromVerificationReport(t *testing.T) {
	path := repoRootPath(t, "skills", "implement", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(data)
	for _, want := range []string{
		"{iteration_dir}/verification-report.yaml",
		"`contract_path`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/implement/SKILL.md missing %q", want)
		}
	}
	if strings.Contains(content, "{phase_dir}/testing-contract.yaml") {
		t.Fatalf("skills/implement/SKILL.md still points testing-contract discovery at phase_dir")
	}
}

func TestResearchSkillDocumentsProgressHandoff(t *testing.T) {
	path := repoRootPath(t, "skills", "research-codebase", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(data)
	for _, want := range []string{
		"research-progress.md",
		"## Handoff State",
		"COMPLETE",
		"HANDOFF.md",
		"phase_complete",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/research-codebase/SKILL.md missing %q", want)
		}
	}
}

func TestImplementSkillDocumentsEvidenceFiles(t *testing.T) {
	path := repoRootPath(t, "skills", "implement", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(data)
	for _, want := range []string{
		"`screenshots/`",
		"`behaviors/`",
		"`evidence.primary`",
		"`evidence.attachments[]`",
		"`passed`, `failed`, and `pending_human`",
		"`blocked`, `not_run`, and `waived`",
		"`blocked_reason`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/implement/SKILL.md missing evidence-file guidance %q", want)
		}
	}
}

func TestImplementSkillReadsPhaseProgressOnResume(t *testing.T) {
	path := repoRootPath(t, "skills", "implement", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(data)
	for _, want := range []string{
		"{phase_dir}/progress.md",
		"### Where I stopped",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/implement/SKILL.md missing resume instruction %q", want)
		}
	}
	if strings.Contains(content, "Read any inlined prior `progress.md` first") {
		t.Fatalf("skills/implement/SKILL.md still depends on inlined progress.md for resume state")
	}
}

func repoRootPath(t testing.TB, elems ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	parts := append([]string{filepath.Dir(filepath.Dir(filepath.Dir(file)))}, elems...)
	return filepath.Join(parts...)
}
