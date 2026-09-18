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

// RoleResolveRebaseConflict is the short-lived session that resolves a
// cherry-pick conflict inside the detached temporary restack worktree while
// a PR stack is replayed onto a new target.
const RoleResolveRebaseConflict Role = "resolve_rebase_conflict"

var conflictResolverRoleSpec = RoleSpec{
	Phase:        feature.PhaseImplement,
	Role:         RoleResolveRebaseConflict,
	SkillName:    "resolve-rebase-conflict",
	UserTemplate: "conflict_resolution.user",
	Required:     []feature.Phase{feature.PhaseImplement},
	OutputRoots: []OutputRootSpec{
		attemptDirOutputRoot("Single conflict-resolution attempt directory: prompt, output, and completion-receipt root for one replayed commit."),
	},
	// No artifacts: the session's only deliverable is the conflict-free
	// state of the conflicted files in the restack worktree itself, which
	// the orchestrator verifies by inspecting the worktree — there is
	// nothing in the attempt directory to validate, so the semantic
	// completion contract commits on the success outcome alone.
	// ReadOnlyOutsideRoots stays false: unlike document-only planning
	// roles, this session must edit source files (the conflicted paths)
	// inside its working directory; the permission handler, not the
	// system prompt's read-only clause, scopes those edits.
}

// ConflictResolverRoleSpec returns the RoleSpec-backed rebase
// conflict-resolution role.
func ConflictResolverRoleSpec() RoleSpec {
	return CloneRoleSpec(conflictResolverRoleSpec)
}

// ConflictResolutionUserInput is the data passed to
// conflict_resolution.user.tmpl.
type ConflictResolutionUserInput struct {
	FeatureName        string
	FeatureDescription string
	// LayerPosition/LayerTitle identify the stack layer whose commit is
	// being replayed; RoadmapPhase is that layer's roadmap phase number.
	LayerPosition int
	LayerTitle    string
	RoadmapPhase  int
	// TargetBranch/TargetSHA name the new base the stack is replayed onto.
	TargetBranch string
	TargetSHA    string
	// CommitMessage/CommitPatch carry the replayed commit verbatim so the
	// resolver can reconstruct its intent from the detached worktree state.
	CommitMessage string
	CommitPatch   string
	// ConflictFiles are the conflicted paths relative to the worktree; the
	// resolver may edit only these.
	ConflictFiles []string
	// UpstreamDiff is the old-base→target diff restricted to the conflicted
	// files; empty when the target side did not touch them.
	UpstreamDiff string
	// Feedback names exactly what the previous attempt failed on; empty on
	// the first attempt.
	Feedback string
}

// BuildConflictResolutionPrompt renders the conflict-resolution user prompt.
func BuildConflictResolutionPrompt(in ConflictResolutionUserInput) string {
	return prompts.ConflictResolutionUserPrompt(in)
}
