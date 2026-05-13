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
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// MockConfigPersistence implements ports.ConfigPersistence with configurable
// function overrides.
type MockConfigPersistence struct {
	LoadFn       func(path string) (*config.Config, error)
	SaveFn       func(cfg *config.Config, path string) error
	DefaultError error
}

// NewMockConfigPersistence returns a MockConfigPersistence with zero-value defaults.
func NewMockConfigPersistence() *MockConfigPersistence { return &MockConfigPersistence{} }

func (m *MockConfigPersistence) Load(path string) (*config.Config, error) {
	if m.LoadFn != nil {
		return m.LoadFn(path)
	}
	return nil, m.DefaultError
}

func (m *MockConfigPersistence) Save(cfg *config.Config, path string) error {
	if m.SaveFn != nil {
		return m.SaveFn(cfg, path)
	}
	return m.DefaultError
}
