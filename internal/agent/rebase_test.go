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
	"strings"
	"testing"
)

func TestBuildRebasePlan_UsesOnlyConcreteVerificationCommands(t *testing.T) {
	tests := []struct {
		name          string
		conflictFiles []string
	}{
		{name: "start_rebase", conflictFiles: nil},
		{name: "continue_existing_rebase", conflictFiles: []string{"internal/agent/implement.go"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := BuildRebasePlan(defaultTestBranch, "https://github.com/o/r/pull/1", tt.conflictFiles)

			for _, forbidden := range []string{
				"run the project build command",
				"run the project linter",
				"run the full test suite",
			} {
				if strings.Contains(plan, forbidden) {
					t.Errorf("plan contains guessed command %q", forbidden)
				}
			}
			if !strings.Contains(plan, "git grep -n") || !strings.Contains(plan, "test $? -eq 1") {
				t.Errorf("expected plan to use the portable git conflict-marker check, got %q", plan)
			}
			for _, forbidden := range []string{"grep -rln", "--include", "| head"} {
				if strings.Contains(plan, forbidden) {
					t.Errorf("conflict-marker check contains non-portable fragment %q: %q", forbidden, plan)
				}
			}
		})
	}
}
