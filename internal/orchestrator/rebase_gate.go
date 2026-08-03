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

package orchestrator

import (
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// This file implements the rebase mechanical integration gate. It is the
// kind-specific pre-prepare step that hardens the rebase child landing path so
// a mis-judged agent review can never corrupt the parent branch: before any
// merge candidate is prepared or parent ref is touched, the gate
// re-verifies the git-level exit criteria for every behind repo of a rebase
// child against the targets persisted at creation.
//
// The gate reads persisted creation-time targets only — it never re-resolves
// refs or fetches — so a target branch that moves after creation does not
// change what the gate checks. Any violation aborts integration with a typed,
// durable attention record and leaves every parent ref byte-identical.

// rebaseIntegrationGate verifies the persisted creation-time targets against
// the child worktrees. It returns a non-nil TransactionJournal parked in the
// attention phase when any behind repo violates the gate, carrying per-repo
// diagnostics with stable GateCode values. It returns nil when every behind
// repo satisfies the gate, allowing candidate preparation to proceed.
//
// The gate checks, for each persisted behind repo:
//   - the persisted creation-time target SHA is an ancestor of the child branch
//     head (the child merged the creation-time target);
//   - no merge is in progress in the child worktree;
//   - no tracked file carries literal conflict markers.
//
// A persisted behind-repo target missing its creation-time SHA fails closed
// with a distinct diagnostic. Non-rebase child kinds are never gated.
func (o *Orchestrator) rebaseIntegrationGate(child *feature.Feature) *feature.TransactionJournal {
	if child == nil || child.Parent == nil || child.Parent.Kind != feature.ChildKindRebase {
		return nil
	}

	var entries []feature.RepoTransactionEntry
	var summary []string
	for _, repoName := range child.Parent.RebaseBehind {
		entry, violation := o.evalRebaseGateRepo(child, repoName)
		if violation {
			entries = append(entries, entry)
			summary = append(summary, fmt.Sprintf("%s: %s", repoName, entry.Diagnostics))
		}
	}

	if len(entries) == 0 {
		return nil
	}

	attention := "rebase integration gate failed: " + strings.Join(summary, "; ")
	return &feature.TransactionJournal{
		Phase:     feature.TransactionPhaseAttention,
		Entries:   entries,
		Attention: attention,
	}
}

// evalRebaseGateRepo evaluates the gate for a single behind repo. It returns
// the journal entry recording any violation and a bool reporting whether the
// repo violated the gate. The entry's GateCode and Diagnostics classify the
// failure; ConflictFiles is populated when literal markers are found.
func (o *Orchestrator) evalRebaseGateRepo(child *feature.Feature, repoName string) (feature.RepoTransactionEntry, bool) {
	target, ok := child.RebaseTargetForRepo(repoName)
	if !ok || target.TargetSHA == "" {
		entry := feature.RepoTransactionEntry{
			Repo:     repoName,
			PrepState: feature.RepoPrepFailed,
			GateCode: feature.GateCodeMissingTargetSHA,
			Diagnostics: "rebase gate: creation-time target SHA is missing for repo " +
				repoName + "; the child must be discarded and relaunched",
		}
		return entry, true
	}

	childRepo := featureRepoByName(child, repoName)
	if childRepo == nil {
		entry := feature.RepoTransactionEntry{
			Repo:        repoName,
			PrepState:   feature.RepoPrepFailed,
			GateCode:    feature.GateCodeMissingTargetSHA,
			Diagnostics: "rebase gate: child no longer has behind repo " + repoName,
		}
		return entry, true
	}

	childWorktree := childRepo.WorktreePath
	if childWorktree == "" {
		childWorktree = childRepo.Path
	}

	// 1. Ancestor check: the persisted creation-time target commit must be an
	// ancestor of the child branch head. The descendant is HEAD so uncommitted
	// child changes (committed later by candidate preparation) do not affect
	// the check.
	if !git.IsAncestor(childWorktree, target.TargetSHA, "HEAD") {
		entry := feature.RepoTransactionEntry{
			Repo:        repoName,
			PrepState:   feature.RepoPrepFailed,
			GateCode:    feature.GateCodeNotAncestor,
			Diagnostics: fmt.Sprintf("rebase gate: creation-time target %s is not an ancestor of the child branch head (target SHA %s)", target.Target, target.TargetSHA),
		}
		return entry, true
	}

	// 2. No merge in progress in the child worktree.
	if git.MergeInProgress(childWorktree) {
		entry := feature.RepoTransactionEntry{
			Repo:        repoName,
			PrepState:   feature.RepoPrepFailed,
			GateCode:    feature.GateCodeMergeInProgress,
			Diagnostics: "rebase gate: a merge is in progress in the child worktree (MERGE_HEAD present)",
		}
		return entry, true
	}

	// 3. No literal conflict markers in tracked files. Fail closed on a scan
	// error: the gate cannot prove the worktree is marker-free.
	files, err := git.ConflictMarkerFiles(childWorktree)
	if err != nil {
		entry := feature.RepoTransactionEntry{
			Repo:        repoName,
			PrepState:   feature.RepoPrepFailed,
			GateCode:    feature.GateCodeConflictMarkers,
			Diagnostics: fmt.Sprintf("rebase gate: conflict marker scan failed (failing closed): %v", err),
		}
		return entry, true
	}
	if len(files) > 0 {
		entry := feature.RepoTransactionEntry{
			Repo:         repoName,
			PrepState:    feature.RepoPrepFailed,
			GateCode:     feature.GateCodeConflictMarkers,
			ConflictFiles: append([]string(nil), files...),
			Diagnostics:  fmt.Sprintf("rebase gate: conflict markers remain in tracked files: %v", files),
		}
		return entry, true
	}

	return feature.RepoTransactionEntry{Repo: repoName}, false
}
