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

import "path/filepath"

type boundedHelperSandboxProvider interface {
	UsesBoundedHelperSandbox() bool
}

type sessionResumeProvider interface {
	SupportsSessionResume() bool
}

// maybeWrapHelperSandbox wraps a bounded-helper command so the reviewed worktree
// is read-only at the kernel layer while the helper's shell runs unrestricted —
// a worktree write then fails as an ordinary shell error the model absorbs,
// instead of a permission denial that aborts it. Returns the (possibly wrapped)
// command, whether it was sandboxed, and a cleanup func (never nil).
func maybeWrapHelperSandbox(command []string, enabled bool, stateDir string) ([]string, bool, func()) {
	if !enabled || len(command) == 0 {
		return command, false, func() {}
	}
	worktreesBase := filepath.Join(filepath.Dir(stateDir), "worktrees")
	return wrapHelperSandbox(command, worktreesBase)
}
