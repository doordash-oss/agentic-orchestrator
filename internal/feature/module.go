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
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"go.uber.org/fx"
)

// Params holds fx-injected parameters for the feature module.
type Params struct {
	fx.In
	StateDir string `name:"stateDir"`
	Config   *config.Config
}

// ManagerDeps is the runtime dependency surface the feature manager needs
// from the adapter layer (git worktrees, branches, PR close). These are
// wired in the orchestrator's fx module rather than here, so the feature
// module does not import internal/git directly.
type ManagerDeps struct {
	fx.In
	Worktrees WorktreeOps `optional:"true"`
	Branches  BranchOps   `optional:"true"`
	PRs       PRCloser    `optional:"true"`
}

// Module provides feature Store and Manager via fx. The adapter dependencies
// (WorktreeOps, BranchOps, PRCloser) are wired externally so the feature
// module stays free of internal/git.
var Module = fx.Module("feature",
	fx.Provide(func(p Params) *Store {
		return NewStore(p.StateDir)
	}),
	fx.Provide(func(p Params, deps ManagerDeps, store *Store) *Manager {
		mgr := NewManager(store, p.Config)
		mgr.Worktrees = deps.Worktrees
		mgr.Branches = deps.Branches
		mgr.PRs = deps.PRs
		return mgr
	}),
)
