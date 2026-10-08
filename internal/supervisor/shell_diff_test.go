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
