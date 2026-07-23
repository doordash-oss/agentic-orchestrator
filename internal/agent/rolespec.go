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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type Role = roles.Role

const (
	RoleImplementer                               = roles.RoleImplementer
	RoleFinalReviewFixer                          = roles.RoleFinalReviewFixer
	RolePlanRoadmapPlanner                        = roles.RolePlanRoadmapPlanner
	RolePlanRoadmapReviser                        = roles.RolePlanRoadmapReviser
	RolePlanPhasePlanner                          = roles.RolePlanPhasePlanner
	RolePlanPhaseReviser                          = roles.RolePlanPhaseReviser
	RoleValidateRoadmapArchitecture               = roles.RoleValidateRoadmapArchitecture
	RoleValidateRoadmapScope                      = roles.RoleValidateRoadmapScope
	RoleValidatePhasePlanStructural               = roles.RoleValidatePhasePlanStructural
	RoleValidatePhasePlanScope                    = roles.RoleValidatePhasePlanScope
	RoleValidatePhasePlanGrounding                = roles.RoleValidatePhasePlanGrounding
	RoleValidatePlanSecurity                      = roles.RoleValidatePlanSecurity
	RoleValidatePlanPerformance                   = roles.RoleValidatePlanPerformance
	RoleValidatePlanTesting                       = roles.RoleValidatePlanTesting
	RoleImplementationReviewCraft                 = roles.RoleImplementationReviewCraft
	RoleImplementationReviewFunctionalityEvidence = roles.RoleImplementationReviewFunctionalityEvidence
	RoleImplementationReviewCleanliness           = roles.RoleImplementationReviewCleanliness
	RoleImplementationReviewQA                    = roles.RoleImplementationReviewQA
	RoleImplementationReviewDesign                = roles.RoleImplementationReviewDesign
	RoleRefactorPlanStep                          = roles.RoleRefactorPlanStep
	RoleKnowledgeBaseBuilder                      = roles.RoleKnowledgeBaseBuilder
	RoleInquirer                                  = roles.RoleInquirer
	RoleResearcher                                = roles.RoleResearcher
	RoleDesigner                                  = roles.RoleDesigner
)

type ArtifactPresence = roles.ArtifactPresence

const (
	ArtifactRequired    = roles.ArtifactRequired
	ArtifactOptional    = roles.ArtifactOptional
	ArtifactConditional = roles.ArtifactConditional
)

type RoleRuntime = roles.RoleRuntime
type OutputRootSpec = roles.OutputRootSpec
type RoleArtifactSpec = roles.RoleArtifactSpec

// RoleSpec is the compatibility view of the canonical declaration in
// internal/agent/roles. The child package owns the role facts; this package
// adapts artifact validator names to the existing contract validators.
type RoleSpec roles.RoleSpec

// ImplementRoleSpec returns the RoleSpec-backed implement role.
func ImplementRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.ImplementRoleSpec())
}

// RoadmapCreatorRoleSpec returns the RoleSpec-backed roadmap creation role.
func RoadmapCreatorRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.RoadmapCreatorRoleSpec())
}

// RoadmapReviserRoleSpec returns the RoleSpec-backed roadmap revision role.
func RoadmapReviserRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.RoadmapReviserRoleSpec())
}

// PhasePlanCreatorRoleSpec returns the RoleSpec-backed phase-plan creation role.
func PhasePlanCreatorRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.PhasePlanCreatorRoleSpec())
}

// PhasePlanReviserRoleSpec returns the RoleSpec-backed phase-plan revision role.
func PhasePlanReviserRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.PhasePlanReviserRoleSpec())
}

// KnowledgeBaseBuilderRoleSpec returns the RoleSpec-backed knowledge-base
// builder role.
func KnowledgeBaseBuilderRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.KnowledgeBaseBuilderRoleSpec())
}

// InquirerRoleSpec returns the RoleSpec-backed inquire role.
func InquirerRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.InquirerRoleSpec())
}

// ResearcherRoleSpec returns the RoleSpec-backed research role.
func ResearcherRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.ResearcherRoleSpec())
}

// DesignerRoleSpec returns the canonical Design RoleSpec wrapper.
func DesignerRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.DesignerRoleSpec())
}

// RefactorPlanRoleSpec returns the RoleSpec-backed refactor-plan role.
func RefactorPlanRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.RefactorPlanRoleSpec())
}

// ImplementationReviewAxisRoleSpecs returns the RoleSpec-backed per-axis
// implementation review roles.
func ImplementationReviewAxisRoleSpecs() []RoleSpec {
	return wrapRoleSpecs(roles.ImplementationReviewAxisRoleSpecs())
}

// ImplementationReviewAxisRoleForSkill returns the implementation review axis
// RoleSpec for a skill name such as "review-implementation-craft".
func ImplementationReviewAxisRoleForSkill(skillName string) (RoleSpec, bool) {
	spec, ok := roles.ImplementationReviewAxisRoleForSkill(skillName)
	return wrapRoleSpec(spec), ok
}

// FinalReviewFixerRoleSpec returns the RoleSpec-backed final-review fix role.
func FinalReviewFixerRoleSpec() RoleSpec {
	return wrapRoleSpec(roles.FinalReviewFixerRoleSpec())
}

// PlanValidatorRoleSpecs returns the RoleSpec-backed per-axis validator roles.
func PlanValidatorRoleSpecs() []RoleSpec {
	return wrapRoleSpecs(roles.PlanValidatorRoleSpecs())
}

// PlanValidatorRoleForSkill returns the validator RoleSpec for a validator
// skill name such as "validate-roadmap-architecture".
func PlanValidatorRoleForSkill(skillName string) (RoleSpec, bool) {
	spec, ok := roles.PlanValidatorRoleForSkill(skillName)
	return wrapRoleSpec(spec), ok
}

// SkillOutputRoleSpecs returns the roles whose SKILL.md files carry generated
// Output Files sections.
func SkillOutputRoleSpecs() []RoleSpec {
	return wrapRoleSpecs(roles.SkillOutputRoleSpecs())
}

func lookupRoleSpec(phase feature.Phase, role Role) (RoleSpec, bool) {
	spec, ok := roles.Lookup(phase, role)
	return wrapRoleSpec(spec), ok
}

func wrapRoleSpec(spec roles.RoleSpec) RoleSpec {
	return RoleSpec(spec)
}

func wrapRoleSpecs(specs []roles.RoleSpec) []RoleSpec {
	out := make([]RoleSpec, 0, len(specs))
	for _, spec := range specs {
		out = append(out, wrapRoleSpec(spec))
	}
	return out
}

func (s RoleSpec) roleSpec() roles.RoleSpec {
	return roles.RoleSpec(s)
}

// ArtifactPath resolves an artifact path using the RoleSpec's named output roots.
func (s RoleSpec) ArtifactPath(rt RoleRuntime, artifact RoleArtifactSpec) string {
	return s.roleSpec().ArtifactPath(rt, artifact)
}

// OutputRootPaths resolves every named root for a runtime invocation.
func (s RoleSpec) OutputRootPaths(rt RoleRuntime) map[string]string {
	return s.roleSpec().OutputRootPaths(rt)
}

// MarkerPath resolves the role's phase_complete marker path.
func (s RoleSpec) MarkerPath(rt RoleRuntime) string {
	return s.roleSpec().MarkerPath(rt)
}

// Contract derives the deterministic completion contract from the RoleSpec.
func (s RoleSpec) Contract() RoleContract {
	contract := RoleContract{
		Role:       s.Role,
		NoOp:       s.NoOp,
		NoOpReason: s.NoOpReason,
	}
	if s.NoOp {
		return contract
	}
	for _, artifact := range s.Artifacts {
		switch artifact.Presence {
		case ArtifactRequired:
			contract.Required = append(contract.Required, s.requiredArtifact(artifact))
		case ArtifactOptional:
			contract.Optional = append(contract.Optional, OptionalArtifact{
				Name:          artifact.Name,
				DisplayPath:   artifact.DisplayPath,
				HideFromSkill: artifact.HideFromSkill,
				ResolvePath:   s.artifactPathResolver(artifact),
				Validate:      validatorForRoleArtifact(artifact),
			})
		case ArtifactConditional:
			contract.Conditional = append(contract.Conditional, ConditionalArtifact{
				Name:     artifact.Name,
				When:     conditionForRoleArtifact(artifact),
				Artifact: s.requiredArtifact(artifact),
			})
		}
	}
	return contract
}

func (s RoleSpec) requiredArtifact(artifact RoleArtifactSpec) RequiredArtifact {
	return RequiredArtifact{
		Name:          artifact.Name,
		DisplayPath:   artifact.DisplayPath,
		HideFromSkill: artifact.HideFromSkill,
		ResolvePath:   s.artifactPathResolver(artifact),
		Validate:      validatorForRoleArtifact(artifact),
	}
}

func (s RoleSpec) artifactPathResolver(artifact RoleArtifactSpec) func(string) string {
	return func(iterDir string) string {
		return s.ArtifactPath(RoleRuntime{IterationDir: iterDir}, artifact)
	}
}

func validatorForRoleArtifact(artifact RoleArtifactSpec) func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error) {
	switch artifact.Validate {
	case roles.ValidatorProgress:
		return validateProgressArtifact
	case roles.ValidatorNeedUserInput:
		return validateNeedUserInputArtifact
	case roles.ValidatorRoadmap:
		return validateRoadmapArtifact
	case roles.ValidatorPhasePlanMarkdown:
		return validatePhasePlanMarkdownArtifact
	case roles.ValidatorPlanAttemptMeta:
		return validatePlanAttemptMetaArtifact
	case roles.ValidatorKnowledgeBaseIndex:
		return validateKnowledgeBaseIndexArtifact
	case roles.ValidatorPhaseMarkdown:
		display := artifact.DisplayPath
		return func(_ string, dir string, out *Outcome) ([]ProtocolViolation, error) {
			path := newestPhaseMarkdownArtifact(dir)
			if path == "" {
				return []ProtocolViolation{{Artifact: display, Reason: missingArtifactReason(display, dir)}}, nil
			}
			out.PhaseArtifactPath = path
			return nil, nil
		}
	case roles.ValidatorRefactorPlanMarkdown:
		return validateRefactorPlanMarkdownArtifact
	case roles.ValidatorReviewFeedback:
		displayPath := artifact.DisplayPath
		return func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error) {
			return validateReviewFeedbackArtifactWithDisplay(iterDir, path, displayPath, out)
		}
	case roles.ValidatorPlanValidatorAxisApproval:
		return validatePlanValidatorAxisApprovalArtifact
	default:
		return func(_, _ string, _ *Outcome) ([]ProtocolViolation, error) {
			return []ProtocolViolation{{Artifact: artifact.DisplayPath, Reason: fmt.Sprintf("unknown RoleSpec validator %q", artifact.Validate)}}, nil
		}
	}
}

func conditionForRoleArtifact(artifact RoleArtifactSpec) func(Outcome) bool {
	switch artifact.When {
	case roles.ConditionProgressNeedUserInput:
		return func(out Outcome) bool {
			return out.Progress != nil && out.Progress.OK() && out.Progress.State == StateNeedUserInput
		}
	default:
		return nil
	}
}

// RenderRoleSpecOutputFilesSection renders the generated SKILL.md section
// derived from RoleSpec.Artifacts.
func RenderRoleSpecOutputFilesSection(spec RoleSpec) string {
	return roles.RenderOutputFilesSection(spec.roleSpec())
}

func latestCompletedImplementationReportPath(implementDir string) string {
	entries, err := os.ReadDir(implementDir)
	if err != nil {
		return ""
	}
	am := NewArtifactManager(implementDir)
	latest := 0
	best := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var iteration int
		if _, err := fmt.Sscanf(entry.Name(), "iteration-%d", &iteration); err != nil || iteration <= latest {
			continue
		}
		iterDir := filepath.Join(implementDir, entry.Name())
		meta, err := am.ReadMeta(iterDir)
		if err != nil || !completedImplementationMeta(meta) {
			continue
		}
		reportPath := filepath.Join(iterDir, "verification-report.yaml")
		if _, err := os.Stat(reportPath); err != nil {
			continue
		}
		latest = iteration
		best = reportPath
	}
	return best
}

func completedImplementationMeta(meta IterationMeta) bool {
	if strings.TrimSpace(meta.AgentStatus) != agentStatusSuccess {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(meta.ReviewStatus)) {
	case strings.ToLower(ReviewApproved.String()), reviewStatusSkipped:
		return true
	default:
		return false
	}
}

func extractOutputFilesSection(content string) (string, bool) {
	start := strings.Index(content, "## Output Files\n")
	if start < 0 {
		return "", false
	}
	rest := content[start+len("## Output Files\n"):]
	end := nextMarkdownHeadingIndex(rest)
	if end < 0 {
		return content[start:], true
	}
	return content[start : start+len("## Output Files\n")+end+1], true
}

func replaceOutputFilesSection(content, section string) (string, error) {
	start := strings.Index(content, "## Output Files\n")
	if start < 0 {
		insertAt := strings.Index(content, "\n# ")
		if insertAt < 0 {
			if frontmatterEnd := strings.Index(content, "\n---\n"); frontmatterEnd >= 0 {
				pos := frontmatterEnd + len("\n---\n")
				return content[:pos] + "\n" + section + "\n" + content[pos:], nil
			}
			return content + "\n\n" + section, nil
		}
		lineEnd := strings.Index(content[insertAt+1:], "\n")
		if lineEnd < 0 {
			return content + "\n\n" + section, nil
		}
		pos := insertAt + 1 + lineEnd + 1
		return content[:pos] + "\n" + section + "\n" + content[pos:], nil
	}
	rest := content[start+len("## Output Files\n"):]
	end := nextMarkdownHeadingIndex(rest)
	if end < 0 {
		return content[:start] + section, nil
	}
	endPos := start + len("## Output Files\n") + end + 1
	return content[:start] + section + content[endPos:], nil
}

func nextMarkdownHeadingIndex(s string) int {
	offset := 0
	for {
		i := strings.Index(s[offset:], "\n#")
		if i < 0 {
			return -1
		}
		pos := offset + i
		lineStart := pos + 1
		j := lineStart
		for j < len(s) && s[j] == '#' {
			j++
		}
		if j > lineStart && j < len(s) && s[j] == ' ' {
			return pos
		}
		offset = lineStart + 1
	}
}
