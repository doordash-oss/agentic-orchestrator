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
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// MockRemoteOps is the small test substitute for orchestrator-owned remote
// operations.
type MockRemoteOps struct {
	PushFn            func(worktreePath, branch string) error
	PushLayerBranchFn func(repoPath, branch, localSHA, lastPushedSHA string) (string, error)
	CreatePRFn        func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error)
	PRBaseBranchFn    func(repoPath, prURL string) string
	PRStateFn         func(repoPath, prURL string) (string, error)
	GetPRBodyFn       func(prURL string) (string, error)
	UpdatePRBodyFn    func(prURL, body string) error
	DefaultError      error
	Calls             []MockCall
}

func NewMockRemoteOps() *MockRemoteOps { return &MockRemoteOps{} }

func (m *MockRemoteOps) Push(worktreePath, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "Push", Args: []any{worktreePath, branch}})
	if m.PushFn != nil {
		return m.PushFn(worktreePath, branch)
	}
	return m.DefaultError
}

func (m *MockRemoteOps) PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "PushLayerBranch", Args: []any{repoPath, branch, localSHA, lastPushedSHA}})
	if m.PushLayerBranchFn != nil {
		return m.PushLayerBranchFn(repoPath, branch, localSHA, lastPushedSHA)
	}
	return "", m.DefaultError
}

func (m *MockRemoteOps) CreatePR(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CreatePR", Args: []any{repoPath, branch, title, body, baseBranch, draft}})
	if m.CreatePRFn != nil {
		return m.CreatePRFn(repoPath, branch, title, body, baseBranch, draft)
	}
	return "", m.DefaultError
}

func (m *MockRemoteOps) PRBaseBranch(repoPath, prURL string) string {
	m.Calls = append(m.Calls, MockCall{Method: "PRBaseBranch", Args: []any{repoPath, prURL}})
	if m.PRBaseBranchFn != nil {
		return m.PRBaseBranchFn(repoPath, prURL)
	}
	return ""
}

// PRState defaults to the indeterminate answer, so tests that do not care
// about pull-request state behave as if the lookup were unavailable.
func (m *MockRemoteOps) PRState(repoPath, prURL string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "PRState", Args: []any{repoPath, prURL}})
	if m.PRStateFn != nil {
		return m.PRStateFn(repoPath, prURL)
	}
	return "", m.DefaultError
}

func (m *MockRemoteOps) GetPRBody(prURL string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "GetPRBody", Args: []any{prURL}})
	if m.GetPRBodyFn != nil {
		return m.GetPRBodyFn(prURL)
	}
	return "", m.DefaultError
}

func (m *MockRemoteOps) UpdatePRBody(prURL, body string) error {
	m.Calls = append(m.Calls, MockCall{Method: "UpdatePRBody", Args: []any{prURL, body}})
	if m.UpdatePRBodyFn != nil {
		return m.UpdatePRBodyFn(prURL, body)
	}
	return m.DefaultError
}

type MockPRCloser struct {
	ClosePRFn            func(prURL string) error
	PRStateFn            func(prURL string) (string, error)
	DeleteRemoteBranchFn func(repoPath, branch string) error
	DefaultError         error
	Calls                []MockCall
}

func NewMockPRCloser() *MockPRCloser { return &MockPRCloser{} }

func (m *MockPRCloser) ClosePR(prURL string) error {
	m.Calls = append(m.Calls, MockCall{Method: "ClosePR", Args: []any{prURL}})
	if m.ClosePRFn != nil {
		return m.ClosePRFn(prURL)
	}
	return m.DefaultError
}

// PRState defaults to the indeterminate answer, so tests that do not care
// about pull-request state behave as if the lookup were unavailable.
func (m *MockPRCloser) PRState(prURL string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "PRState", Args: []any{prURL}})
	if m.PRStateFn != nil {
		return m.PRStateFn(prURL)
	}
	return "", m.DefaultError
}

func (m *MockPRCloser) DeleteRemoteBranch(repoPath, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "DeleteRemoteBranch", Args: []any{repoPath, branch}})
	if m.DeleteRemoteBranchFn != nil {
		return m.DeleteRemoteBranchFn(repoPath, branch)
	}
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// MockWorktreeOps implements feature.WorktreeOps
// ---------------------------------------------------------------------------

// MockWorktreeOps implements feature.WorktreeOps with configurable
// function overrides and call tracking.
type MockWorktreeOps struct {
	CreateFn                func(repoPath, workspaceSlug, branch, repoName, startPoint string) (string, error)
	ExpectedPathFn          func(featureSlug, repoName string) string
	RemoveFn                func(worktreePath string, deleteBranch bool) error
	RemoveRefFn             func(worktreePath, mainRepo, branch string) error
	ResetToBaseFn           func(worktreePath, baseBranch string) error
	ResetToBaseLocalFn      func(worktreePath, baseBranch string) error
	ResetToCommitFn         func(worktreePath, commitSHA string) error
	CurrentHeadSHAFn        func(worktreePath string) (string, error)
	CurrentBranchFn         func(worktreePath string) string
	RefSHAFn                func(repoPath, ref string) (string, error)
	UpdateRefFn             func(repoPath, ref, oldSHA, newSHA string) error
	CreateMergeCandidateFn  func(mainRepo, parentTip, childHead, message string) (*git.MergeCandidateResult, error)
	InspectCleanlinessFn    func(worktreePath string, maxPerCategory int) (*git.CleanlinessReport, error)
	RenameBranchFn          func(worktreePath, oldName, newName string) error
	CreateBranchAtHeadFn    func(worktreePath, branch string) error
	SwitchBranchFn          func(worktreePath, branch string) error
	DeleteBranchFn          func(worktreePath, branch string) error
	RestackChainFn          func(mainRepo string, cutPoints []git.RestackCutPoint, ops []git.RestackOp) (*git.RestackResult, error)
	CommitTreeSHAFn         func(repoPath, commitSHA string) (string, error)
	UpdateRefsTransactionFn func(repoPath string, updates []git.RefUpdate) error

	DefaultError error
	Calls        []MockCall
}

// NewMockWorktreeOps returns a MockWorktreeOps with zero-value defaults.
func NewMockWorktreeOps() *MockWorktreeOps { return &MockWorktreeOps{} }

func (m *MockWorktreeOps) Create(repoPath, workspaceSlug, branch, repoName, startPoint string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "Create", Args: []any{repoPath, workspaceSlug, branch, repoName, startPoint}})
	if m.CreateFn != nil {
		return m.CreateFn(repoPath, workspaceSlug, branch, repoName, startPoint)
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

func (m *MockWorktreeOps) CreateMergeCandidate(mainRepo, parentTip, childHead, message string) (*git.MergeCandidateResult, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CreateMergeCandidate", Args: []any{mainRepo, parentTip, childHead, message}})
	if m.CreateMergeCandidateFn != nil {
		return m.CreateMergeCandidateFn(mainRepo, parentTip, childHead, message)
	}
	return nil, m.DefaultError
}

func (m *MockWorktreeOps) InspectCleanliness(worktreePath string, maxPerCategory int) (*git.CleanlinessReport, error) {
	m.Calls = append(m.Calls, MockCall{Method: "InspectCleanliness", Args: []any{worktreePath, maxPerCategory}})
	if m.InspectCleanlinessFn != nil {
		return m.InspectCleanlinessFn(worktreePath, maxPerCategory)
	}
	return &git.CleanlinessReport{}, m.DefaultError
}

func (m *MockWorktreeOps) RenameBranch(worktreePath, oldName, newName string) error {
	m.Calls = append(m.Calls, MockCall{Method: "RenameBranch", Args: []any{worktreePath, oldName, newName}})
	if m.RenameBranchFn != nil {
		return m.RenameBranchFn(worktreePath, oldName, newName)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) CreateBranchAtHead(worktreePath, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "CreateBranchAtHead", Args: []any{worktreePath, branch}})
	if m.CreateBranchAtHeadFn != nil {
		return m.CreateBranchAtHeadFn(worktreePath, branch)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) SwitchBranch(worktreePath, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "SwitchBranch", Args: []any{worktreePath, branch}})
	if m.SwitchBranchFn != nil {
		return m.SwitchBranchFn(worktreePath, branch)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) DeleteBranch(worktreePath, branch string) error {
	m.Calls = append(m.Calls, MockCall{Method: "DeleteBranch", Args: []any{worktreePath, branch}})
	if m.DeleteBranchFn != nil {
		return m.DeleteBranchFn(worktreePath, branch)
	}
	return m.DefaultError
}

func (m *MockWorktreeOps) RestackChain(mainRepo string, cutPoints []git.RestackCutPoint, ops []git.RestackOp) (*git.RestackResult, error) {
	m.Calls = append(m.Calls, MockCall{Method: "RestackChain", Args: []any{mainRepo, cutPoints, ops}})
	if m.RestackChainFn != nil {
		return m.RestackChainFn(mainRepo, cutPoints, ops)
	}
	return nil, m.DefaultError
}

func (m *MockWorktreeOps) CommitTreeSHA(repoPath, commitSHA string) (string, error) {
	m.Calls = append(m.Calls, MockCall{Method: "CommitTreeSHA", Args: []any{repoPath, commitSHA}})
	if m.CommitTreeSHAFn != nil {
		return m.CommitTreeSHAFn(repoPath, commitSHA)
	}
	return "", m.DefaultError
}

func (m *MockWorktreeOps) UpdateRefsTransaction(repoPath string, updates []git.RefUpdate) error {
	m.Calls = append(m.Calls, MockCall{Method: "UpdateRefsTransaction", Args: []any{repoPath, updates}})
	if m.UpdateRefsTransactionFn != nil {
		return m.UpdateRefsTransactionFn(repoPath, updates)
	}
	return m.DefaultError
}
