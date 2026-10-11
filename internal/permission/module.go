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

package permission

import (
	"path/filepath"

	"go.uber.org/fx"
)

// params declares fx-injected parameters for the permission module.
type params struct {
	fx.In
	StateDir string `name:"stateDir"`
}

// Module provides the permission Store and Cache via fx.
//
// Both the orchestrator (via Hooks.OnFeatureCreated) and the desktop app consume the
// Store and Cache built here. Construction ensures the global defaults file
// exists before anything tries to read it.
var Module = fx.Module("permission",
	fx.Provide(func(p params) (*Store, error) {
		permDir := filepath.Join(filepath.Dir(p.StateDir), "permissions")
		store := newStore(permDir)
		if err := store.ensureGlobalDefaults(); err != nil {
			return nil, err
		}
		return store, nil
	}),
	fx.Provide(func(s *Store) *Cache {
		c := newCache(s)
		c.LoadAndMerge("")
		return c
	}),
)
