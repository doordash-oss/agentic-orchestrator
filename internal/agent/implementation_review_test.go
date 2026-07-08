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
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestImplementationReviewAxesForGate(t *testing.T) {
	tests := []struct {
		name    string
		gate    implementationReviewGate
		profile feature.PipelineProfile
		want    []string
	}{
		{
			name:    "moonshot selects the three read-only axes",
			gate:    implementationReviewGatePerPhase,
			profile: feature.PipelineMoonshot,
			want:    []string{"Craft", "Functionality/Evidence", "Cleanliness"},
		},
		{
			name:    "medium skips per-phase implementation review axes",
			gate:    implementationReviewGatePerPhase,
			profile: feature.PipelineMedium,
			want:    nil,
		},
		{
			name:    "large skips per-phase implementation review axes",
			gate:    implementationReviewGatePerPhase,
			profile: feature.PipelineLarge,
			want:    nil,
		},
		{
			name:    "medium selects final review axes",
			gate:    implementationReviewGateFinal,
			profile: feature.PipelineMedium,
			want:    []string{"Craft", "Cleanliness", "Functionality/Evidence"},
		},
		{
			name:    "large selects final review axes",
			gate:    implementationReviewGateFinal,
			profile: feature.PipelineLarge,
			want:    []string{"Craft", "Cleanliness", "Functionality/Evidence"},
		},
		{
			name:    "moonshot selects final review axes",
			gate:    implementationReviewGateFinal,
			profile: feature.PipelineMoonshot,
			want:    []string{"Craft", "Cleanliness", "Functionality/Evidence"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			axes := implementationReviewAxesForGate(tt.gate, tt.profile)
			got := make([]string, 0, len(axes))
			for _, axis := range axes {
				got = append(got, axis.Name)
				if !axis.ReadOnly {
					t.Errorf("axis %s ReadOnly = false, want true", axis.Name)
				}
				if axis.SkillName == "" || axis.ShortName == "" || axis.Role == "" {
					t.Errorf("axis %s has incomplete registry entry: %+v", axis.Name, axis)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("implementationReviewAxesForGate(%s, %s) = %v, want %v", tt.gate, tt.profile, got, tt.want)
			}
		})
	}
}

func TestImplementationReviewAxisRegistryProducesWellFormedRoleSpecs(t *testing.T) {
	wantSkills := map[string]Role{
		"review-implementation-craft":                  RoleImplementationReviewCraft,
		"review-implementation-functionality-evidence": RoleImplementationReviewFunctionalityEvidence,
		"review-implementation-cleanliness":            RoleImplementationReviewCleanliness,
	}
	if len(implementationReviewAxisRegistry) != len(wantSkills) {
		t.Fatalf("implementationReviewAxisRegistry length = %d, want %d", len(implementationReviewAxisRegistry), len(wantSkills))
	}

	seen := map[string]bool{}
	for _, axis := range implementationReviewAxisRegistry {
		wantRole, ok := wantSkills[axis.SkillName]
		if !ok {
			t.Fatalf("unexpected implementation review axis skill %q in registry", axis.SkillName)
		}
		if seen[axis.SkillName] {
			t.Fatalf("duplicate implementation review axis skill %q in registry", axis.SkillName)
		}
		seen[axis.SkillName] = true
		if axis.Role != wantRole {
			t.Fatalf("registry role for %q = %q, want %q", axis.SkillName, axis.Role, wantRole)
		}
		if len(axis.Memberships) == 0 || axis.Name == "" || axis.ShortName == "" || !axis.ReadOnly {
			t.Fatalf("registry row for %q is not well-formed: %+v", axis.SkillName, axis)
		}
		for _, membership := range axis.Memberships {
			if membership.Gate == "" || membership.Order == 0 || membership.Applies == nil {
				t.Fatalf("registry membership for %q is not well-formed: %+v", axis.SkillName, membership)
			}
		}

		spec, ok := ImplementationReviewAxisRoleForSkill(axis.SkillName)
		if !ok {
			t.Fatalf("ImplementationReviewAxisRoleForSkill(%q) ok = false, want true", axis.SkillName)
		}
		if spec.Role != axis.Role || spec.Phase != feature.PhaseReview || spec.SkillName != axis.SkillName {
			t.Fatalf("role spec for %q = {role:%q phase:%q skill:%q}, want registry role %q phase %q skill %q", axis.SkillName, spec.Role, spec.Phase, spec.SkillName, axis.Role, feature.PhaseReview, axis.SkillName)
		}
		if spec.ReadOnlyOutsideRoots != axis.ReadOnly {
			t.Fatalf("%s ReadOnlyOutsideRoots = %v, want registry ReadOnly=%v", axis.SkillName, spec.ReadOnlyOutsideRoots, axis.ReadOnly)
		}
		if spec.UserTemplate != "implementation_review_axis.user" {
			t.Fatalf("%s UserTemplate = %q, want implementation_review_axis.user", axis.SkillName, spec.UserTemplate)
		}
		if len(spec.OutputRoots) != 1 || spec.OutputRoots[0].Name != "helper_dir" || spec.MarkerRoot != "helper_dir" {
			t.Fatalf("%s roots = %+v marker=%q, want helper_dir-only axis helper", axis.SkillName, spec.OutputRoots, spec.MarkerRoot)
		}
		if got := spec.MarkerPath(RoleRuntime{IterationDir: "/tmp/iter-01/review/" + implementationReviewAxisSlug(axis.Name)}); !strings.HasSuffix(got, "/phase_complete") {
			t.Fatalf("%s MarkerPath() = %q, want helper-local phase_complete", axis.SkillName, got)
		}
		if len(spec.Artifacts) != 1 {
			t.Fatalf("%s artifact count = %d, want review feedback only", axis.SkillName, len(spec.Artifacts))
		}
		if artifact := spec.Artifacts[0]; artifact.RootName != "helper_dir" || artifact.RelativePath != "review-feedback.md" || artifact.Presence != ArtifactRequired || artifact.Validate != roles.ValidatorReviewFeedback {
			t.Fatalf("%s feedback artifact = %+v, want helper_dir required review-feedback.md review validator", axis.SkillName, artifact)
		}
	}
}

func TestComposeImplementationReviewFeedbackUsesStrictAllApprove(t *testing.T) {
	results := []reviewAxisResult{
		{Axis: "Craft", Status: ReviewApproved, Feedback: "## Findings\n- (none)\n\n## Suggestions\n- (none)\n\n## Verdict\nAPPROVED\n"},
		{Axis: "Functionality/Evidence", Status: ReviewChangesRequested, Feedback: "## Findings\n- **Critical**: missing required verification evidence\n\n## Suggestions\n- (none)\n\n## Verdict\nCHANGES_REQUESTED\n"},
		{Axis: "Cleanliness", Status: ReviewApproved, Feedback: "## Findings\n- (none)\n\n## Suggestions\n- **Low**: tidy temporary note\n\n## Verdict\nAPPROVED\n"},
	}

	status, feedback, err := composeImplementationReviewFeedback(results, 3)
	if err != nil {
		t.Fatalf("composeImplementationReviewFeedback() error = %v", err)
	}
	if status != ReviewChangesRequested {
		t.Fatalf("composeImplementationReviewFeedback() status = %s, want CHANGES_REQUESTED", status)
	}
	for _, want := range []string{
		"## Findings",
		"### Craft",
		"### Functionality/Evidence",
		"missing required verification evidence",
		"## Suggestions",
		"tidy temporary note",
		"## Verdict\nCHANGES_REQUESTED",
	} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("aggregate feedback missing %q in:\n%s", want, feedback)
		}
	}

	status, _, err = composeImplementationReviewFeedback(results[:2], 3)
	if err != nil {
		t.Fatalf("composeImplementationReviewFeedback(fewer results) error = %v", err)
	}
	if status != ReviewChangesRequested {
		t.Fatalf("composeImplementationReviewFeedback(fewer results) status = %s, want CHANGES_REQUESTED", status)
	}
}

func TestBuildImplementationReviewAxisPromptIncludesSingleReviewerContextPlusAxis(t *testing.T) {
	got := BuildImplementationReviewAxisPrompt(
		"/plan.md",
		"Feature fully implemented",
		"/progress.md",
		"/iter-01",
		"/testing-contract.yaml",
		"/iter-01/verification-report.yaml",
		1,
		[]RequiredVerificationItem{{Name: "Build passes", Requirement: "go build ./..."}},
		"/roadmap.md",
		"tdd-fill-in",
		"/iter-01/review/craft/review-feedback.md",
		"Craft",
	)

	for _, want := range []string{
		"Axis under review: Craft",
		"Iteration under review: 1",
		"Iteration artifacts directory: /iter-01",
		"Read the approved roadmap at: /roadmap.md",
		"Read the implementation plan (source of truth) at: /plan.md",
		"Feature fully implemented",
		"Read the verification report at: /iter-01/verification-report.yaml",
		"Read the binding testing contract at: /testing-contract.yaml",
		"Read the current progress at: /progress.md",
		"This is a **tdd-fill-in** phase",
		"Review feedback output: /iter-01/review/craft/review-feedback.md",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildImplementationReviewAxisPrompt() missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildImplementationReviewAxisPromptIncludesFinalGateContext(t *testing.T) {
	got := BuildImplementationReviewAxisPromptWithOpts(ImplementationReviewAxisPromptOpts{
		Gate:                                 implementationReviewGateFinal,
		AxisLabel:                            "Functionality/Evidence",
		FeatureDescription:                   "Assembled feature intent.",
		DesignArtifactPath:                   "/design.md",
		ExitCriteria:                         "Feature fully implemented.",
		DiffBase:                             "main",
		PreviousFeedback:                     "Prior aggregate finding.",
		Iteration:                            2,
		RoadmapPath:                          "/roadmap.md",
		PlanPath:                             "/phase-plan.md",
		PhaseType:                            "tdd-fill-in",
		IterDir:                              "/review/iteration-02",
		FeedbackPath:                         "/review/iteration-02/functionality-evidence/review-feedback.md",
		PriorImplementationReportPaths:       []string{"/phase-01/implement/iteration-01/verification-report.yaml"},
		PriorImplementationEvidenceRootDirs:  []string{"/phase-01/implement/iteration-01"},
		PriorImplementationEvidenceArtifacts: []string{"/phase-01/implement/iteration-01/screenshots/dashboard.png"},
	})

	for _, want := range []string{
		"Axis under review: Functionality/Evidence",
		"Gate under review: Final Review",
		"Cumulative diff base: main",
		"Review the assembled feature across the cumulative cross-repo diff.",
		"Approved design: /design.md",
		"Assembled feature intent.",
		"Feature fully implemented.",
		"Read the approved roadmap at: /roadmap.md",
		"Read the implementation plan at: /phase-plan.md",
		"Prior aggregate finding.",
		"/phase-01/implement/iteration-01/verification-report.yaml",
		"/phase-01/implement/iteration-01",
		"/phase-01/implement/iteration-01/screenshots/dashboard.png",
		"Review feedback output: /review/iteration-02/functionality-evidence/review-feedback.md",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildImplementationReviewAxisPromptWithOpts() missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"## Phase Type",
		"This is a **tdd-fill-in** phase",
		"Read the implementation plan (source of truth) at: /phase-plan.md",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("BuildImplementationReviewAxisPromptWithOpts() unexpectedly included %q in Final prompt:\n%s", unwanted, got)
		}
	}
}

func TestRunImplementationReviewAxesEmitsEventsAndPersistsStatus(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	iterDir := filepath.Join(artifactDir, "iteration-01")
	reviewDir := filepath.Join(iterDir, "review")
	stateDir := filepath.Join(tmpDir, "state")
	observeDir := filepath.Join(tmpDir, "observe")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{workDir, iterDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	progressPath := filepath.Join(artifactDir, "progress.md")
	if err := os.WriteFile(progressPath, []byte("# Iteration Progress\n\n## Iteration State\n\nSUCCESS\n"), 0o644); err != nil {
		t.Fatalf("write progress: %v", err)
	}
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	if err := os.WriteFile(reportPath, []byte("version: 1\nrequired_checks: []\n"), 0o644); err != nil {
		t.Fatalf("write verification report: %v", err)
	}
	planPath := filepath.Join(artifactDir, "phase-plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	f.ID = "test-implementation-review-events"
	f.Name = "Implementation Review Events"
	f.TraceID = "trace-implementation-review-events"
	f.Pipeline = feature.PipelineMoonshot
	f.CurrentPhase = feature.PhaseImplement
	if err := os.MkdirAll(filepath.Join(observeDir, f.ID), 0o755); err != nil {
		t.Fatalf("mkdir observe feature dir: %v", err)
	}
	store := feature.NewStore(stateDir)
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
		StateDir:     stateDir,
		BuildSession: mockBuildSession("", reviewScript),
		Observer:     observe.New(true, observeDir, false, "", false, "test"),
	}
	status, feedback, err := runImplementationReviewAxes(
		cfg,
		sm,
		1,
		iterDir,
		reviewDir,
		observe.SpanContext{TraceID: f.TraceID, SpanID: "review-span", FeatureID: f.ID, FeatureName: f.Name, RunNumber: 1},
		implementationReviewInput{ProgressPath: progressPath, VerificationReportPath: reportPath},
	)
	if err != nil {
		t.Fatalf("runImplementationReviewAxes() error = %v", err)
	}
	if status != ReviewApproved {
		t.Fatalf("status = %s, want APPROVED; feedback:\n%s", status, feedback)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if updated.ValidatorStatuses != nil {
		t.Fatalf("ValidatorStatuses = %v, want cleared after implementation review completes", updated.ValidatorStatuses)
	}

	events := readObserveEvents(t, observeDir, f.ID)
	validationStarted := filterEventsByType(events, "validation.started")
	if len(validationStarted) != 1 {
		t.Fatalf("validation.started count = %d, want 1", len(validationStarted))
	}
	if validationStarted[0].Phase != "implementation_review" || validationStarted[0].Data["validator_count"] != float64(3) {
		t.Fatalf("validation.started = %+v, want implementation_review with validator_count=3", validationStarted[0])
	}
	validationCompleted := filterEventsByType(events, "validation.completed")
	if len(validationCompleted) != 1 {
		t.Fatalf("validation.completed count = %d, want 1", len(validationCompleted))
	}
	if validationCompleted[0].Phase != "implementation_review" || validationCompleted[0].Status != "APPROVED" || validationCompleted[0].Data["validator_count"] != float64(3) {
		t.Fatalf("validation.completed = %+v, want implementation_review APPROVED with validator_count=3", validationCompleted[0])
	}

	started := validatorNamesByEvent(events, "validator.started")
	completed := validatorNamesByEvent(events, "validator.completed")
	wantNames := []string{"Cleanliness", "Craft", "Functionality/Evidence"}
	if !slices.Equal(started, wantNames) {
		t.Fatalf("validator.started names = %v, want %v", started, wantNames)
	}
	if !slices.Equal(completed, wantNames) {
		t.Fatalf("validator.completed names = %v, want %v", completed, wantNames)
	}
}

func validatorNamesByEvent(events []observe.Event, eventType string) []string {
	var names []string
	for _, event := range filterEventsByType(events, eventType) {
		if name, ok := event.Data["validator_name"].(string); ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
