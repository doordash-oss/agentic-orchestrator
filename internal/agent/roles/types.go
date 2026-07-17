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

// Package roles declares the RoleSpec manifest for autonomous agent roles.
package roles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// Role identifies the agent-session role whose completion artifacts are being
// validated.
type Role string

// ArtifactPresence describes whether a RoleSpec artifact is always required,
// optional, or required only when its predicate matches parsed state.
type ArtifactPresence string

const (
	// ArtifactRequired means the artifact must exist for every completed run.
	ArtifactRequired ArtifactPresence = "required"
	// ArtifactOptional means the artifact may be present and must parse if it is.
	ArtifactOptional ArtifactPresence = "optional"
	// ArtifactConditional means the artifact is required only when When matches.
	ArtifactConditional ArtifactPresence = "conditional"
)

// ArtifactValidator names the deterministic validator used by the agent
// package for a RoleSpec artifact.
type ArtifactValidator string

const (
	ValidatorProgress                  ArtifactValidator = "progress"
	ValidatorVerificationReport        ArtifactValidator = "verification_report"
	ValidatorNeedUserInput             ArtifactValidator = "need_user_input"
	ValidatorRoadmap                   ArtifactValidator = "roadmap"
	ValidatorPhasePlanMarkdown         ArtifactValidator = "phase_plan_markdown"
	ValidatorPlanAttemptMeta           ArtifactValidator = "plan_attempt_meta"
	ValidatorKnowledgeBaseIndex        ArtifactValidator = "knowledge_base_index"
	ValidatorPhaseMarkdown             ArtifactValidator = "phase_markdown"
	ValidatorRefactorPlanMarkdown      ArtifactValidator = "refactor_plan_markdown"
	ValidatorReviewFeedback            ArtifactValidator = "review_feedback"
	ValidatorPlanValidatorAxisApproval ArtifactValidator = "plan_validator_axis_approval"
)

// ArtifactCondition names the parsed-state predicate that makes a conditional
// artifact required.
type ArtifactCondition string

const (
	ConditionProgressNeedUserInput ArtifactCondition = "progress_need_user_input"
)

// RoleRuntime carries runtime paths used to resolve named output roots.
type RoleRuntime struct {
	IterationDir string
}

// OutputRootSpec declares one named root a role exposes to its agent.
type OutputRootSpec struct {
	Name        string
	Description string
	ResolvePath func(RoleRuntime) string
}

// RoleArtifactSpec declares one artifact in a role's completion contract.
type RoleArtifactSpec struct {
	Name          string
	DisplayPath   string
	RootName      string
	RelativePath  string
	Presence      ArtifactPresence
	Condition     string
	Description   string
	HideFromSkill bool
	ResolvePath   func(RoleRuntime, RoleArtifactSpec) string
	When          ArtifactCondition
	Validate      ArtifactValidator
}

// RoleSpec is the canonical declaration for one phase/role pairing.
//
// ReadOnlyOutsideRoots marks a role whose only deliverables are documents
// in the named OutputRoots. When set, the RoleSpec-backed system prompt
// adds an absolute prohibition against writing anywhere else on disk —
// including the working tree of any target repo — and an explicit "this
// role never writes code" reminder. Use it for the Inquiry/Design/Roadmap/
// Plan family of roles; leave it false for Implement and any other role
// that has to modify source code.
type RoleSpec struct {
	Phase                feature.Phase
	Role                 Role
	SkillName            string
	UserTemplate         string
	OutputRoots          []OutputRootSpec
	MarkerRoot           string
	Artifacts            []RoleArtifactSpec
	Required             []feature.Phase
	AskingClauseFor      func(model string) string
	NoOp                 bool
	NoOpReason           string
	ReadOnlyOutsideRoots bool
}

// CloneRoleSpec returns a copy that callers can inspect without mutating the
// package-level manifest.
func CloneRoleSpec(spec RoleSpec) RoleSpec {
	spec.OutputRoots = append([]OutputRootSpec(nil), spec.OutputRoots...)
	spec.Artifacts = append([]RoleArtifactSpec(nil), spec.Artifacts...)
	spec.Required = append([]feature.Phase(nil), spec.Required...)
	return spec
}

// ArtifactPath resolves an artifact path using the RoleSpec's named output
// roots.
func (s RoleSpec) ArtifactPath(rt RoleRuntime, artifact RoleArtifactSpec) string {
	if artifact.ResolvePath != nil {
		return artifact.ResolvePath(rt, artifact)
	}
	roots := s.OutputRootPaths(rt)
	root := roots[artifact.RootName]
	if root == "" {
		return artifact.RelativePath
	}
	if artifact.RelativePath == "" {
		return root
	}
	return filepath.Join(root, artifact.RelativePath)
}

// OutputRootPaths resolves every named root for a runtime invocation.
func (s RoleSpec) OutputRootPaths(rt RoleRuntime) map[string]string {
	roots := make(map[string]string, len(s.OutputRoots))
	for _, root := range s.OutputRoots {
		if root.ResolvePath == nil {
			continue
		}
		roots[root.Name] = root.ResolvePath(rt)
	}
	return roots
}

// MarkerPath resolves the role's phase_complete marker path.
func (s RoleSpec) MarkerPath(rt RoleRuntime) string {
	root := s.OutputRootPaths(rt)[s.MarkerRoot]
	if root == "" {
		return "phase_complete"
	}
	return filepath.Join(root, "phase_complete")
}

// RenderOutputFilesSection renders the generated SKILL.md section derived from
// RoleSpec.Artifacts.
func RenderOutputFilesSection(spec RoleSpec) string {
	var b strings.Builder
	b.WriteString("## Output Files\n\n")
	b.WriteString("| Artifact | Path | Requirement | Purpose |\n")
	b.WriteString("|----------|------|-------------|---------|\n")
	for _, artifact := range spec.Artifacts {
		if artifact.HideFromSkill {
			continue
		}
		requirement := string(artifact.Presence)
		if artifact.Condition != "" {
			requirement = fmt.Sprintf("%s: %s", requirement, artifact.Condition)
		}
		fmt.Fprintf(&b, "| `%s` | `{%s}/%s` | %s | %s |\n",
			artifact.DisplayPath,
			artifact.RootName,
			artifact.RelativePath,
			requirement,
			artifact.Description,
		)
	}
	b.WriteString("\n")
	return b.String()
}

func artifactDirOutputRoot(description string) OutputRootSpec {
	return OutputRootSpec{
		Name:        "artifact_dir",
		Description: description,
		ResolvePath: func(rt RoleRuntime) string {
			return filepath.Dir(rt.IterationDir)
		},
	}
}

func attemptDirOutputRoot(description string) OutputRootSpec {
	return OutputRootSpec{
		Name:        "attempt_dir",
		Description: description,
		ResolvePath: func(rt RoleRuntime) string {
			return rt.IterationDir
		},
	}
}

func singleShotPhaseDirOutputRoot(description string) OutputRootSpec {
	return OutputRootSpec{
		Name:        "phase_dir",
		Description: description,
		ResolvePath: func(rt RoleRuntime) string {
			return rt.IterationDir
		},
	}
}

func iterationDirOutputRoot(description string) OutputRootSpec {
	return OutputRootSpec{
		Name:        "iteration_dir",
		Description: description,
		ResolvePath: func(rt RoleRuntime) string {
			return rt.IterationDir
		},
	}
}

func validatorAttemptDirOutputRoot() OutputRootSpec {
	return OutputRootSpec{
		Name:        "attempt_dir",
		Description: "Parent planning attempt directory that owns this validator helper.",
		ResolvePath: func(rt RoleRuntime) string {
			return filepath.Dir(rt.IterationDir)
		},
	}
}

func validatorHelperDirOutputRoot() OutputRootSpec {
	return OutputRootSpec{
		Name:        "helper_dir",
		Description: "Validator helper artifact directory for this axis.",
		ResolvePath: func(rt RoleRuntime) string {
			return rt.IterationDir
		},
	}
}

func roadmapMarkdownRoleArtifact() RoleArtifactSpec {
	return RoleArtifactSpec{
		Name:         "roadmap",
		DisplayPath:  "roadmap markdown",
		RootName:     "artifact_dir",
		RelativePath: "roadmap.md",
		Presence:     ArtifactRequired,
		Description:  "roadmap markdown matching the create-roadmap format contract",
		ResolvePath:  resolvePlanMarkdownRoleArtifact("roadmap.md"),
		Validate:     ValidatorRoadmap,
	}
}

func phasePlanMarkdownRoleArtifact() RoleArtifactSpec {
	return RoleArtifactSpec{
		Name:         "phase_plan_markdown",
		DisplayPath:  "phase plan markdown",
		RootName:     "artifact_dir",
		RelativePath: "phase-plan.md",
		Presence:     ArtifactRequired,
		Description:  "phase plan markdown matching the plan-phase format contract",
		ResolvePath:  resolvePlanMarkdownRoleArtifact("phase-plan.md"),
		Validate:     ValidatorPhasePlanMarkdown,
	}
}

func planAttemptMetaRoleArtifact() RoleArtifactSpec {
	return RoleArtifactSpec{
		Name:          "plan_attempt_meta",
		DisplayPath:   "meta.yaml",
		RootName:      "attempt_dir",
		RelativePath:  "meta.yaml",
		Presence:      ArtifactRequired,
		Description:   "harness-written planning attempt metadata",
		HideFromSkill: true,
		Validate:      ValidatorPlanAttemptMeta,
	}
}

func phaseMarkdownRoleArtifact(display string) RoleArtifactSpec {
	return RoleArtifactSpec{
		Name:         "phase_markdown_artifact",
		DisplayPath:  display,
		RootName:     "phase_dir",
		RelativePath: "<newest non-excluded *.md>",
		Presence:     ArtifactRequired,
		Description:  "newest non-excluded markdown artifact in the phase directory",
		ResolvePath: func(rt RoleRuntime, _ RoleArtifactSpec) string {
			return rt.IterationDir
		},
		Validate: ValidatorPhaseMarkdown,
	}
}

func reviewFeedbackRoleArtifact(rootName string) RoleArtifactSpec {
	return RoleArtifactSpec{
		Name:         "review_feedback",
		DisplayPath:  "review-feedback.md",
		RootName:     rootName,
		RelativePath: "review-feedback.md",
		Presence:     ArtifactRequired,
		Description:  "structured review feedback markdown with findings, suggestions, and verdict",
		Validate:     ValidatorReviewFeedback,
	}
}

type reviewFeedbackAxisRoleSpecConfig struct {
	Phase                feature.Phase
	Role                 Role
	SkillName            string
	UserTemplate         string
	Required             []feature.Phase
	OutputRoots          []OutputRootSpec
	MarkerRoot           string
	Artifact             RoleArtifactSpec
	ReadOnlyOutsideRoots bool
}

func reviewFeedbackAxisRoleSpec(cfg reviewFeedbackAxisRoleSpecConfig) RoleSpec {
	return RoleSpec{
		Phase:                cfg.Phase,
		Role:                 cfg.Role,
		SkillName:            cfg.SkillName,
		UserTemplate:         cfg.UserTemplate,
		Required:             cfg.Required,
		OutputRoots:          cfg.OutputRoots,
		MarkerRoot:           cfg.MarkerRoot,
		Artifacts:            []RoleArtifactSpec{cfg.Artifact},
		ReadOnlyOutsideRoots: cfg.ReadOnlyOutsideRoots,
	}
}

func resolvePlanMarkdownRoleArtifact(fallbackName string) func(RoleRuntime, RoleArtifactSpec) string {
	return func(rt RoleRuntime, _ RoleArtifactSpec) string {
		artifactDir := artifactDirOutputRoot("").ResolvePath(rt)
		if path := newestPlanMarkdownArtifact(artifactDir); path != "" {
			return path
		}
		return filepath.Join(artifactDir, fallbackName)
	}
}

func newestPlanMarkdownArtifact(artifactDir string) string {
	var bestPath string
	var bestModTime int64

	matches, _ := filepath.Glob(filepath.Join(artifactDir, "*.md"))
	for _, path := range matches {
		if artifactExcluded(filepath.Base(path)) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if mt := info.ModTime().UnixNano(); bestPath == "" || mt > bestModTime {
			bestPath = path
			bestModTime = mt
		}
	}
	return bestPath
}

func artifactExcluded(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case lower == "system-prompt.md" || lower == "user-prompt.md":
		return true
	case strings.HasPrefix(lower, "validation-"):
		return true
	case strings.HasPrefix(lower, "debug-"):
		return true
	case lower == "error.log" || lower == "output.txt":
		return true
	case strings.HasSuffix(lower, "-prompt.md"):
		return true
	case lower == "qa-answers.md":
		return true
	default:
		return false
	}
}
