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
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// fingerprint computes SHA256 of a file's contents.
func fingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading file for fingerprint: %w", err)
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h), nil
}

// progressTracker tracks consecutive no-progress iterations.
type progressTracker struct {
	noProgressCount int
	hasOutcome      bool
	bestBlockers    int
	// Verified stall-evidence state: the last worktree fingerprint seen by
	// ObserveVerifiedOutcomeWithWorktree.
	lastVerifiedWorktree string
	// RETRY stall-evidence state: the last handoff-narrative and worktree
	// fingerprints seen by ObserveRetryOutcome.
	hasRetryObservation bool
	lastRetryNarrative  string
	lastRetryWorktree   string
}

func newProgressTracker() *progressTracker {
	return &progressTracker{}
}

// consecutiveNoProgress returns the number of consecutive no-progress iterations.
func (pt *progressTracker) consecutiveNoProgress() int {
	return pt.noProgressCount
}

// observeVerifiedOutcomeWithWorktree records a deterministic verifier/reviewer
// outcome together with the repository state that produced it. Fewer blockers
// or a changed worktree is progress; an empty fingerprint cannot disarm the
// safety rail.
func (pt *progressTracker) observeVerifiedOutcomeWithWorktree(blockers int, worktreeFP string) bool {
	if blockers < 0 {
		blockers = 0
	}
	worktreeChanged := worktreeFP != "" &&
		pt.lastVerifiedWorktree != "" &&
		worktreeFP != pt.lastVerifiedWorktree
	if worktreeFP != "" {
		pt.lastVerifiedWorktree = worktreeFP
	}
	if !pt.hasOutcome {
		pt.hasOutcome = true
		pt.bestBlockers = blockers
		return true
	}
	if blockers < pt.bestBlockers || worktreeChanged {
		if blockers < pt.bestBlockers {
			pt.bestBlockers = blockers
		}
		pt.noProgressCount = 0
		return true
	}
	pt.noProgressCount++
	return false
}

// observeUnverifiedOutcome records an iteration that did not produce a
// verifier/reviewer outcome, such as a protocol violation.
func (pt *progressTracker) observeUnverifiedOutcome() bool {
	pt.noProgressCount++
	return false
}

// observeRetryOutcome records a self-declared partial (RETRY) iteration.
// A RETRY produces no review verdict, but absence of a verdict is not
// evidence of a stall: a short-turn model legitimately splits one task
// across many partial iterations. The iteration counts as no-progress only
// when neither the handoff narrative nor the worktree state changed since
// the previous RETRY observation. An empty fingerprint (unreadable
// progress.md, git failure) is treated as unchanged so a broken signal
// cannot disarm the rail. A progressing RETRY does not reset the counter
// accumulated by non-improving verified outcomes — it only declines to add
// to it — so review churn without fewer blockers still trips the rail.
func (pt *progressTracker) observeRetryOutcome(narrativeFP, worktreeFP string) bool {
	first := !pt.hasRetryObservation
	narrativeChanged := narrativeFP != "" && narrativeFP != pt.lastRetryNarrative
	worktreeChanged := worktreeFP != "" && worktreeFP != pt.lastRetryWorktree
	pt.hasRetryObservation = true
	pt.lastRetryNarrative = narrativeFP
	pt.lastRetryWorktree = worktreeFP
	if first || narrativeChanged || worktreeChanged {
		return true
	}
	pt.noProgressCount++
	return false
}

// worktreeStateFingerprint hashes each repo worktree's HEAD, porcelain
// status, tracked diff, and untracked-file stats (path, size, mtime — new
// files stay untracked for the whole phase, so their edits never appear in
// `git diff HEAD`). Returns "" when no signal could be gathered so callers
// treat it as unchanged. A nil runner falls back to direct execution.
func worktreeStateFingerprint(ctx context.Context, runner ports.CommandRunner, paths []string) string {
	h := sha256.New()
	gotSignal := false
	for _, p := range paths {
		for _, args := range [][]string{
			{"rev-parse", "HEAD"},
			{"status", "--porcelain", "--untracked-files=all"},
			{"diff", "HEAD"},
		} {
			out, err := gitOutput(ctx, runner, p, args...)
			if err != nil {
				continue
			}
			gotSignal = true
			h.Write(out)
		}
		out, err := gitOutput(ctx, runner, p, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			continue
		}
		gotSignal = true
		for _, rel := range strings.Split(string(out), "\x00") {
			if rel == "" {
				continue
			}
			if fi, err := os.Stat(filepath.Join(p, rel)); err == nil {
				fmt.Fprintf(h, "%s|%d|%d\n", rel, fi.Size(), fi.ModTime().UnixNano())
			}
		}
	}
	if !gotSignal {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

var blockingReviewFindingRE = regexp.MustCompile(`(?im)^\s*[-*]\s+\*\*\[?(critical|high)(?:\]|\b)`)

func countBlockingReviewFindings(feedback string) int {
	return len(blockingReviewFindingRE.FindAllString(feedback, -1))
}
