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
// preflight helpers. The desktop never reconstructs lifecycle or Git rules:
// it renders the preview, and execution rejects a stale source_revision
// before any mutation.
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

const (
	preflightFreshnessBehind     = "behind"
	preflightFreshnessUpToDate   = "up_to_date"
	preflightFreshnessUnknown    = "unknown"
	preflightBlockerNoTarget     = "rebase target not found"
	preflightBlockerNoWorktree   = "worktree not available"
	preflightBlockerRebaseInProg = "rebase already in progress"
)

// repoFreshnessAndBlocker is the single source of truth for the
// side-effect-free freshness/blocker decision shared by CompletionPreflight.
// It inspects only local remote-tracking refs and never mutates a worktree. It
// returns the freshness label, a non-empty blocker when the repo cannot be
// safely operated on, and the behind flag.
func (o *Orchestrator) repoFreshnessAndBlocker(out RebaseRepoFreshnessInput) (freshness, blocker string, behind bool) {
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
