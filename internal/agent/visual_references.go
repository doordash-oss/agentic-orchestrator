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
