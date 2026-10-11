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
	RoleValidatePhasePlanGrounding,
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
		{RoleValidatePhasePlanGrounding, "grounding"},
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

	var domains []validatorDomain
	for _, risk := range []feature.RiskLevel{feature.RiskLow, feature.RiskHigh} {
		domains = append(domains, roadmapValidatorsForRisk(risk)...)
		domains = append(domains, phasePlanValidatorsForRisk(risk)...)
	}
	for _, domain := range domains {
		spec, ok := planValidatorRoleForSkill(domain.Template)
		if !ok || spec.SkillName != domain.Template || spec.Phase != feature.PhasePlan {
			t.Fatalf("plan validator domain %q resolves to (skill %q, phase %s, ok %v), want plan validator", domain.Template, spec.SkillName, spec.Phase, ok)
		}
		if _, ok := lookupRoleSpec(feature.PhasePlan, spec.Role); !ok {
			t.Fatalf("plan validator %q is not in the manifest", spec.Role)
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
