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

package roles

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RoleIterationReviewer is a bounded helper that reviews one implement
// iteration.
const RoleIterationReviewer Role = "iteration_reviewer"

var iterationReviewerRoleSpec = RoleSpec{
	Phase:        feature.PhaseReview,
	Role:         RoleIterationReviewer,
	SkillName:    "review-implementation",
	UserTemplate: "review.user",
	Required:     []feature.Phase{feature.PhaseImplement},
	OutputRoots: []OutputRootSpec{
		{
			Name:        "helper_dir",
			Description: "Iteration review helper artifact directory.",
			ResolvePath: func(rt RoleRuntime) string {
				return rt.IterationDir
			},
		},
	},
	MarkerRoot: "helper_dir",
	Artifacts: []RoleArtifactSpec{
		reviewFeedbackRoleArtifact("helper_dir"),
	},
}

// IterationReviewerRoleSpec returns the RoleSpec-backed implementation review
// helper role.
func IterationReviewerRoleSpec() RoleSpec {
	return CloneRoleSpec(iterationReviewerRoleSpec)
}

// VerificationItemView projects a RequiredVerificationItem for review templates.
type VerificationItemView struct {
	Name        string
	Requirement string
}

// ReviewUserInput is the data passed to review.user.tmpl.
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

// BuildReviewPrompt renders the implementation review prompt.
func BuildReviewPrompt(in ReviewUserInput) string {
	return prompts.ReviewUserPrompt(in)
}
