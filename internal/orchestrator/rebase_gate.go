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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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

// rebaseGateFinding is one behind repo's gate violation: the progress-state
// journal entry, the catalog code classifying the violation, the literal
// marker files when known, and the raw one-line diagnostics.
type rebaseGateFinding struct {
	entry         feature.RepoTransactionEntry
	code          errcat.Code
	conflictFiles []string
	diagnostics   string
}

// rebaseIntegrationGate verifies the persisted creation-time targets against
// the child worktrees. It returns a non-nil TransactionJournal parked in the
// attention phase when any behind repo violates the gate, carrying the
// per-repo entries plus one stored attention record built from every
// violation. It returns nil when every behind repo satisfies the gate,
// allowing candidate preparation to proceed.
//
// The gate checks, for each persisted behind repo:
//   - the persisted creation-time target SHA is an ancestor of the child branch
//     head (the child contains the creation-time target);
//   - no tracked file carries literal conflict markers.
//
// A persisted behind-repo target missing its creation-time SHA fails closed
// with a distinct code. Non-rebase child kinds are never gated.
func (o *Orchestrator) rebaseIntegrationGate(child *feature.Feature) *feature.TransactionJournal {
	if child == nil || child.Parent == nil || child.Parent.Kind != feature.ChildKindRebase {
		return nil
	}

	var findings []rebaseGateFinding
	for _, repoName := range child.Parent.RebaseWorkRepos {
		finding, violation := o.evalRebaseGateRepo(child, repoName)
		if violation {
			findings = append(findings, finding)
		}
	}

	if len(findings) == 0 {
		return nil
	}

	journal := &feature.TransactionJournal{
		Phase: feature.TransactionPhaseAttention,
	}
	integration := make([]integrationFinding, 0, len(findings))
	for _, finding := range findings {
		journal.Entries = append(journal.Entries, finding.entry)
		item := entryFinding(&finding.entry, finding.code, finding.diagnostics)
		item.ctx.ConflictFiles = finding.conflictFiles
		integration = append(integration, item)
	}
	journal.Attention = findingsRecord(integration)
	return journal
}

// rebaseGateAgentRemediation states the agent-oriented fix for a gate
// violation, keyed by the stable catalog code. The catalog's remediation
// hint is user-facing; attention surfaces need the mechanical instruction.
func rebaseGateAgentRemediation(code errcat.Code) string {
	switch code {
	case errcat.RebaseGateTargetMissing:
		return "discard the child and relaunch it; a missing creation-time target SHA cannot be re-derived."
	case errcat.RebaseGateNotAncestor:
		return "reset the child branch onto the persisted creation-time target commit; do not substitute a newer or re-resolved target."
	case errcat.RebaseGateConflictMarkers:
		return "resolve every conflict hunk, remove the literal markers, and commit the resolution."
	default:
		return ""
	}
}

// evalRebaseGateRepo evaluates the gate for a single behind repo. It returns
// the finding recording any violation (progress-state entry, catalog code,
// conflict files, raw diagnostics) and a bool reporting whether the repo
// violated the gate.
func (o *Orchestrator) evalRebaseGateRepo(child *feature.Feature, repoName string) (rebaseGateFinding, bool) {
	target, ok := child.RebaseTargetForRepo(repoName)
	if !ok || target.TargetSHA == "" {
		return rebaseGateFinding{
			entry: feature.RepoTransactionEntry{
				Repo:      repoName,
				PrepState: feature.RepoPrepFailed,
			},
			code: errcat.RebaseGateTargetMissing,
			diagnostics: "rebase gate: creation-time target SHA is missing for repo " +
				repoName + "; the child must be discarded and relaunched",
		}, true
	}

	childRepo := featureRepoByName(child, repoName)
	if childRepo == nil {
		return rebaseGateFinding{
			entry: feature.RepoTransactionEntry{
				Repo:      repoName,
				PrepState: feature.RepoPrepFailed,
			},
			code:        errcat.RebaseGateTargetMissing,
			diagnostics: "rebase gate: child no longer has behind repo " + repoName,
		}, true
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
		return rebaseGateFinding{
			entry: feature.RepoTransactionEntry{
				Repo:      repoName,
				PrepState: feature.RepoPrepFailed,
			},
			code: errcat.RebaseGateNotAncestor,
			diagnostics: fmt.Sprintf("rebase gate: creation-time target %s is not an ancestor of the child branch head (target SHA %s)",
				target.Target, target.TargetSHA),
		}, true
	}

	// 2. No literal conflict markers in tracked files. Fail closed on a scan
	// error: the gate cannot prove the worktree is marker-free.
	files, err := git.ConflictMarkerFiles(childWorktree)
	if err != nil {
		return rebaseGateFinding{
			entry: feature.RepoTransactionEntry{
				Repo:      repoName,
				PrepState: feature.RepoPrepFailed,
			},
			code:        errcat.RebaseGateConflictMarkers,
			diagnostics: fmt.Sprintf("rebase gate: conflict marker scan failed (failing closed): %v", err),
		}, true
	}
	if len(files) > 0 {
		return rebaseGateFinding{
			entry: feature.RepoTransactionEntry{
				Repo:      repoName,
				PrepState: feature.RepoPrepFailed,
			},
			code:          errcat.RebaseGateConflictMarkers,
			conflictFiles: append([]string(nil), files...),
			diagnostics:   fmt.Sprintf("rebase gate: conflict markers remain in tracked files: %v", files),
		}, true
	}

	return rebaseGateFinding{}, false
}
