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

package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestFinalReviewRootArtifactScrub_RemovesOnlyUntrackedRootOrchestrationFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-git root-artifact scrub regression in short mode; extended orchestrator run owns git ls-files semantics")
	}

	repo := testutil.InitGitRepo(t)
	for _, name := range []string{"phase_complete", "progress.md", "verification-report.yaml", "review-feedback.md"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("stray\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", "phase_complete"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write nested marker: %v", err)
	}
	testutil.CommitFile(t, repo, "meta.yaml", "tracked\n", "track meta")
	if err := os.WriteFile(filepath.Join(repo, "meta.yaml"), []byte("tracked change\n"), 0o644); err != nil {
		t.Fatalf("modify tracked meta.yaml: %v", err)
	}

	o := New(Deps{CmdRunner: agent.NewExecCommandRunner()}, Hooks{})
	if err := o.scrubFinalReviewRootArtifacts(context.Background(), repo); err != nil {
		t.Fatalf("scrubFinalReviewRootArtifacts() error = %v", err)
	}

	for _, name := range []string{"phase_complete", "progress.md", "verification-report.yaml", "review-feedback.md"} {
		if _, err := os.Stat(filepath.Join(repo, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists or stat errored: %v", name, err)
		}
	}
	for _, name := range []string{"meta.yaml", filepath.Join("nested", "phase_complete")} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatalf("%s was removed, want preserved: %v", name, err)
		}
	}
}
