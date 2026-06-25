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

// openCodeProviderName matches opencode.Provider.Name(); the read-only sandbox
// is applied only to OpenCode bounded helpers. OpenCode can run models that
// abort their turn on a permission denial instead of treating it as a
// recoverable error, so the helper's shell must run unrestricted with worktree
// mutation blocked at the kernel layer instead. Other providers keep the
// client-side read-only-bash allowlist and are not sandboxed.
const openCodeProviderName = "opencode"

// maybeWrapHelperSandbox wraps a bounded-helper command so the reviewed worktree
// is read-only at the kernel layer while the helper's shell runs unrestricted —
// a worktree write then fails as an ordinary shell error the model absorbs,
// instead of a permission denial that aborts it. Applies only to OpenCode, via
// the platform's wrapHelperSandbox (macOS sandbox-exec; Linux bubblewrap; a
// no-op elsewhere). Returns the (possibly wrapped) command, whether it was
// sandboxed, and a cleanup func (never nil). It fails open: with no usable
// mechanism the helper runs unsandboxed under the (still safe) read-only-bash
// allowlist, so worktree integrity is never traded for the abort fix.
func maybeWrapHelperSandbox(command []string, providerName, stateDir string) ([]string, bool, func()) {
	if providerName != openCodeProviderName || len(command) == 0 {
		return command, false, func() {}
	}
	worktreesBase := filepath.Join(filepath.Dir(stateDir), "worktrees")
	return wrapHelperSandbox(command, worktreesBase)
}
