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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"go.uber.org/fx"
)

// OrchestratorParams declares fx-injected dependencies.
type OrchestratorParams struct {
	fx.In
	FeatureManager  *feature.Manager
	FeatureStore    *feature.Store
	SessionManager  *session.Manager
	PhaseRunner     *agent.PhaseRunner
	Observer        *observe.Observer
	PermissionStore *permission.Store
	Lifecycle       fx.Lifecycle
	Worktrees       feature.WorktreeOps
}

// Module provides the Orchestrator via fx. It also registers an OnStop hook
// so fx.Shutdown propagates to Orchestrator.Shutdown — this is the ONLY site
// where lifecycle shutdown is registered; keeping it co-located with the
// provider prevents double-registration in API and CLI wiring.
var Module = fx.Module("orchestrator",
	fx.Provide(func(p OrchestratorParams) *Orchestrator {
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
			Worktrees:   p.Worktrees,      // retained feature.WorktreeOps seam
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
		}, BuildHooks(p.Observer, p.PermissionStore, p.FeatureStore, p.FeatureStore.BaseDir))

		p.Lifecycle.Append(fx.Hook{
			OnStop: func(_ context.Context) error {
				return o.Shutdown()
			},
		})

		return o
	}),
)
