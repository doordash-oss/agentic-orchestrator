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
