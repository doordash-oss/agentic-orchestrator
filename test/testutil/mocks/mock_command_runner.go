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
	"context"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// MockCommandRunnerCall records a single Run invocation.
type MockCommandRunnerCall struct {
	Name string
	Args []string
	Opts ports.CommandOpts
}

// MockCommandRunner implements ports.CommandRunner with a configurable function
// override and call tracking.
type MockCommandRunner struct {
	RunFn        func(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error)
	DefaultError error
	Calls        []MockCommandRunnerCall
}

// NewMockCommandRunner returns a MockCommandRunner with zero-value defaults.
func NewMockCommandRunner() *MockCommandRunner { return &MockCommandRunner{} }

func (m *MockCommandRunner) Run(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
	m.Calls = append(m.Calls, MockCommandRunnerCall{Name: name, Args: args, Opts: opts})
	if m.RunFn != nil {
		return m.RunFn(ctx, name, args, opts)
	}
	return nil, m.DefaultError
}
