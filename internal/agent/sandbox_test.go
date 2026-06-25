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

import "testing"

// TestMaybeWrapHelperSandbox_NonOpenCodeUnchanged is platform-agnostic: no
// provider other than OpenCode is ever wrapped, on any OS.
func TestMaybeWrapHelperSandbox_NonOpenCodeUnchanged(t *testing.T) {
	cmd := []string{"opencode", "acp"}
	got, sandboxed, cleanup := maybeWrapHelperSandbox(cmd, "claude", "/Users/x/.agentic-workflow/features")
	cleanup()
	if sandboxed || len(got) != 2 || got[0] != "opencode" || got[1] != "acp" {
		t.Errorf("non-opencode provider: got=%v sandboxed=%v, want unchanged [opencode acp]", got, sandboxed)
	}
}
