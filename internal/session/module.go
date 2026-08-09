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

package session

import "go.uber.org/fx"

// SessionParams holds fx-injected parameters for the session module.
type SessionParams struct {
	fx.In
	EventCh  chan interface{} `name:"eventCh"`
	StateDir string           `name:"stateDir"`
}

// Module provides the session Manager via fx.
var Module = fx.Module("session",
	fx.Provide(func(p SessionParams) *Manager {
		return NewRecoveringManager(p.EventCh, p.StateDir)
	}),
)
