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

package prompts

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/utilskill"
)

// updateGolden controls whether failing snapshot tests rewrite their
// .golden files. Run `go test ./internal/agent/prompts/... -update` to
// refresh expectations after a deliberate prose change.
var updateGolden = flag.Bool("update", false, "rewrite .golden files with current rendered output")

// testSkillFixtures maps each registered utility-skill name to the
// SkillView the goldens should render. Descriptions / topics / paths are
// pinned here (rather than read from skilldef.ParseEmbedded) so frontmatter
// edits to the shipped SKILL.md files don't thrash byte-exact goldens.
//
// Keep in sync with the names registered in internal/utilskill — every
// entry returned by utilskill.ForPhase MUST have a fixture row, otherwise
// testSkills will silently drop it and the goldens will under-represent
// reality.
var testSkillFixtures = map[string]SkillView{
	"frontend-design": {
		Name:        "frontend-design",
		Description: "Create distinctive, production-grade frontend interfaces with high design quality.",
		Topics:      "UI, UX, frontend, design, components",
		Path:        "/skills/frontend-design/SKILL.md",
	},
	"knowledge-reader": {
		Name:        "knowledge-reader",
		Description: "Skim a repo's knowledge-base index and read leaves on demand.",
		Topics:      "knowledge base, KB, architecture, conventions",
		Path:        "/skills/knowledge-reader/SKILL.md",
	},
	"guideline-reader": {
		Name:        "guideline-reader",
		Description: "Navigate the language-specific coding guidelines to follow best practices, idioms, and conventions when writing or reviewing code.",
		Topics:      "guidelines, coding standards, best practices, idioms, conventions, error handling, concurrency, naming, testing, style",
		Path:        "/skills/guideline-reader/SKILL.md",
	},
}

// testSkills returns the SkillView rows the "Additional Skills" system prompt
// table should render for the given phase. Membership tracks the live
// utilskill registry (so adding/removing a skill from a phase auto-shows
// up in the goldens), while the per-skill fields are pinned by
// testSkillFixtures so SKILL.md frontmatter tweaks don't churn snapshots.
func testSkills(phase feature.Phase) []SkillView {
	names := utilskill.ForPhase(phase)
	out := make([]SkillView, 0, len(names))
	for _, n := range names {
		if v, ok := testSkillFixtures[n]; ok {
			out = append(out, v)
		}
	}
	return out
}

// testGuidelines returns a stable list of GuidelineView rows the
// "Guidelines" system prompt subsection should render. The list is hand-pinned
// (rather than derived from guidelinedef.ParseEmbedded) so adding a new
// embedded language does not churn every golden that exercises the
// Guidelines section. Real production rendering uses every embedded
// language; that path is covered by buildPreflightInput's unit tests.
func testGuidelines() []GuidelineView {
	return []GuidelineView{
		{Language: "Go", IndexPath: "/state/guidelines/go/index.md"},
		{Language: "Python", IndexPath: "/state/guidelines/python/index.md"},
	}
}

type InquireUserInput struct {
	Name        string
	Description string
	Images      []string
	Attachments []string
	Repos       []RepoView

	Inquireness GrillMeInquirenessInput
}

type ResearchFromQuestionsUserInput struct {
	QuestionsPath string
	Repos         []RepoView
}

type KBBuildUserInput struct {
	RepoName       string
	RepoPath       string
	KBRootDir      string
	KBIndexPath    string
	ExistingKBPath string
	LastCommit     string
}

type DesignUserInput struct {
	Name        string
	Description string
	Images      []string
	Attachments []string
	Repos       []RepoView

	MultiRepo            bool
	ResearchArtifactPath string

	QAFiles     QAFilesInput
	Inquireness GrillMeInquirenessInput
}

type RefactorPlanUserInput struct {
	Request        string
	FeatureContext string
	Repos          []RepoView
}

type RoadmapUserInput struct {
	Name        string
	Description string
	Repos       []RepoView

	DesignArtifactPath   string
	ResearchArtifactPath string

	VisualReferences VisualReferencesInput
	QAFiles          QAFilesInput

	MultiRepo bool

	Inquireness GrillMeInquirenessInput
}

type RoadmapRevisionUserInput struct {
	Attempt        int
	CriticFeedback string

	PriorAxisApprovals  PriorAxisApprovalsInput
	PreviousRoadmapPath string

	RoadmapFormatPath string

	Inquireness AutonomousInquirenessInput
}

type PhasePlanView struct {
	Number        int
	Name          string
	Type          string
	Goal          string
	StubsToRetire []string
}

type PhasePlanUserInput struct {
	Phase                PhasePlanView
	RoadmapPath          string
	ResearchArtifactPath string

	QAFiles QAFilesInput

	Inquireness GrillMeInquirenessInput
}

type PhasePlanRevisionUserInput struct {
	Attempt int
	Phase   PhasePlanView

	Feedback string

	PriorAxisApprovals PriorAxisApprovalsInput
	PhasePlanPath      string

	PhasePlanFormatPath string

	Inquireness AutonomousInquirenessInput
}

type VerificationItemView struct {
	Name        string
	Requirement string
}

type ImplementUserInput struct {
	PlanPath              string
	ExitCriteria          string
	Feedback              string
	PlanRevisionFeedback  string
	HelpAnswers           string
	PriorUserInputAnswers string
	Iteration             int
}

type ReviewUserInput struct {
	Iteration int
	IterDir   string

	RoadmapPath            string
	PlanPath               string
	ExitCriteria           string
	VerificationReportPath string

	ContractPath         string
	RequiredVerification []VerificationItemView

	ProgressPath string
	PhaseType    string

	FeedbackPath string
}

type FinalFixUserInput struct {
	VisualReferences VisualReferencesInput

	Iteration              int
	ExitCriteria           string
	Feedback               string
	FeedbackPath           string
	VerificationReportPath string

	IncludeManualVerificationOutcomes bool
	Publishable                       bool
}

type FinalReviewUserInput struct {
	VisualReferences VisualReferencesInput

	Iteration     int
	IsCycleReview bool

	PhaseType          string
	DiffBase           string
	RoadmapPath        string
	DesignArtifactPath string

	FeatureDescription string
	ExitCriteria       string
	CycleFocus         string

	PriorImplementationReportPaths       []string
	PriorImplementationEvidenceRootDirs  []string
	PriorImplementationEvidenceArtifacts []string

	FeedbackPath     string
	Publishable      bool
	PreviousFeedback string
}

type ValidateSpecializedUserInput struct {
	Name         string
	Description  string
	ExitCriteria string
	RiskLevel    string

	DomainName string
	PlanPath   string

	IncludePriorPhaseContext bool
	PriorPhasePlanPaths      []string

	IsRoadmapKind bool

	ResearchPath string

	FeedbackPath string
	AxisLabel    string
}

// TestGoldenSnapshots exercises each user-facing template with realistic
// inputs and compares the rendered output to a checked-in fixture. The
// assertion is byte-exact: a single character of drift (whitespace,
// punctuation, missing newline) fails the test, which is the whole point.
//
// To intentionally change a prompt: edit the .tmpl file, run with -update,
// review the .golden diff in the PR.
func TestGoldenSnapshots(t *testing.T) {
	tcs := []struct {
		name   string
		render func() string
	}{
		{
			// The Inquire user prompt stays limited to invocation inputs;
			// RoleSpec system prompts own resource discovery.
			name: "inquire_user_high_with_kb",
			render: func() string {
				in := InquireUserInput{
					Name:        "Add OAuth login",
					Description: "Users should be able to sign in with Google.\nMust support PKCE.",
					Images:      []string{"/tmp/login-mock.png"},
					Attachments: []string{"/tmp/spec.pdf"},
					Repos: []RepoView{
						{Name: "web", Path: "/state/wt/web"},
						{Name: "api", Path: "/repos/api"},
					},
					Inquireness: GrillMeInquirenessInput{Level: "high"},
				}
				return InquireUserPrompt(in)
			},
		},
		{
			name: "research_from_questions_user",
			render: func() string {
				in := ResearchFromQuestionsUserInput{
					QuestionsPath: "/state/feat-x/inquire/questions.md",
					Repos: []RepoView{
						{Name: "web", Path: "/state/wt/web"},
					},
				}
				return ResearchFromQuestionsUserPrompt(in)
			},
		},
		{
			name: "kb_build_full",
			render: func() string {
				return KBBuildUserPrompt(KBBuildUserInput{
					RepoName:    "myrepo",
					RepoPath:    "/repos/myrepo",
					KBRootDir:   "/state/kb/myrepo",
					KBIndexPath: "/state/kb/myrepo/index.md",
				})
			},
		},
		{
			name: "refactor_plan_user",
			render: func() string {
				return RefactorPlanUserPrompt(RefactorPlanUserInput{
					Request:        "Extract shared config loading.",
					FeatureContext: "Existing feature context stays available for planning.",
					Repos: []RepoView{
						{Name: "api", Path: "/repos/api"},
						{Name: "web", Path: "/repos/web"},
					},
				})
			},
		},
		{
			// High-coverage design fixture: multi-repo, research
			// artifact, Q&A, and ambiguity handling. The paired system
			// prompt golden below locks down resource discovery.
			name: "design_user_multi_repo",
			render: func() string {
				in := DesignUserInput{
					Name:        "OAuth login",
					Description: "Sign in with Google.",
					Repos: []RepoView{
						{Name: "web", Path: "/repos/web"},
						{Name: "api", Path: "/repos/api"},
					},
					MultiRepo:            true,
					ResearchArtifactPath: "/state/feat-x/run-1/research/research.md",
					QAFiles: QAFilesInput{
						Paths: []string{"/state/feat-x/run-1/inquire/qa.md"},
						Lead:  "Read these Q&A files for important context about their intent and preferences:",
					},
					Inquireness: GrillMeInquirenessInput{Level: "medium"},
				}
				return DesignUserPrompt(in)
			},
		},
		{
			name: "roadmap_user_multi_repo",
			render: func() string {
				in := RoadmapUserInput{
					Name:               "OAuth login",
					Description:        "Sign in with Google.",
					Repos:              []RepoView{{Name: "web", Path: "/repos/web"}, {Name: "api", Path: "/repos/api"}},
					DesignArtifactPath: "/state/feat-x/run-1/design/design.md",
					VisualReferences: VisualReferencesInput{
						Images: []string{"/tmp/login.png"},
						Label:  "producing the roadmap",
					},
					QAFiles: QAFilesInput{
						Paths: []string{
							"/state/feat-x/run-1/inquire/qa.md",
							"/state/feat-x/run-1/research/qa.md",
						},
						Lead:          "Read these Q&A files for important context about their intent and preferences — do not re-ask questions that have already been answered:",
						TrailingBlank: true,
					},
					MultiRepo:   true,
					Inquireness: GrillMeInquirenessInput{Level: "medium"},
				}
				return RoadmapUserPrompt(in)
			},
		},
		{
			name: "roadmap_revision_user",
			render: func() string {
				return RoadmapRevisionUserPrompt(RoadmapRevisionUserInput{
					Attempt:        2,
					CriticFeedback: "The roadmap omits the PKCE flow.",
					PriorAxisApprovals: PriorAxisApprovalsInput{
						ArtifactName: "roadmap",
						Approvals: []AxisApprovalView{
							{Axis: "scope", FrozenSections: []string{"## Scope"}},
						},
					},
					PreviousRoadmapPath: "/state/feat-x/run-1/roadmap/plan.md",
					RoadmapFormatPath:   "/skills/create-roadmap/format.md",
					Inquireness:         AutonomousInquirenessInput{},
				})
			},
		},
		{
			// Phase-Plan primary builder always emits the grillme_inquireness
			// partial with Level="none" — see BuildPhasePlanPrompt for the
			// hardcoded override.
			name: "phase_plan_user_autonomous",
			render: func() string {
				in := PhasePlanUserInput{
					Phase: PhasePlanView{
						Number: 2, Name: "Wire up auth UI", Type: "tdd-fill-in",
						Goal: "Make the login button work.",
					},
					RoadmapPath: "/state/feat-x/run-1/roadmap/plan.md",
					Inquireness: GrillMeInquirenessInput{Level: "none"},
				}
				return PhasePlanUserPrompt(in)
			},
		},
		{
			name: "phase_plan_revision_user",
			render: func() string {
				return PhasePlanRevisionUserPrompt(PhasePlanRevisionUserInput{
					Attempt:  2,
					Phase:    PhasePlanView{Number: 2, Name: "Wire up auth UI", Type: "tdd-fill-in"},
					Feedback: "Plan groundwork is fine but missing PKCE.",
					PriorAxisApprovals: PriorAxisApprovalsInput{
						ArtifactName: "phase plan",
						Approvals: []AxisApprovalView{
							{Axis: "scope", FrozenSections: []string{"## Scope"}},
						},
					},
					PhasePlanPath:       "/state/feat-x/run-1/phase-2/plan.md",
					PhasePlanFormatPath: "/skills/plan-phase/format.md",
					Inquireness:         AutonomousInquirenessInput{},
				})
			},
		},
		{
			name: "implement_system_rolespec",
			render: func() string {
				return RoleSystemPrompt(RoleSystemInput{
					OutputRoots: []OutputRootView{
						{Name: "phase_dir", Path: "/state/feat-x/run-1/phase-1/implement", Description: "Phase-level implement artifact root shared across iterations."},
						{Name: "iteration_dir", Path: "/state/feat-x/run-1/phase-1/implement/iteration-02", Description: "Active iteration artifact directory."},
					},
					MarkerPath: "/state/feat-x/run-1/phase-1/implement/iteration-02/phase_complete",
					SkillPath:  "/skills/implement/SKILL.md",
					Preflight: PreflightInput{
						KBInfos: []KBView{
							{Name: "agentic", IndexPath: "/state/kb/agentic/index.md", RootDir: "/state/kb/agentic"},
						},
						Guidelines:    testGuidelines(),
						Skills:        testSkills(feature.PhaseImplement),
						HasKB:         true,
						HasGuidelines: true,
						HasSkills:     true,
					},
					SubagentsAvailable: true,
					AskingClause:       "## Asking Questions\n\nAsk one question at a time.",
				})
			},
		},
		{
			name: "design_system_rolespec",
			render: func() string {
				return RoleSystemPrompt(RoleSystemInput{
					OutputRoots: []OutputRootView{
						{Name: "phase_dir", Path: "/state/feat-x/run-1/design", Description: "Design phase artifact directory."},
					},
					MarkerPath: "/state/feat-x/run-1/design/phase_complete",
					SkillPath:  "/skills/design/SKILL.md",
					Preflight: PreflightInput{
						KBInfos: []KBView{
							{Name: "web", IndexPath: "/state/kb/web/index.md", RootDir: "/state/kb/web"},
							{Name: "api", IndexPath: "/state/kb/api/index.md", RootDir: "/state/kb/api"},
						},
						Guidelines:    testGuidelines(),
						Skills:        testSkills(feature.PhaseDesign),
						HasKB:         true,
						HasGuidelines: true,
						HasSkills:     true,
					},
					SubagentsAvailable: true,
					AskingClause:       "## Asking Questions\n\nUse numbered alternatives.",
				})
			},
		},
		{
			name: "summary_user",
			render: func() string {
				return SummaryUserPrompt(SummaryUserInput{
					Name:        "Add OAuth login",
					Description: "Sign in with Google.",
				})
			},
		},
		{
			name: "pr_description_user_full",
			render: func() string {
				return PRDescriptionUserPrompt(PRDescriptionUserInput{
					FeatureName:        "Add OAuth login",
					FeatureDescription: "Sign in with Google.",
					Roadmap:            "Phase 1: scaffolding.\nPhase 2: PKCE.",
					CommitBodies:       "feat: add login route\n---commit---\nfeat: wire PKCE",
					DiffStat:           " 5 files changed, 100 insertions(+), 4 deletions(-)",
				})
			},
		},
		{
			name: "final_fix_user_with_manual",
			render: func() string {
				return FinalFixUserPrompt(FinalFixUserInput{
					VisualReferences:                  VisualReferencesInput{Images: []string{"/tmp/login.png"}, Label: "applying this fix"},
					Iteration:                         2,
					ExitCriteria:                      "Relevant tests pass.",
					Feedback:                          "Manual verification bullet 'Inspect login UI' is unattested.",
					FeedbackPath:                      "/state/feat-x/run-1/review/iteration-2/review-feedback.md",
					VerificationReportPath:            "/state/feat-x/run-1/phase-1/iter-2/verification-report.yaml",
					IncludeManualVerificationOutcomes: true,
					Publishable:                       true,
				})
			},
		},
		{
			name: "final_review_user_phase",
			render: func() string {
				return FinalReviewUserPrompt(FinalReviewUserInput{
					VisualReferences:   VisualReferencesInput{Images: []string{"/tmp/login.png"}, Label: "conducting this final review"},
					Iteration:          1,
					IsCycleReview:      false,
					PhaseType:          "tdd-fill-in",
					DiffBase:           "main",
					RoadmapPath:        "/state/feat-x/run-1/roadmap/plan.md",
					DesignArtifactPath: "/state/feat-x/run-1/design/design.md",
					FeatureDescription: "Sign in with Google.",
					ExitCriteria:       "Relevant tests pass.",
					FeedbackPath:       "/state/feat-x/run-1/final-review/iter-1/review-feedback.md",
					Publishable:        true,
				})
			},
		},
		{
			name: "scout_user",
			render: func() string {
				return ScoutUserPrompt(ScoutUserInput{
					Query: "How does the auth module wrap errors?",
					Files: []ScoutFile{
						{Path: "auth/login.go", Purpose: "login flow", Content: "package auth\n\nfunc Login() error { ... }"},
					},
				})
			},
		},
		{
			name: "validate_specialized_grounding",
			render: func() string {
				return ValidateSpecializedUserPrompt(ValidateSpecializedUserInput{
					Name:                     "OAuth login",
					Description:              "Sign in with Google.",
					ExitCriteria:             "Relevant tests pass.",
					RiskLevel:                "medium",
					DomainName:               "grounding",
					PlanPath:                 "/state/feat-x/run-1/phase-2/plan.md",
					IncludePriorPhaseContext: true,
					PriorPhasePlanPaths:      []string{"/state/feat-x/run-1/phase-1/plan.md"},
					ResearchPath:             "/state/feat-x/run-1/research/research.md",
				})
			},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render()
			golden := filepath.Join("testdata", tc.name+".golden")

			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update to create)", err)
			}
			if string(want) != got {
				t.Errorf("byte-diff against %s\n--- WANT ---\n%s\n--- GOT ---\n%s\n",
					golden, string(want), got)
			}
		})
	}
}

func TestImplementUserPromptDoesNotRenderRequiredVerification(t *testing.T) {
	got := ImplementUserPrompt(ImplementUserInput{
		PlanPath:     "/state/feat-x/run-1/phase-1/plan.md",
		ExitCriteria: "Relevant tests pass.",
		Iteration:    3,
	})

	for _, unwanted := range []string{
		"Required verification items for this iteration",
		"auth-flow",
		"Run e2e.",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("ImplementUserPrompt() rendered static verification %q in:\n%s", unwanted, got)
		}
	}
}

func TestRoleSystemPromptSuppressesUsefulResourcesWhenEmpty(t *testing.T) {
	got := RoleSystemPrompt(RoleSystemInput{
		OutputRoots: []OutputRootView{
			{Name: "phase_dir", Path: "/state/feat-x/inquire"},
		},
		MarkerPath: "/state/feat-x/inquire/phase_complete",
		SkillPath:  "/skills/inquire/SKILL.md",
	})
	if strings.Contains(got, "# Useful Resources") {
		t.Fatalf("RoleSystemPrompt() rendered empty Useful Resources section:\n%s", got)
	}
}

func TestRoleSystemPromptGatesSubagentClause(t *testing.T) {
	const clause = "Sub-agents are available."
	base := RoleSystemInput{
		OutputRoots: []OutputRootView{{Name: "phase_dir", Path: "/state/feat-x/x"}},
		MarkerPath:  "/state/feat-x/x/phase_complete",
	}

	avail := base
	avail.SubagentsAvailable = true
	if !strings.Contains(RoleSystemPrompt(avail), clause) {
		t.Errorf("SubagentsAvailable=true: subagent clause missing")
	}

	unavail := base
	unavail.SubagentsAvailable = false
	if strings.Contains(RoleSystemPrompt(unavail), clause) {
		t.Errorf("SubagentsAvailable=false: subagent clause should be omitted (helper has no subagents)")
	}
}

func TestChatSystemPromptDefinesAMASpecificProtocol(t *testing.T) {
	got := ChatSystemPrompt(ChatSystemInput{
		SkillPath:       "/state/skills/chat/SKILL.md",
		RuntimeRoot:     "/isolated/agentico",
		StateDir:        "/isolated/agentico/features",
		ConfigPath:      "/isolated/agentico/config.yaml",
		WorkspaceDir:    "/workspace/repo",
		CurrentFeatures: "- **2d-retro-game-maker** (ID: feat-1): Build a game maker - Status: Implementing",
	})

	for _, want := range []string{
		"Agentic Orchestrator Expert Assistant",
		"Answer directly whenever the user's request is clear enough",
		"/state/skills/chat/SKILL.md",
		"Runtime root: `/isolated/agentico`",
		"Feature state directory: `/isolated/agentico/features`",
		"Config file: `/isolated/agentico/config.yaml`",
		"Workspace: `/workspace/repo`",
		"Do not substitute the default paths from the user guide",
		"2d-retro-game-maker",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ChatSystemPrompt() missing %q:\n%s", want, got)
		}
	}
	paths := strings.Join([]string{
		"- Runtime root: `/isolated/agentico`",
		"- Feature state directory: `/isolated/agentico/features`",
		"- Config file: `/isolated/agentico/config.yaml`",
		"- Workspace: `/workspace/repo`",
	}, "\n")
	if !strings.Contains(got, paths) {
		t.Fatalf("ChatSystemPrompt() runtime paths are not rendered as separate bullets:\n%s", got)
	}
}

func TestLegacyPromptSurfacesAreRemoved(t *testing.T) {
	removedPaths := []string{
		filepath.Join("templates", "system_completion_protocol.tmpl"),
		filepath.Join("templates", "system_implement_completion_protocol.tmpl"),
		filepath.Join("templates", "system_validator.tmpl"),
		filepath.Join("templates", "system_final_review.tmpl"),
		filepath.Join("partials", "design_reference.tmpl"),
		filepath.Join("partials", "skill_instruction.tmpl"),
		filepath.Join("partials", "preflight.tmpl"),
		filepath.Join("testdata", "completion_protocol_with_clause.golden"),
		filepath.Join("testdata", "implement_completion_protocol.golden"),
		filepath.Join("testdata", "system_validator.golden"),
		filepath.Join("testdata", "system_final_review.golden"),
		filepath.Join("testdata", "skill_instruction.golden"),
		filepath.Join("testdata", "preflight_kb_and_skills.golden"),
		filepath.Join("testdata", "preflight_all_three.golden"),
	}
	for _, path := range removedPaths {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("legacy prompt surface %s still exists", path)
		} else if !os.IsNotExist(err) {
			t.Errorf("checking %s: %v", path, err)
		}
	}
}
