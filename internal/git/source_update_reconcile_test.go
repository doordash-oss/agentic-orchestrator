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
	"testing"
)

// These tests fork real git and follow the source-update convention of not
// using t.Parallel(): the race-detector binary misbehaves when many
// fork-heavy tests run at once.

func TestReconcileSourceUpdateStateClassifiesObservedTip(t *testing.T) {
	fx := newUpdateFixture(t)
	local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
	origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")

	report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("ReconcileSourceUpdateState() error = %v", err)
	}
	if report.State != SourceReconcileOriginalTip || report.LocalSHA != local {
		t.Fatalf("report = %+v; want original tip %s", report, local)
	}

	// The branch advances to the expected target: the read settles on the
	// target observation, never on transport completion.
	runGitUpdateTest(t, fx.repo, "fetch", "origin")
	if err := casUpdateBranchRef(context.Background(), fx.repo, "main", local, origin, SourceUpdateOptions{}, nil); err != nil {
		t.Fatalf("advance main: %v", err)
	}
	report, err = ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("ReconcileSourceUpdateState() error = %v", err)
	}
	if report.State != SourceReconcileTargetPresent || report.LocalSHA != origin {
		t.Fatalf("report = %+v; want target present %s", report, origin)
	}

	// A third commit moves the branch: the read reports the changed state
	// without inferring anything about the attempt.
	third := runGitUpdateTest(t, fx.repo, "commit-tree", "refs/heads/main^{tree}", "-m", "third")
	runGitUpdateTest(t, fx.repo, "update-ref", "refs/heads/main", third)
	report, err = ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("ReconcileSourceUpdateState() error = %v", err)
	}
	if report.State != SourceReconcileLocalChanged || report.LocalSHA != third {
		t.Fatalf("report = %+v; want local changed %s", report, third)
	}
}

func TestReconcileSourceUpdateStateReportsMissingBranch(t *testing.T) {
	fx := newUpdateFixture(t)
	local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
	origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")
	runGitUpdateTest(t, fx.repo, "update-ref", "-d", "refs/heads/main")

	report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("ReconcileSourceUpdateState() error = %v", err)
	}
	if report.State != SourceReconcileBranchMissing || report.LocalSHA != "" {
		t.Fatalf("report = %+v; want branch missing", report)
	}
}

func TestReconcileSourceUpdateStateRejectsMalformedExpectations(t *testing.T) {
	fx := newUpdateFixture(t)
	local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
	origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")
	for name, mutate := range map[string]func(e *string, b *string){
		"short local sha": func(e *string, _ *string) { *e = "abc123" },
		"bad branch":      func(_ *string, b *string) { *b = "../escape" },
	} {
		expectedLocal, branch := local, "main"
		mutate(&expectedLocal, &branch)
		if _, err := ReconcileSourceUpdateState(context.Background(), fx.repo, branch, expectedLocal, origin, OriginCheckOptions{}); err == nil {
			t.Fatalf("%s: ReconcileSourceUpdateState() succeeded; want a closed failure", name)
		}
	}
}
