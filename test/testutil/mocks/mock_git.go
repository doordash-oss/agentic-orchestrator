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

package mocks

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// ---------------------------------------------------------------------------
// MockPublisher implements ports.Publisher
// ---------------------------------------------------------------------------

// MockPublisher implements ports.Publisher with configurable function overrides
// and call tracking.
type MockPublisher struct {
	PushFn                  func(worktreePath, branch string) error
	CreatePRFn              func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error)
	ClosePRFn               func(prURL string) error
	HasUncommittedChangesFn func(worktreePath string) (bool, error)
	HasLocalCommitsFn       func(worktreePath string) (bool, error)
	CommitAllFn             func(worktreePath, message string) error
	CommitAllAndGetHeadFn   func(worktreePath, message string) (string, error)
	DiffSummaryFn           func(worktreePath, baseBranch string) (string, error)
	CommitLogFn             func(worktreePath, baseBranch string) (string, error)
	CommitBodiesFn          func(worktreePath, baseBranch string) (string, error)
	DiffStatFn              func(worktreePath, baseBranch string) (string, error)

	DefaultError error
	Calls        []MockCall
}

// NewMockPublisher returns a MockPublisher with zero-value defaults.
func NewMockPublisher() *MockPublisher { return &MockPublisher{} }

func (m *MockPublisher) Push(worktreePath, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "Push", Args: []any{worktreePath, branch}})
	if m.PushFn != nil {
		return m.PushFn(worktreePath, branch)
	}
	return m.DefaultError
}

func (m *MockPublisher) CreatePR(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CreatePR", Args: []any{repoPath, branch, title, body, baseBranch, draft}})
	if m.CreatePRFn != nil {
		return m.CreatePRFn(repoPath, branch, title, body, baseBranch, draft)
	}
	return "", m.DefaultError
}

func (m *MockPublisher) ClosePR(prURL string) error {
	m.Calls = append(m.Calls, MockCall{Method: "ClosePR", Args: []any{prURL}})
	if m.ClosePRFn != nil {
		return m.ClosePRFn(prURL)
	}
	return m.DefaultError
}

func (m *MockPublisher) HasUncommittedChanges(worktreePath string) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "HasUncommittedChanges", Args: []any{worktreePath}})
	if m.HasUncommittedChangesFn != nil {
		return m.HasUncommittedChangesFn(worktreePath)
	}
	return false, m.DefaultError
}

func (m *MockPublisher) HasLocalCommits(worktreePath string) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "HasLocalCommits", Args: []any{worktreePath}})
	if m.HasLocalCommitsFn != nil {
		return m.HasLocalCommitsFn(worktreePath)
	}
	return false, m.DefaultError
}

func (m *MockPublisher) CommitAll(worktreePath, message string) error {
	m.Calls = append(m.Calls, MockCall{Method: "CommitAll", Args: []any{worktreePath, message}})
	if m.CommitAllFn != nil {
		return m.CommitAllFn(worktreePath, message)
	}
	return m.DefaultError
}

func (m *MockPublisher) CommitAllAndGetHead(worktreePath, message string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CommitAllAndGetHead", Args: []any{worktreePath, message}})
	if m.CommitAllAndGetHeadFn != nil {
		return m.CommitAllAndGetHeadFn(worktreePath, message)
	}
	if m.CommitAllFn != nil {
		if err := m.CommitAllFn(worktreePath, message); err != nil {
			return "", err
		}
		return "0000000000000000000000000000000000000000", nil
	}
	return "", m.DefaultError
}

func (m *MockPublisher) DiffSummary(worktreePath, baseBranch string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "DiffSummary", Args: []any{worktreePath, baseBranch}})
	if m.DiffSummaryFn != nil {
		return m.DiffSummaryFn(worktreePath, baseBranch)
	}
	return "", m.DefaultError
}

func (m *MockPublisher) CommitLog(worktreePath, baseBranch string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CommitLog", Args: []any{worktreePath, baseBranch}})
	if m.CommitLogFn != nil {
		return m.CommitLogFn(worktreePath, baseBranch)
	}
	return "", m.DefaultError
}

func (m *MockPublisher) CommitBodies(worktreePath, baseBranch string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CommitBodies", Args: []any{worktreePath, baseBranch}})
	if m.CommitBodiesFn != nil {
		return m.CommitBodiesFn(worktreePath, baseBranch)
	}
	return "", m.DefaultError
}

func (m *MockPublisher) DiffStat(worktreePath, baseBranch string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "DiffStat", Args: []any{worktreePath, baseBranch}})
	if m.DiffStatFn != nil {
		return m.DiffStatFn(worktreePath, baseBranch)
	}
	return "", m.DefaultError
}

// ---------------------------------------------------------------------------
// MockRebaseOperator implements ports.RebaseOperator
// ---------------------------------------------------------------------------

// MockRebaseOperator implements ports.RebaseOperator with configurable function
// overrides and call tracking.
type MockRebaseOperator struct {
	PullRebaseFn         func(worktreePath, branch string) git.PullRebaseResult
	FetchFn              func(worktreePath string) error
	RebaseOntoFn         func(worktreePath, target string) git.RebaseResult
	RebaseFn             func(worktreePath, baseBranch string) error
	RebaseLocalFn        func(worktreePath, baseBranch string) error
	ForcePushFn          func(worktreePath, branch string) error
	IsBehindRemoteFn     func(worktreePath, baseBranch string) (bool, error)
	IsBehindLocalFn      func(worktreePath, baseBranch string) (bool, error)
	MergeFeatureBranchFn func(repoPath, featureBranch, baseBranch string) error
	PRBaseBranchFn       func(repoPath, prURL string) (string, error)
	RebaseInProgressFn   func(worktreePath string) (bool, error)

	DefaultError error
	Calls        []MockCall
}

// NewMockRebaseOperator returns a MockRebaseOperator with zero-value defaults.
func NewMockRebaseOperator() *MockRebaseOperator { return &MockRebaseOperator{} }

func (m *MockRebaseOperator) PullRebase(worktreePath, branch string) git.PullRebaseResult {
	m.Calls = append(m.Calls, MockCall{Method: "PullRebase", Args: []any{worktreePath, branch}})
	if m.PullRebaseFn != nil {
		return m.PullRebaseFn(worktreePath, branch)
	}
	return git.PullRebaseResult{}
}

func (m *MockRebaseOperator) Fetch(worktreePath string) error {
	m.Calls = append(m.Calls, MockCall{Method: "Fetch", Args: []any{worktreePath}})
	if m.FetchFn != nil {
		return m.FetchFn(worktreePath)
	}
	return m.DefaultError
}

func (m *MockRebaseOperator) RebaseOnto(worktreePath, target string) git.RebaseResult {
	m.Calls = append(m.Calls, MockCall{Method: "RebaseOnto", Args: []any{worktreePath, target}})
	if m.RebaseOntoFn != nil {
		return m.RebaseOntoFn(worktreePath, target)
	}
	return git.RebaseResult{}
}

func (m *MockRebaseOperator) Rebase(worktreePath, baseBranch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "Rebase", Args: []any{worktreePath, baseBranch}})
	if m.RebaseFn != nil {
		return m.RebaseFn(worktreePath, baseBranch)
	}
	return m.DefaultError
}

func (m *MockRebaseOperator) RebaseLocal(worktreePath, baseBranch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "RebaseLocal", Args: []any{worktreePath, baseBranch}})
	if m.RebaseLocalFn != nil {
		return m.RebaseLocalFn(worktreePath, baseBranch)
	}
	return m.DefaultError
}

func (m *MockRebaseOperator) ForcePush(worktreePath, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "ForcePush", Args: []any{worktreePath, branch}})
	if m.ForcePushFn != nil {
		return m.ForcePushFn(worktreePath, branch)
	}
	return m.DefaultError
}

func (m *MockRebaseOperator) IsBehindRemote(worktreePath, baseBranch string) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "IsBehindRemote", Args: []any{worktreePath, baseBranch}})
	if m.IsBehindRemoteFn != nil {
		return m.IsBehindRemoteFn(worktreePath, baseBranch)
	}
	return false, m.DefaultError
}

func (m *MockRebaseOperator) IsBehindLocal(worktreePath, baseBranch string) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "IsBehindLocal", Args: []any{worktreePath, baseBranch}})
	if m.IsBehindLocalFn != nil {
		return m.IsBehindLocalFn(worktreePath, baseBranch)
	}
	return false, m.DefaultError
}

func (m *MockRebaseOperator) MergeFeatureBranch(repoPath, featureBranch, baseBranch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "MergeFeatureBranch", Args: []any{repoPath, featureBranch, baseBranch}})
	if m.MergeFeatureBranchFn != nil {
		return m.MergeFeatureBranchFn(repoPath, featureBranch, baseBranch)
	}
	return m.DefaultError
}

func (m *MockRebaseOperator) PRBaseBranch(repoPath, prURL string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "PRBaseBranch", Args: []any{repoPath, prURL}})
	if m.PRBaseBranchFn != nil {
		return m.PRBaseBranchFn(repoPath, prURL)
	}
	return "", m.DefaultError
}

func (m *MockRebaseOperator) RebaseInProgress(worktreePath string) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "RebaseInProgress", Args: []any{worktreePath}})
	if m.RebaseInProgressFn != nil {
		return m.RebaseInProgressFn(worktreePath)
	}
	return false, m.DefaultError
}

// ---------------------------------------------------------------------------
// MockWorktreeOps implements feature.WorktreeOps
// ---------------------------------------------------------------------------

// MockWorktreeOps implements feature.WorktreeOps with configurable
// function overrides and call tracking.
type MockWorktreeOps struct {
	CreateFn               func(repoPath, featureSlug, repoName, startPoint string) (string, error)
	ExpectedPathFn         func(featureSlug, repoName string) string
	RemoveFn               func(worktreePath string, deleteBranch bool) error
	RemoveRefFn            func(worktreePath, mainRepo, branch string) error
	ResetToBaseFn          func(worktreePath, baseBranch string) error
	ResetToBaseLocalFn     func(worktreePath, baseBranch string) error
	ResetToCommitFn        func(worktreePath, commitSHA string) error
	CurrentHeadSHAFn       func(worktreePath string) (string, error)
	CurrentBranchFn        func(worktreePath string) string
	RefSHAFn               func(repoPath, ref string) (string, error)
	UpdateRefFn            func(repoPath, ref, oldSHA, newSHA string) error
	CreateMergeCandidateFn func(mainRepo, parentTip, childHead, message string) (*feature.MergeCandidateResult, error)

	DefaultError error
	Calls        []MockCall
}

// NewMockWorktreeOps returns a MockWorktreeOps with zero-value defaults.
func NewMockWorktreeOps() *MockWorktreeOps { return &MockWorktreeOps{} }

func (m *MockWorktreeOps) Create(repoPath, featureSlug, repoName, startPoint string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "Create", Args: []any{repoPath, featureSlug, repoName, startPoint}})
	if m.CreateFn != nil {
		return m.CreateFn(repoPath, featureSlug, repoName, startPoint)
	}
	return "", m.DefaultError
}

func (m *MockWorktreeOps) ExpectedPath(featureSlug, repoName string) string {
	m.Calls = append(m.Calls, MockCall{Method: "ExpectedPath", Args: []any{featureSlug, repoName}})
	if m.ExpectedPathFn != nil {
		return m.ExpectedPathFn(featureSlug, repoName)
	}
	return ""
}

func (m *MockWorktreeOps) Remove(worktreePath string, deleteBranch bool) error {
	m.Calls = append(m.Calls, MockCall{Method: "Remove", Args: []any{worktreePath, deleteBranch}})
	if m.RemoveFn != nil {
		return m.RemoveFn(worktreePath, deleteBranch)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) RemoveRef(worktreePath, mainRepo, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "RemoveRef", Args: []any{worktreePath, mainRepo, branch}})
	if m.RemoveRefFn != nil {
		return m.RemoveRefFn(worktreePath, mainRepo, branch)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) ResetToBase(worktreePath, baseBranch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "ResetToBase", Args: []any{worktreePath, baseBranch}})
	if m.ResetToBaseFn != nil {
		return m.ResetToBaseFn(worktreePath, baseBranch)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) ResetToBaseLocal(worktreePath, baseBranch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "ResetToBaseLocal", Args: []any{worktreePath, baseBranch}})
	if m.ResetToBaseLocalFn != nil {
		return m.ResetToBaseLocalFn(worktreePath, baseBranch)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) ResetToCommit(worktreePath, commitSHA string) error {
	m.Calls = append(m.Calls, MockCall{Method: "ResetToCommit", Args: []any{worktreePath, commitSHA}})
	if m.ResetToCommitFn != nil {
		return m.ResetToCommitFn(worktreePath, commitSHA)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) CurrentHeadSHA(worktreePath string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CurrentHeadSHA", Args: []any{worktreePath}})
	if m.CurrentHeadSHAFn != nil {
		return m.CurrentHeadSHAFn(worktreePath)
	}
	return "", m.DefaultError
}

func (m *MockWorktreeOps) CurrentBranch(worktreePath string) string {
	m.Calls = append(m.Calls, MockCall{Method: "CurrentBranch", Args: []any{worktreePath}})
	if m.CurrentBranchFn != nil {
		return m.CurrentBranchFn(worktreePath)
	}
	return ""
}

func (m *MockWorktreeOps) RefSHA(repoPath, ref string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "RefSHA", Args: []any{repoPath, ref}})
	if m.RefSHAFn != nil {
		return m.RefSHAFn(repoPath, ref)
	}
	return "", m.DefaultError
}

func (m *MockWorktreeOps) UpdateRef(repoPath, ref, oldSHA, newSHA string) error {
	m.Calls = append(m.Calls, MockCall{Method: "UpdateRef", Args: []any{repoPath, ref, oldSHA, newSHA}})
	if m.UpdateRefFn != nil {
		return m.UpdateRefFn(repoPath, ref, oldSHA, newSHA)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) CreateMergeCandidate(mainRepo, parentTip, childHead, message string) (*feature.MergeCandidateResult, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CreateMergeCandidate", Args: []any{mainRepo, parentTip, childHead, message}})
	if m.CreateMergeCandidateFn != nil {
		return m.CreateMergeCandidateFn(mainRepo, parentTip, childHead, message)
	}
	return nil, m.DefaultError
}

// ---------------------------------------------------------------------------
// MockBranchOperator implements feature.BranchOps
// ---------------------------------------------------------------------------

// MockBranchOperator implements feature.BranchOps with configurable function
// overrides and call tracking.
type MockBranchOperator struct {
	BranchNameFn           func(featureSlug string) string
	BranchExistsOnRemoteFn func(repoPath, branch string) (bool, error)
	HasOriginRemoteFn      func(repoPath string) (bool, error)
	DefaultBranchFn        func(repoPath string) (string, error)
	CurrentBranchFn        func(repoPath string) (string, error)
	CreateBackupBranchFn   func(worktreePath, slug string) (string, error)

	DefaultError error
	Calls        []MockCall
}

// NewMockBranchOperator returns a MockBranchOperator with zero-value defaults.
func NewMockBranchOperator() *MockBranchOperator { return &MockBranchOperator{} }

func (m *MockBranchOperator) BranchName(featureSlug string) string {
	m.Calls = append(m.Calls, MockCall{Method: "BranchName", Args: []any{featureSlug}})
	if m.BranchNameFn != nil {
		return m.BranchNameFn(featureSlug)
	}
	return ""
}

func (m *MockBranchOperator) BranchExistsOnRemote(repoPath, branch string) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "BranchExistsOnRemote", Args: []any{repoPath, branch}})
	if m.BranchExistsOnRemoteFn != nil {
		return m.BranchExistsOnRemoteFn(repoPath, branch)
	}
	return false, m.DefaultError
}

func (m *MockBranchOperator) HasOriginRemote(repoPath string) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "HasOriginRemote", Args: []any{repoPath}})
	if m.HasOriginRemoteFn != nil {
		return m.HasOriginRemoteFn(repoPath)
	}
	return false, m.DefaultError
}

func (m *MockBranchOperator) DefaultBranch(repoPath string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "DefaultBranch", Args: []any{repoPath}})
	if m.DefaultBranchFn != nil {
		return m.DefaultBranchFn(repoPath)
	}
	return "", m.DefaultError
}

func (m *MockBranchOperator) CurrentBranch(repoPath string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CurrentBranch", Args: []any{repoPath}})
	if m.CurrentBranchFn != nil {
		return m.CurrentBranchFn(repoPath)
	}
	return "", m.DefaultError
}

func (m *MockBranchOperator) CreateBackupBranch(worktreePath, slug string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CreateBackupBranch", Args: []any{worktreePath, slug}})
	if m.CreateBackupBranchFn != nil {
		return m.CreateBackupBranchFn(worktreePath, slug)
	}
	return "", m.DefaultError
}
