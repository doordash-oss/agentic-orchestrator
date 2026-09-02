// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"fmt"
	"strings"
)

// Warning codes, one per distinct remediation. All are warning class with no
// action references: a warning never blocks progress, never gates a lane,
// and never offers an action. The feature codes are computed at the boundary
// that produces them; the relationship codes are the two optional records a
// transaction journal entry can store; the rewind and repository-diff codes
// classify their carriers' typed failures at the mutation-target boundary.
const (
	// EffortCapabilityDrift reports a role effort the resolved model does
	// not support; Auto is in use until the configuration changes.
	EffortCapabilityDrift Code = "effort_capability_drift"
	// FeatureLoadFailed reports a feature file that failed to load at list
	// level; the load error rides as bounded diagnostics.
	FeatureLoadFailed Code = "feature_load_failed"
	// ChildCleanupIncomplete reports a repository whose worktree/branch
	// cleanup after a child pass did not finish.
	ChildCleanupIncomplete Code = "child_cleanup_incomplete"
	// ReviewFeedbackTailIncomplete reports a repository whose review
	// feedback integration tail (push, reply, thread resolution) did not
	// finish; each failure is one diagnostics line.
	ReviewFeedbackTailIncomplete Code = "review_feedback_tail_incomplete"
	// RewindPullRequestCloseFailed reports a repository whose pull request
	// could not be closed during a rewind.
	RewindPullRequestCloseFailed Code = "rewind_pull_request_close_failed"
	// RewindBackupBranchFailed reports a repository whose backup branch
	// could not be created during a rewind.
	RewindBackupBranchFailed Code = "rewind_backup_branch_failed"
	// RewindWorktreeResetFailed reports a repository whose worktree could
	// not be reset during a rewind.
	RewindWorktreeResetFailed Code = "rewind_worktree_reset_failed"
	// RepositoryWorktreeUnavailable reports a repository whose worktree is
	// not available for diff inspection.
	RepositoryWorktreeUnavailable Code = "repository_worktree_unavailable"
	// RepositoryDiffFailed reports a repository whose diff could not be
	// computed; the git error rides as bounded diagnostics.
	RepositoryDiffFailed Code = "repository_diff_failed"
)

// Orphan-session recovery codes. An orphan session is a recovery item whose
// PID file names a phase run Agentico no longer supervises; liveness picks
// the code. Both are needs_action preconditions that reference the resume
// action and declare the phase and repositories blocks.
const (
	// OrphanSessionLive marks an orphan whose process is still running.
	OrphanSessionLive Code = "orphan_session_live"
	// OrphanSessionStale marks an orphan with no process running.
	OrphanSessionStale Code = "orphan_session_stale"
)

// relationshipWarningCodes is the closed set of warning codes a transaction
// journal entry can store as a record. RenderRecord routes their stored
// records through the repositories-derived params so a stored record renders
// the same text as a freshly built one.
var relationshipWarningCodes = map[Code]bool{
	ChildCleanupIncomplete:       true,
	ReviewFeedbackTailIncomplete: true,
}

// IsRelationshipWarning reports whether code is one of the relationship
// warning codes a transaction journal entry can store.
func IsRelationshipWarning(code Code) bool {
	return relationshipWarningCodes[code]
}

// orphanSessionCodes is the closed set of codes an orphan-session recovery
// item can carry.
var orphanSessionCodes = map[Code]bool{
	OrphanSessionLive:  true,
	OrphanSessionStale: true,
}

// IsOrphanSession reports whether code is one of the orphan-session recovery
// codes.
func IsOrphanSession(code Code) bool {
	return orphanSessionCodes[code]
}

// EffortDriftParams carries the role label, effort, and model names an
// effort-drift summary interpolates.
type EffortDriftParams struct {
	Role   string
	Effort string
	Model  string
}

func (EffortDriftParams) params() {}

// FeatureLoadFailedParams carries the feature ID a load-failure summary
// interpolates.
type FeatureLoadFailedParams struct {
	FeatureID string
}

func (FeatureLoadFailedParams) params() {}

// WarningRepoParams carries the repositories block a repository-keyed
// warning summary interpolates. RenderRecord derives it from a stored
// record's repositories block; the static summary applies when the record
// carries no repositories.
type WarningRepoParams struct {
	Repositories []CodeRepository
}

func (WarningRepoParams) params() {}

// OrphanSessionParams carries the phase name, iteration, and repository
// names an orphan-session summary interpolates.
type OrphanSessionParams struct {
	Phase        string
	Iteration    int
	Repositories []string
}

func (OrphanSessionParams) params() {}

// warningRepoParams extracts the repositories from a warning params value,
// reporting whether the template may render.
func warningRepoParams(p Params) ([]CodeRepository, bool) {
	params, ok := p.(WarningRepoParams)
	if !ok || len(params.Repositories) == 0 {
		return nil, false
	}
	return params.Repositories, true
}

// warningRepoClause renders `repository "web"` — with the branch when it is
// known — for the repository-keyed warning summaries, or "" when the
// repository has no name.
func warningRepoClause(repo CodeRepository) string {
	name := strings.TrimSpace(repo.Name)
	if name == "" {
		return ""
	}
	if branch := strings.TrimSpace(repo.Branch); branch != "" {
		return fmt.Sprintf("repository %q (branch %q)", name, branch)
	}
	return fmt.Sprintf("repository %q", name)
}

// warningRepoSummary renders the repository-keyed warning sentence via
// format, naming the first repository (and its branch when known), or "" for
// the static fallback.
func warningRepoSummary(p Params, format string) string {
	repos, ok := warningRepoParams(p)
	if !ok {
		return ""
	}
	clause := warningRepoClause(repos[0])
	if clause == "" {
		return ""
	}
	return fmt.Sprintf(format, clause)
}

// effortCapabilityDriftSummary names the role, effort, and model of the
// drifted effort, matching the configuration message users saw before the
// catalog owned this text.
func effortCapabilityDriftSummary(p Params) string {
	params, ok := p.(EffortDriftParams)
	if !ok || strings.TrimSpace(params.Role) == "" {
		return ""
	}
	return fmt.Sprintf(
		"%s effort %q is not supported by model %q; using Auto until the configuration is updated",
		params.Role, params.Effort, params.Model,
	)
}

// featureLoadFailedSummary names the feature that could not be loaded.
func featureLoadFailedSummary(p Params) string {
	params, ok := p.(FeatureLoadFailedParams)
	if !ok || strings.TrimSpace(params.FeatureID) == "" {
		return ""
	}
	return fmt.Sprintf("Feature %q could not be loaded.", params.FeatureID)
}

// orphanSessionSummary names the phase, iteration, and repository of an
// orphaned session run, closing with the liveness-specific state clause.
func orphanSessionSummary(p Params, stateClause string) string {
	params, ok := p.(OrphanSessionParams)
	if !ok || strings.TrimSpace(params.Phase) == "" {
		return ""
	}
	sentence := fmt.Sprintf("The %s phase", displayPhaseName(params.Phase))
	if params.Iteration > 0 {
		sentence += fmt.Sprintf(" at iteration %d", params.Iteration)
	}
	if len(params.Repositories) > 0 && strings.TrimSpace(params.Repositories[0]) != "" {
		sentence += fmt.Sprintf(" in repository %q", params.Repositories[0])
	}
	return sentence + " " + stateClause + "."
}
