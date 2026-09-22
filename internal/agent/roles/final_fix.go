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
	// No required artifacts: no testing contract executes at Final Review;
	// the next review iteration's live-run axes re-exercise the product. The
	// fix manifest is optional: PR-stack features use it to route changed
	// files to the stack layer that owns them.
	Artifacts: []RoleArtifactSpec{
		{
			Name:         "fix_manifest",
			DisplayPath:  "fix-manifest.yaml",
			RootName:     "iteration_dir",
			RelativePath: "fix-manifest.yaml",
			Presence:     ArtifactOptional,
			Description:  "optional manifest assigning this fix round's changed files to their owning stack layers",
			Validate:     ValidatorFixManifest,
		},
	},
}

// FinalReviewFixerRoleSpec returns the RoleSpec-backed final-review fix role.
func FinalReviewFixerRoleSpec() RoleSpec {
	return CloneRoleSpec(finalReviewFixerRoleSpec)
}

// FinalFixUserInput is the data passed to final_fix.user.tmpl.
type FinalFixUserInput struct {
	VisualReferences prompts.VisualReferencesInput

	Iteration        int
	ExitCriteria     string
	AcceptanceClause string
	Feedback         string
	FeedbackPath     string

	IncludeManualVerificationOutcomes bool
	Publishable                       bool
	// RefactorPassForkPoint resolves the spec's "fork point" references for a
	// refactor child ("repo @ sha"). Empty for top-level features.
	RefactorPassForkPoint string
	// Stack is the feature's PR-stack layers; empty for features that do not
	// deliver as a stack.
	Stack []feature.StackLayer
	// FixManifestPath is the resolved artifact path of the optional
	// fix-manifest.yaml, shown to the fixer alongside the stack layers.
	FixManifestPath string
}

// BuildFinalFixPrompt renders the final-review fix prompt.
func BuildFinalFixPrompt(in FinalFixUserInput) string {
	return prompts.FinalFixUserPrompt(in)
}
