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
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// deferralsDueThisPhaseSection renders a "## Deferrals Due This Phase"
// prompt fragment listing every ledger entry whose DueByPhase matches the
// given phase and whose status is still open.
//
// This is the carry-forward mechanism that keeps prose hedges from
// cascading silently across phase boundaries. A Phase-1 implement iteration
// that said "Tailwind lands in Phase 3" created a ledger entry with
// DueByPhase=3; when the Phase-3 plan and implement prompts are built,
// this helper surfaces that entry verbatim so the agent cannot claim
// ignorance.
//
// Behavior modes:
//
//   - kindPlan: the block is a planning-time reminder. The planner must
//     either include each entry in the phase's declared scope OR
//     re-defer (emit an updated structured entry with a cited reason).
//     Silent omission is rejected by the phase-plan validator.
//
//   - kindImplement: the block is an implementation-time closing gate.
//     The implementer must either produce evidence the work landed and
//     cite the ID in closed_deferrals, OR re-defer in the deferrals:
//     block with a cited reason. The Report Integrity Gate refuses
//     SUCCESS while any due-this-phase entry is still open and uncited.
//
//   - kindReview: reminds the reviewer what to verify. closed_deferrals
//     IDs must be backed by actual diff evidence.
//
// Returns "" when no deferrals are due this phase.
//
// repoName scopes the per-repo filter via feature.DueForPhaseScopedTo.
// Empty repoName is permissive (no entries filtered out) — preserves
// callers that pre-date per-repo prompt threading.
//
// The literal prose lives in
// internal/agent/prompts/partials/deferrals_due.tmpl.
func deferralsDueThisPhaseSection(deferrals []feature.Deferral, phase int, kind deferralPromptKind, repoName string) string {
	due := feature.DueForPhaseScopedTo(deferrals, phase, repoName)
	if len(due) == 0 {
		return ""
	}

	views := make([]prompts.DeferralView, 0, len(due))
	for _, d := range due {
		views = append(views, prompts.DeferralView{
			ID:              d.ID,
			Description:     d.Description,
			CreatedInPhase:  d.CreatedInPhase,
			CreatedInKind:   d.CreatedInKind,
			DueByPhase:      d.DueByPhase,
			Reason:          d.Reason,
			RedeferralCount: d.RedeferralCount(),
		})
	}

	return prompts.DeferralsDue(prompts.DeferralsDueInput{
		Phase:   phase,
		Kind:    deferralPromptKindString(kind),
		Entries: views,
	})
}

// deferralPromptKindString maps the internal enum to the string Kind the
// deferrals_due partial switches on. Unknown values render as the empty
// string so the partial omits the per-kind preface.
func deferralPromptKindString(k deferralPromptKind) string {
	switch k {
	case deferralPromptKindPlan:
		return "plan"
	case deferralPromptKindImplement:
		return "implement"
	case deferralPromptKindReview:
		return "review"
	default:
		return ""
	}
}

// deferralPromptKind selects the wording and enforcement tone.
type deferralPromptKind int

const (
	deferralPromptKindPlan deferralPromptKind = iota
	deferralPromptKindImplement
	deferralPromptKindReview
)
