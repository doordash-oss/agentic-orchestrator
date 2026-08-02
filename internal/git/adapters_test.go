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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// ---------------------------------------------------------------------------
// Compile-time interface satisfaction checks — git adapters
// ---------------------------------------------------------------------------

var _ ports.Publisher = (*git.PublishAdapter)(nil)
var _ ports.RebaseOperator = (*git.RebaseAdapter)(nil)
var _ feature.BranchOps = (*git.BranchAdapter)(nil)
var _ feature.WorktreeOps = (*git.WorktreeManager)(nil)

// ---------------------------------------------------------------------------
// Compile-time interface satisfaction checks — session widening
// ---------------------------------------------------------------------------

var _ ports.SessionHandle = (*session.Session)(nil)
var _ ports.SessionManager = (*session.Manager)(nil)

// ---------------------------------------------------------------------------
// Error-bridging tests
// ---------------------------------------------------------------------------

func TestPublishAdapter_HasUncommittedChanges_BridgesErrorReturn(t *testing.T) {
	// The underlying git.HasUncommittedChanges returns a bare bool.
	// The adapter bridges to (bool, error) returning nil error.
	a := &git.PublishAdapter{}
	// Use a non-existent path — function returns false when git fails.
	got, err := a.HasUncommittedChanges("/nonexistent-path-for-test")
	if err != nil {
		t.Errorf("expected nil error from bridged adapter, got %v", err)
	}
	if got {
		t.Error("expected false for nonexistent path")
	}
}

func TestPublishAdapter_HasLocalCommits_BridgesErrorReturn(t *testing.T) {
	a := &git.PublishAdapter{}
	got, err := a.HasLocalCommits("/nonexistent-path-for-test")
	if err != nil {
		t.Errorf("expected nil error from bridged adapter, got %v", err)
	}
	if got {
		t.Error("expected false for nonexistent path")
	}
}

func TestRebaseAdapter_IsBehindRemote_BridgesErrorReturn(t *testing.T) {
	a := &git.RebaseAdapter{}
	got, err := a.IsBehindRemote("/nonexistent-path-for-test", "main")
	if err != nil {
		t.Errorf("expected nil error from bridged adapter, got %v", err)
	}
	if got {
		t.Error("expected false for nonexistent path")
	}
}

func TestRebaseAdapter_PRBaseBranch_BridgesErrorReturn(t *testing.T) {
	a := &git.RebaseAdapter{}
	got, err := a.PRBaseBranch("/nonexistent-path-for-test", "https://example.com/pr/1")
	if err != nil {
		t.Errorf("expected nil error from bridged adapter, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for nonexistent path, got %q", got)
	}
}

func TestBranchAdapter_BridgesErrorReturn(t *testing.T) {
	a := &git.BranchAdapter{}
	// Use a temp dir that is NOT a git repo so git commands return empty.
	nonGitDir := t.TempDir()

	t.Run("BranchExistsOnRemote", func(t *testing.T) {
		got, err := a.BranchExistsOnRemote(nonGitDir, "main")
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if got {
			t.Error("expected false for non-git dir")
		}
	})

	t.Run("HasOriginRemote", func(t *testing.T) {
		got, err := a.HasOriginRemote(nonGitDir)
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if got {
			t.Error("expected false for non-git dir")
		}
	})

	t.Run("DefaultBranch", func(t *testing.T) {
		// DefaultBranch has a hardcoded "main" fallback, so it never returns
		// empty. We only verify the adapter bridges to (string, nil).
		got, err := a.DefaultBranch(nonGitDir)
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if got == "" {
			t.Error("expected non-empty fallback from DefaultBranch")
		}
	})

	t.Run("CurrentBranch", func(t *testing.T) {
		got, err := a.CurrentBranch(nonGitDir)
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Variadic normalization test
// ---------------------------------------------------------------------------

func TestPublishAdapter_BranchName_PureComputation(t *testing.T) {
	a := &git.BranchAdapter{}
	got := a.BranchName("my-feature")
	if got == "" {
		t.Error("expected non-empty branch name")
	}
}

// ---------------------------------------------------------------------------
// Source-scan regression guard
// ---------------------------------------------------------------------------

func TestAdaptersNoDirectExecCommand(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(".", "adapters*.go"))
	if err != nil {
		t.Fatalf("glob adapters*.go: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no adapter files found — glob may be wrong")
	}

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		content := string(data)
		if strings.Contains(content, "exec.Command") {
			t.Errorf("%s contains exec.Command — adapters must delegate to existing git package functions", path)
		}
	}
}
