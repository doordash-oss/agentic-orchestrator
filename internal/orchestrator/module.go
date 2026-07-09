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

import (
	"context"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"go.uber.org/fx"
)

// OrchestratorParams declares fx-injected dependencies.
type OrchestratorParams struct {
	fx.In
	FeatureManager  *feature.Manager
	FeatureStore    *feature.Store
	SessionManager  *session.Manager
	Publisher       *git.PublishAdapter
	Differ          *git.DiffAdapter
	Rebaser         *git.RebaseAdapter
	CrossRef        *git.CrossRefAdapter
	Reviewer        *git.ReviewCommentAdapter
	Branch          *git.BranchAdapter
	PhaseRunner     *agent.PhaseRunner
	Observer        *observe.Observer
	PermissionStore *permission.Store
	Lifecycle       fx.Lifecycle
	StateDir        string `name:"stateDir"`
}

// worktreeManagerParams isolates the stateDir dependency so the
// WorktreeManager provider does not depend on the feature.Manager — avoiding
// an fx cycle where feature.Manager consumes the worktree ops that depend on
// it.
type worktreeManagerParams struct {
	fx.In
	StateDir string `name:"stateDir"`
}

// Module provides the Orchestrator via fx. It also registers an OnStop hook
// so fx.Shutdown propagates to Orchestrator.Shutdown — this is the ONLY site
// where lifecycle shutdown is registered; keeping it co-located with the
// provider prevents double-registration in TUI/CLI wiring.
var Module = fx.Module("orchestrator",
	// Expose the git.WorktreeManager as the feature.WorktreeOps / ports.WorktreeOperator.
	// This is wired here (not in feature.Module) so feature does not import git.
	fx.Provide(
		func(p worktreeManagerParams) *git.WorktreeManager {
			// p.StateDir is the features base dir (e.g. ~/.agentic-workflow/features).
			// Worktrees live alongside it (~/.agentic-workflow/worktrees).
			wtBaseDir := filepath.Join(filepath.Dir(p.StateDir), "worktrees")
			return git.NewWorktreeManager(wtBaseDir)
		},
		// Adapter-shaped providers so feature.Module gets its injections
		// without importing internal/git.
		func(wm *git.WorktreeManager) feature.WorktreeOps { return wm },
		func(b *git.BranchAdapter) feature.BranchOps { return b },
		func(p *git.PublishAdapter) feature.PRCloser { return p },
		func(wm *git.WorktreeManager) ports.WorktreeOperator { return wm },
	),
	fx.Provide(func(p OrchestratorParams, wm *git.WorktreeManager) *Orchestrator {
		// Install the attach-drop metric reporter on the session manager
		// so any session critical-send timeout emits
		// session.critical_message_dropped to events.jsonl. Safe on a nil
		// observer (tests / disabled-observability path): the adapter
		// constructor returns nil and the session manager leaves its
		// reporter slot untouched.
		if p.Observer != nil {
			if reporter := newAttachDropObserver(p.Observer, p.FeatureStore); reporter != nil {
				p.SessionManager.SetAttachDropReporter(reporter)
			}
		}

		o := New(Deps{
			Lifecycle:   p.FeatureManager, // implicit satisfaction
			Store:       p.FeatureStore,   // implicit satisfaction
			Sessions:    p.SessionManager, // *session.Manager satisfies ports.SessionManager
			Publisher:   p.Publisher,      // *git.PublishAdapter satisfies ports.Publisher
			Differ:      p.Differ,         // *git.DiffAdapter satisfies ports.DiffOperator
			Rebaser:     p.Rebaser,        // *git.RebaseAdapter satisfies ports.RebaseOperator
			CrossRef:    p.CrossRef,       // *git.CrossRefAdapter satisfies ports.CrossRefOperator
			Reviewer:    p.Reviewer,       // *git.ReviewCommentAdapter satisfies ports.ReviewCommentOperator
			Branch:      p.Branch,         // *git.BranchAdapter satisfies ports.BranchOperator
			Worktrees:   wm,               // *git.WorktreeManager satisfies ports.WorktreeOperator
			PhaseRunner: p.PhaseRunner,    // concrete *agent.PhaseRunner
			// Reuse PhaseRunner.CommandRunner so KB freshness checks
			// (agent.IsKBFresh → GetCurrentCommit) have a real runner to
			// shell out to git. PhaseRunner constructs an execCommandRunner
			// in production and a MockCommandRunner in tests, so wiring it
			// through here avoids a nil dereference in startKB.
			CmdRunner: p.PhaseRunner.CommandRunner,
			// Recovery adapter bound to the feature store's BaseDir and the
			// feature manager so orphan-session discovery and dispatch can
			// reach the right state without widening ports.SessionManager.
			Recovery: session.NewRecoveryAdapter(p.FeatureStore.BaseDir, p.FeatureManager),
			// Rate-limit backoff policy derived from user config; nil-safe
			// when the feature manager has no config attached.
			RateLimitRetry: rateLimitRetryFromManager(p.FeatureManager),
		}, BuildHooks(p.Observer, p.PermissionStore, p.FeatureStore, p.FeatureStore.BaseDir))

		p.Lifecycle.Append(fx.Hook{
			OnStop: func(_ context.Context) error {
				return o.Shutdown()
			},
		})

		return o
	}),
)

// rateLimitRetryFromManager derives the rate-limit backoff policy from the
// feature manager's config, returning nil (defaults) when no config is
// attached so the orchestrator's getter supplies the built-in policy.
func rateLimitRetryFromManager(m *feature.Manager) *agent.RateLimitRetryPolicy {
	if m == nil || m.Config == nil {
		return nil
	}
	policy := RateLimitRetryPolicyFromConfig(m.Config.Defaults.RateLimitRetry)
	return &policy
}
