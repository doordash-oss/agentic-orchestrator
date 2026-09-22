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
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestConflictResolverHandler_AllowsOnlyConflictedPathsInsideWorktree(t *testing.T) {
	workDir := t.TempDir()
	handler := &ConflictResolverHandler{
		WorkDir:       workDir,
		ConflictPaths: []string{"internal/agent/foo.go", "internal/agent/sub/bar.go"},
	}

	// Reads and exploration are open, anywhere.
	requirePermissionAllowed(t, handler, "Read", `{"file_path":"`+filepath.Join(workDir, "internal", "agent", "foo.go")+`"}`)
	requirePermissionAllowed(t, handler, "Grep", `{"pattern":"<<<<<<<"}`)
	requirePermissionAllowed(t, handler, "LS", `{}`)
	requirePermissionAllowed(t, handler, "Agent", `{"prompt":"explore the surrounding code"}`)

	// Edit and write on a conflicted path inside the workdir are allowed,
	// including a nested conflicted path.
	requirePermissionAllowed(t, handler, toolNameEdit, `{"file_path":"`+filepath.Join(workDir, "internal", "agent", "foo.go")+`"}`)
	requirePermissionAllowed(t, handler, toolNameWrite, `{"file_path":"`+filepath.Join(workDir, "internal", "agent", "foo.go")+`"}`)
	requirePermissionAllowed(t, handler, toolNameWrite, `{"file_path":"`+filepath.Join(workDir, "internal", "agent", "sub", "bar.go")+`"}`)

	// A non-conflicted path inside the workdir is denied.
	requirePermissionDenied(t, handler, toolNameEdit, `{"file_path":"`+filepath.Join(workDir, "internal", "agent", "other.go")+`"}`)
	// A conflicted-looking path outside the workdir is denied.
	outside := t.TempDir()
	requirePermissionDenied(t, handler, toolNameWrite, `{"file_path":"`+filepath.Join(outside, "internal", "agent", "foo.go")+`"}`)
	// A parent-relative escape from the workdir is denied.
	requirePermissionDenied(t, handler, toolNameEdit, `{"file_path":"`+workDir+"/../"+filepath.Join("internal", "agent", "foo.go")+`"}`)
	// A path inside the workdir that is not itself a conflicted file is denied.
	requirePermissionDenied(t, handler, toolNameWrite, `{"file_path":"`+workDir+`"}`)
}

func TestConflictResolverHandler_ShellIsReadOnlyInspectionOnly(t *testing.T) {
	workDir := t.TempDir()
	handler := &ConflictResolverHandler{
		WorkDir:       workDir,
		ConflictPaths: []string{"internal/agent/foo.go"},
	}

	// Read-only inspection, including read-only git, is allowed.
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"ls `+workDir+`"}`)
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"cat internal/agent/foo.go"}`)
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"git diff --stat"}`)
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"git status"}`)

	// Mutating git commands are denied.
	for _, command := range []string{
		"git commit -m resolved",
		"git checkout -- internal/agent/foo.go",
		"git add internal/agent/foo.go",
		"git rebase --continue",
	} {
		t.Run("denies "+command, func(t *testing.T) {
			requirePermissionDenied(t, handler, toolNameBash, `{"command":"`+command+`"}`)
		})
	}

	// Redirection into the worktree and file-creating commands are denied.
	requirePermissionDenied(t, handler, toolNameBash, `{"command":"echo x > `+filepath.Join(workDir, "internal", "agent", "foo.go")+`"}`)
	requirePermissionDenied(t, handler, toolNameBash, `{"command":"touch `+filepath.Join(workDir, "new.go")+`"}`)
	requirePermissionDenied(t, handler, toolNameBash, `{"command":"mkdir -p `+filepath.Join(workDir, "internal")+`"}`)
}

func TestConflictResolverHandler_DeniesUndeclaredToolsWithGuardrailReason(t *testing.T) {
	handler := &ConflictResolverHandler{WorkDir: t.TempDir(), ConflictPaths: []string{"a.go"}}

	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{ToolName: "KillShell", Input: `{}`})
	if err != nil {
		t.Fatalf("CanUseTool(KillShell) error = %v", err)
	}
	if decision.Behavior != DecisionDeny {
		t.Fatalf("CanUseTool(KillShell).Behavior = %q, want deny", decision.Behavior)
	}
	if !strings.Contains(decision.Reason, "conflict resolution may not use KillShell") {
		t.Fatalf("Reason = %q, want the guardrail naming the denied tool", decision.Reason)
	}
}
