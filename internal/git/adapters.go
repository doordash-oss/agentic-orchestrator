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

package git

// PublishAdapter wraps git package-level publish functions behind ports.Publisher.
type PublishAdapter struct{}

func (a *PublishAdapter) Push(worktreePath, branch string) error {
	return Push(worktreePath, branch)
}

func (a *PublishAdapter) CreatePR(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
	return CreatePR(repoPath, branch, title, body, draft, baseBranch)
}

func (a *PublishAdapter) ClosePR(prURL string) error {
	return ClosePR(prURL)
}

func (a *PublishAdapter) HasUncommittedChanges(worktreePath string) (bool, error) {
	return HasUncommittedChanges(worktreePath), nil
}

func (a *PublishAdapter) HasLocalCommits(worktreePath string) (bool, error) {
	return HasLocalCommits(worktreePath), nil
}

func (a *PublishAdapter) CommitAll(worktreePath, message string) error {
	return CommitAll(worktreePath, message)
}

func (a *PublishAdapter) CommitAllAndGetHead(worktreePath, message string) (string, error) {
	return CommitAllAndGetHead(worktreePath, message)
}

func (a *PublishAdapter) DiffSummary(worktreePath, baseBranch string) (string, error) {
	return DiffSummary(worktreePath, baseBranch)
}

func (a *PublishAdapter) CommitLog(worktreePath, baseBranch string) (string, error) {
	return CommitLog(worktreePath, baseBranch)
}

func (a *PublishAdapter) CommitBodies(worktreePath, baseBranch string) (string, error) {
	return CommitBodies(worktreePath, baseBranch)
}

func (a *PublishAdapter) DiffStat(worktreePath, baseBranch string) (string, error) {
	return DiffStat(worktreePath, baseBranch)
}

// RebaseAdapter wraps git package-level rebase functions behind ports.RebaseOperator.
type RebaseAdapter struct{}

func (a *RebaseAdapter) PullRebase(worktreePath, branch string) PullRebaseResult {
	return PullRebase(worktreePath, branch)
}

func (a *RebaseAdapter) Fetch(worktreePath string) error {
	return Fetch(worktreePath)
}

func (a *RebaseAdapter) RebaseOnto(worktreePath, target string) RebaseResult {
	return RebaseOnto(worktreePath, target)
}

func (a *RebaseAdapter) Rebase(worktreePath, baseBranch string) error {
	return Rebase(worktreePath, baseBranch)
}

func (a *RebaseAdapter) RebaseLocal(worktreePath, baseBranch string) error {
	return RebaseLocal(worktreePath, baseBranch)
}

func (a *RebaseAdapter) ForcePush(worktreePath, branch string) error {
	return ForcePush(worktreePath, branch)
}

func (a *RebaseAdapter) IsBehindRemote(worktreePath, baseBranch string) (bool, error) {
	return IsBehindRemote(worktreePath, baseBranch), nil
}

func (a *RebaseAdapter) IsBehindLocal(worktreePath, baseBranch string) (bool, error) {
	return IsBehindLocal(worktreePath, baseBranch), nil
}

func (a *RebaseAdapter) MergeFeatureBranch(repoPath, featureBranch, baseBranch string) error {
	return MergeFeatureBranch(repoPath, featureBranch, baseBranch)
}

func (a *RebaseAdapter) PRBaseBranch(repoPath, prURL string) (string, error) {
	return PRBaseBranch(repoPath, prURL), nil
}

func (a *RebaseAdapter) RebaseInProgress(worktreePath string) (bool, error) {
	return RebaseInProgress(worktreePath), nil
}

// BranchAdapter exposes branch operations to feature.BranchOps consumers.
type BranchAdapter struct{}

func (a *BranchAdapter) BranchName(featureSlug string) string {
	return BranchName(featureSlug)
}

func (a *BranchAdapter) BranchExistsOnRemote(repoPath, branch string) (bool, error) {
	return BranchExistsOnRemote(repoPath, branch), nil
}

func (a *BranchAdapter) HasOriginRemote(repoPath string) (bool, error) {
	return HasOriginRemote(repoPath), nil
}

func (a *BranchAdapter) DefaultBranch(repoPath string) (string, error) {
	return DefaultBranch(repoPath), nil
}

func (a *BranchAdapter) CurrentBranch(repoPath string) (string, error) {
	return CurrentBranch(repoPath), nil
}

func (a *BranchAdapter) CreateBackupBranch(worktreePath, slug string) (string, error) {
	return CreateBackupBranch(worktreePath, slug)
}
