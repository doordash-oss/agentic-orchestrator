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

package git_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestHasUncommittedChangesExcludingUntracked(t *testing.T) {
	repo := testutil.InitGitRepo(t)

	if git.HasUncommittedChangesExcludingUntracked(repo, "progress.md") {
		t.Fatal("clean repo reported dirty")
	}

	// Known orchestration artifact stranded untracked at the root: ignored.
	if err := os.WriteFile(filepath.Join(repo, "progress.md"), []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("write stray artifact: %v", err)
	}
	if git.HasUncommittedChangesExcludingUntracked(repo, "progress.md", "meta.yaml") {
		t.Fatal("untracked known artifact must be ignored")
	}
	if !git.HasUncommittedChanges(repo) {
		t.Fatal("sanity: HasUncommittedChanges must still see the stray file")
	}

	// An unrelated untracked file still counts.
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}
	if !git.HasUncommittedChangesExcludingUntracked(repo, "progress.md") {
		t.Fatal("untracked unknown file must count as dirty")
	}

	// A tracked modification of an excluded name still counts.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	if !git.HasUncommittedChangesExcludingUntracked(repo, "README.md") {
		t.Fatal("tracked change to an excluded-name path must count as dirty")
	}
}
