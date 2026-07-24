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

package ports

// Publisher abstracts git push and PR creation.
type Publisher interface {
	Push(worktreePath, branch string) error
	CreatePR(repoPath, branch, title, body, baseBranch string, draft bool) (prURL string, err error)
	ClosePR(prURL string) error
	HasUncommittedChanges(worktreePath string) (bool, error)
	HasLocalCommits(worktreePath string) (bool, error)
	CommitAll(worktreePath, message string) error
	CommitAllAndGetHead(worktreePath, message string) (string, error)
	DiffSummary(worktreePath, baseBranch string) (string, error)
	CommitLog(worktreePath, baseBranch string) (string, error)
	CommitBodies(worktreePath, baseBranch string) (string, error)
	DiffStat(worktreePath, baseBranch string) (string, error)
}

// DiffOperator abstracts branch-vs-base diff inspection.
type DiffOperator interface {
	BranchDiffPreviews(worktreePath, baseBranch string) ([]DiffPreview, error)
	SingleFileDiffPreview(worktreePath, baseBranch, relPath string) (*DiffPreview, error)
}

// RebaseOperator abstracts git rebase and sync operations.
type RebaseOperator interface {
	PullRebase(worktreePath, branch string) PullRebaseResult
	Fetch(worktreePath string) error
	RebaseOnto(worktreePath, target string) RebaseResult
	Rebase(worktreePath, baseBranch string) error
	RebaseLocal(worktreePath, baseBranch string) error
	ForcePush(worktreePath, branch string) error
	IsBehindRemote(worktreePath, baseBranch string) (bool, error)
	IsBehindLocal(worktreePath, baseBranch string) (bool, error)
	MergeFeatureBranch(repoPath, featureBranch, baseBranch string) error
	PRBaseBranch(repoPath, prURL string) (string, error)
	// RebaseInProgress reports whether the worktree has an unfinished rebase
	// (rebase-merge or rebase-apply directory in the git dir).
	RebaseInProgress(worktreePath string) (bool, error)
}

// CrossRefOperator abstracts cross-repo PR body management.
type CrossRefOperator interface {
	UpdatePRBody(prURL, newBody string) error
	GetPRBody(prURL string) (string, error)
	RetroactivelyUpdateCrossRefs(featureName string,
		entries []CrossRefEntry, currentRepoName string) []error
	// BuildCrossReferenceSection and InjectCrossReferenceSection are pure
	// string helpers that domain code uses to assemble PR bodies. Exposing
	// them via the port keeps consumers free of the git package.
	BuildCrossReferenceSection(featureName string, entries []CrossRefEntry) string
	InjectCrossReferenceSection(body, section string) string
}

// ReviewCommentOperator abstracts PR comment interaction.
type ReviewCommentOperator interface {
	FetchPRComments(repoPath, prURL string) ([]ReviewComment, error)
	ReplyToPRComment(repoPath, prURL string, commentID int, body string) error
	ReplyToIssueComment(repoPath, prURL, body string) error
	FetchReviewThreadMap(repoPath, prURL string) (map[int]string, error)
	ResolveReviewThread(repoPath, threadNodeID string) error
	LatestCommitSHA(worktreePath string) (string, error)
}

// WorktreeOperator abstracts git worktree lifecycle.
type WorktreeOperator interface {
	Create(repoPath, featureSlug, repoName, startPoint string) (worktreePath string, err error)
	Remove(worktreePath string, deleteBranch bool) error
	List() ([]WorktreeInfo, error)
	DetectStale(activeFeatureIDs []string) ([]WorktreeInfo, error)
	ResetToBase(worktreePath, baseBranch string) error
	ResetToBaseLocal(worktreePath, baseBranch string) error
	ResetToCommit(worktreePath, commitSHA string) error
	HasUncommittedChanges(repoPath string) (bool, error)
}

// BranchOperator abstracts branch name and existence checks.
type BranchOperator interface {
	BranchName(featureSlug string) string
	BranchExistsOnRemote(repoPath, branch string) (bool, error)
	HasOriginRemote(repoPath string) (bool, error)
	DefaultBranch(repoPath string) (string, error)
	CurrentBranch(repoPath string) (string, error)
	CreateBackupBranch(worktreePath, slug string) (string, error)
}
