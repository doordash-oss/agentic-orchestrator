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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	defaultBranchProbeTimeout         = 2 * time.Second
	defaultBranchProbeDiagnosticLimit = 4 << 10
)

// BranchProbeState distinguishes a proved result from an unavailable remote.
type BranchProbeState string

const (
	BranchProbeAbsent      BranchProbeState = "absent"
	BranchProbeCollision   BranchProbeState = "collision"
	BranchProbeUnavailable BranchProbeState = "unavailable"
)

// BranchProbeResult is the bounded result of probing one branch name.
type BranchProbeResult struct {
	State       BranchProbeState
	Diagnostics string
}

// BranchProbeRepository is one selected checkout participating in collision
// checking. ProbeOrigin is false for local-only repositories.
type BranchProbeRepository struct {
	Name        string
	Path        string
	ProbeOrigin bool
}

// BranchProbeWarning attributes an unavailable best-effort origin probe.
type BranchProbeWarning struct {
	Repository  string
	Branch      string
	Diagnostics string
}

// BranchCandidateResult reports whether a candidate is usable. Unavailable
// means locally unique and usable, with warnings describing unknown origins.
type BranchCandidateResult struct {
	State    BranchProbeState
	Warnings []BranchProbeWarning
}

// BranchProbeOptions contains per-call controls. Zero values use bounded
// production defaults, keeping tests from mutating package globals.
type BranchProbeOptions struct {
	OperationTimeout time.Duration
	DiagnosticLimit  int
	Runner           BranchProbeRunner
}

// BranchProbeRunner is the process boundary used by branch probes.
type BranchProbeRunner interface {
	Run(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult
}

// BranchProbeRunnerFunc adapts a function for deterministic callers and tests.
type BranchProbeRunnerFunc func(context.Context, string, []string, int) BranchProbeCommandResult

func (f BranchProbeRunnerFunc) Run(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult {
	return f(ctx, repoPath, args, diagnosticLimit)
}

// BranchProbeCommandResult is a completed and reaped command result.
type BranchProbeCommandResult struct {
	Stdout      string
	Diagnostics string
	ExitCode    int
	Err         error
}

// ExecBranchProbeRunner runs the production argument-vector probe. Executable
// defaults to git and is injectable for deterministic process-lifecycle tests.
type ExecBranchProbeRunner struct {
	Executable string
	// afterStart is a test synchronization hook invoked after the process is
	// running and before Wait. Production callers leave it nil.
	afterStart func()
}

// ProbeRemoteBranch checks the exact origin branch without fetching. Exit code
// 2 from git ls-remote --exit-code proves absence; all other failures are
// unavailable rather than evidence of absence.
func ProbeRemoteBranch(ctx context.Context, repoPath, branch string, options BranchProbeOptions) BranchProbeResult {
	result := runBranchProbe(ctx, repoPath, []string{"ls-remote", "--exit-code", "--heads", "origin", "refs/heads/" + branch}, options)
	switch result.ExitCode {
	case 0:
		if strings.TrimSpace(result.Stdout) != "" {
			return BranchProbeResult{State: BranchProbeCollision}
		}
	case 2:
		return BranchProbeResult{State: BranchProbeAbsent}
	}
	return BranchProbeResult{State: BranchProbeUnavailable, Diagnostics: nonemptyBranchProbeDiagnostic(result.Diagnostics)}
}

// ProbeBranchCandidate checks every local branch before contacting any origin.
// Local probe failures fail closed because local uniqueness must be proved.
func ProbeBranchCandidate(ctx context.Context, repos []BranchProbeRepository, branch string, options BranchProbeOptions) (BranchCandidateResult, error) {
	for _, repo := range repos {
		result := runBranchProbe(ctx, repo.Path, []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch}, options)
		switch result.ExitCode {
		case 0:
			return BranchCandidateResult{State: BranchProbeCollision}, nil
		case 1:
			// Git show-ref uses 1 for a missing exact ref.
		default:
			return BranchCandidateResult{}, fmt.Errorf("checking local branch in repo %q: %s", repo.Name, nonemptyBranchProbeDiagnostic(result.Diagnostics))
		}
	}

	var warnings []BranchProbeWarning
	for _, repo := range repos {
		if !repo.ProbeOrigin {
			continue
		}
		result := ProbeRemoteBranch(ctx, repo.Path, branch, options)
		switch result.State {
		case BranchProbeCollision:
			return BranchCandidateResult{State: BranchProbeCollision}, nil
		case BranchProbeUnavailable:
			warnings = append(warnings, BranchProbeWarning{Repository: repo.Name, Branch: branch, Diagnostics: result.Diagnostics})
		}
	}
	if len(warnings) > 0 {
		return BranchCandidateResult{State: BranchProbeUnavailable, Warnings: warnings}, nil
	}
	return BranchCandidateResult{State: BranchProbeAbsent}, nil
}

func nonemptyBranchProbeDiagnostic(value string) string {
	if strings.TrimSpace(value) == "" {
		return "git branch probe failed"
	}
	return value
}

func runBranchProbe(ctx context.Context, repoPath string, args []string, options BranchProbeOptions) BranchProbeCommandResult {
	timeout := options.OperationTimeout
	if timeout <= 0 {
		timeout = defaultBranchProbeTimeout
	}
	limit := options.DiagnosticLimit
	if limit <= 0 {
		limit = defaultBranchProbeDiagnosticLimit
	}
	runner := options.Runner
	if runner == nil {
		runner = ExecBranchProbeRunner{}
	}
	if err := ctx.Err(); err != nil {
		return BranchProbeCommandResult{ExitCode: -1, Err: err, Diagnostics: sanitizeBranchProbeDiagnostics("branch availability probe timed out", limit)}
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := runner.Run(opCtx, repoPath, args, limit)
	if errors.Is(opCtx.Err(), context.DeadlineExceeded) {
		result.ExitCode = -1
		result.Err = context.DeadlineExceeded
		result.Diagnostics = "branch availability probe timed out"
	} else if result.Diagnostics == "" && result.Err != nil {
		result.Diagnostics = "git branch probe failed"
	}
	result.Diagnostics = sanitizeBranchProbeDiagnostics(result.Diagnostics, limit)
	return result
}

func (r ExecBranchProbeRunner) Run(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult {
	gitArgs := append([]string{"-C", repoPath}, args...)
	executable := r.Executable
	if executable == "" {
		executable = "git"
	}
	cmd := exec.CommandContext(ctx, executable, gitArgs...)
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
	stdout := &limitedDrainWriter{limit: diagnosticLimit}
	stderr := &limitedDrainWriter{limit: diagnosticLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Start()
	if err == nil {
		if r.afterStart != nil {
			r.afterStart()
		}
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

type limitedDrainWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *limitedDrainWriter) Write(p []byte) (int, error) {
	if remaining := w.limit - w.buf.Len(); remaining > 0 {
		_, _ = w.buf.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func (w *limitedDrainWriter) String() string { return w.buf.String() }

func nonInteractiveGitEnv(env []string) []string {
	sshCommand := "ssh -oBatchMode=yes"
	for _, value := range env {
		if strings.HasPrefix(value, "GIT_SSH_COMMAND=") {
			sshCommand = strings.TrimPrefix(value, "GIT_SSH_COMMAND=") + " -oBatchMode=yes"
			break
		}
	}
	overridden := map[string]struct{}{
		"GIT_OPTIONAL_LOCKS": {}, "GIT_TERMINAL_PROMPT": {}, "GIT_ASKPASS": {},
		"SSH_ASKPASS": {}, "SSH_ASKPASS_REQUIRE": {}, "GCM_INTERACTIVE": {}, "GIT_SSH_COMMAND": {},
	}
	result := make([]string, 0, len(env)+len(overridden))
	for _, value := range env {
		key, _, ok := strings.Cut(value, "=")
		if _, replace := overridden[key]; ok && replace {
			continue
		}
		result = append(result, value)
	}
	return append(result,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",
		"SSH_ASKPASS=true",
		"SSH_ASKPASS_REQUIRE=never",
		"GCM_INTERACTIVE=never",
		"GIT_SSH_COMMAND="+sshCommand,
	)
}

var (
	branchProbeCSI        = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	branchProbeOSC        = regexp.MustCompile(`\x1b\][^\x07]*(?:\x07|\x1b\\)`)
	branchProbeCredential = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@|((?:token|password|passwd|authorization|credential)\s*[=:]\s*)[^\r\n]+`)
)

func sanitizeBranchProbeDiagnostics(value string, limit int) string {
	value = branchProbeCSI.ReplaceAllString(value, "")
	value = branchProbeOSC.ReplaceAllString(value, "")
	value = branchProbeCredential.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.Index(match, "://"); index >= 0 {
			return match[:index+3] + "[redacted]@"
		}
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			return match[:index+1] + "[redacted]"
		}
		return "[redacted]"
	})
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= ' ' && r != 0x7f && (r < 0x80 || r > 0x9f)) {
			return r
		}
		return -1
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

// BranchName returns the full branch name for a feature.
func BranchName(featureSlug string) string {
	return "feature/" + featureSlug
}

// BranchExistsOnRemote checks whether a branch exists on the origin remote.
// Returns false if the bounded probe is unavailable or confirms absence.
// New callers should use ProbeRemoteBranch to retain the tri-state result.
func BranchExistsOnRemote(repoPath, branch string) bool {
	return ProbeRemoteBranch(context.Background(), repoPath, branch, BranchProbeOptions{}).State == BranchProbeCollision
}

// HasOriginRemote returns true if the repo at repoPath has an "origin" remote configured.
func HasOriginRemote(repoPath string) bool {
	cmd := readGitCmd(repoPath, "remote")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true
		}
	}
	return false
}

// CreateBackupBranch creates a backup branch at the current HEAD in the given worktree.
// Returns the branch name. Format: feature/<slug>-pre-rewind-<unix_timestamp>
func CreateBackupBranch(worktreePath, slug string) (string, error) {
	branchName := fmt.Sprintf("feature/%s-pre-rewind-%d", slug, time.Now().Unix())
	cmd := exec.Command("git", "branch", branchName)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating backup branch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return branchName, nil
}
