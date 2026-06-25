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

package opencode

import "testing"

// TestEditPermission_EmitsCwdRelativeGlob guards the regression where a writable
// root outside the session cwd was expressed only as an absolute glob. OpenCode
// evaluates the edit surface against the tool-supplied path relative to cwd
// (e.g. "../../../knowledge-base/<repo>/..."), so an absolute-only glob never
// matched and every external edit fell through to the catch-all deny. The root
// must therefore also appear as a cwd-relative glob.
func TestEditPermission_EmitsCwdRelativeGlob(t *testing.T) {
	// Mirrors the KB phase: cwd is the repo worktree; the writable KB root is a
	// sibling tree three levels up.
	workDir := "/Users/x/.agentic-workflow/worktrees/feature/repo"
	kbRoot := "/Users/x/.agentic-workflow/knowledge-base/repo"

	got := editPermission("allow", workDir, []string{kbRoot})
	patterns, ok := got.(map[string]string)
	if !ok {
		t.Fatalf("editPermission returned %T, want map[string]string", got)
	}

	absGlob := "/Users/x/.agentic-workflow/knowledge-base/repo/**"
	relGlob := "../../../knowledge-base/repo/**"

	if patterns[catchAllPattern] != "deny" {
		t.Errorf("catch-all = %q, want deny", patterns[catchAllPattern])
	}
	if patterns[absGlob] != "allow" {
		t.Errorf("absolute glob %q = %q, want allow", absGlob, patterns[absGlob])
	}
	if patterns[relGlob] != "allow" {
		t.Errorf("cwd-relative glob %q missing/!=allow (got %q); OpenCode matches the edit path relative to cwd, so without this every external edit hits the catch-all deny", relGlob, patterns[relGlob])
	}
}

// TestRootGlobs_NoWorkDirIsAbsoluteOnly confirms the additive nature of the fix:
// with no cwd known, only the absolute glob is emitted, preserving prior behavior.
func TestRootGlobs_NoWorkDirIsAbsoluteOnly(t *testing.T) {
	globs := rootGlobs("", "/Users/x/state")
	if len(globs) != 1 || globs[0] != "/Users/x/state/**" {
		t.Fatalf("rootGlobs(\"\", root) = %v, want [\"/Users/x/state/**\"]", globs)
	}
}
