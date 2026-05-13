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
	"context"
	"os/exec"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// execCommandRunner implements ports.CommandRunner using os/exec.
type execCommandRunner struct{}

// NewExecCommandRunner returns a ports.CommandRunner backed by os/exec.
func NewExecCommandRunner() ports.CommandRunner {
	return &execCommandRunner{}
}

// Compile-time interface check.
var _ ports.CommandRunner = (*execCommandRunner)(nil)

func (r *execCommandRunner) Run(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
		return nil, cmd.Run()
	}
	return cmd.Output()
}
