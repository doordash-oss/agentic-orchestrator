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

// Package ports owns backend-agnostic value types used across domain
// boundaries. These types are defined here — not re-exported from adapter
// packages — so domain consumers (orchestrator, agent, feature) can depend
// only on ports and swap adapters (git, session) without touching ports or
// its consumers.
package ports

// DiffPreview is a compact, file-scoped preview of a working tree change.
type DiffPreview struct {
	Path         string
	OldPath      string
	Operation    string // add, update, delete, rename
	AddedLines   int
	RemovedLines int
	Patch        string
	Fingerprint  string
}

// PullRebaseOutcome categorises the result of a PullRebase operation.
type PullRebaseOutcome int

const (
	// PullRebaseSuccess — rebase succeeded or was a no-op (remote branch
	// absent, already up-to-date).
	PullRebaseSuccess PullRebaseOutcome = iota
	// PullRebaseConflict — rebase encountered merge conflicts. The rebase
	// has been aborted and the worktree is left clean.
	PullRebaseConflict
	// PullRebaseFailure — non-conflict failure (network, auth, fetch, etc.).
	PullRebaseFailure
)

// PullRebaseResult is the outcome + error from a PullRebase operation.
type PullRebaseResult struct {
	Outcome PullRebaseOutcome
	Err     error // nil on Success; non-nil on Conflict/Failure
}

// RebaseOutcome categorises the result of a RebaseOnto operation.
type RebaseOutcome int

const (
	// RebaseSuccess — rebase completed without conflicts.
	RebaseSuccess RebaseOutcome = iota
	// RebaseConflict — conflicts detected; rebase still in progress with
	// conflict markers left in the worktree.
	RebaseConflict
	// RebaseFailed — non-conflict failure. The rebase has been aborted and
	// the worktree is clean.
	RebaseFailed
)

// RebaseResult is the outcome + conflict files + error from a RebaseOnto.
type RebaseResult struct {
	Outcome       RebaseOutcome
	ConflictFiles []string // only populated on RebaseConflict
	Err           error    // nil on Success; non-nil on Conflict/Failed
}

// CrossRefEntry describes one repo's PR status for cross-reference rendering.
type CrossRefEntry struct {
	RepoName string
	Branch   string
	PRURL    string // empty = pending, "(failed)" = failed repo
}

// PR comment type constants.
const (
	CommentTypeReview     = "review"
	CommentTypeIssue      = "issue"
	CommentTypeReviewBody = "review_body"
)

// ReviewComment is a GitHub PR comment (inline review or issue conversation).
type ReviewComment struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	DiffHunk  string `json:"diff_hunk"`
	InReplyTo int    `json:"in_reply_to_id"`
	Type      string `json:"type"`
	RepoName  string `json:"repo_name,omitempty"`
}

// WorktreeInfo describes one discovered worktree.
type WorktreeInfo struct {
	Path      string
	Branch    string
	FeatureID string
	RepoName  string
	RepoPath  string
}
