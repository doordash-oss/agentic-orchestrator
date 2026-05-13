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

// RoleFinalReviewer is the feature-level Final Review reviewer session.
const RoleFinalReviewer Role = "final_reviewer"

var finalReviewerRoleSpec = RoleSpec{
	Phase:        feature.PhaseReview,
	Role:         RoleFinalReviewer,
	SkillName:    "final-review",
	UserTemplate: "final_review.user",
	Required:     []feature.Phase{feature.PhaseImplement},
	OutputRoots: []OutputRootSpec{
		iterationDirOutputRoot("Final-review iteration artifact directory."),
	},
	MarkerRoot: "iteration_dir",
	Artifacts: []RoleArtifactSpec{
		reviewFeedbackRoleArtifact("iteration_dir"),
		{
			Name:         "verification_report",
			DisplayPath:  "verification-report.yaml",
			RootName:     "iteration_dir",
			RelativePath: "verification-report.yaml",
			Presence:     ArtifactRequired,
			Description:  "iteration-local final-review verification report updated by the reviewer",
			Validate:     ValidatorFinalReviewVerificationReport,
		},
	},
}

// FinalReviewerRoleSpec returns the RoleSpec-backed final-review gate role.
func FinalReviewerRoleSpec() RoleSpec {
	return CloneRoleSpec(finalReviewerRoleSpec)
}

// FinalReviewUserInput is the data passed to final_review.user.tmpl.
type FinalReviewUserInput struct {
	VisualReferences prompts.VisualReferencesInput

	Iteration     int
	IsCycleReview bool

	PhaseType   string
	DiffBase    string
	RoadmapPath string

	FeatureDescription string
	ExitCriteria       string
	CycleFocus         string

	TestingContractPath string

	VerificationPath string

	FeedbackPath     string
	Publishable      bool
	PreviousFeedback string
}

// BuildFinalReviewPrompt renders the final-review prompt.
func BuildFinalReviewPrompt(in FinalReviewUserInput) string {
	return prompts.FinalReviewUserPrompt(in)
}
