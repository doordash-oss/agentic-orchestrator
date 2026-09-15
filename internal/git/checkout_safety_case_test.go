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

package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// These tests fork real git and follow the source-update convention of not
// using t.Parallel().

func TestUpdateSourceFromOriginalCheckoutCaseOnlyIgnoredCollisionRefuses(t *testing.T) {
	// Regression: on a case-insensitive filesystem an ignored ADDED.txt and
	// the incoming tracked added.txt are one directory entry, and the
	// fast-forward checkout treats ignored content as expendable. The
	// collision check compared raw bytes, so the update silently replaced
	// the local bytes.
	fx := newOriginalCheckoutFixture(t)
	if folds, err := checkoutFilesystemFoldsCase(fx.repo); err != nil {
		t.Fatalf("probing the fixture filesystem: %v", err)
	} else if !folds {
		t.Skip("the filesystem is case-sensitive; a case-only collision cannot occur")
	}
	expected := fx.originalExpectation(t, LocalSourceModeCurrent)
	if err := os.WriteFile(filepath.Join(fx.repo, ".git", "info", "exclude"), []byte("ADDED.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(fx.repo, "ADDED.txt")
	if err := os.WriteFile(precious, []byte("precious local data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result == SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want a refusal for a case-only ignored-path collision", outcome.Result, outcome.Reason)
	}
	got, readErr := os.ReadFile(precious)
	if readErr != nil || string(got) != "precious local data\n" {
		t.Fatalf("ADDED.txt = %q err = %v; want the ignored local bytes preserved", got, readErr)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
	if got := gitUpdateSHA(t, fx.repo, "HEAD^{commit}"); got != expected.CheckoutHeadSHA {
		t.Fatalf("HEAD = %s; want the pre-update checkout preserved", got)
	}
}

func TestCheckoutIgnoredPathCollisionHonorsRecordedCaseFolding(t *testing.T) {
	// The recorded worktree property, not the host filesystem, decides how
	// paths fold, so a case-only collision is provable on any filesystem.
	repo := testutil.InitGitRepo(t)
	runGitUpdateTest(t, repo, "config", "core.ignorecase", "true")
	old := gitUpdateSHA(t, repo, "HEAD")
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", "added.txt"), []byte("added remotely\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, repo, "add", "-A")
	runGitUpdateTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "add nested/added.txt")
	incoming := gitUpdateSHA(t, repo, "HEAD")
	runGitUpdateTest(t, repo, "reset", "--hard", old)

	// Hold the same directory entry locally under a differently cased,
	// ignored spelling.
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("NESTED/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "NESTED"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "NESTED", "ADDED.txt"), []byte("precious local data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runGitUpdateTest(t, repo, "ls-files", "--others", "--ignored", "--exclude-standard"); got == "" {
		t.Fatal("the ignored listing is empty; the probe file must be ignored")
	}

	collision, err := checkoutIgnoredPathCollision(context.Background(), repo, old, incoming, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("checkoutIgnoredPathCollision: %v", err)
	}
	if !collision {
		t.Fatal("collision = false; want a collision when the recorded folding makes the paths one entry")
	}
}

func TestCheckoutIgnoredPathCollisionAllowsUnrelatedIgnoredContent(t *testing.T) {
	// Case folding must not turn unrelated ignored content into a refusal.
	repo := testutil.InitGitRepo(t)
	runGitUpdateTest(t, repo, "config", "core.ignorecase", "true")
	old := gitUpdateSHA(t, repo, "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "added.txt"), []byte("added remotely\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, repo, "add", "-A")
	runGitUpdateTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "add added.txt")
	incoming := gitUpdateSHA(t, repo, "HEAD")
	runGitUpdateTest(t, repo, "reset", "--hard", old)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("local.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "local.log"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	collision, err := checkoutIgnoredPathCollision(context.Background(), repo, old, incoming, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("checkoutIgnoredPathCollision: %v", err)
	}
	if collision {
		t.Fatal("collision = true; want unrelated ignored content to remain allowed")
	}
}

func TestCheckoutPathFoldingKeyFoldsOnlyRecordedProperties(t *testing.T) {
	const decomposed = "cafe\u0301/Added.txt" // combining acute
	const precomposed = "caf\u00e9/Added.txt" // precomposed
	if got := (checkoutPathFolding{}).key(decomposed); got != decomposed {
		t.Fatalf("unrecorded folding altered %q to %q", decomposed, got)
	}
	if (checkoutPathFolding{}).key(precomposed) == (checkoutPathFolding{}).key(decomposed) {
		t.Fatal("unrecorded folding merged distinct spellings")
	}
	if folding := (checkoutPathFolding{precompose: true}); folding.key(decomposed) != folding.key(precomposed) {
		t.Fatal("precomposing folding must give both normalizations one key")
	}
	if folding := (checkoutPathFolding{foldCase: true}); folding.key("NESTED/ADDED.txt") != folding.key("nested/added.txt") {
		t.Fatal("case folding must give both spellings one key")
	}
}
