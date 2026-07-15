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

package main

import (
	"bytes"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
)

// TestRunArgsVersionPrintsVersionLine pins the --version/-v contract that
// desktop package verification parses: the exact tui.VersionLine() banner on
// stdout, exit 0, no launcher/server/updater side effects.
func TestRunArgsVersionPrintsVersionLine(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		var stdout, stderr bytes.Buffer
		code := runArgs(
			[]string{flag},
			&stdout,
			&stderr,
			func(string, string, bool, []string, bool) int {
				t.Fatalf("%s must not launch the client", flag)
				return 1
			},
			failingServerLauncher(t),
			failingUpdater(t),
		)
		if code != 0 {
			t.Fatalf("runArgs(%s) code = %d, want 0", flag, code)
		}
		if got, want := stdout.String(), tui.VersionLine()+"\n"; got != want {
			t.Fatalf("runArgs(%s) stdout = %q, want %q", flag, got, want)
		}
		if stderr.Len() != 0 {
			t.Fatalf("runArgs(%s) stderr = %q, want empty", flag, stderr.String())
		}
	}
}
