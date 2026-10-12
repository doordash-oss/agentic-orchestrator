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

import "context"

// This file exposes merge-candidate, ref-CAS, and merge-outcome names that
// are internal to the package but still unexported, to the package's
// external tests (package git_test), which stay external because they import
// test/testutil. It also adapts the branch-probe and source-update runner
// boundaries to exported-only shapes, so external tests in other packages
// (feature, server) can script or delegate probe and ref-update commands
// without naming the internal result types.

var (
	CreateMergeCandidate = createMergeCandidate
	UpdateRefCAS         = updateRefCAS

	// ErrMergeCandidateNoChanges reports a child head already contained in
	// the parent tip.
	ErrMergeCandidateNoChanges = errMergeCandidateNoChanges
	// MergeIntoFailed is the merge-into outcome for a non-conflict failure.
	MergeIntoFailed = mergeIntoFailed
)

// ScriptedBranchProbeRunnerForTest adapts a script function over exported
// shapes to the branch-probe runner boundary.
func ScriptedBranchProbeRunnerForTest(fn func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) (stdout, diagnostics string, exitCode int, err error)) branchProbeRunner {
	return scriptedProbeRunnerForTest(fn)
}

type scriptedProbeRunnerForTest func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) (stdout, diagnostics string, exitCode int, err error)

func (f scriptedProbeRunnerForTest) Run(ctx context.Context, repoPath string, args []string, diagnosticLimit int) branchProbeCommandResult {
	stdout, diagnostics, exitCode, err := f(ctx, repoPath, args, diagnosticLimit)
	return branchProbeCommandResult{Stdout: stdout, Diagnostics: diagnostics, ExitCode: exitCode, Err: err}
}

// RunBranchProbeForTest runs one probe command through the production exec
// runner, so an external scripted runner can delegate unscripted commands.
func RunBranchProbeForTest(ctx context.Context, repoPath string, args []string, diagnosticLimit int) (stdout, diagnostics string, exitCode int, err error) {
	result := (execBranchProbeRunner{}).Run(ctx, repoPath, args, diagnosticLimit)
	return result.Stdout, result.Diagnostics, result.ExitCode, result.Err
}

// ScriptedSourceUpdateRefRunnerForTest adapts a script function over exported
// shapes to the source-update ref runner boundary.
func ScriptedSourceUpdateRefRunnerForTest(fn func(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) (stdout, diagnostics string, exitCode int, err error)) sourceUpdateRefRunner {
	return scriptedSourceUpdateRefRunner(fn)
}

type scriptedSourceUpdateRefRunner func(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) (stdout, diagnostics string, exitCode int, err error)

func (f scriptedSourceUpdateRefRunner) Run(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) branchProbeCommandResult {
	stdout, diagnostics, exitCode, err := f(ctx, repoPath, stdin, args, diagnosticLimit)
	return branchProbeCommandResult{Stdout: stdout, Diagnostics: diagnostics, ExitCode: exitCode, Err: err}
}

// RunSourceUpdateRefForTest runs one ref-update command through the
// production exec runner, so an external scripted runner can delegate
// unscripted commands.
func RunSourceUpdateRefForTest(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) (stdout, diagnostics string, exitCode int, err error) {
	result := (execSourceUpdateRefRunner{}).Run(ctx, repoPath, stdin, args, diagnosticLimit)
	return result.Stdout, result.Diagnostics, result.ExitCode, result.Err
}
