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

func (a *PublishAdapter) CreatePR(repoPath, branch, title, body, baseBranch string) (string, error) {
	return CreatePR(repoPath, branch, title, body, baseBranch)
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

// DiffAdapter wraps git package-level diff functions behind ports.DiffOperator.
type DiffAdapter struct{}

func (a *DiffAdapter) WorkingTreeDiffPreviews(worktreePath string) ([]DiffPreview, error) {
	return WorkingTreeDiffPreviews(worktreePath)
}

func (a *DiffAdapter) SingleFileDiffPreview(worktreePath, relPath string) (*DiffPreview, error) {
	return SingleFileDiffPreview(worktreePath, relPath)
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

// CrossRefAdapter wraps git package-level cross-reference functions behind ports.CrossRefOperator.
type CrossRefAdapter struct{}

func (a *CrossRefAdapter) UpdatePRBody(prURL, newBody string) error {
	return UpdatePRBody(prURL, newBody)
}

func (a *CrossRefAdapter) GetPRBody(prURL string) (string, error) {
	return GetPRBody(prURL)
}

func (a *CrossRefAdapter) RetroactivelyUpdateCrossRefs(featureName string,
	entries []CrossRefEntry, currentRepoName string) []error {
	return RetroactivelyUpdateCrossRefs(featureName, entries, currentRepoName)
}

// BuildCrossReferenceSection delegates to the package helper so domain code
// can compose PR bodies without importing internal/git.
func (a *CrossRefAdapter) BuildCrossReferenceSection(featureName string, entries []CrossRefEntry) string {
	return BuildCrossReferenceSection(featureName, entries)
}

// InjectCrossReferenceSection delegates to the package helper.
func (a *CrossRefAdapter) InjectCrossReferenceSection(body, section string) string {
	return InjectCrossReferenceSection(body, section)
}

// ReviewCommentAdapter wraps git package-level review functions behind ports.ReviewCommentOperator.
type ReviewCommentAdapter struct{}

func (a *ReviewCommentAdapter) FetchPRComments(repoPath, prURL string) ([]ReviewComment, error) {
	return FetchPRComments(repoPath, prURL)
}

func (a *ReviewCommentAdapter) ReplyToPRComment(repoPath, prURL string, commentID int, body string) error {
	return ReplyToPRComment(repoPath, prURL, commentID, body)
}

func (a *ReviewCommentAdapter) ReplyToIssueComment(repoPath, prURL string, body string) error {
	return ReplyToIssueComment(repoPath, prURL, body)
}

func (a *ReviewCommentAdapter) FetchReviewThreadMap(repoPath, prURL string) (map[int]string, error) {
	return FetchReviewThreadMap(repoPath, prURL)
}

func (a *ReviewCommentAdapter) ResolveReviewThread(repoPath, threadNodeID string) error {
	return ResolveReviewThread(repoPath, threadNodeID)
}

func (a *ReviewCommentAdapter) LatestCommitSHA(worktreePath string) (string, error) {
	return LatestCommitSHA(worktreePath)
}

// BranchAdapter wraps git package-level branch functions behind ports.BranchOperator.
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
