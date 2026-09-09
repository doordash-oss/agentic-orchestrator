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
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OriginCheckStatus is the typed outcome of comparing a selected local source
// with its mapped origin branch. Every value is a distinct user-facing state;
// unavailable fields are represented by their absence, never by invented
// SHAs, counts, or timestamps.
type OriginCheckStatus string

const (
	// OriginCheckChecking reports an attempt in flight.
	OriginCheckChecking OriginCheckStatus = "checking"
	// OriginCheckUpToDate reports local == freshly fetched origin.
	OriginCheckUpToDate OriginCheckStatus = "up_to_date"
	// OriginCheckBehind reports the origin branch has commits the local
	// source lacks.
	OriginCheckBehind OriginCheckStatus = "behind"
	// OriginCheckAhead reports the local source has commits origin lacks.
	OriginCheckAhead OriginCheckStatus = "ahead"
	// OriginCheckDiverged reports both sides have unique commits.
	OriginCheckDiverged OriginCheckStatus = "diverged"
	// OriginCheckNoOrigin reports no origin remote is configured.
	OriginCheckNoOrigin OriginCheckStatus = "no_origin"
	// OriginCheckRemoteBranchMissing reports the mapped origin branch was
	// proved absent by the current attempt.
	OriginCheckRemoteBranchMissing OriginCheckStatus = "remote_branch_missing"
	// OriginCheckOtherUpstream reports the selected branch explicitly tracks
	// an upstream other than origin (including local upstream tracking).
	OriginCheckOtherUpstream OriginCheckStatus = "other_upstream"
	// OriginCheckDetached reports a detached local source; there is no branch
	// to track and no fetch is attempted.
	OriginCheckDetached OriginCheckStatus = "detached"
	// OriginCheckLocalBaseMissing reports the selected local source is
	// missing or unborn; no remote-only fallback exists.
	OriginCheckLocalBaseMissing OriginCheckStatus = "local_base_missing"
	// OriginCheckUnknown reports an attempt that could not prove any of the
	// above (inspection, mapping, authentication, trust, network, or timeout
	// failures).
	OriginCheckUnknown OriginCheckStatus = "unknown"
)

// OriginMapping is the resolved origin tracking for one selected branch. The
// mapping comes from the branch's configured upstream (exact remote branch,
// including differently named branches) or, without upstream configuration,
// the same-name origin fallback. A configured mapping is preserved even when
// its cached remote-tracking ref is absent.
type OriginMapping struct {
	Branch string
}

// OriginComparison is one proved comparison between a recorded local SHA and
// a freshly fetched origin SHA. It is evidence about those SHAs only, never a
// transaction against external Git state.
type OriginComparison struct {
	Status       OriginCheckStatus
	LocalSHA     string
	FetchedSHA   string
	OriginBranch string
	AheadCount   int
	BehindCount  int
	CheckedAt    time.Time
}

// UpdateBlocker names one observed, advisory reason a future branch update
// would not be safe or defined. Eligibility never authorizes a mutation.
type UpdateBlocker string

const (
	// UpdateBlockerLocalNotBehind reports the local source is not strictly
	// behind origin (ahead, diverged, or up to date), so no plain update is
	// defined.
	UpdateBlockerLocalNotBehind UpdateBlocker = "local_not_behind"
	// UpdateBlockerDirtyTargetCheckout reports the checkout holding the
	// selected branch has uncommitted changes.
	UpdateBlockerDirtyTargetCheckout UpdateBlocker = "dirty_target_checkout"
	// UpdateBlockerGitOperationInProgress reports a Git mutation guarded by
	// Agentico's common-directory boundary is running for the repository.
	UpdateBlockerGitOperationInProgress UpdateBlocker = "git_operation_in_progress"
	// UpdateBlockerBranchCheckedOutInWorktree reports the selected branch is
	// checked out in a linked worktree.
	UpdateBlockerBranchCheckedOutInWorktree UpdateBlocker = "branch_checked_out_in_worktree"
	// UpdateBlockerComparisonUnavailable reports no fresh comparison exists
	// to base an update decision on.
	UpdateBlockerComparisonUnavailable UpdateBlocker = "comparison_unavailable"
)

const (
	defaultOriginCommandTimeout = 5 * time.Second
	originFetchRefspecPrefix    = "+refs/heads/"
)

// OriginCheckOptions carries per-call controls. Zero values use bounded
// production defaults; tests inject runners and clocks instead of mutating
// package globals. Network commands (ls-remote, fetch) are bounded only by
// the caller's context deadline, which must cover the whole attempt.
type OriginCheckOptions struct {
	// CommandTimeout bounds cheap local commands (config, rev-parse,
	// rev-list, worktree list, status).
	CommandTimeout time.Duration
	// DiagnosticLimit bounds captured diagnostics.
	DiagnosticLimit int
	// Runner is the process boundary. nil uses the production
	// argument-vector runner with non-interactive environment.
	Runner BranchProbeRunner
	// Now reports the comparison time. nil uses time.Now.
	Now func() time.Time
}

func (o OriginCheckOptions) commandTimeout() time.Duration {
	if o.CommandTimeout > 0 {
		return o.CommandTimeout
	}
	return defaultOriginCommandTimeout
}

func (o OriginCheckOptions) diagnosticLimit() int {
	if o.DiagnosticLimit > 0 {
		return o.DiagnosticLimit
	}
	return defaultBranchProbeDiagnosticLimit
}

func (o OriginCheckOptions) runner() BranchProbeRunner {
	if o.Runner != nil {
		return o.Runner
	}
	return ExecBranchProbeRunner{}
}

func (o OriginCheckOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// OriginCheckPlan is the local-only resolution of one selected source: the
// locally resolved source plus either a terminal status that needs no origin
// contact or a mapping whose branch must be fetched and compared.
type OriginCheckPlan struct {
	Source      LocalSource
	Status      OriginCheckStatus
	Mapping     *OriginMapping
	Diagnostics string
}

// FetchOriginState distinguishes a proved fetch outcome from an unavailable
// attempt. Absent means the current attempt proved the remote branch missing;
// every other failure is Unavailable, never evidence of absence.
type FetchOriginState string

const (
	FetchOriginFetched     FetchOriginState = "fetched"
	FetchOriginAbsent      FetchOriginState = "absent"
	FetchOriginUnavailable FetchOriginState = "unavailable"
)

// FetchOriginBranchResult is the bounded outcome of fetching the mapped
// origin branch.
type FetchOriginBranchResult struct {
	State       FetchOriginState
	SHA         string
	Diagnostics string
}

// PlanOriginCheck resolves the selected source and its origin mapping with
// local commands only. It never contacts origin: detached, no-origin,
// other-upstream, local-base-missing, and unknown outcomes are complete
// here, while a resolved mapping requires a fetch before comparison.
func PlanOriginCheck(ctx context.Context, repoPath string, mode LocalSourceMode, options OriginCheckOptions) OriginCheckPlan {
	source, err := InspectLocalSource(ctx, repoPath, mode)
	if err != nil {
		if errors.Is(err, ErrLocalSourceMissing) {
			return OriginCheckPlan{Status: OriginCheckLocalBaseMissing, Diagnostics: sanitizeBranchProbeDiagnostics(err.Error(), options.diagnosticLimit())}
		}
		return OriginCheckPlan{Status: OriginCheckUnknown, Diagnostics: sanitizeBranchProbeDiagnostics(err.Error(), options.diagnosticLimit())}
	}
	plan := OriginCheckPlan{Source: source}
	if source.Kind == LocalSourceDetached {
		plan.Status = OriginCheckDetached
		return plan
	}
	mapping, status, diagnostics := resolveOriginMapping(ctx, repoPath, source.Branch, options)
	plan.Status = status
	plan.Diagnostics = diagnostics
	plan.Mapping = mapping
	return plan
}

// resolveOriginMapping reads the branch's configured upstream. An explicit
// non-origin remote (including local "." tracking) is other-upstream; no
// upstream permits only the same-name origin fallback; malformed or
// unsupported mappings fail safely as unknown.
func resolveOriginMapping(ctx context.Context, repoPath, branch string, options OriginCheckOptions) (*OriginMapping, OriginCheckStatus, string) {
	remote, remoteErr := originConfigValue(ctx, repoPath, branch, "remote", options)
	if remoteErr != nil {
		return nil, OriginCheckUnknown, remoteErr.Error()
	}
	merge, mergeErr := originConfigValue(ctx, repoPath, branch, "merge", options)
	if mergeErr != nil {
		return nil, OriginCheckUnknown, mergeErr.Error()
	}
	switch {
	case remote != "":
		if remote != "origin" {
			return nil, OriginCheckOtherUpstream, ""
		}
		remoteBranch, ok := strings.CutPrefix(merge, "refs/heads/")
		if !ok || !validRemoteBranchName(remoteBranch) {
			return nil, OriginCheckUnknown, fmt.Sprintf("branch %q has an unsupported origin merge configuration", branch)
		}
		return &OriginMapping{Branch: remoteBranch}, "", ""
	default:
		if !originRemoteConfigured(ctx, repoPath, options) {
			return nil, OriginCheckNoOrigin, ""
		}
		if !validRemoteBranchName(branch) {
			return nil, OriginCheckUnknown, fmt.Sprintf("branch %q is not a usable origin branch name", branch)
		}
		return &OriginMapping{Branch: branch}, "", ""
	}
}

// originConfigValue reads branch.<branch>.<key> from the repository config.
// An unset key returns "", nil. Git parses the last dot as the key
// separator, so slash- and dot-containing branch names resolve correctly.
func originConfigValue(ctx context.Context, repoPath, branch, key string, options OriginCheckOptions) (string, error) {
	args := []string{"config", "--get", "branch." + branch + "." + key}
	result := runOriginCommand(ctx, repoPath, args, options.commandTimeout(), options)
	switch {
	case result.ExitCode == 0:
		return strings.TrimSpace(result.Stdout), nil
	case result.ExitCode == 1:
		return "", nil
	default:
		return "", fmt.Errorf("reading branch %s %s configuration: %s", branch, key, nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
}

func originRemoteConfigured(ctx context.Context, repoPath string, options OriginCheckOptions) bool {
	result := runOriginCommand(ctx, repoPath, []string{"remote"}, options.commandTimeout(), options)
	if result.ExitCode != 0 {
		return false
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true
		}
	}
	return false
}

// FetchOriginBranch proves the mapped branch's remote state and fetches only
// that branch. Existence is proved by the current ls-remote attempt (exit 2
// proves absence); a present branch is fetched with an explicit refspec plus
// a refmap override so configured fetch mappings, tag following, and
// submodule recursion cannot broaden the operation or target local branches.
// The returned SHA comes from the remote-tracking ref this fetch wrote, never
// from a cached ref or FETCH_HEAD.
func FetchOriginBranch(ctx context.Context, repoPath string, mapping OriginMapping, options OriginCheckOptions) FetchOriginBranchResult {
	if !validRemoteBranchName(mapping.Branch) {
		return FetchOriginBranchResult{State: FetchOriginUnavailable, Diagnostics: "mapped origin branch name is not usable"}
	}
	fullRef := "refs/heads/" + mapping.Branch
	probe := runOriginCommand(ctx, repoPath, []string{"ls-remote", "--exit-code", "--heads", "origin", fullRef}, 0, options)
	switch probe.ExitCode {
	case 2:
		return FetchOriginBranchResult{State: FetchOriginAbsent}
	case 0:
		if strings.TrimSpace(probe.Stdout) == "" {
			return FetchOriginBranchResult{State: FetchOriginUnavailable, Diagnostics: "origin branch existence probe returned no result"}
		}
	default:
		return FetchOriginBranchResult{State: FetchOriginUnavailable, Diagnostics: nonemptyBranchProbeDiagnostic(probe.Diagnostics)}
	}
	refspec := originFetchRefspecPrefix + mapping.Branch + ":refs/remotes/origin/" + mapping.Branch
	fetch := runOriginCommand(ctx, repoPath, []string{"fetch", "--no-tags", "--refmap=", "--recurse-submodules=no", "origin", refspec}, 0, options)
	if fetch.ExitCode != 0 {
		return FetchOriginBranchResult{State: FetchOriginUnavailable, Diagnostics: nonemptyBranchProbeDiagnostic(fetch.Diagnostics)}
	}
	sha := runOriginCommand(ctx, repoPath, []string{"rev-parse", "--verify", "--quiet", "refs/remotes/origin/" + mapping.Branch + "^{commit}"}, options.commandTimeout(), options)
	if sha.ExitCode != 0 || !validFullCommit(strings.TrimSpace(sha.Stdout)) {
		return FetchOriginBranchResult{State: FetchOriginUnavailable, Diagnostics: "fetched origin branch did not resolve to a commit"}
	}
	return FetchOriginBranchResult{State: FetchOriginFetched, SHA: strings.TrimSpace(sha.Stdout)}
}

// CompareOriginSource compares the recorded local SHA with the freshly
// fetched SHA. The comparison is evidence about exactly those SHAs.
func CompareOriginSource(ctx context.Context, repoPath string, localSHA, fetchedSHA string, mapping OriginMapping, options OriginCheckOptions) (OriginComparison, error) {
	comparison := OriginComparison{
		LocalSHA:     localSHA,
		FetchedSHA:   fetchedSHA,
		OriginBranch: mapping.Branch,
		CheckedAt:    options.now(),
	}
	if localSHA == fetchedSHA {
		comparison.Status = OriginCheckUpToDate
		return comparison, nil
	}
	result := runOriginCommand(ctx, repoPath, []string{"rev-list", "--left-right", "--count", localSHA + "..." + fetchedSHA}, options.commandTimeout(), options)
	if result.ExitCode != 0 {
		return OriginComparison{}, fmt.Errorf("counting commits between %s and %s: %s", localSHA, fetchedSHA, nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) != 2 {
		return OriginComparison{}, fmt.Errorf("counting commits between %s and %s: unexpected git output", localSHA, fetchedSHA)
	}
	ahead, aheadErr := strconv.Atoi(fields[0])
	behind, behindErr := strconv.Atoi(fields[1])
	if aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		return OriginComparison{}, fmt.Errorf("counting commits between %s and %s: unexpected git output", localSHA, fetchedSHA)
	}
	comparison.AheadCount = ahead
	comparison.BehindCount = behind
	switch {
	case ahead == 0 && behind == 0:
		comparison.Status = OriginCheckUpToDate
	case ahead > 0 && behind == 0:
		comparison.Status = OriginCheckAhead
	case ahead == 0 && behind > 0:
		comparison.Status = OriginCheckBehind
	default:
		comparison.Status = OriginCheckDiverged
	}
	return comparison, nil
}

// ProbeUpdateEligibility reports the advisory update eligibility for one
// selected branch together with every observed blocker. It must be called
// outside Agentico's common-directory mutation boundary: the boundary lock
// itself is one of the observed blockers. Unrelated dirty files in other
// checkouts never disqualify an unoccupied branch.
func ProbeUpdateEligibility(ctx context.Context, repoPath, branch string, options OriginCheckOptions) (bool, []UpdateBlocker) {
	blockers := make([]UpdateBlocker, 0, 2)
	if worktreeMutationInProgress(repoPath) {
		blockers = append(blockers, UpdateBlockerGitOperationInProgress)
	}
	holder, found, err := branchCheckoutHolder(ctx, repoPath, branch, options)
	if err == nil && found {
		if !sameCheckoutPath(holder, repoPath) {
			blockers = append(blockers, UpdateBlockerBranchCheckedOutInWorktree)
		} else if checkoutDirty(ctx, holder, options) {
			blockers = append(blockers, UpdateBlockerDirtyTargetCheckout)
		}
	}
	sort.Slice(blockers, func(i, j int) bool { return blockers[i] < blockers[j] })
	return len(blockers) == 0, blockers
}

// branchCheckoutHolder reports the worktree path the branch is checked out
// in, if any.
func branchCheckoutHolder(ctx context.Context, repoPath, branch string, options OriginCheckOptions) (string, bool, error) {
	result := runOriginCommand(ctx, repoPath, []string{"worktree", "list", "--porcelain"}, options.commandTimeout(), options)
	if result.ExitCode != 0 {
		return "", false, fmt.Errorf("listing worktrees: %s", nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
	holder := ""
	for _, line := range strings.Split(result.Stdout, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			holder = strings.TrimSpace(path)
			continue
		}
		if ref, ok := strings.CutPrefix(line, "branch "); ok {
			if strings.TrimSpace(ref) == "refs/heads/"+branch && holder != "" {
				return holder, true, nil
			}
		}
	}
	return "", false, nil
}

// sameCheckoutPath compares two worktree paths after resolving symlinks, so
// a primary checkout reported through a linked path (common on macOS temp
// directories) still matches the inspected repository path.
func sameCheckoutPath(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return resolvedA == resolvedB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func checkoutDirty(ctx context.Context, worktreePath string, options OriginCheckOptions) bool {
	result := runOriginCommand(ctx, worktreePath, []string{"status", "--porcelain"}, options.commandTimeout(), options)
	return result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != ""
}

// runOriginCommand runs one bounded git command for origin checks. A zero
// timeout runs the command under the ambient context only, which callers use
// for network commands whose bound is the whole-attempt deadline.
func runOriginCommand(ctx context.Context, repoPath string, args []string, timeout time.Duration, options OriginCheckOptions) BranchProbeCommandResult {
	limit := options.diagnosticLimit()
	if err := ctx.Err(); err != nil {
		return BranchProbeCommandResult{ExitCode: -1, Err: err, Diagnostics: "origin check command aborted before start"}
	}
	runner := options.runner()
	var result BranchProbeCommandResult
	if timeout > 0 {
		opCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result = runner.Run(opCtx, repoPath, args, limit)
		if errors.Is(opCtx.Err(), context.DeadlineExceeded) {
			result.ExitCode = -1
			result.Err = context.DeadlineExceeded
			result.Diagnostics = "origin check command timed out"
		}
	} else {
		result = runner.Run(ctx, repoPath, args, limit)
		if result.Diagnostics == "" && result.Err != nil {
			result.Diagnostics = "git origin check command failed"
		}
	}
	result.Diagnostics = sanitizeBranchProbeDiagnostics(result.Diagnostics, limit)
	return result
}

// validRemoteBranchName applies Git's ref-name rules to one branch name so a
// malformed or hostile mapping fails safely instead of reaching a refspec.
func validRemoteBranchName(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".lock") {
		return false
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return false
	}
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasPrefix(branch, ".") || strings.HasSuffix(branch, ".") {
		return false
	}
	for _, r := range branch {
		if r <= ' ' || r == '~' || r == '^' || r == ':' || r == '?' || r == '*' || r == '[' || r == '\\' || r == 0x7f {
			return false
		}
	}
	return true
}
