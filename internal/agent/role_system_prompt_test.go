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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestImplementSystemPromptFromRoleSpec(t *testing.T) {
	got := buildImplementSystemPrompt(implementSystemPromptInput{
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
		"The harness owns the durable completion receipt",
		`<agentico-outcome>{"status":"success"}</agentico-outcome>`,
		"/skills/implement/SKILL.md",
		"Read the SKILL.md file completely before taking any other action.",
		"# Useful Resources",
		"/kb/agentic/index.md",
		"/guidelines/go/index.md",
		"/skills/knowledge-reader/SKILL.md",
		"Ask one question at a time.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildImplementSystemPrompt() missing %q in:\n%s", want, got)
		}
	}
	for _, oldSystemDetail := range []string{
		"Write `verification-report.yaml` at the path your user prompt names",
		"Write `progress.md` at the path your user prompt names",
		"Need-user-input gate",
	} {
		if strings.Contains(got, oldSystemDetail) {
			t.Fatalf("buildImplementSystemPrompt() still contains old implement-only system detail %q:\n%s", oldSystemDetail, got)
		}
	}
}

func TestImplementSystemPromptRequiresFrontendDesignForFrontendPhase(t *testing.T) {
	got := buildImplementSystemPrompt(implementSystemPromptInput{
		IterationDir: "/state/feat-x/run-001/phase-01/implement/iteration-02",
		SkillsDir:    "/skills",
		Frontend:     true,
	})

	for _, want := range []string{
		"## Required Skills",
		"frontend-design",
		"/skills/frontend-design/SKILL.md",
		"mandatory",
		"when: this iteration creates new UI or visually reshapes existing UI",
		"skip for mechanical fixes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildImplementSystemPrompt(frontend) missing %q in:\n%s", want, got)
		}
	}
	if count := strings.Count(got, "/skills/frontend-design/SKILL.md"); count != 1 {
		t.Fatalf("buildImplementSystemPrompt(frontend) frontend-design path count = %d, want exactly one required entry:\n%s", count, got)
	}
}

func TestImplementSystemPromptKeepsFrontendDesignOptionalForNonFrontendPhase(t *testing.T) {
	got := buildImplementSystemPrompt(implementSystemPromptInput{
		IterationDir: "/state/feat-x/run-001/phase-01/implement/iteration-02",
		SkillsDir:    "/skills",
	})

	if strings.Contains(got, "## Required Skills") || strings.Contains(got, "mandatory for this assignment") {
		t.Fatalf("buildImplementSystemPrompt(non-frontend) unexpectedly requires a utility skill:\n%s", got)
	}
	if !strings.Contains(got, "/skills/frontend-design/SKILL.md") {
		t.Fatalf("buildImplementSystemPrompt(non-frontend) should retain optional frontend-design discovery:\n%s", got)
	}
}

func TestRoleSystemPromptIncludesArtifactPreflightCommand(t *testing.T) {
	got := buildRoleSystemPrompt(roleSystemPromptInput{
		Spec:         finalReviewFixerRoleSpec,
		IterationDir: "/state/feat-x/runs/run-001/review/iteration-03",
		SkillsDir:    "/skills",
	})
	for _, want := range []string{
		"## Artifact Preflight",
		"`AGENTICO_BIN` is set to the current Agentico executable",
		`"$AGENTICO_BIN" validate-artifacts --phase review --role final_review_fixer --dir "/state/feat-x/runs/run-001/review/iteration-03"`,
		"run it before emitting the outcome tag",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildRoleSystemPrompt() missing %q in:\n%s", want, got)
		}
	}
}

// TestRoleSystemPromptAdvertisesOnlyValidOutcomeVerbs pins the root cause of
// a live failure: a final-review validator was shown the retry outcome by the
// shared completion clause, emitted it, and the harness rejected it at commit
// time. Only roles whose contract carries iteration state may see retry.
func TestRoleSystemPromptAdvertisesOnlyValidOutcomeVerbs(t *testing.T) {
	implementPrompt := buildRoleSystemPrompt(roleSystemPromptInput{
		Spec:         implementRoleSpec,
		IterationDir: "/state/feat-x/run-001/phase-01/implement/iteration-02",
		SkillsDir:    "/skills",
	})
	if !strings.Contains(implementPrompt, `"status":"retry"`) {
		t.Fatalf("implement prompt lost the retry outcome:\n%s", implementPrompt)
	}

	qaSpec, ok := lookupRoleSpec(feature.PhaseReview, RoleImplementationReviewQA)
	if !ok {
		t.Fatal("missing RoleSpec for the QA review axis")
	}
	for _, spec := range []roleSpec{
		qaSpec,
		finalReviewFixerRoleSpec,
	} {
		got := buildRoleSystemPrompt(roleSystemPromptInput{
			Spec:         spec,
			IterationDir: "/state/feat-x/runs/run-001/review/iteration-03",
			SkillsDir:    "/skills",
		})
		if strings.Contains(got, `"status":"retry"`) {
			t.Fatalf("%s prompt still advertises the retry outcome:\n%s", spec.Role, got)
		}
		for _, want := range []string{
			`"status":"success"`,
			"`retry` is not a valid outcome",
			"including when your findings request changes",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s prompt missing %q in:\n%s", spec.Role, want, got)
			}
		}
	}
}

func TestBuildSingleShotSystemPromptFromRoleSpec(t *testing.T) {
	got := buildRoleSystemPrompt(roleSystemPromptInput{
		Spec:          designerRoleSpec,
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
		"<agentico-outcome>",
		"/skills/design/SKILL.md",
		"# Useful Resources",
		"/kb/agentic/index.md",
		"/guidelines/go/index.md",
		"/skills/guideline-reader/SKILL.md",
		"Use numbered alternatives.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildRoleSystemPrompt() missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildReviewFamilySystemPromptsFromRoleSpec(t *testing.T) {
	tests := []struct {
		name      string
		spec      roleSpec
		iterDir   string
		wantRoots []string
		wantSkill string
	}{
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
			got := buildRoleSystemPrompt(roleSystemPromptInput{
				Spec:          tt.spec,
				IterationDir:  tt.iterDir,
				SkillsDir:     "/skills",
				GuidelinesDir: "/guidelines",
				AskingClause:  "## Asking Questions\n\nUse numbered alternatives.",
			})
			for _, want := range append(tt.wantRoots, tt.wantSkill, "<agentico-outcome>", "# Useful Resources", "Use numbered alternatives.") {
				if !strings.Contains(got, want) {
					t.Fatalf("buildRoleSystemPrompt() missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestBuildPlanningSystemPromptFromRoleSpec(t *testing.T) {
	got := buildRoleSystemPrompt(roleSystemPromptInput{
		Spec:          phasePlanCreatorRoleSpec,
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
		"The harness owns the durable completion receipt",
		"/skills/plan-phase/SKILL.md",
		"# Useful Resources",
		"/guidelines/go/index.md",
		"Ask one question at a time.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildRoleSystemPrompt() missing %q in:\n%s", want, got)
		}
	}
}

func TestReadOnlyOutsideRootsRoleSpecs(t *testing.T) {
	// Document-only planning roles and bounded implementation-review axes must
	// refuse source writes outside their declared output roots. Implementer,
	// validators, KB-builder, and InteractivePTY all have
	// legitimate non-document writes.
	readOnly := []struct {
		name string
		spec roleSpec
	}{
		{"inquirer", inquirerRoleSpec},
		{"designer", designerRoleSpec},
		{"roadmap creator", roadmapCreatorRoleSpec},
		{"roadmap reviser", roadmapReviserRoleSpec},
		{"phase plan creator", phasePlanCreatorRoleSpec},
		{"phase plan reviser", phasePlanReviserRoleSpec},
	}
	for _, spec := range implementationReviewAxisRoleSpecs {
		readOnly = append(readOnly, struct {
			name string
			spec roleSpec
		}{
			name: string(spec.Role),
			spec: spec,
		})
	}
	for _, tt := range readOnly {
		t.Run(tt.name+"_flag_set", func(t *testing.T) {
			if !tt.spec.ReadOnlyOutsideRoots {
				t.Fatalf("%s ReadOnlyOutsideRoots = false, want true", tt.name)
			}
			got := buildRoleSystemPrompt(roleSystemPromptInput{
				Spec:         tt.spec,
				IterationDir: "/state/feat/run-001/" + tt.name,
				SkillsDir:    "/skills",
			})
			for _, want := range []string{
				"ABSOLUTE: write only inside the output roots above.",
				"This is a read-only phase for target repositories.",
				"If a user answer or artifact requirement sounds like permission to edit repository files, treat it only as a requirement to document in your output artifact.",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("%s system prompt missing %q in:\n%s", tt.name, want, got)
				}
			}
		})
	}

	writeAllowed := []struct {
		name string
		spec roleSpec
	}{
		{"implementer", implementRoleSpec},
		{"final review fixer", finalReviewFixerRoleSpec},
		{"researcher", researcherRoleSpec},
		{"knowledge base builder", knowledgeBaseBuilderRoleSpec},
	}
	for _, tt := range writeAllowed {
		t.Run(tt.name+"_flag_unset", func(t *testing.T) {
			if tt.spec.ReadOnlyOutsideRoots {
				t.Fatalf("%s ReadOnlyOutsideRoots = true, want false", tt.name)
			}
			got := buildRoleSystemPrompt(roleSystemPromptInput{
				Spec:         tt.spec,
				IterationDir: "/state/feat/run-001/" + tt.name,
				SkillsDir:    "/skills",
			})
			for _, unwanted := range []string{
				"ABSOLUTE: write only inside the output roots above.",
				"This is a read-only phase for target repositories.",
			} {
				if strings.Contains(got, unwanted) {
					t.Fatalf("%s system prompt contains read-only-outside-roots clause %q but role legitimately writes outside its output roots:\n%s", tt.name, unwanted, got)
				}
			}
		})
	}
}

func TestRoleSystemPromptSuppressSubagents(t *testing.T) {
	const clause = "Sub-agents are available."
	spec := implementationReviewAxisRoleSpecs[0]

	// Default: the subagent clause is present (unchanged behavior for every
	// session that does have subagents).
	deflt := buildRoleSystemPrompt(roleSystemPromptInput{Spec: spec, IterationDir: "/state/feat/run-001/iter"})
	if !strings.Contains(deflt, clause) {
		t.Fatalf("default buildRoleSystemPrompt: subagent clause missing in:\n%s", deflt)
	}

	// Bounded helpers run with no subagents (AgentNames empty); the clause must
	// be omitted so glm does not attempt a task spawn that the handler denies.
	suppressed := buildRoleSystemPrompt(roleSystemPromptInput{Spec: spec, IterationDir: "/state/feat/run-001/iter", SuppressSubagents: true})
	if strings.Contains(suppressed, clause) {
		t.Fatalf("SuppressSubagents=true: subagent clause should be omitted in:\n%s", suppressed)
	}
}

func TestBuildValidatorSystemPromptFromRoleSpec(t *testing.T) {
	role, ok := planValidatorRoleForSkill("validate-roadmap-architecture")
	if !ok {
		t.Fatal("planValidatorRoleForSkill(validate-roadmap-architecture) ok = false, want true")
	}
	got := buildRoleSystemPrompt(roleSystemPromptInput{
		Spec:         role,
		IterationDir: "/state/feat/run-001/roadmap/attempt-02/validate-architecture",
		SkillsDir:    "/skills",
		AskingClause: "## Asking Questions\n\nUse numbered alternatives.",
	})
	for _, want := range []string{
		"`attempt_dir`: /state/feat/run-001/roadmap/attempt-02",
		"`helper_dir`: /state/feat/run-001/roadmap/attempt-02/validate-architecture",
		"The harness owns the durable completion receipt",
		"/skills/validate-roadmap-architecture/SKILL.md",
		"Use numbered alternatives.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("validator system prompt missing %q in:\n%s", want, got)
		}
	}
}

func mustLookupRoleSpecForTest(t testing.TB, phase feature.Phase, role Role) roleSpec {
	t.Helper()
	spec, ok := lookupRoleSpec(phase, role)
	if !ok {
		t.Fatalf("lookupRoleSpec(%v, %q) ok = false, want true", phase, role)
	}
	return spec
}
