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

// TestEditPermission_EmitsExactFileRoot guards bounded review helpers, which
// pass exact artifact file paths (for example validation feedback and
// phase_complete) as writable roots. OpenCode evaluates a write to the file
// path itself, so a recursive child-only pattern like "<file>/**" does not
// match and the tool falls through to the catch-all deny.
func TestEditPermission_EmitsExactFileRoot(t *testing.T) {
	workDir := "/Users/x/.agentic-workflow/worktrees/feature/repo"
	feedbackPath := "/Users/x/.agentic-workflow/features/feature/runs/run-002/roadmap/attempt-01/validate-scope/validation-scope-feedback.md"

	got := editPermission("ask", workDir, []string{feedbackPath})
	patterns, ok := got.(map[string]string)
	if !ok {
		t.Fatalf("editPermission returned %T, want map[string]string", got)
	}

	relPath := "../../../features/feature/runs/run-002/roadmap/attempt-01/validate-scope/validation-scope-feedback.md"
	strippedPath := "Users/x/.agentic-workflow/features/feature/runs/run-002/roadmap/attempt-01/validate-scope/validation-scope-feedback.md"

	if patterns[catchAllPattern] != "deny" {
		t.Errorf("catch-all = %q, want deny", patterns[catchAllPattern])
	}
	if patterns[feedbackPath] != "ask" {
		t.Errorf("exact absolute file pattern %q = %q, want ask", feedbackPath, patterns[feedbackPath])
	}
	if patterns[relPath] != "ask" {
		t.Errorf("exact cwd-relative file pattern %q = %q, want ask", relPath, patterns[relPath])
	}
	// The leading-slash-stripped form is what OpenCode actually evaluates the edit
	// against, so the helper's feedback write resolves to ask through the plain
	// glob map — no edit delegation needed.
	if patterns[strippedPath] != "ask" {
		t.Errorf("exact leading-slash-stripped file pattern %q = %q, want ask", strippedPath, patterns[strippedPath])
	}
}

// TestPermissionConfig_EditIsPatternMapForWritableRoots confirms a writable root
// yields the path-pattern edit map (catch-all deny + per-root globs) that bounds
// edits to mounted roots.
func TestPermissionConfig_EditIsPatternMapForWritableRoots(t *testing.T) {
	workDir := "/Users/x/.agentic-workflow/worktrees/feature/repo"
	kbRoot := "/Users/x/.agentic-workflow/knowledge-base/repo"

	perm := permissionConfig(false, workDir, []string{kbRoot}, []string{workDir})

	if _, ok := perm[permKeyEdit].(map[string]string); !ok {
		t.Errorf("perm[%q] = %#v, want path-pattern map", permKeyEdit, perm[permKeyEdit])
	}
}

// TestRootGlobs_EmitsLeadingSlashStrippedAbsoluteForm guards the implement-phase
// regression where worktree edits were denied: OpenCode evaluates an edit against
// a leading-slash-stripped absolute path ("Users/x/.../repo/file") when its
// git-worktree detection resolves to "/", so an absolute ("/Users/...") or
// cwd-relative glob matches neither and the write falls through to the catch-all
// deny. The stripped form must also be emitted.
func TestRootGlobs_EmitsLeadingSlashStrippedAbsoluteForm(t *testing.T) {
	workDir := "/Users/x/.agentic-workflow/worktrees/feat"
	root := "/Users/x/.agentic-workflow/worktrees/feat/repo"
	got := rootGlobs(workDir, root)
	for _, want := range []string{
		"Users/x/.agentic-workflow/worktrees/feat/repo",
		"Users/x/.agentic-workflow/worktrees/feat/repo/**",
	} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("rootGlobs(%q, %q) = %v; missing leading-slash-stripped pattern %q", workDir, root, got, want)
		}
	}
}

// TestRootGlobs_NoWorkDirIsAbsoluteAndStrippedOnly confirms that with no cwd
// known, only the absolute and its leading-slash-stripped forms are emitted (no
// cwd-relative form).
func TestRootGlobs_NoWorkDirIsAbsoluteAndStrippedOnly(t *testing.T) {
	globs := rootGlobs("", "/Users/x/state")
	want := []string{"/Users/x/state", "/Users/x/state/**", "Users/x/state", "Users/x/state/**"}
	if len(globs) != len(want) {
		t.Fatalf("rootGlobs(\"\", root) = %v, want %v", globs, want)
	}
	for i := range want {
		if globs[i] != want[i] {
			t.Fatalf("rootGlobs(\"\", root) = %v, want %v", globs, want)
		}
	}
}
