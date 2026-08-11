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

import "github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"

// VerificationItemView projects a RequiredVerificationItem for review templates.
type VerificationItemView struct {
	Name        string
	Requirement string
}

// RepoDeltaBlock carries the per-repo incremental diff context for a
// Final Review round N>1 axis prompt. CommitMessages holds the commit
// messages in the range; DiffText holds the inline diff (or the --stat
// fallback when Capped is true). IsEmpty marks an empty range (head ==
// base, clean round).
type RepoDeltaBlock struct {
	RepoName       string
	CommitMessages string
	DiffText       string
	IsEmpty        bool
	Capped         bool
}

// ReviewUserInput is the data passed to review.user.tmpl.
type ReviewUserInput struct {
	Iteration int
	IterDir   string

	GateLabel          string
	FinalGate          bool
	LiveRunAxis        bool
	DiffBase           string
	FeatureDescription string
	DesignArtifactPath string
	PreviousFeedback   string
	// PriorAxisReport carries this axis's own verbatim round N-1
	// review-feedback.md for Final Review round N>1. Empty on round 1
	// (or when the prior report is missing/corrupt), so the template
	// omits the section.
	PriorAxisReport string
	// RepoDeltas carries the per-repo incremental diff blocks for Final
	// Review round N>1. Empty on round 1 (or when no prior anchors exist),
	// so the template omits the section.
	RepoDeltas []RepoDeltaBlock
	// RefactorPassForkPoint names a refactor child's fork-point commits
	// ("repo @ sha"). It resolves the spec's "fork point" references and
	// attributes cumulative-diff hunks between parent and pass. Empty for
	// top-level features.
	RefactorPassForkPoint string

	RoadmapPath            string
	PlanPath               string
	ExitCriteria           string
	AcceptanceClause       string
	VerificationReportPath string

	ContractPath         string
	RequiredVerification []VerificationItemView

	PriorImplementationReportPaths       []string
	PriorImplementationEvidenceRootDirs  []string
	PriorImplementationEvidenceArtifacts []string

	ProgressPath string
	PhaseType    string

	FeedbackPath string
}

// BuildReviewPrompt renders the implementation review prompt.
func BuildReviewPrompt(in ReviewUserInput) string {
	return prompts.ReviewUserPrompt(in)
}
