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
)

// These tests fork real git and follow the source-update convention of not
// using t.Parallel(): the race-detector binary misbehaves when many
// fork-heavy tests run at once.

func TestReconcileSourceUpdateStateClassifiesObservedTip(t *testing.T) {
	fx := newUpdateFixture(t)
	local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
	origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")

	report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, false, OriginCheckOptions{})
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
	report, err = ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, false, OriginCheckOptions{})
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
	report, err = ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, false, OriginCheckOptions{})
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

	report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, false, OriginCheckOptions{})
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
		if _, err := ReconcileSourceUpdateState(context.Background(), fx.repo, branch, expectedLocal, origin, false, OriginCheckOptions{}); err == nil {
			t.Fatalf("%s: ReconcileSourceUpdateState() succeeded; want a closed failure", name)
		}
	}
}

func TestReconcileSourceUpdateStateObservesOriginalCheckout(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
	origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")

	// Without the original-checkout binding the settlement reports the tip
	// only: a ref-only attempt's checkout state is not part of its claim.
	report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, false, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.State != SourceReconcileOriginalTip || report.Checkout != nil {
		t.Fatalf("report = %+v; want the original tip and no checkout observation", report)
	}

	// An original-checkout binding observes the clean checkout holding the
	// branch at the observed tip.
	report, err = ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, true, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Checkout == nil || report.Checkout.State != CheckoutReconcileClean ||
		report.Checkout.HeadRef != "refs/heads/main" || report.Checkout.HeadSHA != local {
		t.Fatalf("checkout = %+v; want a clean checkout at the original tip", report.Checkout)
	}

	// A completed fast-forward settles on the target tip with the checkout
	// consistent with it: the whole-checkout completion is proved, not
	// inferred from the ref alone.
	if _, err := UpdateSourceFromOrigin(context.Background(), fx.repo, fx.originalExpectation(t, LocalSourceModeDefault), SourceUpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	report, err = ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, true, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.State != SourceReconcileTargetPresent || report.Checkout == nil ||
		report.Checkout.State != CheckoutReconcileClean || report.Checkout.HeadSHA != origin {
		t.Fatalf("report = %+v; want the target tip with a clean consistent checkout", report)
	}
}

func TestReconcileSourceUpdateStateObservesDirtyAndBusyCheckouts(t *testing.T) {
	for name, dirty := range map[string]func(t *testing.T, repo string) CheckoutReconcileState{
		"dirty": func(t *testing.T, repo string) CheckoutReconcileState {
			if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("local edit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return CheckoutReconcileDirty
		},
		"operation in progress": func(t *testing.T, repo string) CheckoutReconcileState {
			gitDir := runGitUpdateTest(t, repo, "rev-parse", "--git-dir")
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(repo, gitDir)
			}
			if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte("marker\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return CheckoutReconcileOperationInProgress
		},
	} {
		fx := newOriginalCheckoutFixture(t)
		local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
		origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")
		want := dirty(t, fx.repo)

		report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, true, OriginCheckOptions{})
		if err != nil {
			t.Fatalf("%s: reconcile: %v", name, err)
		}
		if report.Checkout == nil || report.Checkout.State != want {
			t.Fatalf("%s: checkout = %+v; want state %q", name, report.Checkout, want)
		}
		// The tip settlement is unaffected by the checkout observation.
		if report.State != SourceReconcileOriginalTip || report.LocalSHA != local {
			t.Fatalf("%s: report = %+v; want the original tip", name, report)
		}
	}
}

func TestReconcileSourceUpdateStateUnobservedCheckoutFailsTruthfully(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
	origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")
	options := OriginCheckOptions{Runner: BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult {
		if len(args) > 0 && args[0] == "status" {
			return BranchProbeCommandResult{ExitCode: 128, Diagnostics: "checkout inspection failed"}
		}
		return ExecBranchProbeRunner{}.Run(ctx, repoPath, args, diagnosticLimit)
	})}

	report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, true, options)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.State != SourceReconcileOriginalTip {
		t.Fatalf("report = %+v; want the settled tip", report)
	}
	if report.Checkout == nil || report.Checkout.State != CheckoutReconcileUnobserved {
		t.Fatalf("checkout = %+v; want an unobserved checkout that supports no whole-checkout claim", report.Checkout)
	}
}

func TestReconcileSourceUpdateStateOriginalCheckoutNoLongerHoldsBranch(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	local := gitUpdateSHA(t, fx.repo, "refs/heads/main")
	origin := gitUpdateSHA(t, fx.bare, "refs/heads/main")
	runGitUpdateTest(t, fx.repo, "checkout", "-b", "elsewhere")

	report, err := ReconcileSourceUpdateState(context.Background(), fx.repo, "main", local, origin, true, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.State != SourceReconcileOriginalTip {
		t.Fatalf("report = %+v; want the original tip", report)
	}
	if report.Checkout != nil {
		t.Fatalf("checkout = %+v; want no checkout observation once the original checkout no longer holds the branch", report.Checkout)
	}
}
