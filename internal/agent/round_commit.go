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
	"fmt"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RoundCommitKind classifies a completed loop round for commit messaging.
type RoundCommitKind string

const (
	// RoundCommitImplement is a round that started without pending reviewer
	// feedback (initial implementation work or a fresh continuation).
	RoundCommitImplement RoundCommitKind = "implement"
	// RoundCommitFix is a phase-implement round that started with pending
	// reviewer feedback from a CHANGES_REQUESTED review cycle.
	RoundCommitFix RoundCommitKind = "fix"
	// RoundCommitFinalReviewFix is a feature-level Final Review fix round.
	RoundCommitFinalReviewFix RoundCommitKind = "final_review_fix"
)

// RoundCommitInput is the per-round completion event the phase-implement and
// final-review loops emit right after a round's agent session ends (before
// the review gate runs). The orchestrator layer owns the actual git commit;
// the loop only reports the round identity and the worktrees the round could
// have dirtied.
type RoundCommitInput struct {
	FeatureID string

	// PhaseNumber is the roadmap phase the round ran in (0 for non-roadmap
	// features); TotalPhases and PhaseType carry the rest of the phase
	// identity used by the commit message prefix.
	PhaseNumber int
	TotalPhases int
	PhaseType   string

	// Iteration is the loop iteration the round ran in.
	Iteration int

	// Kind classifies the round (implement / fix / final-review fix).
	Kind RoundCommitKind

	// FixNumber is the per-phase (or feature-level for Final Review) fix
	// counter when Kind is a fix kind; 0 otherwise.
	FixNumber int

	// FirstImplementCommit marks the phase's first implementation commit.
	// The hook renders it unlabeled so the happy-path log reads like the
	// historical single-commit-per-phase format.
	FirstImplementCommit bool

	// Repos maps repo name -> worktree path for every repo the round could
	// have dirtied. The hook commits only those with uncommitted changes.
	Repos map[string]string
}

// RoundCommitHook commits a completed round's work. It is invoked once per
// implementation/fix round; a clean worktree is a no-op inside the hook.
// A returned error fails the loop loudly — a round whose changes cannot be
// committed must not silently strand a dirty worktree mid-phase.
type RoundCommitHook func(RoundCommitInput) error

// FinalReviewRootOrchestrationArtifacts lists repo-root files that review
// sessions have historically stranded as untracked files. The publish path
// scrubs them; the Final Review approval dirty-check ignores them so a known
// orchestration artifact never fails an otherwise clean approval.
var FinalReviewRootOrchestrationArtifacts = []string{
	PhaseCompleteFile,
	"progress.md",
	"verification-report.yaml",
	"review-feedback.md",
	"meta.yaml",
}

// scanIterationMetas walks the persisted iteration metas 1..startIter in
// order, skipping missing or unreadable entries. Both round-commit trackers
// rebuild their accounting through it so a crash-resumed loop labels rounds
// identically to the interrupted run.
func scanIterationMetas(am *ArtifactManager, artifactDir string, startIter int, visit func(IterationMeta)) {
	for j := 1; j <= startIter; j++ {
		meta, err := am.ReadMeta(filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", j)))
		if err != nil {
			continue
		}
		visit(meta)
	}
}

// implementRoundCommitTracker carries the per-phase round-commit accounting
// for the implementation loop. Both counters are recovered from durable
// iteration metas so a crash-resumed loop labels rounds identically to the
// interrupted run.
type implementRoundCommitTracker struct {
	// changesRequested counts CHANGES_REQUESTED review cycles completed so
	// far this phase. The fix round following the Nth such review is
	// labeled "fix round N".
	changesRequested int
	// semanticSessions records whether any prior round ended its session
	// with a semantic outcome (SUCCESS/RETRY). The first such round of the
	// phase is the unlabeled implementation commit.
	semanticSessions bool
}

// newImplementRoundCommitTracker scans the phase's persisted iteration metas
// (1..startIter) to rebuild the round-commit accounting after a restart.
func newImplementRoundCommitTracker(am *ArtifactManager, artifactDir string, startIter int) *implementRoundCommitTracker {
	t := &implementRoundCommitTracker{}
	scanIterationMetas(am, artifactDir, startIter, func(meta IterationMeta) {
		if meta.ReviewStatus == agentStatusChangesRequested {
			t.changesRequested++
		}
		if meta.AgentStatus == agentStatusSuccess || meta.AgentStatus == "RETRY" {
			t.semanticSessions = true
		}
	})
	return t
}

// repoEditPath returns the path a feature repo's edits land in: the worktree
// when set, falling back to the checkout path — the same resolution
// BuildWorkspace uses to mount repos into agent sessions.
func repoEditPath(r feature.FeatureRepo) string {
	if r.WorktreePath != "" {
		return r.WorktreePath
	}
	return r.Path
}

// implementRoundCommitRepos resolves the repo name -> worktree path map a
// round commit should consider, mirroring the session's actual edit surface
// (repoEditPath per repo). The repo-less fallback is the session WorkDir.
func implementRoundCommitRepos(cfg ImplementConfig) map[string]string {
	if len(cfg.RoundCommitRepos) > 0 {
		return cfg.RoundCommitRepos
	}
	if cfg.Feature != nil && len(cfg.Feature.Repos) > 0 {
		repos := make(map[string]string, len(cfg.Feature.Repos))
		for _, r := range cfg.Feature.Repos {
			if path := repoEditPath(r); path != "" {
				repos[r.Name] = path
			}
		}
		if len(repos) > 0 {
			return repos
		}
	}
	if cfg.WorkDir == "" {
		return nil
	}
	return map[string]string{"": cfg.WorkDir}
}

// frMetaChangesRequested is the review status the Final Review loop writes
// into iteration meta.yaml for a changes-requested iteration (lowercase,
// unlike the implement loop's agentStatusChangesRequested).
const frMetaChangesRequested = "changes_requested"

// frRoundCommitTracker carries the feature-level fix-round counter for the
// Final Review loop, seeded from persisted iteration metas so a crash resume
// keeps numbering stable.
type frRoundCommitTracker struct {
	changesRequested int
}

func newFRRoundCommitTracker(am *ArtifactManager, artifactDir string, startIter int) *frRoundCommitTracker {
	t := &frRoundCommitTracker{}
	scanIterationMetas(am, artifactDir, startIter, func(meta IterationMeta) {
		if meta.ReviewStatus == frMetaChangesRequested {
			t.changesRequested++
		}
	})
	return t
}
