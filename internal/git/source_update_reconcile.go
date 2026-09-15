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
	"fmt"
	"strings"
)

// SourceReconcileState is the typed settlement of one uncertain update
// attempt: what the requested branch's tip proves after the attempt can no
// longer mutate. It is an observation of the ref, never an inference about
// transport completion.
type SourceReconcileState string

const (
	// SourceReconcileTargetPresent reports the branch tip is the expected
	// origin SHA the attempt was expected to advance it to.
	SourceReconcileTargetPresent SourceReconcileState = "expected_target_present"
	// SourceReconcileOriginalTip reports the branch tip is still the
	// expected local SHA displayed before the attempt.
	SourceReconcileOriginalTip SourceReconcileState = "original_tip_remains"
	// SourceReconcileLocalChanged reports the branch tip is neither
	// expected value: something else moved it, and the observation says
	// nothing about whether the attempt succeeded.
	SourceReconcileLocalChanged SourceReconcileState = "local_state_changed"
	// SourceReconcileBranchMissing reports the branch no longer resolves.
	SourceReconcileBranchMissing SourceReconcileState = "branch_missing"
)

// CheckoutReconcileState is the observed state of the original checkout that
// held the attempted update's branch. The target branch SHA alone only proves
// the target commit is present; the original tip alone does not prove the
// index and files were untouched. Only a clean checkout whose HEAD is the
// observed branch tip supports a whole-checkout completion claim.
type CheckoutReconcileState string

const (
	// CheckoutReconcileClean reports a checkout with a consistent index and
	// tracked working tree and no in-progress Git operation.
	CheckoutReconcileClean CheckoutReconcileState = "clean"
	// CheckoutReconcileDirty reports a checkout with staged, unstaged, or
	// otherwise inconsistent tracked content — possibly a partial or
	// externally changed checkout that truthful guidance must not attribute
	// to Agentico.
	CheckoutReconcileDirty CheckoutReconcileState = "dirty"
	// CheckoutReconcileOperationInProgress reports a merge, rebase,
	// cherry-pick, or revert in progress in the checkout.
	CheckoutReconcileOperationInProgress CheckoutReconcileState = "operation_in_progress"
	// CheckoutReconcileUnobserved reports the checkout state could not be
	// completely inspected; no whole-checkout claim may be based on it.
	CheckoutReconcileUnobserved CheckoutReconcileState = "unobserved"
)

// CheckoutReconcileObservation is one settlement read's observation of the
// original checkout holding the attempted branch. HeadRef and HeadSHA are
// empty when they could not be read.
type CheckoutReconcileObservation struct {
	HeadRef string
	HeadSHA string
	State   CheckoutReconcileState
}

// SourceReconcileReport is one settlement read's typed observation.
type SourceReconcileReport struct {
	State    SourceReconcileState
	LocalSHA string
	// Checkout is the observed state of the original checkout holding the
	// branch, populated only when the caller asked to settle an
	// original-checkout update (the echoed binding's checkout HEAD was the
	// branch itself) and the original checkout still holds the branch. A nil
	// Checkout on such a request means the checkout no longer holds the
	// branch.
	Checkout *CheckoutReconcileObservation
}

// ReconcileSourceUpdateState reads the requested branch's current tip and
// reports it against the attempted update's two expected tips, optionally
// observing the original checkout that held the branch. The caller must
// already hold the repository's canonical common-directory mutation lock and
// must have established that no admitted update attempt on this repository
// can still mutate: only then is the read a settlement. The read is
// local-only — it never fetches, mutates, or infers anything from a cached
// comparison. Unresolvable branches and unobservable checkouts are settled
// observations; inspection failures of the tip fail closed.
func ReconcileSourceUpdateState(ctx context.Context, repoPath, branch, expectedLocalSHA, expectedOriginSHA string, observeCheckout bool, options OriginCheckOptions) (SourceReconcileReport, error) {
	if !validRemoteBranchName(branch) {
		return SourceReconcileReport{}, fmt.Errorf("expected local branch %q is not a usable branch name", branch)
	}
	if !validFullCommit(expectedLocalSHA) || !validFullCommit(expectedOriginSHA) {
		return SourceReconcileReport{}, fmt.Errorf("expected local and origin SHAs must be valid full SHAs")
	}
	if err := ctx.Err(); err != nil {
		return SourceReconcileReport{}, &SourceUpdateUnavailableError{Diagnostics: "reconciliation deadline expired before the read"}
	}
	// Exit code 1 from rev-parse --verify --quiet proves the ref does not
	// resolve; any other failure is an inspection failure and fails closed.
	result := runOriginCommand(ctx, repoPath, []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + branch + "^{commit}"}, options.commandTimeout(), options)
	var report SourceReconcileReport
	switch result.ExitCode {
	case 0:
		sha := strings.TrimSpace(result.Stdout)
		if !validFullCommit(sha) {
			return SourceReconcileReport{}, fmt.Errorf("local branch %q resolved to an unusable commit", branch)
		}
		report.LocalSHA = sha
		switch {
		case strings.EqualFold(sha, expectedOriginSHA):
			report.State = SourceReconcileTargetPresent
		case strings.EqualFold(sha, expectedLocalSHA):
			report.State = SourceReconcileOriginalTip
		default:
			report.State = SourceReconcileLocalChanged
		}
	case 1:
		report.State = SourceReconcileBranchMissing
	default:
		return SourceReconcileReport{}, fmt.Errorf("reading the local branch tip: %s", nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
	if observeCheckout && report.State != SourceReconcileBranchMissing {
		observation, err := observeOriginalCheckoutSettlement(ctx, repoPath, branch, options)
		if err != nil {
			return SourceReconcileReport{}, err
		}
		report.Checkout = observation
	}
	return report, nil
}

// observeOriginalCheckoutSettlement observes the original checkout's HEAD,
// tracked-tree consistency, and operation state when it still holds the
// branch. When the original checkout no longer holds the branch the
// observation is nil: the checkout cannot support any whole-checkout claim
// about this branch. The observation never attributes changes to Agentico.
func observeOriginalCheckoutSettlement(ctx context.Context, repoPath, branch string, options OriginCheckOptions) (*CheckoutReconcileObservation, error) {
	checkouts, err := listWorktreeCheckouts(ctx, repoPath, options)
	if err != nil {
		return nil, err
	}
	held := false
	for _, holder := range holdersOfBranch(checkouts, branch) {
		if sameCheckoutPath(holder, repoPath) {
			held = true
		}
	}
	if !held {
		return nil, nil
	}
	operation, err := checkoutOperationInProgress(ctx, repoPath, options)
	if err != nil {
		return &CheckoutReconcileObservation{State: CheckoutReconcileUnobserved}, nil
	}
	head, headErr := observeCheckoutHead(ctx, repoPath, options)
	clean, cleanErr := checkoutStatusClean(ctx, repoPath, false, options)
	switch {
	case operation:
		observation := &CheckoutReconcileObservation{State: CheckoutReconcileOperationInProgress}
		if headErr == nil {
			observation.HeadRef, observation.HeadSHA = head.Ref, head.SHA
		}
		return observation, nil
	case headErr != nil || cleanErr != nil:
		return &CheckoutReconcileObservation{State: CheckoutReconcileUnobserved}, nil
	case !clean:
		return &CheckoutReconcileObservation{HeadRef: head.Ref, HeadSHA: head.SHA, State: CheckoutReconcileDirty}, nil
	default:
		return &CheckoutReconcileObservation{HeadRef: head.Ref, HeadSHA: head.SHA, State: CheckoutReconcileClean}, nil
	}
}
