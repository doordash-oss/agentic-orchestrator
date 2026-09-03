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
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
)

func firstLine(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("output is empty")
	}
	return lines[0]
}

func TestRunArgsParseFailuresRenderInvalidUsage(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantSummary string
	}{
		{
			name:        "unknown flag on desktop path",
			args:        []string{"--bogus"},
			wantSummary: "unknown flag: --bogus",
		},
		{
			name:        "unknown command",
			args:        []string{"feature"},
			wantSummary: "unknown command: feature",
		},
		{
			name:        "server-only flag on desktop path",
			args:        []string{"--listen", "8080"},
			wantSummary: "--listen is available only with the headless server; run 'agentico server --listen ...'",
		},
		{
			name:        "update parse error",
			args:        []string{"update", "--bogus"},
			wantSummary: "unknown flag: --bogus",
		},
		{
			name:        "validate-artifacts parse error",
			args:        []string{cliSubcommandValidateArtifacts, "--bogus"},
			wantSummary: "unknown validate-artifacts flag: --bogus",
		},
		{
			name:        "verify-evidence parse error",
			args:        []string{cliSubcommandVerifyEvidence, "--bogus"},
			wantSummary: "unknown verify-evidence flag: --bogus",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runArgs(tc.args, &stdout, &stderr, failingServerLauncher(t), failingUpdater(t))
			if code != 1 {
				t.Fatalf("runArgs(%v) code = %d, want 1", tc.args, code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if got := firstLine(t, stderr.String()); got != "error[invalid_usage]: Invalid usage" {
				t.Fatalf("stderr first line = %q, want the invalid-usage heading:\n%s", got, stderr.String())
			}
			if !strings.Contains(stderr.String(), "  "+tc.wantSummary+"\n") {
				t.Fatalf("stderr = %q, want summary line %q", stderr.String(), tc.wantSummary)
			}
			if !strings.Contains(stderr.String(), "  hint: Run 'agentico --help'") {
				t.Fatalf("stderr = %q, want the help hint", stderr.String())
			}
		})
	}
}

func TestRunArgsHelpAndVersionWriteNothingToStderr(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"-h"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		code := runArgs(args, &stdout, &stderr, failingServerLauncher(t), failingUpdater(t))
		if code != 0 {
			t.Fatalf("runArgs(%v) code = %d, want 0", args, code)
		}
		if stderr.Len() != 0 {
			t.Fatalf("runArgs(%v) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestRunArgsServerProviderSelection(t *testing.T) {
	t.Run("no valid names exits through the normal path", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var launchedServer bool
		code := runArgs(
			[]string{cliSubcommandServer, "--config", testServerConfigPath, "--state-dir", testStateFeaturesDir, "--providers", "bogus,also-bogus"},
			&stdout, &stderr,
			func(string, string, bool, []string, bool, string, string) int {
				launchedServer = true
				return 0
			},
			failingUpdater(t),
		)
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if launchedServer {
			t.Fatal("server launcher ran despite an empty valid-provider set")
		}
		if got := firstLine(t, stderr.String()); got != "error[invalid_usage]: Invalid usage" {
			t.Fatalf("stderr first line = %q, want the invalid-usage heading:\n%s", got, stderr.String())
		}
		if !strings.Contains(stderr.String(), "no valid providers specified in --providers flag") {
			t.Fatalf("stderr = %q, want the no-valid-providers summary", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	})
	t.Run("unknown names warn and the valid set launches", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var gotProviders []string
		code := runArgs(
			[]string{cliSubcommandServer, "--config", testServerConfigPath, "--state-dir", testStateFeaturesDir, "--providers", "claude,bogus"},
			&stdout, &stderr,
			func(_ string, _ string, _ bool, providers []string, _ bool, _ string, _ string) int {
				gotProviders = providers
				return 0
			},
			failingUpdater(t),
		)
		if code != 0 {
			t.Fatalf("code = %d, want 0; stderr = %q", code, stderr.String())
		}
		if got := firstLine(t, stderr.String()); got != "warning[provider_unavailable]: Provider unavailable" {
			t.Fatalf("stderr first line = %q, want the unknown-provider warning heading:\n%s", got, stderr.String())
		}
		if !strings.Contains(stderr.String(), `unknown provider "bogus", skipping`) {
			t.Fatalf("stderr = %q, want the unknown-provider diagnostics", stderr.String())
		}
		if len(gotProviders) != 1 || gotProviders[0] != providerNameClaude {
			t.Fatalf("launcher providers = %v, want [claude]", gotProviders)
		}
	})
}

func TestRunArgsVerifyEvidenceUnreadableContractRendersContractInputUnreadable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	contractPath := filepath.Join(t.TempDir(), "missing-contract.yaml")
	code := runArgs(
		[]string{cliSubcommandVerifyEvidence, cliFlagContract, contractPath, cliFlagDir, t.TempDir()},
		&stdout, &stderr,
		failingServerLauncher(t), failingUpdater(t),
	)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if got := firstLine(t, stderr.String()); got != "error[contract_input_unreadable]: Contract input unreadable" {
		t.Fatalf("stderr first line = %q, want the contract-input heading:\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "  detail: reading testing contract: ") {
		t.Fatalf("stderr = %q, want the read error under detail", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunArgsValidateArtifactsInvalidPhaseRendersInvalidUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runArgs(
		[]string{cliSubcommandValidateArtifacts, cliFlagPhase, "not-a-phase", cliFlagRole, "implementer", cliFlagDir, t.TempDir()},
		&stdout, &stderr,
		failingServerLauncher(t), failingUpdater(t),
	)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if got := firstLine(t, stderr.String()); got != "error[invalid_usage]: Invalid usage" {
		t.Fatalf("stderr first line = %q, want the invalid-usage heading:\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), `unknown phase "not-a-phase"`) {
		t.Fatalf("stderr = %q, want the raw phase error in the summary", stderr.String())
	}
}

func TestRunArgsValidateArtifactsZeroViolationsRendersHeadingAndSummaryAlone(t *testing.T) {
	// An unregistered role yields exactly one violation today, so drive the
	// zero-violation branch directly through the renderer helper.
	var buf bytes.Buffer
	renderProtocolViolations(&buf, "artifact contract", nil)
	want := "error[protocol_violation]: Protocol violation\n  The artifact contract check found 0 violations.\n"
	if buf.String() != want {
		t.Fatalf("rendered = %q, want heading and summary alone:\n%q", buf.String(), want)
	}
}

func TestRenderProtocolViolationsListsViolationsInOrder(t *testing.T) {
	var buf bytes.Buffer
	renderProtocolViolations(&buf, "evidence contract", []agent.ProtocolViolation{
		{Artifact: "screenshots/home.png", Reason: "file is empty"},
		{Reason: "behavioral evidence has no capture"},
	})
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	want := []string{
		"error[protocol_violation]: Protocol violation",
		"  The evidence contract check found 2 violations.",
		"  - screenshots/home.png: file is empty",
		"  - behavioral evidence has no capture",
		"  hint: " + errcat.New(errcat.ProtocolViolation).Remediation.Hint,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rendered:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestClassifyStartupFailure(t *testing.T) {
	cases := []struct {
		name            string
		err             error
		wantCode        errcat.Code
		wantSummaryHas  string
		wantDiagnostics string
	}{
		{
			name:            "instance lock busy",
			err:             runtimeLockBusyError{stateDir: "/tmp/features", owner: instancelock.Owner{PID: 123}},
			wantCode:        errcat.RuntimeAlreadyRunning,
			wantSummaryHas:  "/tmp/features",
			wantDiagnostics: "Another Agentic instance is already running",
		},
		{
			name:            "discovery already running",
			err:             alreadyRunningError{baseURL: "http://127.0.0.1:4317"},
			wantCode:        errcat.RuntimeAlreadyRunning,
			wantSummaryHas:  "http://127.0.0.1:4317",
			wantDiagnostics: "Agentic server is already running at http://127.0.0.1:4317",
		},
		{
			name:     "missing required tool",
			err:      &toolStartupError{issues: []agent.ToolIssue{{Code: errcat.MissingExecutable, Diagnostics: "git not found in PATH"}}},
			wantCode: errcat.MissingExecutable,
		},
		{
			name:            "runtime init failure",
			err:             &runtimeInitError{fmt.Errorf("initializing: fx boom")},
			wantCode:        errcat.RuntimeInitFailed,
			wantDiagnostics: "initializing: fx boom",
		},
		{
			name:            "wrapped runtime init failure",
			err:             fmt.Errorf("bootstrap: %w", &runtimeInitError{errors.New("auth token boom")}),
			wantCode:        errcat.RuntimeInitFailed,
			wantDiagnostics: "auth token boom",
		},
		{
			name:            "server start failure",
			err:             &serverStartError{fmt.Errorf("starting server: listen boom")},
			wantCode:        errcat.ServerStartFailed,
			wantDiagnostics: "starting server: listen boom",
		},
		{
			name:            "unrecognized error falls back",
			err:             errors.New("mystery failure"),
			wantCode:        errcat.InternalError,
			wantDiagnostics: "mystery failure",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := classifyStartupFailure(tc.err)
			if rendered.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", rendered.Code, tc.wantCode)
			}
			if tc.wantSummaryHas != "" && !strings.Contains(rendered.Summary, tc.wantSummaryHas) {
				t.Errorf("summary = %q, want it to contain %q", rendered.Summary, tc.wantSummaryHas)
			}
			if tc.wantDiagnostics != "" && !strings.Contains(rendered.Diagnostics, tc.wantDiagnostics) {
				t.Errorf("diagnostics = %q, want it to contain %q", rendered.Diagnostics, tc.wantDiagnostics)
			}
		})
	}
}

func TestDeferredCloseWarningRendersWithoutTimestampAndKeepsExitCode(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := func() (code int) {
		defer func() {
			reportDeferredClose(&stderr, "close server", errors.New("listener leak"))
		}()
		return 0
	}()
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; deferred close errors never change it", exitCode)
	}
	out := stderr.String()
	if got := firstLine(t, out); got != "warning[shutdown_incomplete]: Shutdown incomplete" {
		t.Fatalf("stderr first line = %q, want the shutdown warning heading:\n%s", got, out)
	}
	if !strings.Contains(out, "  detail: close server: listener leak") {
		t.Fatalf("stderr = %q, want the close error under detail", out)
	}
	// log.Printf-style prefixes look like 2026/09/01 15:46:12; the renderer
	// must never emit one.
	ts := regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)
	if ts.MatchString(out) {
		t.Fatalf("stderr carries a timestamp prefix: %q", out)
	}
}

// TestCLINonTestSourcesCarryNoAdHocPrefixes is the static guard for the CLI
// error-rendering contract: no non-test source in this package may print
// pre-formatted `Error: ` / `Warning: ` strings, call log.Printf, or call
// os.Exit outside the top-level entry point.
func TestCLINonTestSourcesCarryNoAdHocPrefixes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	exitsByFile := make(map[string]int)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.STRING &&
					(strings.Contains(node.Value, "Error: ") || strings.Contains(node.Value, "Warning: ")) {
					t.Errorf("%s: string literal %s carries an ad-hoc error/warning prefix; render through errcat instead", name, node.Value)
				}
			case *ast.SelectorExpr:
				if ident, ok := node.X.(*ast.Ident); ok {
					if ident.Name == "log" && node.Sel.Name == "Printf" {
						t.Errorf("%s: log.Printf call; deferred errors render through the shared renderer", name)
					}
					if ident.Name == "os" && node.Sel.Name == "Exit" {
						exitsByFile[name]++
					}
				}
			}
			return true
		})
	}
	total := 0
	for file, count := range exitsByFile {
		total += count
		if file != "main.go" {
			t.Errorf("%s: os.Exit call outside the top-level entry point", file)
		}
	}
	if total != 1 {
		t.Errorf("os.Exit call count = %d, want exactly 1 (the top-level run entry point)", total)
	}
}
