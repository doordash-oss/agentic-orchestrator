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

package agent

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"go.uber.org/fx"
)

// AgentParams holds fx-injected parameters for the agent module.
type AgentParams struct {
	fx.In
	SessionManager ports.SessionManager
	FeatureStore   ports.FeatureStore
	Config         *config.Config
	Registry       *llm.Registry
	StateDir       string `name:"stateDir"`
	DSP            bool   `name:"dsp"`
	Observer       *observe.Observer
}

// Module provides the PhaseRunner via fx.
var Module = fx.Module("agent",
	// Bridge concrete types from sibling modules to port interfaces.
	fx.Provide(func(sm *session.Manager) ports.SessionManager { return sm }),
	fx.Provide(func(store *feature.Store) ports.FeatureStore { return store }),
	fx.Provide(func(p AgentParams) *PhaseRunner {
		pr := NewPhaseRunner(p.SessionManager, p.FeatureStore, p.StateDir)
		pr.CommandRunner = &execCommandRunner{}
		pr.Registry = p.Registry
		pr.Config = p.Config
		pr.DangerouslySkipPermissions = p.DSP
		pr.Observer = p.Observer
		return pr
	}),
)
