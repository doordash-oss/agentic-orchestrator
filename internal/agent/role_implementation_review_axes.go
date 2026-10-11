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

import "github.com/doordash-oss/agentic-orchestrator/internal/feature"

const (
	// RoleImplementationReviewCraft reviews implementation craft.
	RoleImplementationReviewCraft Role = "implementation_review_craft"
	// RoleImplementationReviewFunctionalityEvidence reviews functionality and evidence.
	RoleImplementationReviewFunctionalityEvidence Role = "implementation_review_functionality_evidence"
	// RoleImplementationReviewCleanliness reviews change-set cleanliness.
	RoleImplementationReviewCleanliness Role = "implementation_review_cleanliness"
	// RoleImplementationReviewQA performs hands-on functional QA at Final Review.
	RoleImplementationReviewQA Role = "implementation_review_qa"
	// RoleImplementationReviewDesign reviews visible UI design and originality.
	RoleImplementationReviewDesign Role = "implementation_review_design"
)

var implementationReviewAxisRoleSpecs = []roleSpec{
	implementationReviewAxisRoleSpec(RoleImplementationReviewCraft, "review-implementation-craft"),
	implementationReviewAxisRoleSpec(RoleImplementationReviewFunctionalityEvidence, "review-implementation-functionality-evidence"),
	implementationReviewAxisRoleSpec(RoleImplementationReviewCleanliness, "review-implementation-cleanliness"),
	implementationReviewAxisRoleSpec(RoleImplementationReviewQA, "review-implementation-qa"),
	implementationReviewAxisRoleSpec(RoleImplementationReviewDesign, "review-implementation-design"),
}

func implementationReviewAxisRoleSpec(role Role, skillName string) roleSpec {
	return roleSpec{
		Phase:     feature.PhaseReview,
		Role:      role,
		SkillName: skillName,
		OutputRoots: []outputRootSpec{
			{
				Name:        "helper_dir",
				Description: "Implementation review axis helper artifact directory.",
				ResolvePath: func(rt roleRuntime) string {
					return rt.IterationDir
				},
			},
		},
		Artifacts:            []roleArtifactSpec{reviewFeedbackRoleArtifact("helper_dir")},
		ReadOnlyOutsideRoots: true,
	}
}

// implementationReviewAxisUserInput is the data passed to
// implementation_review_axis.user.tmpl.
type implementationReviewAxisUserInput struct {
	reviewUserInput
	AxisLabel string
}
