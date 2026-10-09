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
	"path/filepath"
	"slices"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// allRoles lists every exported role constant. The coverage test requires the
// manifest to declare exactly these roles.
var allRoles = []Role{
	RoleImplementer,
	RoleFinalReviewFixer,
	RolePlanRoadmapPlanner,
	RolePlanRoadmapReviser,
	RolePlanPhasePlanner,
	RolePlanPhaseReviser,
	RoleValidateRoadmapArchitecture,
	RoleValidateRoadmapScope,
	RoleValidatePhasePlanStructural,
	RoleValidatePhasePlanScope,
	RoleValidatePlanSecurity,
	RoleValidatePlanPerformance,
	RoleValidatePlanTesting,
	RoleImplementationReviewCraft,
	RoleImplementationReviewFunctionalityEvidence,
	RoleImplementationReviewCleanliness,
	RoleImplementationReviewQA,
	RoleImplementationReviewDesign,
	RoleKnowledgeBaseBuilder,
	RoleInquirer,
	RoleResearcher,
	RoleDesigner,
}

func TestRoleContractsResolveArtifactPaths(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	implementIter := filepath.Join(base, "phase-01", "implement", "iteration-02")
	roadmapAttempt := filepath.Join(base, "roadmap", "attempt-02")
	phasePlanAttempt := filepath.Join(base, "phase-02", "plan", "attempt-03")
	kbDir := filepath.Join(base, "knowledge-base", "agentic")
	inquireDir := filepath.Join(base, "runs", "run-001", "inquire")
	researchDir := filepath.Join(base, "runs", "run-001", "research")
	designDir := filepath.Join(base, "runs", "run-001", "design")
	reviewIter := filepath.Join(base, "runs", "run-001", "review", "iteration-03")
	axisDir := filepath.Join(reviewIter, "craft")

	roadmapPaths := map[string]string{
		"roadmap":           filepath.Join(base, "roadmap", "roadmap.md"),
		"plan_attempt_meta": filepath.Join(roadmapAttempt, "meta.yaml"),
	}
	phasePlanPaths := map[string]string{
		"phase_plan_markdown": filepath.Join(base, "phase-02", "plan", "phase-plan.md"),
		"plan_attempt_meta":   filepath.Join(phasePlanAttempt, "meta.yaml"),
	}
	validatorPaths := func(axis string) (string, map[string]string) {
		helperDir := filepath.Join(roadmapAttempt, "validate-"+axis)
		return helperDir, map[string]string{
			"plan_validator_feedback": filepath.Join(helperDir, "validation-"+axis+"-feedback.md"),
		}
	}
	reviewAxisPaths := map[string]string{"review_feedback": filepath.Join(axisDir, "review-feedback.md")}

	type contractCase struct {
		phase     feature.Phase
		role      Role
		iterDir   string
		wantPaths map[string]string
	}
	tests := []contractCase{
		{feature.PhaseImplement, RoleImplementer, implementIter, map[string]string{"progress": filepath.Join(base, "phase-01", "implement", "progress.md")}},
		{feature.PhasePlan, RolePlanRoadmapPlanner, roadmapAttempt, roadmapPaths},
		{feature.PhasePlan, RolePlanRoadmapReviser, roadmapAttempt, roadmapPaths},
		{feature.PhasePlan, RolePlanPhasePlanner, phasePlanAttempt, phasePlanPaths},
		{feature.PhasePlan, RolePlanPhaseReviser, phasePlanAttempt, phasePlanPaths},
		{feature.PhaseKnowledgeBase, RoleKnowledgeBaseBuilder, kbDir, map[string]string{"knowledge_base_index": filepath.Join(kbDir, "index.md")}},
		{feature.PhaseInquire, RoleInquirer, inquireDir, map[string]string{"phase_markdown_artifact": inquireDir}},
		{feature.PhaseResearch, RoleResearcher, researchDir, map[string]string{"phase_markdown_artifact": researchDir}},
		{feature.PhaseDesign, RoleDesigner, designDir, map[string]string{"phase_markdown_artifact": designDir}},
		// The fixer has no required artifacts: no testing contract executes at
		// Final Review.
		{feature.PhaseReview, RoleFinalReviewFixer, reviewIter, map[string]string{}},
	}
	for _, v := range []struct {
		role Role
		axis string
	}{
		{RoleValidateRoadmapArchitecture, "architecture"},
		{RoleValidateRoadmapScope, "scope"},
		{RoleValidatePhasePlanStructural, "structural"},
		{RoleValidatePhasePlanScope, "scope"},
		{RoleValidatePlanSecurity, "security"},
		{RoleValidatePlanPerformance, "performance"},
		{RoleValidatePlanTesting, "testing"},
	} {
		helperDir, paths := validatorPaths(v.axis)
		tests = append(tests, contractCase{feature.PhasePlan, v.role, helperDir, paths})
	}
	for _, role := range []Role{
		RoleImplementationReviewCraft,
		RoleImplementationReviewFunctionalityEvidence,
		RoleImplementationReviewCleanliness,
		RoleImplementationReviewQA,
		RoleImplementationReviewDesign,
	} {
		tests = append(tests, contractCase{feature.PhaseReview, role, axisDir, reviewAxisPaths})
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			t.Parallel()
			contract, ok := Lookup(tt.phase, tt.role)
			if !ok {
				t.Fatalf("Lookup(%s, %q) ok = false, want true", tt.phase, tt.role)
			}
			if contract.Role != tt.role {
				t.Fatalf("contract.Role = %q, want %q", contract.Role, tt.role)
			}
			var gotNames []string
			for _, artifact := range contract.Required {
				gotNames = append(gotNames, artifact.Name)
				want, ok := tt.wantPaths[artifact.Name]
				if !ok {
					t.Fatalf("unexpected required artifact %q", artifact.Name)
				}
				if got := artifact.ResolvePath(tt.iterDir); got != want {
					t.Fatalf("artifact %q path = %q, want %q", artifact.Name, got, want)
				}
			}
			if len(gotNames) != len(tt.wantPaths) {
				t.Fatalf("required artifacts = %v, want %d artifacts", gotNames, len(tt.wantPaths))
			}
		})
	}
	if len(tests) != len(allRoles) {
		t.Fatalf("contract table covers %d roles, want all %d", len(tests), len(allRoles))
	}
}

func TestRoleManifestCoverage(t *testing.T) {
	t.Parallel()
	type key struct {
		phase feature.Phase
		role  Role
	}
	seen := map[key]bool{}
	var manifestRoles []Role
	for _, spec := range roleSpecs {
		k := key{spec.Phase, spec.Role}
		if seen[k] {
			t.Fatalf("duplicate manifest entry for phase %s role %q", spec.Phase, spec.Role)
		}
		seen[k] = true
		manifestRoles = append(manifestRoles, spec.Role)

		got, ok := lookupRoleSpec(spec.Phase, spec.Role)
		if !ok || got.SkillName != spec.SkillName {
			t.Fatalf("lookupRoleSpec(%s, %q) = (%q, %v), want manifest entry", spec.Phase, spec.Role, got.SkillName, ok)
		}
		if _, ok := Lookup(spec.Phase, spec.Role); !ok {
			t.Fatalf("Lookup(%s, %q) ok = false, want true", spec.Phase, spec.Role)
		}
		if spec.SkillName == "" {
			t.Fatalf("%q has no skill name", spec.Role)
		}
		for _, artifact := range spec.Artifacts {
			if artifact.Validate == nil {
				t.Fatalf("%q artifact %q has no validator", spec.Role, artifact.Name)
			}
			if artifact.DisplayPath == "" {
				t.Fatalf("%q artifact %q has no display path", spec.Role, artifact.Name)
			}
		}
	}
	slices.Sort(manifestRoles)
	want := slices.Clone(allRoles)
	slices.Sort(want)
	if !slices.Equal(manifestRoles, want) {
		t.Fatalf("manifest roles = %v, want %v", manifestRoles, want)
	}

	for _, axis := range implementationReviewAxisRegistry {
		spec, ok := lookupRoleSpec(feature.PhaseReview, axis.Role)
		if !ok || spec.SkillName != axis.SkillName || spec.Phase != feature.PhaseReview {
			t.Fatalf("review axis %q resolves to (skill %q, phase %s, ok %v), want skill %q in review", axis.Role, spec.SkillName, spec.Phase, ok, axis.SkillName)
		}
	}

	for _, axis := range planValidatorAxes {
		spec, ok := lookupRoleSpec(feature.PhasePlan, axis.Role)
		if !ok || spec.SkillName != axis.SkillName {
			t.Fatalf("plan validator axis %q resolves to (skill %q, ok %v), want skill %q in plan", axis.Role, spec.SkillName, ok, axis.SkillName)
		}
	}
}

func TestOnlyIterationStateRolesAllowRetry(t *testing.T) {
	t.Parallel()
	for _, spec := range roleSpecs {
		if got, want := spec.IterationState, spec.Role == RoleImplementer; got != want {
			t.Fatalf("%q IterationState = %v, want %v", spec.Role, got, want)
		}
	}
}

// TestPlanValidatorAxisNamesArePinned pins every plan-validator row, in
// selection order, and the on-disk names derived from its axis string (which
// is also the prompt axis label and stall-tracker key).
func TestPlanValidatorAxisNamesArePinned(t *testing.T) {
	t.Parallel()
	type pinned struct {
		name, axis, skill, helperDir, feedbackFile string
		role                                       Role
	}
	want := []pinned{
		{"Architecture", "architecture", "validate-roadmap-architecture", "validate-architecture", "validation-architecture-feedback.md", RoleValidateRoadmapArchitecture},
		{"Structural", "structural", "validate-phase-plan-structural", "validate-structural", "validation-structural-feedback.md", RoleValidatePhasePlanStructural},
		{"Scope", "scope", "validate-phase-plan-scope", "validate-scope", "validation-scope-feedback.md", RoleValidatePhasePlanScope},
		{"Security", "security", "validate-plan-security", "validate-security", "validation-security-feedback.md", RoleValidatePlanSecurity},
		{"Performance", "performance", "validate-plan-performance", "validate-performance", "validation-performance-feedback.md", RoleValidatePlanPerformance},
		{"Scope", "scope", "validate-roadmap-scope", "validate-scope", "validation-scope-feedback.md", RoleValidateRoadmapScope},
		{"Testing", "testing", "validate-plan-testing", "validate-testing", "validation-testing-feedback.md", RoleValidatePlanTesting},
	}
	if len(planValidatorAxes) != len(want) {
		t.Fatalf("planValidatorAxes has %d rows, want %d", len(planValidatorAxes), len(want))
	}
	for i, w := range want {
		axis := planValidatorAxes[i]
		got := pinned{axis.Name, axis.Axis, axis.SkillName, axis.helperDirName(), axis.feedbackFileName(), axis.Role}
		if got != w {
			t.Errorf("planValidatorAxes[%d] = %+v, want %+v", i, got, w)
		}
		spec := planValidatorRoleSpec(axis)
		artifact := spec.Artifacts[0]
		if artifact.DisplayPath != w.feedbackFile || artifact.RelativePath != w.feedbackFile {
			t.Errorf("%s feedback artifact = (%q, %q), want %q", axis.Role, artifact.DisplayPath, artifact.RelativePath, w.feedbackFile)
		}
	}
}

// TestImplementationReviewAxisSlugsArePinned pins every review axis slug and
// the role-spec list derived from the registry.
func TestImplementationReviewAxisSlugsArePinned(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"Craft":                  "craft",
		"Functionality/Evidence": "functionality-evidence",
		"Cleanliness":            "cleanliness",
		"QA":                     "qa",
		"Design":                 "design",
	}
	if len(implementationReviewAxisRegistry) != len(want) {
		t.Fatalf("implementationReviewAxisRegistry has %d rows, want %d", len(implementationReviewAxisRegistry), len(want))
	}
	if len(implementationReviewAxisRoleSpecs) != len(implementationReviewAxisRegistry) {
		t.Fatalf("implementationReviewAxisRoleSpecs has %d specs, want %d", len(implementationReviewAxisRoleSpecs), len(implementationReviewAxisRegistry))
	}
	for i, axis := range implementationReviewAxisRegistry {
		if axis.Slug != want[axis.Name] {
			t.Errorf("%s slug = %q, want %q", axis.Name, axis.Slug, want[axis.Name])
		}
		spec := implementationReviewAxisRoleSpecs[i]
		if spec.Role != axis.Role || spec.SkillName != axis.SkillName {
			t.Errorf("implementationReviewAxisRoleSpecs[%d] = (%s, %s), want (%s, %s)", i, spec.Role, spec.SkillName, axis.Role, axis.SkillName)
		}
	}
}

// TestRoleOutputRootsArePinned pins every role's output-root names and whether
// each resolves to the iteration directory or its parent.
func TestRoleOutputRootsArePinned(t *testing.T) {
	t.Parallel()
	const iter, parent = "iteration", "parent"
	validatorRoots := []string{"attempt_dir=" + parent, "helper_dir=" + iter}
	reviewRoots := []string{"helper_dir=" + iter}
	planRoots := []string{"artifact_dir=" + parent, "attempt_dir=" + iter}
	want := map[Role][]string{
		RoleImplementer:                               {"phase_dir=" + parent, "iteration_dir=" + iter},
		RolePlanRoadmapPlanner:                        planRoots,
		RolePlanRoadmapReviser:                        planRoots,
		RolePlanPhasePlanner:                          planRoots,
		RolePlanPhaseReviser:                          planRoots,
		RoleKnowledgeBaseBuilder:                      {"phase_dir=" + iter},
		RoleInquirer:                                  {"phase_dir=" + iter},
		RoleResearcher:                                {"phase_dir=" + iter},
		RoleDesigner:                                  {"phase_dir=" + iter},
		RoleFinalReviewFixer:                          {"iteration_dir=" + iter},
		RoleValidateRoadmapArchitecture:               validatorRoots,
		RoleValidateRoadmapScope:                      validatorRoots,
		RoleValidatePhasePlanStructural:               validatorRoots,
		RoleValidatePhasePlanScope:                    validatorRoots,
		RoleValidatePlanSecurity:                      validatorRoots,
		RoleValidatePlanPerformance:                   validatorRoots,
		RoleValidatePlanTesting:                       validatorRoots,
		RoleImplementationReviewCraft:                 reviewRoots,
		RoleImplementationReviewFunctionalityEvidence: reviewRoots,
		RoleImplementationReviewCleanliness:           reviewRoots,
		RoleImplementationReviewQA:                    reviewRoots,
		RoleImplementationReviewDesign:                reviewRoots,
	}
	if len(roleSpecs) != len(want) {
		t.Fatalf("roleSpecs has %d specs, want %d", len(roleSpecs), len(want))
	}
	rt := roleRuntime{IterationDir: filepath.Join("run", "iter")}
	for _, spec := range roleSpecs {
		var got []string
		for _, root := range spec.OutputRoots {
			var kind string
			switch root.ResolvePath(rt) {
			case rt.IterationDir:
				kind = iter
			case filepath.Dir(rt.IterationDir):
				kind = parent
			}
			got = append(got, root.Name+"="+kind)
		}
		if !slices.Equal(got, want[spec.Role]) {
			t.Errorf("%s output roots = %v, want %v", spec.Role, got, want[spec.Role])
		}
	}
}
