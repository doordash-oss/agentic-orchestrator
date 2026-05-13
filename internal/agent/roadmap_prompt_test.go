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

func TestBuildRoadmapPrompt(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test feature",
		Inquireness: feature.InquirenessMedium,
	}

	prompt := BuildRoadmapPrompt(f, "", "", "/path/to/brainstorm.md", nil, KBInfo{
		IndexPath: "/kb/index.md",
		RootDir:   "/kb/",
	})

	if !strings.Contains(prompt, "Design Document: /path/to/brainstorm.md") {
		t.Error("prompt should contain Design Document line with brainstorm path")
	}
	// Feature name/description are sourced from the design doc when the
	// brainstorm artifact is present, so they MUST be omitted from the
	// roadmap prompt — keeping them creates two competing sources of truth.
	if strings.Contains(prompt, "Test Feature") {
		t.Error("prompt should not contain feature name when design doc is present")
	}
	if strings.Contains(prompt, "A test feature") {
		t.Error("prompt should not contain feature description when design doc is present")
	}
	if !strings.Contains(prompt, "Ambiguity Resolution") {
		t.Error("prompt should contain inquireness directive")
	}
	if strings.Contains(prompt, "/kb/") {
		t.Error("prompt should not contain KB directory path; RoleSpec system prompt owns useful resources")
	}
	// Roadmap decomposition methodology is in SKILL.md, not in the prompt.
}

func TestBuildRoadmapPromptLeavesRoleSpecOwnedContentToSystemPrompt(t *testing.T) {
	f := &feature.Feature{
		Name:        "Dark Mode",
		Description: "Add dark theme support",
	}
	prompt := BuildRoadmapPrompt(f, "/skills", "/guidelines", "/path/to/brainstorm.md", nil, KBInfo{
		Name:      "agentic",
		IndexPath: "/kb/agentic/index.md",
		RootDir:   "/kb/agentic",
	})

	for _, forbidden := range []string{
		"Before starting your task, read the methodology instructions",
		"# Useful Resources",
		"/skills/create-roadmap/SKILL.md",
		"/guidelines/go/index.md",
		"/kb/agentic/index.md",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("BuildRoadmapPrompt() contains RoleSpec-owned content %q:\n%s", forbidden, prompt)
		}
	}
	if !strings.Contains(prompt, "Design Document: /path/to/brainstorm.md") {
		t.Fatalf("BuildRoadmapPrompt() missing per-call design path:\n%s", prompt)
	}
}

func TestBuildPhasePlanPromptLeavesRoleSpecOwnedContentToSystemPrompt(t *testing.T) {
	f := &feature.Feature{Name: "Dark Mode", Description: "Add dark theme support"}
	phase := RoadmapPhase{Number: 2, Name: "Preference persistence", Goal: "Persist selected theme"}
	prompt := BuildPhasePlanPrompt(f, "/skills", "/guidelines", "/roadmap.md", phase, []string{"/answers.md"}, KBInfo{
		Name:      "agentic",
		IndexPath: "/kb/agentic/index.md",
		RootDir:   "/kb/agentic",
	})

	for _, forbidden := range []string{
		"Before starting your task, read the methodology instructions",
		"# Useful Resources",
		"/skills/plan-phase/SKILL.md",
		"/guidelines/go/index.md",
		"/kb/agentic/index.md",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("BuildPhasePlanPrompt() contains RoleSpec-owned content %q:\n%s", forbidden, prompt)
		}
	}
	for _, want := range []string{"/roadmap.md", "/answers.md", "Persist selected theme"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("BuildPhasePlanPrompt() missing per-call content %q:\n%s", want, prompt)
		}
	}
}

func TestPlanningRevisionPromptsLeaveRoleSpecOwnedContentToSystemPrompt(t *testing.T) {
	f := &feature.Feature{Name: "Dark Mode", Description: "Add dark theme support"}
	phase := RoadmapPhase{Number: 2, Name: "Preference persistence"}

	tests := []struct {
		name      string
		prompt    string
		skillPath string
	}{
		{
			name:      "roadmap revision",
			prompt:    BuildRoadmapRevisionPrompt(f, "/skills", "/roadmap.md", "/prev.md", "Fix scope", "/brainstorm.md", 2, nil),
			skillPath: "/skills/revise-roadmap/SKILL.md",
		},
		{
			name:      "phase plan revision",
			prompt:    BuildPhasePlanRevisionPrompt(f, "/skills", "/phase-plan.md", "Fix scope", "/brainstorm.md", phase, 2, nil),
			skillPath: "/skills/revise-phase-plan/SKILL.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, forbidden := range []string{
				"Before starting your task, read the methodology instructions",
				tt.skillPath,
			} {
				if strings.Contains(tt.prompt, forbidden) {
					t.Fatalf("%s prompt contains RoleSpec-owned content %q:\n%s", tt.name, forbidden, tt.prompt)
				}
			}
			for _, want := range []string{"/brainstorm.md", "Fix scope"} {
				if want == "/brainstorm.md" {
					if strings.Contains(tt.prompt, want) {
						t.Fatalf("%s prompt unexpectedly includes brainstorm reference %q:\n%s", tt.name, want, tt.prompt)
					}
					continue
				}
				if !strings.Contains(tt.prompt, want) {
					t.Fatalf("%s prompt missing per-call content %q:\n%s", tt.name, want, tt.prompt)
				}
			}
		})
	}
}

func TestBuildRoadmapPrompt_Medium(t *testing.T) {
	f := &feature.Feature{
		Name:        "Medium Feature",
		Description: "Build a new widget",
		Inquireness: feature.InquirenessHigh,
	}

	// Medium: no brainstorm artifact — feature description must be in prompt
	prompt := BuildRoadmapPrompt(f, "", "", "", nil)

	if !strings.Contains(prompt, "Medium Feature") {
		t.Error("prompt should contain feature name")
	}
	if !strings.Contains(prompt, "Build a new widget") {
		t.Error("prompt should contain feature description when no brainstorm artifact")
	}
	if strings.Contains(prompt, "Design Document") {
		t.Error("prompt should not contain Design Document section without artifact")
	}
}

func TestBuildRoadmapPromptNoKB(t *testing.T) {
	f := &feature.Feature{
		Name:        "Simple Feature",
		Description: "Simple",
		Inquireness: feature.InquirenessNone,
	}

	prompt := BuildRoadmapPrompt(f, "", "", "/path/to/design.md", nil)

	if !strings.Contains(prompt, "/path/to/design.md") {
		t.Error("prompt should contain design path")
	}
	if strings.Contains(prompt, "Knowledge Base") {
		t.Error("prompt should not contain KB section when no KBInfos")
	}
	if !strings.Contains(prompt, "## Ambiguity Resolution [grill-me]") {
		t.Error("prompt should contain [grill-me] header for none inquireness")
	}
	if strings.Contains(prompt, "strictly greater than") || strings.Contains(prompt, "auto-pick") {
		t.Errorf("prompt should not expose harness auto-pick policy:\n%s", prompt)
	}
}

func TestBuildRoadmapPrompt_MultiRepo(t *testing.T) {
	t.Run("multi_repo_includes_target_repos", func(t *testing.T) {
		f := &feature.Feature{
			Name: "test-feature",
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/path/a"},
				{Name: "repo-b", Path: "/path/b"},
			},
		}
		prompt := BuildRoadmapPrompt(f, "", "", "/tmp/brainstorm.md", nil)
		if !strings.Contains(prompt, "Target Repositories") {
			t.Error("expected 'Target Repositories' section")
		}
		if !strings.Contains(prompt, "repo-a") || !strings.Contains(prompt, "repo-b") {
			t.Error("expected both repo names")
		}
	})
	t.Run("execution_order_not_part_of_roadmap_prompt", func(t *testing.T) {
		// Multi-repo task routing now lives in phase-plan `**Repo:** tags.
		// The roadmap prompt only signals "this is multi-repo" via Target
		// Repositories; it must not resurrect the old execution-order file.
		f := &feature.Feature{
			Name: "test-feature",
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/path/a"},
				{Name: "repo-b", Path: "/path/b"},
			},
		}
		prompt := BuildRoadmapPrompt(f, "", "", "/tmp/brainstorm.md", nil)
		if strings.Contains(prompt, "execution-order.yaml") {
			t.Error("roadmap prompt should not mention execution-order.yaml")
		}
		if strings.Contains(prompt, "## Execution Order") {
			t.Error("roadmap prompt should not include an Execution Order section")
		}
	})
	t.Run("single_repo_omits_target_repositories", func(t *testing.T) {
		f := &feature.Feature{
			Name:  "test-feature",
			Repos: []feature.FeatureRepo{{Name: "repo-a", Path: "/path/a"}},
		}
		prompt := BuildRoadmapPrompt(f, "", "", "/tmp/brainstorm.md", nil)
		if strings.Contains(prompt, "Target Repositories") {
			t.Error("single-repo should not have Target Repositories section")
		}
	})
	t.Run("multi_repo_uses_worktree_path_when_available", func(t *testing.T) {
		f := &feature.Feature{
			Name: "test-feature",
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/path/a", WorktreePath: "/worktree/a"},
				{Name: "repo-b", Path: "/path/b"},
			},
		}
		prompt := BuildRoadmapPrompt(f, "", "", "/tmp/brainstorm.md", nil)
		if !strings.Contains(prompt, "/worktree/a") {
			t.Error("expected worktree path for repo-a")
		}
		if strings.Contains(prompt, "/path/a") {
			t.Error("should use worktree path instead of original path for repo-a")
		}
		if !strings.Contains(prompt, "/path/b") {
			t.Error("expected original path for repo-b (no worktree)")
		}
	})
}

func TestBuildPhasePlanPromptOmitsPhaseType(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test",
		Inquireness: feature.InquirenessMedium,
	}

	phase := RoadmapPhase{
		Number: 1,
		Name:   "First Vertical Slice",
		Type:   "tracer-bullet",
		Goal:   "Build the E2E skeleton",
	}

	prompt := BuildPhasePlanPrompt(f, "", "", "/path/to/roadmap.md", phase, nil, KBInfo{
		IndexPath: "/kb/index.md",
		RootDir:   "/kb/",
	})

	if !strings.Contains(prompt, "Phase 1") {
		t.Error("prompt should contain phase number")
	}
	if strings.Contains(prompt, "tracer-bullet") || strings.Contains(prompt, "Phase Type") {
		t.Error("prompt should not expose phase type")
	}
	if !strings.Contains(prompt, "/path/to/roadmap.md") {
		t.Error("prompt should contain roadmap path")
	}
	if !strings.Contains(prompt, "Build the E2E skeleton") {
		t.Error("prompt should contain phase goal")
	}
	// First-slice implementation guidance is in SKILL.md, not in the prompt.
}

func TestBuildPhasePlanPromptLaterPhaseWithStubRetirements(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test",
		Inquireness: feature.InquirenessNone,
	}

	phase := RoadmapPhase{
		Number:        2,
		Name:          "Fill in Parser",
		Type:          "tdd-fill-in",
		Goal:          "Replace parser stub",
		StubsToRetire: []string{"Parser stub", "Validator stub"},
	}

	prompt := BuildPhasePlanPrompt(f, "", "", "/roadmap.md", phase, nil)

	if !strings.Contains(prompt, "Phase 2") {
		t.Error("prompt should contain phase number")
	}
	if strings.Contains(prompt, "tdd-fill-in") || strings.Contains(prompt, "Phase Type") {
		t.Error("prompt should not expose phase type")
	}
	if !strings.Contains(prompt, "Parser stub") {
		t.Error("prompt should contain stubs to retire")
	}
	// Later-phase implementation guidance is in SKILL.md, not in the prompt.
}

func TestBuildRoadmapPrompt_WithQAFiles(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test feature",
		Inquireness: feature.InquirenessMedium,
	}

	qaFiles := []string{"/state/feat/inquire/qa-answers.md", "/state/feat/research/qa-answers.md"}
	prompt := BuildRoadmapPrompt(f, "", "", "/path/to/brainstorm.md", qaFiles)

	if !strings.Contains(prompt, "User Decisions") {
		t.Error("prompt should contain User Decisions section when QA files present")
	}
	if !strings.Contains(prompt, "/state/feat/inquire/qa-answers.md") {
		t.Error("prompt should contain inquire QA file path")
	}
	if !strings.Contains(prompt, "/state/feat/research/qa-answers.md") {
		t.Error("prompt should contain research QA file path")
	}
	if !strings.Contains(prompt, "do not re-ask") {
		t.Error("prompt should instruct not to re-ask answered questions")
	}
}

func TestBuildRoadmapPrompt_NoQAFiles(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test feature",
	}

	prompt := BuildRoadmapPrompt(f, "", "", "/path/to/brainstorm.md", nil)

	if strings.Contains(prompt, "User Decisions") {
		t.Error("prompt should not contain User Decisions section when no QA files")
	}
}

// TestBuildPhasePlanPrompt_WithQAFiles pins the grill-me Phase-Plan contract:
// upstream Q&A files are re-injected so the planner can respect prior
// decisions and avoid re-asking clarified questions.
func TestBuildPhasePlanPrompt_WithQAFiles(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test",
		Inquireness: feature.InquirenessMedium,
	}

	phase := RoadmapPhase{
		Number: 1,
		Name:   "Phase One",
		Type:   "tracer-bullet",
	}

	qaFiles := []string{"/state/feat/brainstorm/qa-answers.md"}
	prompt := BuildPhasePlanPrompt(f, "", "", "/roadmap.md", phase, qaFiles)

	if !strings.Contains(prompt, "User Decisions") {
		t.Errorf("BuildPhasePlanPrompt(...) missing User Decisions section")
	}
	if !strings.Contains(prompt, "/state/feat/brainstorm/qa-answers.md") {
		t.Errorf("BuildPhasePlanPrompt(...) missing Q&A file path %q", qaFiles[0])
	}
	if !strings.Contains(prompt, "do not re-ask") {
		t.Errorf("BuildPhasePlanPrompt(...) missing do-not-re-ask guidance")
	}
}

func TestBuildPhasePlanPrompt_WithRoadmapAssignedCommitments(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test",
		Inquireness: feature.InquirenessMedium,
	}

	phase := RoadmapPhase{
		Number:        3,
		Name:          "Fill in Validator",
		Type:          "tdd-fill-in",
		StubsToRetire: []string{"Validator stub"},
	}

	prompt := BuildPhasePlanPrompt(f, "", "", "/roadmap.md", phase, nil)

	if strings.Contains(prompt, "Prior Phase Context") {
		t.Error("prompt should not contain Prior Phase Context")
	}
	if !strings.Contains(prompt, "Roadmap-Assigned Phase Commitments") {
		t.Error("prompt should contain roadmap-assigned commitments section")
	}
	if !strings.Contains(prompt, "Validator stub") {
		t.Error("prompt should contain roadmap-assigned commitment")
	}
}

func TestBuildPhasePlanPrompt_NoPriorPhases(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test",
	}

	phase := RoadmapPhase{
		Number: 1,
		Name:   "Tracer Bullet",
		Type:   "tracer-bullet",
	}

	prompt := BuildPhasePlanPrompt(f, "", "", "/roadmap.md", phase, nil)

	if strings.Contains(prompt, "Prior Phase Context") {
		t.Error("prompt should not contain Prior Phase Context for phase 1")
	}
}

func TestBuildRoadmapRevisionPrompt(t *testing.T) {
	f := &feature.Feature{
		Name:        "Test Feature",
		Description: "A test",
	}

	prompt := BuildRoadmapRevisionPrompt(f, "", "/roadmap.md", "/prev.md", "Fix the stub inventory", "", 2, nil)

	if !strings.Contains(prompt, "Revision") {
		t.Error("prompt should mention revision")
	}
	if !strings.Contains(prompt, "Fix the stub inventory") {
		t.Error("prompt should contain feedback")
	}
	if !strings.Contains(prompt, "attempt 2") {
		t.Error("prompt should mention attempt number")
	}
	if strings.Contains(prompt, "Prior Axis Approvals") {
		t.Error("prompt should not include approvals section when approvals are nil")
	}
}

func TestBuildRoadmapRevisionPromptWithApprovals(t *testing.T) {
	f := &feature.Feature{Name: "Test Feature", Description: "A test"}

	approvals := []AxisApproval{
		{Axis: "architecture", FrozenSections: []string{"Phase 3: Wire the hedging dispatcher", "Architecture Approach"}},
		{Axis: "scope", FrozenSections: []string{"Deferred Work"}},
	}

	prompt := BuildRoadmapRevisionPrompt(f, "", "/roadmap.md", "/prev.md", "Testing axis flagged a gap", "", 3, approvals)

	for _, want := range []string{
		"Prior Axis Approvals",
		"Sticky Approval Respect",
		"### architecture",
		"- Phase 3: Wire the hedging dispatcher",
		"- Architecture Approach",
		"### scope",
		"- Deferred Work",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n%s", want, prompt)
		}
	}

	// Axis approved with no frozen sections still appears but with the placeholder bullet.
	emptyApprovals := []AxisApproval{{Axis: "scope"}}
	emptyPrompt := BuildRoadmapRevisionPrompt(f, "", "/r.md", "/p.md", "feedback", "", 2, emptyApprovals)
	if !strings.Contains(emptyPrompt, "(no specific sections listed") {
		t.Errorf("expected placeholder for empty FrozenSections, got:\n%s", emptyPrompt)
	}
}

func TestBuildPhasePlanRevisionPrompt(t *testing.T) {
	f := &feature.Feature{
		Name: "Test Feature",
	}

	phase := RoadmapPhase{
		Number: 2,
		Name:   "Fill in Parser",
		Type:   "tdd-fill-in",
	}

	prompt := BuildPhasePlanRevisionPrompt(f, "", "/phase-plan.md", "Add missing tests", "", phase, 2, nil)

	if !strings.Contains(prompt, "Phase 2") {
		t.Error("prompt should contain phase number")
	}
	if !strings.Contains(prompt, "Add missing tests") {
		t.Error("prompt should contain feedback")
	}
	if strings.Contains(prompt, "tdd-fill-in") {
		t.Error("prompt should not expose phase type")
	}
	if strings.Contains(prompt, "Prior Axis Approvals") {
		t.Error("prompt should not include approvals section when approvals are nil")
	}
}

func TestBuildPhasePlanRevisionPromptWithApprovals(t *testing.T) {
	f := &feature.Feature{Name: "Test Feature"}
	phase := RoadmapPhase{Number: 1, Name: "Tracer", Type: "tracer-bullet"}

	approvals := []AxisApproval{
		{Axis: "structural", FrozenSections: []string{"Desired End State", "Changes Required"}},
		{Axis: "grounding", FrozenSections: []string{"## Grounding"}},
	}

	prompt := BuildPhasePlanRevisionPrompt(f, "", "/phase-plan.md", "Scope axis flagged drift", "", phase, 3, approvals)

	for _, want := range []string{
		"Prior Axis Approvals",
		"Sticky Approval Respect",
		"phase plan",
		"### structural",
		"- Desired End State",
		"- Changes Required",
		"### grounding",
		"- ## Grounding",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n%s", want, prompt)
		}
	}

	// Axis approved with no frozen sections still appears with the placeholder bullet.
	emptyApprovals := []AxisApproval{{Axis: "scope"}}
	emptyPrompt := BuildPhasePlanRevisionPrompt(f, "", "/p.md", "feedback", "", phase, 2, emptyApprovals)
	if !strings.Contains(emptyPrompt, "(no specific sections listed") {
		t.Errorf("expected placeholder for empty FrozenSections, got:\n%s", emptyPrompt)
	}
}

// TestBuildPhasePlanRevisionPrompt_OmitsPriorPhaseContext verifies phase-plan
// revision stays focused on the prior plan plus critic feedback, without
// injecting previous phase plans as broad context.
func TestBuildPhasePlanRevisionPrompt_OmitsPriorPhaseContext(t *testing.T) {
	f := &feature.Feature{Name: "Test Feature"}
	phase := RoadmapPhase{Number: 2, Name: "Fill-in", Type: "tdd-fill-in"}

	prompt := BuildPhasePlanRevisionPrompt(f, "", "/p.md", "grounding axis failed", "", phase, 2, nil)

	if strings.Contains(prompt, "## Prior Phase Context") {
		t.Errorf("revision prompt should omit Prior Phase Context:\n%s", prompt)
	}
}

func TestBuildPhasePlanRevisionPrompt_OmitsPriorPhaseContextForPhaseOne(t *testing.T) {
	f := &feature.Feature{Name: "Test Feature"}
	phase := RoadmapPhase{Number: 1, Name: "Tracer", Type: "tracer-bullet"}

	prompt := BuildPhasePlanRevisionPrompt(f, "", "/p.md", "some feedback", "", phase, 2, nil)

	if strings.Contains(prompt, "## Prior Phase Context") {
		t.Errorf("revision prompt should omit Prior Phase Context when no prior phases:\n%s", prompt)
	}
}
