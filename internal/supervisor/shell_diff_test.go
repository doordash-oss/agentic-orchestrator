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

package supervisor

import (
	"context"
	"encoding/json"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shellRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nconst Value = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "-C", root, "add", "app.go")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCoordinatorShellWritesStreamDiffBeforeToolReturns(t *testing.T) {
	root := shellRepo(t)
	// Pre-existing dirty content must be the baseline, not an agent change.
	os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nconst Value = 2\n"), 0600)
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	c.opts.WorkDir = root
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "edit through shell", "", "shell-diff"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	input, _ := json.Marshal(map[string]string{"command": "python3 rewrite.py", "cwd": root})
	sess.emit(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "tool_use", Name: "Bash", ID: "shell-1", Input: input}}}}})
	cmd := exec.Command("sh", "-c", "printf 'package app\nconst Value = 3\n' > app.go; printf 'new file\n' > new.txt")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var changes []llm.FileChangeEvent
	for time.Now().Before(deadline) {
		changes = nil
		for _, rec := range allRecords(t, c) {
			var data ContentData
			json.Unmarshal(rec.Data, &data)
			changes = append(changes, data.FileChanges...)
		}
		if len(changes) >= 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(changes) != 2 {
		t.Fatalf("no live shell diff nuggets: %+v", changes)
	}
	if c.State().Lifecycle != LifecycleRunning {
		t.Fatal("diff only arrived after turn ended")
	}
	var patch string
	for _, change := range changes {
		patch += change.Detail
	}
	if !strings.Contains(patch, "-const Value = 2") || !strings.Contains(patch, "+const Value = 3") || strings.Contains(patch, "-const Value = 1") {
		t.Fatalf("wrong baseline: %s", patch)
	}
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
}

func TestShellObserverIncrementalSnapshotsAndExclusions(t *testing.T) {
	root := shellRepo(t)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=before"), 0600)
	outside := filepath.Join(t.TempDir(), "private.txt")
	os.WriteFile(outside, []byte("private before"), 0600)
	os.Symlink(outside, filepath.Join(root, "link.txt"))
	repo := &shellRepository{root: root}
	var ok bool
	repo.files, ok = repo.snapshot()
	if !ok {
		t.Fatal("baseline failed")
	}
	os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nconst Value = 9\n"), 0600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=after"), 0600)
	os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored"), 0600)
	os.WriteFile(outside, []byte("private after"), 0600)
	os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2}, 0600)
	changes := repo.changes()
	if len(changes) != 1 || !strings.HasSuffix(changes[0].Path, "app.go") {
		t.Fatalf("unexpected changes: %+v", changes)
	}
	if again := repo.changes(); len(again) != 0 {
		t.Fatalf("duplicate nuggets: %+v", again)
	}
	os.Remove(filepath.Join(root, "app.go"))
	changes = repo.changes()
	if len(changes) != 1 || changes[0].Operation != "delete" || !strings.Contains(changes[0].Detail, "-const Value = 9") {
		t.Fatalf("wrong deletion: %+v", changes)
	}
}

func TestShellRootsFindLinkedWorktreeInsideScript(t *testing.T) {
	root := shellRepo(t)
	input, _ := json.Marshal(map[string]string{"command": "python3 - <<'PY'\nroot = '" + root + "'\nPY"})
	roots := shellRoots("/", llm.ContentBlock{Input: input})
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0] != canonical {
		t.Fatalf("roots: %+v", roots)
	}
}

func TestShellObserverNativeCreationHasOneOwnerAndLaterShellEditsRemainVisible(t *testing.T) {
	root := shellRepo(t)
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	c.opts.WorkDir = root
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "create and edit", "", "native-diff"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	emitTool := func(id, name string, input any) {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		sess.emit(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "tool_use", ID: id, Name: name, Input: raw}}}}})
	}
	emitResult := func(id string, failed bool, changes ...llm.FileChangeEvent) {
		sess.emit(llm.SDKMessage{Type: "user", User: &llm.UserMessage{Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "tool_result", ToolUseID: id, IsError: failed, Content: json.RawMessage(`"done"`)}}}}, FileChanges: changes})
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	changes := func() (native, observed []llm.FileChangeEvent) {
		t.Helper()
		for _, record := range allRecords(t, c) {
			var data ContentData
			if err := json.Unmarshal(record.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.ObservedFiles {
				observed = append(observed, data.FileChanges...)
			} else {
				native = append(native, data.FileChanges...)
			}
		}
		return
	}

	// A prior read-only shell tool activates observation for the whole turn.
	emitTool("shell-1", "Bash", map[string]string{"command": "pwd", "cwd": root})
	emitResult("shell-1", false)
	path := filepath.Join(root, "created.ts")
	emitTool("native-1", "Write", map[string]any{"file_path": path, "paths": []string{path}})
	write("created.ts", "export const value = 1;\n")
	write("shell.txt", "a genuine parallel shell write\n")
	// Force the same capture as the polling timer, while the native edit is pending.
	observer := sess.observer.(*generationObserver)
	observer.diff.mu.Lock()
	observer.captureShellDiffLocked(sess.IDVal, observer.diff.turn)
	observer.diff.mu.Unlock()
	if _, observed := changes(); len(observed) != 1 || !strings.HasSuffix(observed[0].Path, "shell.txt") {
		t.Fatalf("native creation leaked into observer, or parallel shell write lost: %+v", observed)
	}
	emitResult("native-1", false, llm.FileChangeEvent{Path: path, Operation: "write", Detail: "export const value = 1;\n"})
	if native, observed := changes(); len(native) != 1 || len(observed) != 1 {
		t.Fatalf("creation reported more than once: native=%+v observed=%+v", native, observed)
	}

	write("created.ts", "export const value = 2;\n")
	emitResult("shell-2", false)
	if _, observed := changes(); len(observed) != 2 || !strings.Contains(observed[1].Detail, "-export const value = 1;") || !strings.Contains(observed[1].Detail, "+export const value = 2;") {
		t.Fatalf("later shell edit lost or used pre-native baseline: %+v", observed)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	emitResult("shell-3", false)
	write("created.ts", "export const value = 3;\n")
	emitResult("shell-4", false)
	if _, observed := changes(); len(observed) != 4 || observed[2].Operation != "delete" || observed[3].Operation != "add" {
		t.Fatalf("delete/recreate incorrectly deduplicated: %+v", observed)
	}

	// An unsuccessful native edit may have left a partial write: retain it.
	emitTool("native-failed", "Edit", map[string]string{"file_path": path})
	write("created.ts", "partial native write\n")
	observer.diff.mu.Lock()
	observer.captureShellDiffLocked(sess.IDVal, observer.diff.turn)
	observer.diff.mu.Unlock()
	emitResult("native-failed", true)
	if _, observed := changes(); len(observed) != 5 || !strings.Contains(observed[4].Detail, "+partial native write") {
		t.Fatalf("failed native edit's partial mutation was lost: %+v", observed)
	}

	// Success alone is insufficient: a paths-only Codex call needs result
	// metadata, otherwise the observer is the only source of a visible diff.
	emitTool("native-missing-metadata", "Write", map[string]string{"file_path": path})
	write("created.ts", "successful but unreported write\n")
	emitResult("native-missing-metadata", false)
	if _, observed := changes(); len(observed) != 6 || !strings.Contains(observed[5].Detail, "+successful but unreported write") {
		t.Fatalf("successful native edit without metadata was lost: %+v", observed)
	}

	// Claude's content-bearing tool call already projects a card itself.
	emitTool("native-content", "Write", map[string]string{"file_path": path, "content": "content from call\n"})
	write("created.ts", "content from call\n")
	emitResult("native-content", false)
	if _, observed := changes(); len(observed) != 6 {
		t.Fatalf("content-bearing native call reported twice: %+v", observed)
	}

	// A multi-file completion may omit metadata for just one target.
	otherPath := filepath.Join(root, "other.ts")
	emitTool("native-partial-metadata", "Write", map[string]any{"file_path": path, "paths": []string{path, otherPath}})
	write("created.ts", "reported update\n")
	write("other.ts", "unreported creation\n")
	emitResult("native-partial-metadata", false, llm.FileChangeEvent{Path: path, Operation: "write", Detail: "reported update\n"})
	if _, observed := changes(); len(observed) != 7 || !strings.HasSuffix(observed[6].Path, "other.ts") {
		t.Fatalf("partially reported completion hid its unreported target: %+v", observed)
	}

	// Ending the root turn without a matching tool result must retain any
	// partial native write; a child task result must not release ownership.
	emitTool("native-unfinished", "Write", map[string]string{"file_path": path})
	write("created.ts", "unfinished native write\n")
	childResult := successResult()
	childResult.Origin.Kind = llm.EventOriginTask
	sess.emit(childResult)
	if _, observed := changes(); len(observed) != 7 {
		t.Fatalf("child task result released root native ownership: %+v", observed)
	}
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	if _, observed := changes(); len(observed) != 8 || !strings.Contains(observed[7].Detail, "+unfinished native write") {
		t.Fatalf("root result lost unfinished native mutation: %+v", observed)
	}
}

func TestNativeChangePathsCanonicalizeMissingNestedDirectories(t *testing.T) {
	root := shellRepo(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"file_path": filepath.Join(alias, "new", "nested", "created.ts")})
	paths := nativeChangePaths("/", llm.ContentBlock{Type: "tool_use", Name: "Write", Input: input})
	want := filepath.Join(canonical, "new", "nested", "created.ts")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("paths = %v, want %s", paths, want)
	}
}
