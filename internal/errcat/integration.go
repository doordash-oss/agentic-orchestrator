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

// Integration attention codes. Every condition that parks a child pass's
// integration transaction classifies into exactly one of these at the
// transaction boundary; all are fix-then-retry preconditions that reference
// the retry action and declare only the repositories block.
const (
	IntegrationMergeConflict        Code = "integration_merge_conflict"
	IntegrationParentDirty          Code = "integration_parent_dirty"
	IntegrationParentRefDrift       Code = "integration_parent_ref_drift"
	IntegrationRefRace              Code = "integration_ref_race"
	IntegrationParentBranchMismatch Code = "integration_parent_branch_mismatch"
	IntegrationRepositoryMissing    Code = "integration_repository_missing"
	IntegrationWorktreeSyncFailed   Code = "integration_worktree_sync_failed"
	IntegrationRolledBack           Code = "integration_rolled_back"
	IntegrationCandidateFailed      Code = "integration_candidate_failed"
	RebaseGateTargetMissing         Code = "rebase_gate_target_missing"
	RebaseGateNotAncestor           Code = "rebase_gate_not_ancestor"
	RebaseGateMergeInProgress       Code = "rebase_gate_merge_in_progress"
	RebaseGateConflictMarkers       Code = "rebase_gate_conflict_markers"
	RebaseGatePassthroughModified   Code = "rebase_gate_passthrough_modified"
)

// integrationAttentionCodes is the closed set of codes a parked integration
// transaction can carry. RenderRecord uses it to pick the summary parameter
// shape; the journal never stores a code outside this set.
var integrationAttentionCodes = map[Code]bool{
	IntegrationMergeConflict:        true,
	IntegrationParentDirty:          true,
	IntegrationParentRefDrift:       true,
	IntegrationRefRace:              true,
	IntegrationParentBranchMismatch: true,
	IntegrationRepositoryMissing:    true,
	IntegrationWorktreeSyncFailed:   true,
	IntegrationRolledBack:           true,
	IntegrationCandidateFailed:      true,
	RebaseGateTargetMissing:         true,
	RebaseGateNotAncestor:           true,
	RebaseGateMergeInProgress:       true,
	RebaseGateConflictMarkers:       true,
	RebaseGatePassthroughModified:   true,
}

// IsIntegrationAttention reports whether code is one of the integration
// attention codes a parked transaction journal can carry.
func IsIntegrationAttention(code Code) bool {
	return integrationAttentionCodes[code]
}

// IntegrationRepoParams carries the repositories block an integration
// attention summary interpolates. RenderRecord derives it from a stored
// record's repositories block; the static summary applies when the record
// carries no repositories.
type IntegrationRepoParams struct {
	Repositories []CodeRepository
}

func (IntegrationRepoParams) params() {}

// integrationParams extracts the repositories from an integration params
// value, reporting whether the template may render.
func integrationParams(p Params) ([]CodeRepository, bool) {
	params, ok := p.(IntegrationRepoParams)
	if !ok || len(params.Repositories) == 0 {
		return nil, false
	}
	return params.Repositories, true
}

// integrationRepoNames returns the nonempty repository names, joined for
// multi-repository sentences.
func integrationRepoNames(repos []CodeRepository) []string {
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		if name := strings.TrimSpace(repo.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// integrationOneOrMany renders the single-repository sentence when exactly
// one affected repository is known and a joined-names sentence otherwise.
// Both fall back to "" (the static summary) when no repository is nameable.
func integrationOneOrMany(repos []CodeRepository, one func(repo CodeRepository) string, manyLabel string) string {
	if len(repos) == 1 {
		if sentence := one(repos[0]); sentence != "" {
			return sentence
		}
	}
	names := integrationRepoNames(repos)
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return one(CodeRepository{Name: names[0]})
	}
	return manyLabel + " " + strings.Join(names, ", ") + "."
}

// integrationNamedRepo builds the single-repository sentence via format,
// substituting the static fallback "" when the repository has no name.
func integrationNamedRepo(repo CodeRepository, format string) string {
	name := strings.TrimSpace(repo.Name)
	if name == "" {
		return ""
	}
	return fmt.Sprintf(format, name)
}

// shortSHA abbreviates a full SHA for summary text.
func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// integrationMovedRef renders the "moved from <old> to <new>" clause when
// both SHAs are known, falling back to the bare moved clause.
func integrationMovedRef(oldSHA, newSHA string) string {
	old, newv := shortSHA(oldSHA), shortSHA(newSHA)
	if old != "" && newv != "" {
		return fmt.Sprintf("moved from %s to %s", old, newv)
	}
	return "moved"
}

func fileWord(count int) string {
	if count == 1 {
		return "file"
	}
	return "files"
}

func changeWord(count int) string {
	if count == 1 {
		return "change"
	}
	return "changes"
}

// integrationMergeConflictSummary names the repository and conflict-file
// count of the failed merge candidate.
func integrationMergeConflictSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		name := strings.TrimSpace(repo.Name)
		if name == "" {
			return ""
		}
		count := len(repo.ConflictFiles)
		if count > 0 {
			return fmt.Sprintf("The merge candidate for repository %q conflicted on %d %s.", name, count, fileWord(count))
		}
		return fmt.Sprintf("The merge candidate for repository %q conflicted.", name)
	}, "The integration merge conflicted in repositories:")
}

// integrationParentDirtySummary names the repository and dirty-file count of
// the parent worktree that blocked preparation.
func integrationParentDirtySummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		name := strings.TrimSpace(repo.Name)
		if name == "" {
			return ""
		}
		count := len(repo.DirtyFiles)
		if count > 0 {
			return fmt.Sprintf("The parent worktree for repository %q has %d uncommitted %s.", name, count, changeWord(count))
		}
		return fmt.Sprintf("The parent worktree for repository %q has uncommitted changes.", name)
	}, "Parent worktrees have uncommitted changes in repositories:")
}

// integrationParentRefDriftSummary names the repository whose parent branch
// moved since the pass started, with the anchor and observed tips when both
// are known.
func integrationParentRefDriftSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, fmt.Sprintf(
			"The parent branch for repository %%q %s since the pass started.",
			integrationMovedRef(repo.ParentAnchorSHA, repo.ObservedSHA),
		))
	}, "Parent branches moved since the pass started in repositories:")
}

// integrationRefRaceSummary names the repository whose parent ref moved
// while the transaction was applying, with the expected and observed tips
// when both are known.
func integrationRefRaceSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, fmt.Sprintf(
			"The parent ref for repository %%q %s while the pass was integrating.",
			integrationMovedRef(repo.ExpectedRefSHA, repo.ObservedSHA),
		))
	}, "Parent refs moved while the pass was integrating in repositories:")
}

// integrationParentBranchMismatchSummary names the repository that is not on
// the parent branch the pass expects.
func integrationParentBranchMismatchSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "Repository %q is not on the parent branch this pass expects.")
	}, "Repositories are not on the parent branch this pass expects:")
}

// integrationRepositoryMissingSummary names the repository missing from the
// workspace.
func integrationRepositoryMissingSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "Repository %q is missing from the workspace.")
	}, "Repositories are missing from the workspace:")
}

// integrationWorktreeSyncFailedSummary names the repository whose worktree
// could not be synced after the pass applied.
func integrationWorktreeSyncFailedSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "The worktree for repository %q could not be synced after the pass applied.")
	}, "Worktrees could not be synced after the pass applied in repositories:")
}

// integrationRolledBackSummary names the repository whose failed apply forced
// the rollback.
func integrationRolledBackSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "The pass was rolled back in repository %q after its apply failed.")
	}, "The pass was rolled back after its apply failed in repositories:")
}

// integrationCandidateFailedSummary names the repository whose merge-candidate
// preparation failed.
func integrationCandidateFailedSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "Preparing the merge candidate for repository %q failed.")
	}, "Preparing merge candidates failed in repositories:")
}

// rebaseGateTargetMissingSummary names the repository whose creation-time
// rebase target is missing.
func rebaseGateTargetMissingSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "The creation-time rebase target is missing for repository %q.")
	}, "Creation-time rebase targets are missing for repositories:")
}

// rebaseGateNotAncestorSummary names the repository whose pass branch is no
// longer an ancestor of its rebase target.
func rebaseGateNotAncestorSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "The pass branch for repository %q is no longer an ancestor of its rebase target.")
	}, "Pass branches are no longer ancestors of their rebase targets in repositories:")
}

// rebaseGateMergeInProgressSummary names the repository with a merge in
// progress.
func rebaseGateMergeInProgressSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "Repository %q has a merge in progress.")
	}, "Repositories have a merge in progress:")
}

// rebaseGateConflictMarkersSummary names the repository carrying unresolved
// conflict markers, with the affected files when known.
func rebaseGateConflictMarkersSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		name := strings.TrimSpace(repo.Name)
		if name == "" {
			return ""
		}
		count := len(repo.ConflictFiles)
		if count > 0 {
			return fmt.Sprintf("Repository %q carries unresolved conflict markers on %d %s.", name, count, fileWord(count))
		}
		return fmt.Sprintf("Repository %q carries unresolved conflict markers.", name)
	}, "Repositories carry unresolved conflict markers:")
}

// rebaseGatePassthroughModifiedSummary names the pass-through repository with
// local modifications.
func rebaseGatePassthroughModifiedSummary(p Params) string {
	repos, ok := integrationParams(p)
	if !ok {
		return ""
	}
	return integrationOneOrMany(repos, func(repo CodeRepository) string {
		return integrationNamedRepo(repo, "The pass-through worktree for repository %q has local modifications.")
	}, "Pass-through worktrees have local modifications in repositories:")
}
