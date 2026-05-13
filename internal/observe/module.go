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

package observe

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"go.uber.org/fx"
)

// Params holds fx-injected parameters for the observe module.
type Params struct {
	fx.In
	Config   *config.Config
	StateDir string `name:"stateDir"`
}

// Module provides the Observer via fx.
var Module = fx.Module("observe",
	fx.Provide(func(p Params) *Observer {
		obs := p.Config.Observability
		return New(obs.Events, p.StateDir, obs.OTelEnabled, obs.OTelEndpoint, obs.OTelInsecure, obs.OTelServiceName)
	}),
)
