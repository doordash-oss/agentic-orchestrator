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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// Role identifies the agent-session role whose completion artifacts are being
// validated.
type Role string

// roleRuntime carries runtime paths used to resolve named output roots.
type roleRuntime struct {
	IterationDir string
}

// outputRootSpec declares one named root a role exposes to its agent.
type outputRootSpec struct {
	Name        string
	Description string
	ResolvePath func(roleRuntime) string
}

// roleArtifactSpec declares one artifact in a role's completion contract.
// Every declared artifact is required.
type roleArtifactSpec struct {
	Name          string
	DisplayPath   string
	RootName      string
	RelativePath  string
	Description   string
	HideFromSkill bool
	ResolvePath   func(roleRuntime, roleArtifactSpec) string
	// Validate reads the artifact from its resolved path. It receives the
	// artifact itself so display paths come from this declaration.
	Validate func(artifact roleArtifactSpec, iterDir, path string, out *Outcome) ([]ProtocolViolation, error)
}

// roleSpec is the canonical declaration for one phase/role pairing.
//
// ReadOnlyOutsideRoots marks a role whose only deliverables are documents
// in the named OutputRoots. When set, the system prompt adds an absolute
// prohibition against writing anywhere else on disk — including the working
// tree of any target repo — and an explicit "this role never writes code"
// reminder. Use it for the Inquiry/Design/Roadmap/Plan family of roles; leave
// it false for Implement and any other role that has to modify source code.
//
// IterationState marks a role whose artifact contract carries a structured
// iteration state (progress.md). Only such roles may end a turn with the
// "retry" completion outcome; every other role must complete with "success"
// and record any findings in its artifacts.
type roleSpec struct {
	Phase                feature.Phase
	Role                 Role
	SkillName            string
	OutputRoots          []outputRootSpec
	Artifacts            []roleArtifactSpec
	ReadOnlyOutsideRoots bool
	IterationState       bool
}

// ArtifactPath resolves an artifact path using the spec's named output roots.
func (s roleSpec) ArtifactPath(rt roleRuntime, artifact roleArtifactSpec) string {
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
func (s roleSpec) OutputRootPaths(rt roleRuntime) map[string]string {
	roots := make(map[string]string, len(s.OutputRoots))
	for _, root := range s.OutputRoots {
		if root.ResolvePath == nil {
			continue
		}
		roots[root.Name] = root.ResolvePath(rt)
	}
	return roots
}

// Contract derives the deterministic completion contract from the spec,
// binding each artifact's validator and path resolver once.
func (s roleSpec) Contract() RoleContract {
	contract := RoleContract{Role: s.Role}
	for _, artifact := range s.Artifacts {
		contract.Required = append(contract.Required, RequiredArtifact{
			Name:          artifact.Name,
			DisplayPath:   artifact.DisplayPath,
			HideFromSkill: artifact.HideFromSkill,
			ResolvePath: func(iterDir string) string {
				return s.ArtifactPath(roleRuntime{IterationDir: iterDir}, artifact)
			},
			Validate: func(iterDir, path string, out *Outcome) ([]ProtocolViolation, error) {
				return artifact.Validate(artifact, iterDir, path, out)
			},
		})
	}
	return contract
}

func artifactDirOutputRoot(description string) outputRootSpec {
	return outputRootSpec{
		Name:        "artifact_dir",
		Description: description,
		ResolvePath: func(rt roleRuntime) string {
			return filepath.Dir(rt.IterationDir)
		},
	}
}

func attemptDirOutputRoot(description string) outputRootSpec {
	return outputRootSpec{
		Name:        "attempt_dir",
		Description: description,
		ResolvePath: func(rt roleRuntime) string {
			return rt.IterationDir
		},
	}
}

func singleShotPhaseDirOutputRoot(description string) outputRootSpec {
	return outputRootSpec{
		Name:        "phase_dir",
		Description: description,
		ResolvePath: func(rt roleRuntime) string {
			return rt.IterationDir
		},
	}
}

func iterationDirOutputRoot(description string) outputRootSpec {
	return outputRootSpec{
		Name:        "iteration_dir",
		Description: description,
		ResolvePath: func(rt roleRuntime) string {
			return rt.IterationDir
		},
	}
}

func validatorAttemptDirOutputRoot() outputRootSpec {
	return outputRootSpec{
		Name:        "attempt_dir",
		Description: "Parent planning attempt directory that owns this validator helper.",
		ResolvePath: func(rt roleRuntime) string {
			return filepath.Dir(rt.IterationDir)
		},
	}
}

func validatorHelperDirOutputRoot() outputRootSpec {
	return outputRootSpec{
		Name:        "helper_dir",
		Description: "Validator helper artifact directory for this axis.",
		ResolvePath: func(rt roleRuntime) string {
			return rt.IterationDir
		},
	}
}

func roadmapMarkdownRoleArtifact() roleArtifactSpec {
	return roleArtifactSpec{
		Name:         "roadmap",
		DisplayPath:  "roadmap markdown",
		RootName:     "artifact_dir",
		RelativePath: "roadmap.md",
		Description:  "roadmap markdown matching the create-roadmap format contract",
		ResolvePath:  resolvePlanMarkdownRoleArtifact("roadmap.md"),
		Validate:     validateRoadmapArtifact,
	}
}

func phasePlanMarkdownRoleArtifact() roleArtifactSpec {
	return roleArtifactSpec{
		Name:         "phase_plan_markdown",
		DisplayPath:  "phase plan markdown",
		RootName:     "artifact_dir",
		RelativePath: "phase-plan.md",
		Description:  "phase plan markdown matching the plan-phase format contract",
		ResolvePath:  resolvePlanMarkdownRoleArtifact("phase-plan.md"),
		Validate:     validatePhasePlanMarkdownArtifact,
	}
}

func planAttemptMetaRoleArtifact() roleArtifactSpec {
	return roleArtifactSpec{
		Name:          "plan_attempt_meta",
		DisplayPath:   "meta.yaml",
		RootName:      "attempt_dir",
		RelativePath:  "meta.yaml",
		Description:   "harness-written planning attempt metadata",
		HideFromSkill: true,
		Validate:      validatePlanAttemptMetaArtifact,
	}
}

func phaseMarkdownRoleArtifact(display string, validate func(roleArtifactSpec, string, string, *Outcome) ([]ProtocolViolation, error)) roleArtifactSpec {
	return roleArtifactSpec{
		Name:         "phase_markdown_artifact",
		DisplayPath:  display,
		RootName:     "phase_dir",
		RelativePath: "<newest non-excluded *.md>",
		Description:  "newest non-excluded markdown artifact in the phase directory",
		ResolvePath: func(rt roleRuntime, _ roleArtifactSpec) string {
			return rt.IterationDir
		},
		Validate: validate,
	}
}

func reviewFeedbackRoleArtifact(rootName string) roleArtifactSpec {
	return roleArtifactSpec{
		Name:         "review_feedback",
		DisplayPath:  "review-feedback.md",
		RootName:     rootName,
		RelativePath: "review-feedback.md",
		Description:  "structured review feedback markdown with findings, suggestions, and verdict",
		Validate:     validateReviewFeedbackArtifact,
	}
}

func resolvePlanMarkdownRoleArtifact(fallbackName string) func(roleRuntime, roleArtifactSpec) string {
	return func(rt roleRuntime, _ roleArtifactSpec) string {
		artifactDir := artifactDirOutputRoot("").ResolvePath(rt)
		if path := newestPhaseMarkdownArtifact(artifactDir); path != "" {
			return path
		}
		return filepath.Join(artifactDir, fallbackName)
	}
}
