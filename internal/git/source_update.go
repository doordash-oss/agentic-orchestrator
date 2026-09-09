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
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// SourceUpdateExpectation binds one displayed Update-from-origin decision to
// the exact observed state it was based on. Every field is an expectation the
// update revalidates against freshly resolved state; none of them is
// authority over which repository or ref is mutated.
type SourceUpdateExpectation struct {
	Mode              LocalSourceMode
	Branch            string
	OriginBranch      string
	ExpectedLocalSHA  string
	ExpectedOriginSHA string
	CheckoutHeadRef   string
	CheckoutHeadSHA   string
}

// SourceUpdateResult is the typed outcome of one update attempt. Stale is a
// refusal that left the repository untouched, not a failure to run.
type SourceUpdateResult string

const (
	SourceUpdateUpdated         SourceUpdateResult = "updated"
	SourceUpdateAlreadyUpToDate SourceUpdateResult = "already_up_to_date"
	SourceUpdateStale           SourceUpdateResult = "stale"
)

// SourceUpdateReason names one typed stale refusal.
type SourceUpdateReason string

const (
	// SourceUpdateReasonCheckoutChanged reports the observed checkout HEAD
	// changed since display, even when the selected source is unchanged.
	SourceUpdateReasonCheckoutChanged SourceUpdateReason = "checkout_changed"
	// SourceUpdateReasonSourceChanged reports the selected source no longer
	// matches (mode, kind, branch, or a disappeared local ref).
	SourceUpdateReasonSourceChanged SourceUpdateReason = "source_changed"
	// SourceUpdateReasonMappingChanged reports the origin mapping changed,
	// including a missing origin or a non-origin upstream.
	SourceUpdateReasonMappingChanged SourceUpdateReason = "mapping_changed"
	// SourceUpdateReasonLocalTipChanged reports the local branch tip moved,
	// including a ref change that failed the expected-old-value check.
	SourceUpdateReasonLocalTipChanged SourceUpdateReason = "local_tip_changed"
	// SourceUpdateReasonOriginTipChanged reports the freshly fetched origin
	// tip differs from the displayed one.
	SourceUpdateReasonOriginTipChanged SourceUpdateReason = "origin_tip_changed"
	// SourceUpdateReasonOriginBranchMissing reports this attempt proved the
	// mapped origin branch absent on the remote.
	SourceUpdateReasonOriginBranchMissing SourceUpdateReason = "origin_branch_missing"
	// SourceUpdateReasonNotFastForward reports the local branch is ahead of
	// or diverged from origin; only a proved fast-forward may advance it.
	SourceUpdateReasonNotFastForward SourceUpdateReason = "not_fast_forward"
	// SourceUpdateReasonBranchCheckedOut reports the branch is checked out
	// in some worktree (the original checkout or a linked worktree); it is
	// unavailable for a ref-only update in this phase.
	SourceUpdateReasonBranchCheckedOut SourceUpdateReason = "branch_checked_out"
)

// SourceUpdateOutcome is one update attempt's typed result. For stale
// refusals the snapshot fields carry freshly resolved status evidence for
// the current selection, never the displayed expectations.
type SourceUpdateOutcome struct {
	Result      SourceUpdateResult
	Reason      SourceUpdateReason
	PreviousSHA string
	LocalSHA    string
	FetchedSHA  string

	// Plan is the freshly resolved local-only selection state.
	Plan OriginCheckPlan
	// Comparison is a fresh local-versus-origin comparison for the current
	// selection when this attempt fetched the mapped branch.
	Comparison *OriginComparison
	// RemoteBranchMissing records a proved-absent mapped origin branch.
	RemoteBranchMissing bool
	// FetchUnavailable records that the fresh status fetch could not be
	// proved; the status row must not present old counts as current.
	FetchUnavailable bool
	// Blockers are the observed advisory update blockers for the fresh
	// status row.
	Blockers []UpdateBlocker
	// CheckoutHolders lists the worktree paths holding the branch for
	// branch_checked_out refusals.
	CheckoutHolders []string
}

// ErrSourceUpdateUnavailable reports an attempt that could not prove a safe
// outcome: inspection failure, fetch failure, deadline expiry, or an
// ambiguous mutation boundary. It never claims the branch was rolled back or
// that a completed mutation failed.
var ErrSourceUpdateUnavailable = errors.New("source update unavailable")

// SourceUpdateUnavailableError carries bounded, credential-redacted
// diagnostics for an unprovable attempt.
type SourceUpdateUnavailableError struct {
	Diagnostics string
}

func (e *SourceUpdateUnavailableError) Error() string {
	if e == nil || strings.TrimSpace(e.Diagnostics) == "" {
		return ErrSourceUpdateUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s", ErrSourceUpdateUnavailable, e.Diagnostics)
}

func (e *SourceUpdateUnavailableError) Unwrap() error { return ErrSourceUpdateUnavailable }

// SourceUpdateRefRunner performs the compare-and-swap ref mutation. It is a
// separate boundary from BranchProbeRunner because the CAS needs stdin; the
// production runner reaps its process group under the attempt context.
type SourceUpdateRefRunner interface {
	Run(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) BranchProbeCommandResult
}

// ExecSourceUpdateRefRunner is the production argument-vector runner.
type ExecSourceUpdateRefRunner struct{}

func (ExecSourceUpdateRefRunner) Run(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) BranchProbeCommandResult {
	gitArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = nonInteractiveGitEnv(cmd.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(stdin)
	stdout := &limitedDrainWriter{limit: diagnosticLimit}
	stderr := &limitedDrainWriter{limit: diagnosticLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	diagnostics := stderr.String()
	if diagnostics == "" && err != nil {
		diagnostics = err.Error()
	}
	return BranchProbeCommandResult{Stdout: stdout.String(), Diagnostics: diagnostics, ExitCode: exitCode, Err: err}
}

// SourceUpdateOptions carries per-call controls for the source update.
// Zero values use bounded production defaults; deterministic tests inject
// runners, clocks, and pre-CAS hooks instead of mutating package globals.
type SourceUpdateOptions struct {
	OriginCheckOptions

	// UpdateRefRunner performs the CAS mutation; nil uses the production
	// runner.
	UpdateRefRunner SourceUpdateRefRunner
	// BeforeCAS runs under coordination immediately before the final
	// revalidation and compare-and-swap. Production callers leave it nil;
	// deterministic tests inject races here.
	BeforeCAS func()
}

func (o SourceUpdateOptions) updateRefRunner() SourceUpdateRefRunner {
	if o.UpdateRefRunner != nil {
		return o.UpdateRefRunner
	}
	return ExecSourceUpdateRefRunner{}
}

// CheckoutHeadState is one observed checkout HEAD: the full symbolic ref or
// the literal "detached", plus the resolved commit.
type CheckoutHeadState struct {
	Ref string
	SHA string
}

// UpdateSourceFromOrigin advances exactly one local branch to a freshly
// fetched origin commit by an expected-old-value compare-and-swap.
//
// The caller must already hold the repository's canonical common-directory
// mutation lock: the update, origin checks, feature acceptance, and setup
// serialize on that boundary. Agentico coordination does not claim atomic
// exclusion of arbitrary external Git processes; the CAS old-value check is
// the final arbiter for the ref itself. The update never checks out, resets,
// rebases, merges, forces, stashes, cleans, stages, pushes, or runs hooks,
// never deletes or breaks Git ref/index locks (including old ones), and
// bounds every subprocess by ctx, reaping processes before returning.
//
// Stale refusals return an outcome rather than an error: the mutation was
// refused, not attempted and failed. Unprovable attempts (inspection, fetch,
// ancestry, or deadline failures, and ambiguous mutation boundaries) return
// an error wrapping ErrSourceUpdateUnavailable and leave any claim about the
// ref's final state unproved.
func UpdateSourceFromOrigin(ctx context.Context, repoPath string, expected SourceUpdateExpectation, options SourceUpdateOptions) (SourceUpdateOutcome, error) {
	if err := validateSourceUpdateExpectation(expected); err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: "source update deadline expired before validation"}
	}

	plan := PlanOriginCheck(ctx, repoPath, expected.Mode, options.OriginCheckOptions)
	if plan.Status == OriginCheckUnknown {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: nonemptyBranchProbeDiagnostic(plan.Diagnostics)}
	}
	if plan.Status != "" {
		// A terminal plan state (detached, local base missing, no origin,
		// other upstream) is a changed selection or mapping, never a
		// mutation target.
		reason := SourceUpdateReasonSourceChanged
		if plan.Status == OriginCheckNoOrigin || plan.Status == OriginCheckOtherUpstream {
			reason = SourceUpdateReasonMappingChanged
		}
		return staleOutcome(ctx, repoPath, plan, reason, nil, options)
	}
	if plan.Source.Kind != LocalSourceBranch || plan.Source.Branch != expected.Branch {
		return staleOutcome(ctx, repoPath, plan, SourceUpdateReasonSourceChanged, nil, options)
	}
	if plan.Mapping == nil || plan.Mapping.Branch != expected.OriginBranch {
		return staleOutcome(ctx, repoPath, plan, SourceUpdateReasonMappingChanged, nil, options)
	}

	localTip, err := readBranchTip(ctx, repoPath, expected.Branch, options.OriginCheckOptions)
	if err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}

	head, err := observeCheckoutHead(ctx, repoPath, options.OriginCheckOptions)
	if err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if head.Ref != expected.CheckoutHeadRef || !strings.EqualFold(head.SHA, expected.CheckoutHeadSHA) {
		return staleOutcome(ctx, repoPath, plan, SourceUpdateReasonCheckoutChanged, nil, options)
	}

	checkouts, err := listWorktreeCheckouts(ctx, repoPath, options.OriginCheckOptions)
	if err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if holders := holdersOfBranch(checkouts, expected.Branch); len(holders) > 0 {
		outcome, err := staleOutcome(ctx, repoPath, plan, SourceUpdateReasonBranchCheckedOut, nil, options)
		if err != nil {
			return SourceUpdateOutcome{}, err
		}
		outcome.CheckoutHolders = holders
		outcome.Blockers = append(outcome.Blockers, checkoutHoldersBlockers(repoPath, holders)...)
		return outcome, nil
	}

	// The fetch proves the remote branch exists during this attempt; a
	// cached tracking ref is never current evidence.
	fetch := FetchOriginBranch(ctx, repoPath, *plan.Mapping, options.OriginCheckOptions)
	switch fetch.State {
	case FetchOriginAbsent:
		return staleOutcome(ctx, repoPath, plan, SourceUpdateReasonOriginBranchMissing, &fetch, options)
	case FetchOriginUnavailable:
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: nonemptyBranchProbeDiagnostic(fetch.Diagnostics)}
	}
	if err := ctx.Err(); err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: "source update deadline expired during fetch"}
	}
	if !strings.EqualFold(fetch.SHA, expected.ExpectedOriginSHA) {
		return staleOutcome(ctx, repoPath, plan, SourceUpdateReasonOriginTipChanged, &fetch, options)
	}

	// Equality is a no-op success: a repeated request may find the branch
	// already at the unchanged expected origin SHA after every other
	// expectation revalidated.
	if strings.EqualFold(localTip, fetch.SHA) {
		return SourceUpdateOutcome{
			Result: SourceUpdateAlreadyUpToDate, LocalSHA: localTip, FetchedSHA: fetch.SHA,
			Plan: plan,
		}, nil
	}
	if !strings.EqualFold(localTip, expected.ExpectedLocalSHA) {
		return staleOutcome(ctx, repoPath, plan, SourceUpdateReasonLocalTipChanged, &fetch, options)
	}

	fastForward, err := isAncestorOf(ctx, repoPath, localTip, fetch.SHA, options.OriginCheckOptions)
	if err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if !fastForward {
		outcome, err := staleOutcome(ctx, repoPath, plan, SourceUpdateReasonNotFastForward, &fetch, options)
		if err != nil {
			return SourceUpdateOutcome{}, err
		}
		outcome.Blockers = append(outcome.Blockers, UpdateBlockerLocalNotBehind)
		return outcome, nil
	}

	if options.BeforeCAS != nil {
		options.BeforeCAS()
	}
	if err := ctx.Err(); err != nil {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: "source update deadline expired before the ref update"}
	}

	// Final revalidation immediately before the CAS: any change refuses
	// instead of mutating against stale expectations.
	stale, err := revalidateBeforeCAS(ctx, repoPath, expected, options.OriginCheckOptions)
	if err != nil {
		return SourceUpdateOutcome{}, err
	}
	if stale != nil {
		return *stale, nil
	}

	if err := casUpdateBranchRef(ctx, repoPath, expected.Branch, localTip, fetch.SHA, options, func() (*SourceUpdateOutcome, error) {
		return revalidateBeforeCAS(ctx, repoPath, expected, options.OriginCheckOptions)
	}); err != nil {
		if refusal, ok := unwrapStaleRefusal(err); ok {
			return enrichStaleRefusal(ctx, repoPath, refusal, plan, &fetch, options)
		}
		if errors.Is(err, ErrSourceUpdateRefCASMismatch) {
			return staleOutcome(ctx, repoPath, plan, SourceUpdateReasonLocalTipChanged, &fetch, options)
		}
		if errors.Is(err, ErrSourceUpdateUnavailable) {
			return SourceUpdateOutcome{}, err
		}
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		// The CAS reported success but the deadline expired before the
		// result could be verified: the outcome is unproved, never a
		// claimed failure or rollback.
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: "source update deadline expired before the result could be verified"}
	}

	finalTip, err := readBranchTip(ctx, repoPath, expected.Branch, options.OriginCheckOptions)
	if err != nil || !strings.EqualFold(finalTip, fetch.SHA) {
		return SourceUpdateOutcome{}, &SourceUpdateUnavailableError{Diagnostics: "the ref update could not be verified after the mutation"}
	}
	return SourceUpdateOutcome{
		Result: SourceUpdateUpdated, PreviousSHA: localTip, LocalSHA: finalTip, FetchedSHA: fetch.SHA,
		Plan: plan,
	}, nil
}

// enrichStaleRefusal fills the fresh snapshot fields of a CAS-layer stale
// refusal, which carries only its typed reason and observed holders.
func enrichStaleRefusal(ctx context.Context, repoPath string, refusal SourceUpdateOutcome, plan OriginCheckPlan, fetch *FetchOriginBranchResult, options SourceUpdateOptions) (SourceUpdateOutcome, error) {
	if refusal.Plan.Status == "" && refusal.Plan.Source.Mode == "" {
		refusal.Plan = plan
	}
	if refusal.Comparison == nil && !refusal.RemoteBranchMissing && !refusal.FetchUnavailable && fetch != nil && fetch.State == FetchOriginFetched {
		if plan.Source.Commit != "" {
			comparison, err := CompareOriginSource(ctx, repoPath, plan.Source.Commit, fetch.SHA, *plan.Mapping, options.OriginCheckOptions)
			if err == nil {
				refusal.Comparison = &comparison
				refusal.FetchedSHA = fetch.SHA
			}
		}
	}
	return refusal, nil
}

// staleOutcome builds one stale refusal with freshly resolved status
// evidence. When the plan still maps an origin branch, a fresh fetch (or the
// attempt's own fetch result) produces the comparison for the current
// selection; unprovable fetches mark the snapshot unavailable instead of
// presenting old counts as current.
func staleOutcome(ctx context.Context, repoPath string, plan OriginCheckPlan, reason SourceUpdateReason, fetch *FetchOriginBranchResult, options SourceUpdateOptions) (SourceUpdateOutcome, error) {
	outcome := SourceUpdateOutcome{Result: SourceUpdateStale, Reason: reason, Plan: plan}
	if plan.Mapping == nil {
		return outcome, nil
	}
	if fetch == nil {
		if err := ctx.Err(); err != nil {
			outcome.FetchUnavailable = true
			return outcome, nil
		}
		fetched := FetchOriginBranch(ctx, repoPath, *plan.Mapping, options.OriginCheckOptions)
		fetch = &fetched
	}
	switch fetch.State {
	case FetchOriginAbsent:
		outcome.RemoteBranchMissing = true
	case FetchOriginUnavailable:
		outcome.FetchUnavailable = true
	default:
		if plan.Source.Commit != "" {
			comparison, err := CompareOriginSource(ctx, repoPath, plan.Source.Commit, fetch.SHA, *plan.Mapping, options.OriginCheckOptions)
			if err != nil {
				outcome.FetchUnavailable = true
			} else {
				outcome.Comparison = &comparison
				outcome.FetchedSHA = fetch.SHA
			}
		}
	}
	return outcome, nil
}

// revalidateBeforeCAS re-reads the local tip, checkout HEAD, and worktree
// membership. A change returns the matching stale refusal; an inspection
// failure fails closed.
func revalidateBeforeCAS(ctx context.Context, repoPath string, expected SourceUpdateExpectation, options OriginCheckOptions) (*SourceUpdateOutcome, error) {
	localTip, err := readBranchTip(ctx, repoPath, expected.Branch, options)
	if err != nil {
		return nil, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if !strings.EqualFold(localTip, expected.ExpectedLocalSHA) {
		return &SourceUpdateOutcome{Result: SourceUpdateStale, Reason: SourceUpdateReasonLocalTipChanged}, nil
	}
	head, err := observeCheckoutHead(ctx, repoPath, options)
	if err != nil {
		return nil, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if head.Ref != expected.CheckoutHeadRef || !strings.EqualFold(head.SHA, expected.CheckoutHeadSHA) {
		return &SourceUpdateOutcome{Result: SourceUpdateStale, Reason: SourceUpdateReasonCheckoutChanged}, nil
	}
	checkouts, err := listWorktreeCheckouts(ctx, repoPath, options)
	if err != nil {
		return nil, &SourceUpdateUnavailableError{Diagnostics: err.Error()}
	}
	if holders := holdersOfBranch(checkouts, expected.Branch); len(holders) > 0 {
		outcome := &SourceUpdateOutcome{Result: SourceUpdateStale, Reason: SourceUpdateReasonBranchCheckedOut}
		outcome.CheckoutHolders = holders
		outcome.Blockers = checkoutHoldersBlockers(repoPath, holders)
		return outcome, nil
	}
	return nil, nil
}

// ErrSourceUpdateRefCASMismatch reports that the ref's current value no
// longer matches the expected old value, so the mutation was refused.
var ErrSourceUpdateRefCASMismatch = errors.New("source update ref compare-and-swap mismatch")

// casUpdateBranchRef advances refs/heads/<branch> from oldSHA to newSHA with
// Git's expected-old-value check. Lock contention is retried inside the
// remaining deadline after rechecking membership; Git ref/index locks are
// never deleted or broken, including old ones. This path deliberately does
// not use the generic mutation helper that removes stale locks.
func casUpdateBranchRef(ctx context.Context, repoPath, branch, oldSHA, newSHA string, options SourceUpdateOptions, revalidate func() (*SourceUpdateOutcome, error)) error {
	fullRef := "refs/heads/" + branch
	stdin := "update " + fullRef + " " + newSHA + " " + oldSHA + "\n"
	delay := 50 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return &SourceUpdateUnavailableError{Diagnostics: "source update deadline expired while updating the ref"}
		}
		result := options.updateRefRunner().Run(ctx, repoPath, stdin, []string{"update-ref", "--stdin"}, options.diagnosticLimit())
		if result.ExitCode == 0 {
			return nil
		}
		current, readErr := readBranchTip(ctx, repoPath, branch, options.OriginCheckOptions)
		if readErr == nil && !strings.EqualFold(current, oldSHA) {
			// Git's expected-old-value check refused a competing value;
			// never overwrite it.
			return fmt.Errorf("%w: ref %s expected %s observed %s", ErrSourceUpdateRefCASMismatch, fullRef, oldSHA, current)
		}
		if _, contention := lockContention([]byte(result.Stdout + "\n" + result.Diagnostics)); !contention {
			diagnostics := sanitizeBranchProbeDiagnostics(nonemptyBranchProbeDiagnostic(result.Diagnostics), options.diagnosticLimit())
			return &SourceUpdateUnavailableError{Diagnostics: diagnostics}
		}
		// Bounded lock-contention retry: keep the original expectations and
		// recheck membership before another mutation attempt. The lock
		//itself is never removed.
		if stale, err := revalidate(); err != nil || stale != nil {
			if err != nil {
				return err
			}
			return &staleRefusalError{outcome: *stale}
		}
		if err := ctx.Err(); err != nil {
			return &SourceUpdateUnavailableError{Diagnostics: "source update deadline expired while waiting for a git ref lock"}
		}
		time.Sleep(delay)
		if delay < 500*time.Millisecond {
			delay *= 2
		}
	}
}

// staleRefusalError carries a stale refusal out of the CAS retry loop.
type staleRefusalError struct {
	outcome SourceUpdateOutcome
}

func (e *staleRefusalError) Error() string {
	return fmt.Sprintf("source update refused: %s", e.outcome.Reason)
}

// unwrapStaleRefusal returns the stale outcome carried by a CAS-layer
// refusal, if any.
func unwrapStaleRefusal(err error) (SourceUpdateOutcome, bool) {
	var stale *staleRefusalError
	if errors.As(err, &stale) {
		return stale.outcome, true
	}
	return SourceUpdateOutcome{}, false
}

func validateSourceUpdateExpectation(expected SourceUpdateExpectation) error {
	if expected.Mode != LocalSourceModeDefault && expected.Mode != LocalSourceModeCurrent {
		return fmt.Errorf("unsupported local source mode %q", expected.Mode)
	}
	if !validRemoteBranchName(expected.Branch) {
		return fmt.Errorf("expected local branch %q is not a usable branch name", expected.Branch)
	}
	if !validRemoteBranchName(expected.OriginBranch) {
		return fmt.Errorf("expected origin branch %q is not a usable branch name", expected.OriginBranch)
	}
	if !validFullCommit(expected.ExpectedLocalSHA) {
		return fmt.Errorf("expected local SHA is not a valid full SHA")
	}
	if !validFullCommit(expected.ExpectedOriginSHA) {
		return fmt.Errorf("expected origin SHA is not a valid full SHA")
	}
	if expected.CheckoutHeadRef == "" || len(expected.CheckoutHeadRef) > 512 {
		return fmt.Errorf("observed checkout HEAD reference is missing or exceeds the bounded length")
	}
	if expected.CheckoutHeadRef != "detached" && !strings.HasPrefix(expected.CheckoutHeadRef, "refs/heads/") {
		return fmt.Errorf("observed checkout HEAD reference %q is not a full branch ref or detached", expected.CheckoutHeadRef)
	}
	if expected.CheckoutHeadRef != "detached" && !validRemoteBranchName(strings.TrimPrefix(expected.CheckoutHeadRef, "refs/heads/")) {
		return fmt.Errorf("observed checkout HEAD reference %q is not a usable branch ref", expected.CheckoutHeadRef)
	}
	if !validFullCommit(expected.CheckoutHeadSHA) {
		return fmt.Errorf("observed checkout HEAD SHA is not a valid full SHA")
	}
	return nil
}

// observeCheckoutHead reads the checkout's HEAD as the displayed snapshot
// did: the full symbolic ref or the literal "detached", plus the commit.
func observeCheckoutHead(ctx context.Context, repoPath string, options OriginCheckOptions) (CheckoutHeadState, error) {
	symref := runOriginCommand(ctx, repoPath, []string{"symbolic-ref", "--quiet", "HEAD"}, options.commandTimeout(), options)
	switch symref.ExitCode {
	case 0:
		ref := strings.TrimSpace(symref.Stdout)
		if ref == "" || !strings.HasPrefix(ref, "refs/") {
			return CheckoutHeadState{}, fmt.Errorf("checkout HEAD resolved to an unusable reference")
		}
		sha, err := resolveHeadCommit(ctx, repoPath, options)
		if err != nil {
			return CheckoutHeadState{}, err
		}
		return CheckoutHeadState{Ref: ref, SHA: sha}, nil
	case 1:
		sha, err := resolveHeadCommit(ctx, repoPath, options)
		if err != nil {
			return CheckoutHeadState{}, err
		}
		return CheckoutHeadState{Ref: "detached", SHA: sha}, nil
	default:
		return CheckoutHeadState{}, fmt.Errorf("reading the checkout HEAD: %s", nonemptyBranchProbeDiagnostic(symref.Diagnostics))
	}
}

func resolveHeadCommit(ctx context.Context, repoPath string, options OriginCheckOptions) (string, error) {
	result := runOriginCommand(ctx, repoPath, []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}, options.commandTimeout(), options)
	sha := strings.TrimSpace(result.Stdout)
	if result.ExitCode != 0 || !validFullCommit(sha) {
		return "", fmt.Errorf("the checkout HEAD does not resolve to a commit")
	}
	return sha, nil
}

// readBranchTip resolves the exact full ref refs/heads/<branch> to a commit.
func readBranchTip(ctx context.Context, repoPath, branch string, options OriginCheckOptions) (string, error) {
	result := runOriginCommand(ctx, repoPath, []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + branch + "^{commit}"}, options.commandTimeout(), options)
	sha := strings.TrimSpace(result.Stdout)
	if result.ExitCode != 0 || !validFullCommit(sha) {
		return "", fmt.Errorf("local branch %q does not resolve to a commit", branch)
	}
	return sha, nil
}

// isAncestorOf proves ancestor containment with a bounded command. Exit code
// 1 is a proved negative; any other failure is an inspection failure.
func isAncestorOf(ctx context.Context, repoPath, ancestor, descendant string, options OriginCheckOptions) (bool, error) {
	result := runOriginCommand(ctx, repoPath, []string{"merge-base", "--is-ancestor", ancestor, descendant}, options.commandTimeout(), options)
	switch result.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("inspecting fast-forward ancestry: %s", nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
}

// worktreeCheckout is one parsed `git worktree list --porcelain` record.
type worktreeCheckout struct {
	Path     string
	Branch   string
	Detached bool
	Bare     bool
}

// listWorktreeCheckouts enumerates every checkout of the repository,
// including the original checkout and linked worktrees, with unambiguous
// porcelain parsing. Malformed output is an inspection failure.
func listWorktreeCheckouts(ctx context.Context, repoPath string, options OriginCheckOptions) ([]worktreeCheckout, error) {
	result := runOriginCommand(ctx, repoPath, []string{"worktree", "list", "--porcelain"}, options.commandTimeout(), options)
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("listing worktrees: %s", nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
	var checkouts []worktreeCheckout
	current := worktreeCheckout{}
	seen := false
	flush := func() {
		if seen && current.Path != "" {
			checkouts = append(checkouts, current)
		}
		current = worktreeCheckout{}
		seen = false
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			current.Path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			seen = true
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
		case line == "detached":
			current.Detached = true
		case line == "bare":
			current.Bare = true
		case strings.HasPrefix(line, "HEAD "):
			// HEAD lines carry the commit; membership is decided by branch
			// refs, so the SHA is not needed here.
		case line == "locked" || strings.HasPrefix(line, "locked "), line == "prunable" || strings.HasPrefix(line, "prunable "):
			// Administrative annotations with optional reason text; they do
			// not affect membership.
		default:
			return nil, fmt.Errorf("unrecognized worktree list output %q", line)
		}
	}
	flush()
	if len(checkouts) == 0 {
		return nil, fmt.Errorf("worktree list reported no checkouts")
	}
	return checkouts, nil
}

// holdersOfBranch returns every checkout path the branch is checked out in.
func holdersOfBranch(checkouts []worktreeCheckout, branch string) []string {
	fullRef := "refs/heads/" + branch
	var holders []string
	for _, checkout := range checkouts {
		if checkout.Branch == fullRef {
			holders = append(holders, checkout.Path)
		}
	}
	return holders
}

// checkoutHoldersBlockers classifies each holder: the original checkout is
// unavailable in this phase even when clean; a linked worktree requires
// updating that worktree separately.
func checkoutHoldersBlockers(repoPath string, holders []string) []UpdateBlocker {
	blockers := make([]UpdateBlocker, 0, len(holders))
	for _, holder := range holders {
		if sameCheckoutPath(holder, repoPath) {
			blockers = append(blockers, UpdateBlockerBranchCheckedOutOriginal)
		} else {
			blockers = append(blockers, UpdateBlockerBranchCheckedOutInWorktree)
		}
	}
	return blockers
}

// UpdateBlockerBranchCheckedOutOriginal reports the selected branch is
// checked out in the repository's original checkout: unavailable for a
// ref-only update in this phase, even when clean.
const UpdateBlockerBranchCheckedOutOriginal UpdateBlocker = "branch_checked_out_in_original_checkout"

// ResolveCheckoutHead exposes the observed checkout HEAD for read-model
// snapshots that bind Update expectations.
func ResolveCheckoutHead(ctx context.Context, repoPath string, options OriginCheckOptions) (CheckoutHeadState, error) {
	return observeCheckoutHead(ctx, repoPath, options)
}
