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

// RoleInteractivePTY documents Tweak's carve-out: it is an interactive PTY
// ended by user Ctrl+D and skips the universal phase_complete observer.
const RoleInteractivePTY Role = "interactive_pty"

var interactivePTYRoleSpec = RoleSpec{
	Phase:      feature.PhaseImplement,
	Role:       RoleInteractivePTY,
	NoOp:       true,
	NoOpReason: "Tweak is an interactive PTY ended by user Ctrl+D; it does not use phase_complete.",
}

// InteractivePTYRoleSpec returns Tweak's no-op RoleSpec carve-out.
func InteractivePTYRoleSpec() RoleSpec {
	return CloneRoleSpec(interactivePTYRoleSpec)
}

// TweakUserInput is the data passed to tweak.user.tmpl.
type TweakUserInput struct {
	SkillPath string
	Name      string
	PlanPath  string
	PRURL     string
}

// BuildTweakPrompt renders the interactive tweak seed prompt.
func BuildTweakPrompt(in TweakUserInput) string {
	return prompts.TweakUserPrompt(in)
}
