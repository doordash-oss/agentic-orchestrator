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

package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Failure classification codes carried on terminal records. The server maps
// these onto canonical error codes.
const (
	FailureTimeout                = "clone_timeout"
	FailureAuthentication         = "clone_authentication_failed"
	FailureTrust                  = "clone_trust_failed"
	FailureExecution              = "clone_execution_failed"
	FailurePromptUnsupported      = "clone_prompt_unsupported"
	FailurePublicationUnsupported = "clone_publication_unsupported"
	FailureDestinationConflict    = "clone_destination_conflict"
	FailureCancelled              = "clone_cancelled"
	FailureInterrupted            = "clone_interrupted"
	FailureCleanupPending         = "clone_cleanup_pending"
)

// RunSpec describes one bounded git clone execution.
type RunSpec struct {
	Remote   string
	Staging  string
	Deadline time.Duration
	// Env adds extra environment assignments on top of the preserved
	// server environment (tests use it to script fake git binaries).
	Env []string
	// OnStage is called with authored stage/progress updates derived from
	// sanitized git progress output. It must be cheap and non-blocking.
	OnStage func(stage, progress string)
}

// RunResult is the bounded outcome of one execution.
type RunResult struct {
	ExitCode   int
	Err        error
	TimedOut   bool
	OutputTail string // redacted, bounded diagnostic tail
}

// RunHandle is a live spawned clone process tree.
type RunHandle interface {
	Pid() int
	Pgid() int
	StartIdentity() string
	// Wait blocks until the process tree exits and pipes are drained
	// within their bound. Safe to call once.
	Wait() RunResult
	// Terminate stops the whole process tree (SIGTERM, bounded grace,
	// SIGKILL to the group) and reaps it. Idempotent.
	Terminate()
}

// Runner spawns clone executions. The real runner shells out to git with an
// argument vector; tests inject deterministic fake runners.
type Runner interface {
	Start(spec RunSpec) (RunHandle, error)
}

// realRunner spawns git over an argument vector, never a shell.
type realRunner struct {
	gitBin string
}

// NewRealRunner returns a Runner that executes the git binary directly.
func NewRealRunner(gitBin string) Runner {
	if strings.TrimSpace(gitBin) == "" {
		gitBin = "git"
	}
	return &realRunner{gitBin: gitBin}
}

// cloneEnvironment returns the environment for clone workers: the server's
// own environment (credential helpers, SSH agent, proxies, ssh config and
// host trust all preserved) plus non-interactive prompt suppression. Trust
// checking is never weakened: batch mode only disables interactive
// password/passphrase prompts, not host key verification.
func cloneEnvironment() []string {
	env := os.Environ()
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/true",
		"SSH_ASKPASS=/usr/bin/true",
		"SSH_ASKPASS_REQUIRE=never",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes",
	)
	return env
}

// promptDisabledAskpass reports whether the askpass helper path exists; if
// /usr/bin/true is missing the environment still disables terminal
// prompts, and hangs surface as the bounded deadline instead.
func promptDisabledAskpass() bool {
	_, err := os.Stat("/usr/bin/true")
	return err == nil
}

type realHandle struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	// done is closed exactly once by await after result is set, so any
	// number of Wait/Terminate callers can observe completion safely.
	done       chan struct{}
	result     RunResult
	pid        int
	pgid       int
	identity   string
	termOnce   sync.Once
	tailMu     sync.Mutex
	tail       []string
	progressFn func(stage, progress string)
	// line is owned by exec's stderr-copy goroutine until cmd.Wait returns.
	// Only the bounded prefix is retained; excess bytes are still consumed.
	line []byte
}

func (r *realRunner) Start(spec RunSpec) (RunHandle, error) {
	deadline := spec.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	cmd := exec.CommandContext(ctx, r.gitBin, "clone", "--progress", spec.Remote, spec.Staging)
	cmd.Env = append(cloneEnvironment(), spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Hard-stop the whole group on deadline; WaitDelay bounds the wait so
	// descendants holding pipes open cannot stall the reap.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	h := &realHandle{
		cmd:        cmd,
		cancel:     cancel,
		done:       make(chan struct{}),
		progressFn: spec.OnStage,
	}
	// Let exec own pipe draining so Wait joins the writer before publishing
	// the final diagnostics. Stopping consumption can block a verbose clone.
	cmd.Stderr = h
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start git clone: %w", err)
	}
	h.pid = cmd.Process.Pid
	h.pgid = cmd.Process.Pid
	h.identity = processStartIdentity(cmd.Process.Pid)
	go h.await(ctx)
	return h, nil
}

// Write drains all stderr while retaining a bounded prefix of each record.
// Git progress uses carriage returns as well as newlines; either terminates
// a record, including an overlong one, so later diagnostics are not lost.
func (h *realHandle) Write(data []byte) (int, error) {
	const maxLine = 8 * 1024
	for _, b := range data {
		if b == '\n' || b == '\r' {
			h.flushLine()
		} else if len(h.line) < maxLine {
			h.line = append(h.line, b)
		}
	}
	return len(data), nil
}

func (h *realHandle) flushLine() {
	if len(h.line) > 0 {
		h.ingest(string(h.line))
		h.line = h.line[:0]
	}
}

// progressPattern matches git's carriage-return progress counters, e.g.
// "Receiving objects:  45% (12/26), 4.00 KiB | 4.00 MiB/s".
var progressPattern = regexp.MustCompile(`^(Counting|Compressing|Receiving|Resolving|Updating) [a-z]+:.*`)

// ingest classifies one output line into authored stage/progress and a
// bounded redacted diagnostic tail.
func (h *realHandle) ingest(line string) {
	clean := stripControls(line)
	if clean == "" {
		return
	}
	if h.progressFn != nil && progressPattern.MatchString(clean) {
		h.progressFn(StageTransferring, boundProgress(clean))
	}
	h.tailMu.Lock()
	if len(h.tail) >= 8 {
		h.tail = h.tail[1:]
	}
	h.tail = append(h.tail, RedactDiagnostics(clean))
	h.tailMu.Unlock()
}

// stripControls removes control characters (including CR) from an output
// fragment so control-heavy output cannot disrupt rendering or storage.
func stripControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func boundProgress(s string) string {
	if len(s) > MaxProgressLength {
		s = s[:MaxProgressLength]
	}
	return s
}

var (
	credentialPattern = regexp.MustCompile(`(?i)(https?|ssh)://[^\s/@:]+:[^\s/@]+@[^\s]+`)
	tokenPattern      = regexp.MustCompile(`(?i)(token|password|secret|api[_-]?key)["'= :]+[^\s,;]+`)
	bearerPattern     = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	longSecretPattern = regexp.MustCompile(`\b[0-9a-f]{32,}\b|\b[A-Za-z0-9_-]{28,}\b`)
)

// RedactDiagnostics removes credential-shaped substrings (embedded
// user:pass URLs, token/password assignments, bearer tokens, long opaque
// secret-like tokens) from a diagnostic fragment before it is persisted,
// logged, streamed or shipped over IPC.
func RedactDiagnostics(s string) string {
	s = credentialPattern.ReplaceAllString(s, "$1://[redacted]@")
	s = tokenPattern.ReplaceAllString(s, "$1=[redacted]")
	s = bearerPattern.ReplaceAllString(s, "bearer [redacted]")
	s = longSecretPattern.ReplaceAllString(s, "[redacted]")
	if len(s) > MaxDiagnosticsLength {
		s = s[:MaxDiagnosticsLength]
	}
	return s
}

func (h *realHandle) await(ctx context.Context) {
	waitErr := h.cmd.Wait()
	h.flushLine()
	h.cancel()
	result := RunResult{Err: waitErr, OutputTail: h.tailText()}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			result.Err = nil
		}
	}
	h.result = result
	close(h.done)
}

func (h *realHandle) tailText() string {
	h.tailMu.Lock()
	defer h.tailMu.Unlock()
	return RedactDiagnostics(strings.Join(h.tail, "\n"))
}

func (h *realHandle) Pid() int              { return h.pid }
func (h *realHandle) Pgid() int             { return h.pgid }
func (h *realHandle) StartIdentity() string { return h.identity }

func (h *realHandle) Wait() RunResult {
	<-h.done
	return h.result
}

// Terminate stops the process tree: SIGTERM to the group, a bounded grace
// period, then SIGKILL to the group. It does not block on completion; pair
// it with Wait (directly or via the worker) to observe exit and pipe
// draining within their bound.
func (h *realHandle) Terminate() {
	h.termOnce.Do(func() {
		_ = syscall.Kill(-h.pgid, syscall.SIGTERM)
		select {
		case <-h.done:
			return
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-h.pgid, syscall.SIGKILL)
		}
	})
}

// ClassifyFailure maps a bounded, redacted run outcome onto a failure code.
func ClassifyFailure(res RunResult) string {
	if res.TimedOut {
		return FailureTimeout
	}
	tail := strings.ToLower(res.OutputTail)
	switch {
	case strings.Contains(tail, "authentication failed"),
		strings.Contains(tail, "could not read username"),
		strings.Contains(tail, "permission denied"),
		strings.Contains(tail, "fatal: could not authenticate"),
		strings.Contains(tail, "access denied"):
		return FailureAuthentication
	case strings.Contains(tail, "host key verification failed"),
		strings.Contains(tail, "host key for"),
		strings.Contains(tail, "identity file"),
		strings.Contains(tail, "no such identity"):
		return FailureTrust
	case strings.Contains(tail, "terminal prompts disabled") && !promptDisabledAskpass():
		return FailurePromptUnsupported
	case strings.Contains(tail, "timed out"), strings.Contains(tail, "connection timed out"),
		strings.Contains(tail, "operation timed out"):
		return FailureTimeout
	default:
		return FailureExecution
	}
}

// IsProcessAlive reports whether a process (by pid) currently exists.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// GroupAlive reports whether any process in the group exists.
func GroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	return syscall.Kill(-pgid, 0) == nil
}

// VerifyProcessIdentity re-reads the current process start identity for pid
// and reports whether it matches the recorded identity. PID, path, or
// marker alone is never sufficient proof; a mismatch or unreadable
// identity means PID reuse or uncertainty, and callers must refuse to
// signal or delete based on the pid alone.
func VerifyProcessIdentity(pid int, recorded string) bool {
	if pid <= 0 || recorded == "" {
		return false
	}
	return processStartIdentity(pid) == recorded
}

// redactRemoteForRecord prepares the remote for durable storage. The
// validator has already rejected password- and token-bearing remotes, so
// this is defense in depth.
func redactRemoteForRecord(remote string) string {
	return RedactDiagnostics(remote)
}

var _ = bytes.MinRead
