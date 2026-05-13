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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestBranchName(t *testing.T) {
	t.Parallel()

	got := BranchName("fix-query")
	if got != "feature/fix-query" {
		t.Errorf("BranchName = %q, want %q", got, "feature/fix-query")
	}
}

func TestBranchExistsOnRemote(t *testing.T) {
	t.Parallel()

	localDir, bareDir := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, localDir, "feature/existing-branch")
	testutil.CommitFile(t, localDir, "feature.txt", "feature\n", "feature commit")
	testutil.SimulatePush(t, localDir, bareDir, "feature/existing-branch", "feature/existing-branch")

	t.Run("existing branch returns true", func(t *testing.T) {
		if !BranchExistsOnRemote(localDir, "feature/existing-branch") {
			t.Error("expected true for existing remote branch")
		}
	})

	t.Run("non-existing branch returns false", func(t *testing.T) {
		if BranchExistsOnRemote(localDir, "feature/nonexistent") {
			t.Error("expected false for non-existing remote branch")
		}
	})
}
