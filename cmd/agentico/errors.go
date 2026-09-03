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

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// renderError renders one canonical error to w through the shared renderer.
// Rendering failures are unrecoverable for a terminal writer, so the write
// error is deliberately dropped.
func renderError(w io.Writer, code errcat.Code, opts ...errcat.Option) {
	_ = errcat.Fprint(w, errcat.New(code, opts...))
}

// startupWarning is one structured startup degradation: a catalog warning
// code, optional summary params, and raw diagnostics. Helpers that used to
// return pre-formatted `Warning:` strings return these instead, and the CLI
// renders them — only the catalog authors text.
type startupWarning struct {
	code        errcat.Code
	params      errcat.Params
	diagnostics string
}

func (s startupWarning) render(w io.Writer) {
	var opts []errcat.Option
	if s.params != nil {
		opts = append(opts, errcat.WithParams(s.params))
	}
	if s.diagnostics != "" {
		opts = append(opts, errcat.WithDiagnostics(s.diagnostics))
	}
	renderError(w, s.code, opts...)
}

// renderStartupWarnings renders each warning in order.
func renderStartupWarnings(w io.Writer, warnings []startupWarning) {
	for _, s := range warnings {
		s.render(w)
	}
}

// renderProtocolViolations renders a protocol-violation failure for one
// validation check: a single error heading whose summary names the check and
// the violation count, each violation listed beneath as its own
// `- <artifact>: <reason>` line, then the remediation hint. A not-OK outcome
// with zero violations renders the heading and summary alone.
func renderProtocolViolations(w io.Writer, check string, violations []agent.ProtocolViolation) {
	e := errcat.New(errcat.ProtocolViolation, errcat.WithParams(errcat.ViolationParams{
		Check: check,
		Count: len(violations),
	}))
	_ = errcat.FprintHeading(w, e)
	_ = errcat.FprintSummary(w, e)
	for _, v := range violations {
		line := "  - " + strings.TrimSpace(v.Reason)
		if artifact := strings.TrimSpace(v.Artifact); artifact != "" {
			line = "  - " + artifact + ": " + strings.TrimSpace(v.Reason)
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " \t"))
	}
	if len(violations) > 0 {
		_ = errcat.FprintHint(w, e)
	}
}

// alreadyRunningError marks the discovery decision that another Agentic
// server is already running for this runtime directory.
type alreadyRunningError struct {
	baseURL string
}

func (e alreadyRunningError) Error() string {
	return fmt.Sprintf("Agentic server is already running at %s", e.baseURL)
}

// runtimeInitError marks headless startup failures in runtime
// initialization: fx wiring, provider selection, auth-token preparation,
// server-name resolution, and discovery-metadata validation.
type runtimeInitError struct {
	err error
}

func (e *runtimeInitError) Error() string { return e.err.Error() }
func (e *runtimeInitError) Unwrap() error { return e.err }

// serverStartError marks listen resolution, server start, and discovery
// publish failures.
type serverStartError struct {
	err error
}

func (e *serverStartError) Error() string { return e.err.Error() }
func (e *serverStartError) Unwrap() error { return e.err }

// toolStartupError marks a blocking required-tool failure carrying the
// structured tool issue (catalog code plus diagnostics).
type toolStartupError struct {
	issues []agent.ToolIssue
}

func (e *toolStartupError) Error() string {
	parts := make([]string, 0, len(e.issues))
	for _, issue := range e.issues {
		parts = append(parts, issue.Diagnostics)
	}
	return strings.Join(parts, "\n")
}

// classifyStartupFailure maps one headless startup failure to its canonical
// catalog error. It is pure — no I/O, no listener — so every family is
// testable without binding a socket: the instance-lock-busy error and the
// already-running decision are runtime_already_running; runtime-init and
// server-start markers map to their codes; a blocking required-tool issue
// keeps its own catalog code; anything unrecognized falls back to the
// internal-error code. The original error text always rides along as
// diagnostics.
func classifyStartupFailure(err error) errcat.Error {
	var lockBusy runtimeLockBusyError
	if errors.As(err, &lockBusy) {
		return errcat.New(errcat.RuntimeAlreadyRunning,
			errcat.WithParams(errcat.AlreadyRunningParams{Detail: "state dir " + lockBusy.stateDir}),
			errcat.WithDiagnostics(err.Error()),
		)
	}
	var already alreadyRunningError
	if errors.As(err, &already) {
		return errcat.New(errcat.RuntimeAlreadyRunning,
			errcat.WithParams(errcat.AlreadyRunningParams{Detail: already.baseURL}),
			errcat.WithDiagnostics(err.Error()),
		)
	}
	var tool *toolStartupError
	if errors.As(err, &tool) {
		return errcat.New(tool.issues[0].Code, errcat.WithDiagnostics(tool.Error()))
	}
	var initErr *runtimeInitError
	if errors.As(err, &initErr) {
		return errcat.New(errcat.RuntimeInitFailed, errcat.WithDiagnostics(err.Error()))
	}
	var startErr *serverStartError
	if errors.As(err, &startErr) {
		return errcat.New(errcat.ServerStartFailed, errcat.WithDiagnostics(err.Error()))
	}
	return errcat.New(errcat.InternalError, errcat.WithDiagnostics(err.Error()))
}

// renderStartupFailure renders a classified headless startup fatal.
func renderStartupFailure(w io.Writer, err error) {
	_ = errcat.Fprint(w, classifyStartupFailure(err))
}

// reportDeferredClose renders a deferred close failure as a
// shutdown-incomplete warning: no timestamp, no exit-code change. Deferred
// close errors never affected the process exit status and still do not.
func reportDeferredClose(w io.Writer, what string, err error) {
	if err == nil {
		return
	}
	renderError(w, errcat.ShutdownIncomplete, errcat.WithDiagnostics(what+": "+err.Error()))
}
