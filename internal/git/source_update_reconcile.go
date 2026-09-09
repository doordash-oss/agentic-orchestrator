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

// SourceReconcileReport is one settlement read's typed observation.
type SourceReconcileReport struct {
	State    SourceReconcileState
	LocalSHA string
}

// ReconcileSourceUpdateState reads the requested branch's current tip and
// reports it against the attempted update's two expected tips. The caller
// must already hold the repository's canonical common-directory mutation
// lock and must have established that no admitted update attempt on this
// repository can still mutate: only then is the read a settlement. The read
// is local-only — it never fetches, mutates, or infers anything from a
// cached comparison. An unresolvable branch is a settled observation, not
// an error; inspection failures fail closed.
func ReconcileSourceUpdateState(ctx context.Context, repoPath, branch, expectedLocalSHA, expectedOriginSHA string, options OriginCheckOptions) (SourceReconcileReport, error) {
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
	switch result.ExitCode {
	case 0:
		sha := strings.TrimSpace(result.Stdout)
		if !validFullCommit(sha) {
			return SourceReconcileReport{}, fmt.Errorf("local branch %q resolved to an unusable commit", branch)
		}
		switch {
		case strings.EqualFold(sha, expectedOriginSHA):
			return SourceReconcileReport{State: SourceReconcileTargetPresent, LocalSHA: sha}, nil
		case strings.EqualFold(sha, expectedLocalSHA):
			return SourceReconcileReport{State: SourceReconcileOriginalTip, LocalSHA: sha}, nil
		default:
			return SourceReconcileReport{State: SourceReconcileLocalChanged, LocalSHA: sha}, nil
		}
	case 1:
		return SourceReconcileReport{State: SourceReconcileBranchMissing}, nil
	default:
		return SourceReconcileReport{}, fmt.Errorf("reading the local branch tip: %s", nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
}
