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

// Package orchestrator — preflight.go owns side-effect-free, server-authored
// previews for repository-scoped cycles (rebase). The desktop never
// reconstructs lifecycle or Git rules: it renders the preview, and execution
// rejects a stale source_revision before any mutation.
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// RebasePreflightRepoResult is the per-repository slice of a rebase preflight.
type RebasePreflightRepoResult struct {
	Repo          string
	Target        string
	Publishable   bool
	Behind        bool
	Freshness     string
	Blocker       string
	ConflictFiles []string
}

// RebasePreflightResult is the feature-wide, side-effect-free rebase preview.
// SourceRevision captures the worktree state of every affected repository; a
// rebase execution carrying a mismatched SourceRevision is rejected as stale.
type RebasePreflightResult struct {
	FeatureID      string
	SourceRevision string
	Repos          []RebasePreflightRepoResult
}

const (
	preflightFreshnessBehind     = "behind"
	preflightFreshnessUpToDate   = "up_to_date"
	preflightFreshnessUnknown    = "unknown"
	preflightBlockerNoTarget     = "rebase target not found"
	preflightBlockerNoWorktree   = "worktree not available"
	preflightBlockerRebaseInProg = "rebase already in progress"
)

// RebasePreflight computes a side-effect-free preview of a feature-wide
// rebase. It resolves each repository's target, checks behind state against
// the local remote-tracking ref (no fetch, no rebase), surfaces known
// blockers, and returns a source revision that execution checks for staleness.
// The worktree is never mutated.
func (o *Orchestrator) RebasePreflight(featureID string) (RebasePreflightResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return RebasePreflightResult{}, fmt.Errorf("load feature: %w", err)
	}
	result := RebasePreflightResult{FeatureID: featureID}
	for _, repo := range f.Repos {
		out := o.harnessRebaseOutcomeForRepo(f, repo)
		freshness, blocker, behind := o.repoFreshnessAndBlocker(out)
		repoResult := RebasePreflightRepoResult{
			Repo:        out.RepoName,
			Target:      out.RebaseTarget,
			Publishable: out.Publishable,
			Behind:      behind,
			Freshness:   freshness,
			Blocker:     blocker,
		}
		result.Repos = append(result.Repos, repoResult)
	}
	result.SourceRevision = preflightRevision(o.collectPreflightFingerprints(f))
	return result, nil
}

// repoFreshnessAndBlocker is the single source of truth for the
// side-effect-free freshness/blocker decision shared by RebasePreflight and
// CompletionPreflight. It inspects only local remote-tracking refs and never
// mutates a worktree. It returns the freshness label, a non-empty blocker
// when the repo cannot be safely operated on, and the behind flag.
func (o *Orchestrator) repoFreshnessAndBlocker(out HarnessRebaseRepoOutcome) (freshness, blocker string, behind bool) {
	switch {
	case out.WorktreePath == "":
		return preflightFreshnessUnknown, preflightBlockerNoWorktree, false
	case out.RebaseTarget == "":
		return preflightFreshnessUnknown, preflightBlockerNoTarget, false
	default:
		if git.RebaseInProgress(out.WorktreePath) {
			return preflightFreshnessUnknown, preflightBlockerRebaseInProg, false
		}
		if out.Publishable {
			b := git.IsBehindRemote(out.WorktreePath, out.RebaseTarget)
			if b {
				return preflightFreshnessBehind, "", true
			}
			return preflightFreshnessUpToDate, "", false
		}
		b := git.IsBehindLocal(out.WorktreePath, out.RebaseTarget)
		if b {
			return preflightFreshnessBehind, "", true
		}
		return preflightFreshnessUpToDate, "", false
	}
}

// RebasePreflightSourceRevision recomputes the current rebase preflight source
// revision for the stale-preflight guard at execution time. A non-empty
// expected value that differs from the current revision means repository state
// advanced since the preview and the mutation must be rejected.
func (o *Orchestrator) RebasePreflightSourceRevision(featureID string) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}
	return preflightRevision(o.collectPreflightFingerprints(f)), nil
}

// collectPreflightFingerprints folds every repository's worktree fingerprint
// into the stable list the stale-preflight guard hashes. repo.Name is the
// single name source for both the preview and the execution-time guard, so
// the two paths cannot drift.
func (o *Orchestrator) collectPreflightFingerprints(f *feature.Feature) []string {
	var fingerprints []string
	for _, repo := range f.Repos {
		if fp := o.rebaseWorktreeFingerprint(repo); fp != "" {
			fingerprints = append(fingerprints, repo.Name+"\n"+fp)
		}
	}
	return fingerprints
}

// preflightRevision folds the per-repository worktree fingerprints into one
// stable revision string. The exact value is opaque; only equality matters for
// the stale guard.
func preflightRevision(fingerprints []string) string {
	if len(fingerprints) == 0 {
		return ""
	}
	return revisionHash(fingerprints)
}

// revisionHash returns a short, stable hex digest of the joined inputs.
func revisionHash(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ErrStalePreflight is returned by cycle start paths when the carried source
// revision no longer matches the authoritative repository state.
var ErrStalePreflight = errors.New("preflight is stale: repository state changed since the preview, refresh and try again")
