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
	"testing"
)

func TestMaybeWrapHelperSandbox_DisabledUnchanged(t *testing.T) {
	cmd := []string{"helper", "run"}
	got, sandboxed, cleanup := maybeWrapHelperSandbox(cmd, false, "/Users/x/.agentic-workflow/features")
	cleanup()
	if sandboxed || len(got) != 2 || got[0] != "helper" || got[1] != "run" {
		t.Errorf("disabled sandbox: got=%v sandboxed=%v, want unchanged [helper run]", got, sandboxed)
	}
}
