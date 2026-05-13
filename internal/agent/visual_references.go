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

// visualReferencesSection renders user-attached images (mockups, design
// comps, screenshots of desired state, annotated whiteboards, bug
// repros, etc.) as an explicit prompt section that instructs the agent
// to Read each one before producing output.
//
// Today's pipeline carries f.Images only through Inquire and Brainstorm;
// every downstream phase — Plan, Implement, Review, FinalReview — has
// historically dropped them. For a feature whose intent was communicated
// through pixels, that's the same failure mode as dropping the brainstorm:
// the agent ends up working from text-only summaries of visual commitments.
//
// Returns "" when the images slice is empty.
//
// label is a short phase-specific noun woven into the imperative so each
// phase's prompt reads naturally. Pass "" to use a generic fallback.
//
// The literal prose lives in
// internal/agent/prompts/partials/visual_references.tmpl.
func visualReferencesSection(images []string, label string) string {
	return prompts.VisualReferences(prompts.VisualReferencesInput{
		Images: images,
		Label:  label,
	})
}

// visualEvidenceImplementSection instructs the agent to capture screenshots
// of UI changes into the given iteration directory. Scoped to frontend-tagged
// features via the caller (the runtime path is pure noise for backend work).
// The methodology — why, how, which harnesses — lives in
// skills/frontend-design/SKILL.md and is loaded for frontend-tagged features
// by utilskill's mandatory-skill wiring.
//
// Returns "" when the feature is not frontend-tagged, or when iterDir is
// empty (some test setups and early scaffolding).
//
// The literal prose lives in
// internal/agent/prompts/partials/visual_evidence_implement.tmpl.
func visualEvidenceImplementSection(f *feature.Feature, iterDir string) string {
	if f == nil || !f.HasTag(feature.TagFrontend) || iterDir == "" {
		return ""
	}
	return prompts.VisualEvidenceImplement(prompts.VisualEvidenceImplementInput{
		IterDir: iterDir,
	})
}

// visualEvidenceReviewSection points the reviewer at the iteration's
// screenshots directory and encodes the approval gate ("CHANGES_REQUESTED
// when UI diff has no screenshots"). Tag-gated to frontend features via the
// caller so backend reviews don't carry irrelevant screenshot instructions.
//
// The literal prose lives in
// internal/agent/prompts/partials/visual_evidence_review.tmpl.
func visualEvidenceReviewSection(f *feature.Feature, iterDir string) string {
	if f == nil || !f.HasTag(feature.TagFrontend) || iterDir == "" {
		return ""
	}
	return prompts.VisualEvidenceReview(prompts.VisualEvidenceReviewInput{
		IterDir: iterDir,
	})
}

// behavioralEvidenceImplementSection instructs the implementer to capture
// driven user-journey executions into the iteration's behaviors directory.
//
// Where visualEvidenceImplementSection answers "does it look right",
// this answers "does it actually work for the user". A binary that
// compiles, has unit tests, and has screenshots can still ship an incomplete
// primary mutation — a Create button whose handler is unwired, a service
// surface missing the very method users invoke, a form that submits to
// /dev/null. The behaviors artifact closes that gap by capturing one driven
// execution per primary user journey: Playwright trace, AppleScript log,
// headless-driver output, an HTTP-driven smoke transcript, a Wails event
// capture — whatever the repo's tooling already supports.
//
// Tag-gated to TagFrontend today, where the documented failure mode lives;
// the same shape applies to any binary with a primary user-mutation flow,
// and the gate can broaden as the corresponding tag taxonomy stabilizes.
//
// Returns "" when the feature is not gate-eligible or iterDir is empty.
//
// The literal prose lives in
// internal/agent/prompts/partials/behavioral_evidence_implement.tmpl.
func behavioralEvidenceImplementSection(f *feature.Feature, iterDir string) string {
	if f == nil || !f.HasTag(feature.TagFrontend) || iterDir == "" {
		return ""
	}
	return prompts.BehavioralEvidenceImplement(prompts.BehavioralEvidenceImplementInput{
		IterDir: iterDir,
	})
}

// behavioralEvidenceReviewSection points the reviewer at the iteration's
// behaviors directory and encodes the approval gate ("CHANGES_REQUESTED
// when the diff touches a user-mutation surface but the directory is empty
// or absent"). Tag-gated to frontend features via the caller.
//
// The literal prose lives in
// internal/agent/prompts/partials/behavioral_evidence_review.tmpl.
func behavioralEvidenceReviewSection(f *feature.Feature, iterDir string) string {
	if f == nil || !f.HasTag(feature.TagFrontend) || iterDir == "" {
		return ""
	}
	return prompts.BehavioralEvidenceReview(prompts.BehavioralEvidenceReviewInput{
		IterDir: iterDir,
	})
}

// reviewEvidenceIterDir gates the reviewer-side evidence partials
// (behavioral_evidence_review, visual_evidence_review) which now render
// inline inside review.user.tmpl right after the Progress section. Returns
// iterDir for frontend-tagged features (so the partials publish the
// matching directories and encode their CHANGES_REQUESTED gates), and ""
// for everything else — backend / untagged features see no evidence prose,
// matching the prior call-site prepend behavior.
func reviewEvidenceIterDir(f *feature.Feature, iterDir string) string {
	if f == nil || !f.HasTag(feature.TagFrontend) || iterDir == "" {
		return ""
	}
	return iterDir
}
