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

// RoleFinalReviewFixer is the fix session that runs after Final Review
// requests changes.
const RoleFinalReviewFixer Role = "final_review_fixer"

var finalReviewFixerRoleSpec = RoleSpec{
	Phase:        feature.PhaseReview,
	Role:         RoleFinalReviewFixer,
	SkillName:    "final-fix",
	UserTemplate: "final_fix.user",
	Required:     []feature.Phase{feature.PhaseReview},
	OutputRoots: []OutputRootSpec{
		iterationDirOutputRoot("Final-review fix iteration artifact directory."),
	},
	MarkerRoot: "iteration_dir",
	// No required artifacts: the harness executes the testing contract and
	// writes verification-report.yaml after the fix session, mirroring the
	// implementer role.
	Artifacts: []RoleArtifactSpec{},
}

// FinalReviewFixerRoleSpec returns the RoleSpec-backed final-review fix role.
func FinalReviewFixerRoleSpec() RoleSpec {
	return CloneRoleSpec(finalReviewFixerRoleSpec)
}

// FinalFixUserInput is the data passed to final_fix.user.tmpl.
type FinalFixUserInput struct {
	VisualReferences prompts.VisualReferencesInput

	Iteration              int
	ExitCriteria           string
	Feedback               string
	FeedbackPath           string
	VerificationReportPath string

	IncludeManualVerificationOutcomes bool
	Publishable                       bool
}

// BuildFinalFixPrompt renders the final-review fix prompt.
func BuildFinalFixPrompt(in FinalFixUserInput) string {
	return prompts.FinalFixUserPrompt(in)
}
