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

package feature

import (
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"go.uber.org/fx"
)

// Params holds fx-injected parameters for the feature module.
type Params struct {
	fx.In
	StateDir string `name:"stateDir"`
	Config   *config.Config
}

// ManagerDeps is the runtime dependency surface the feature manager needs.
type ManagerDeps struct {
	fx.In
	Worktrees WorktreeOps     `optional:"true"`
	PRs       RewindRemoteOps `optional:"true"`
}

// gitRewindRemoteOps is the production implementation of the feature-owned
// rewind remote-operations seam.
type gitRewindRemoteOps struct{}

func (gitRewindRemoteOps) ClosePR(prURL string) error { return git.ClosePR(prURL) }

// The repository-path parameter of git.PRState exists for signature
// stability only; the URL fully identifies the pull request.
func (gitRewindRemoteOps) PRState(prURL string) (string, error) {
	return git.PRState("", prURL)
}

func (gitRewindRemoteOps) DeleteRemoteBranch(repoPath, branch string) error {
	return git.DeleteRemoteBranch(repoPath, branch)
}

var _ WorktreeOps = (*git.WorktreeManager)(nil)
var _ RewindRemoteOps = gitRewindRemoteOps{}

// Module provides the feature store, manager, and feature-owned rewind
// remote operations.
var Module = fx.Module("feature",
	fx.Provide(func() RewindRemoteOps { return gitRewindRemoteOps{} }),
	fx.Provide(func(p Params) *git.WorktreeManager {
		return git.NewWorktreeManager(filepath.Join(filepath.Dir(p.StateDir), "worktrees"))
	}),
	fx.Provide(func(wm *git.WorktreeManager) WorktreeOps { return wm }),
	fx.Provide(func(p Params) *Store {
		return NewStore(p.StateDir)
	}),
	fx.Provide(func(p Params, deps ManagerDeps, store *Store) *Manager {
		mgr := NewManager(store, p.Config)
		mgr.Worktrees = deps.Worktrees
		mgr.PRs = deps.PRs
		return mgr
	}),
)
