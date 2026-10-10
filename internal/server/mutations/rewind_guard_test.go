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

package mutations

import (
	"errors"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestWireRewindWarningsClassifiesAndRedacts(t *testing.T) {
	warnings := wireRewindWarnings([]feature.RewindWarning{
		{Kind: feature.RewindWarningPullRequestClose, Repo: "repo-a", Branch: "feature/x", Err: errors.New(" raw initial prompt private-token ")},
		{Kind: feature.RewindWarningBackupBranch, Repo: "repo-b", Err: errors.New("boom")},
		{Kind: feature.RewindWarningWorktreeReset, Repo: "repo-c"},
	})
	if len(warnings) != 3 {
		t.Fatalf("wireRewindWarnings() length = %d; want 3", len(warnings))
	}
	wantCodes := []errcat.Code{
		errcat.RewindPullRequestCloseFailed,
		errcat.RewindBackupBranchFailed,
		errcat.RewindWorktreeResetFailed,
	}
	for i, want := range wantCodes {
		if warnings[i].Code != string(want) {
			t.Fatalf("warnings[%d].Code = %q; want %q", i, warnings[i].Code, want)
		}
		if warnings[i].Class != serverruntime.ErrorClass(errcat.ClassWarning) {
			t.Fatalf("warnings[%d].Class = %q; want warning", i, warnings[i].Class)
		}
	}
	if got, want := warnings[0].Diagnostics, "[redacted prompt] [redacted]"; got != want {
		t.Fatalf("warnings[0].Diagnostics = %q; want %q", got, want)
	}
	if warnings[2].Diagnostics != "" {
		t.Fatalf("warnings[2].Diagnostics = %q; want empty for a nil cause", warnings[2].Diagnostics)
	}
	if warnings[0].Context == nil || len(warnings[0].Context.Repositories) != 1 ||
		warnings[0].Context.Repositories[0].Name != "repo-a" ||
		warnings[0].Context.Repositories[0].Branch != "feature/x" {
		t.Fatalf("warnings[0].Context = %+v; want the repo-a repositories block with branch", warnings[0].Context)
	}
}
