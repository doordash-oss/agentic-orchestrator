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

package orchestrator

import "github.com/doordash-oss/agentic-orchestrator/internal/git"

// RemoteOps is the narrow substitution point for remote operations that
// cannot be exercised hermetically against a local bare repository.
type RemoteOps interface {
	Push(worktreePath, branch string) error
	PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error)
	CreatePR(repoPath, branch, title, body, baseBranch string, draft bool) (string, error)
	PRBaseBranch(repoPath, prURL string) string
	PRState(repoPath, prURL string) (string, error)
	GetPRBody(prURL string) (string, error)
	UpdatePRBody(prURL, body string) error
}

type gitRemoteOps struct{}

func (gitRemoteOps) Push(worktreePath, branch string) error {
	return git.Push(worktreePath, branch)
}

func (gitRemoteOps) PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
	return git.PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA)
}

func (gitRemoteOps) CreatePR(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
	return git.CreatePR(repoPath, branch, title, body, draft, baseBranch)
}

func (gitRemoteOps) PRBaseBranch(repoPath, prURL string) string {
	return git.PRBaseBranch(repoPath, prURL)
}

func (gitRemoteOps) PRState(repoPath, prURL string) (string, error) {
	return git.PRState(repoPath, prURL)
}

func (gitRemoteOps) GetPRBody(prURL string) (string, error) {
	return git.GetPRBody(prURL)
}

func (gitRemoteOps) UpdatePRBody(prURL, body string) error {
	return git.UpdatePRBody(prURL, body)
}
