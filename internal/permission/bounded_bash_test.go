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
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestBoundedHelperBash_ReadOnlyAndArtifactWrites guards the bounded-helper bash
// policy: glm-5p2 ends its turn on ANY denied tool call, so the helper must be
// able to run its read-only analysis (quoted regexes, pipes, awk/jq/etc.) and
// write its own artifacts via shell without ever being denied — while still
// being unable to mutate the worktree.
func TestBoundedHelperBash_ReadOnlyAndArtifactWrites(t *testing.T) {
	helperDir := t.TempDir()
	feedback := filepath.Join(helperDir, "validation-scope-feedback.md")
	marker := filepath.Join(helperDir, "phase_complete")
	h := &BoundedHelperArtifactHandler{AllowedPaths: []string{feedback, marker}}

	bash := func(cmd string) string {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		return string(b)
	}

	allow := []struct{ name, cmd string }{
		// The exact command that aborted the live helper: backtick inside a
		// single-quoted regex, plus pipes and && chaining — all read-only.
		{"rg backtick regex piped", "echo \"=== fences ===\" && rg -c '^\\s*```' README.md && rg -oN '\\]\\(([^h)][^)]*)\\)' README.md | sort -u"},
		{"pipe inside single quotes not split", "rg -n 'foo|bar' README.md"},
		{"awk read-only", "awk '{print $1}' README.md"},
		{"cat piped to wc", "cat README.md | wc -l"},
		{"grep with double-quoted arg", "grep -n \"needle\" README.md"},
		{"plain ls", "ls -la " + helperDir},
		{"test then echo", "test -f " + feedback + " && echo EXISTS"},
		// Artifact writes via shell to the declared paths.
		{"touch the marker", "touch " + marker},
		{"heredoc into feedback", "cat > " + feedback + " << 'EOF'\n## Verdict\nAPPROVED\nEOF"},
		{"echo redirect into marker", "echo done > " + marker},
		{"printf redirect into feedback", "printf '## Verdict\\nAPPROVED\\n' > " + feedback},
	}
	for _, tc := range allow {
		requirePermissionAllowed(t, h, "Bash", bash(tc.cmd))
	}

	deny := []struct{ name, cmd string }{
		{"rm mutates", "rm -rf " + helperDir},
		{"mv mutates", "mv a b"},
		{"redirect to non-artifact", "echo x > " + filepath.Join(helperDir, "notes.md")},
		{"redirect outside helper dir", "cat README.md > /tmp/exfil.md"},
		{"touch non-artifact", "touch " + filepath.Join(helperDir, "other.txt")},
		{"command substitution dollar-paren", "echo $(rm -rf /tmp/x)"},
		{"backtick substitution in double quotes", "rg \"`whoami`\" README.md"},
		{"chained mutator after artifact write", "echo ok > " + marker + " ; rm -rf " + helperDir},
		{"sed in-place edit", "sed -i 's/a/b/' README.md"},
	}
	for _, tc := range deny {
		requirePermissionDenied(t, h, "Bash", bash(tc.cmd))
	}
}
