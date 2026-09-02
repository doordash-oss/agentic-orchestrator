// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// renderLines renders e and returns its non-empty output lines.
func renderLines(t *testing.T, e Error) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := Fprint(&buf, e); err != nil {
		t.Fatalf("Fprint() error = %v", err)
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// assertNoTrailingWhitespace fails when any rendered line ends in spaces or
// tabs.
func assertNoTrailingWhitespace(t *testing.T, lines []string) {
	t.Helper()
	for i, line := range lines {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d carries trailing whitespace: %q", i+1, line)
		}
	}
}

func TestFprintRendersOneErrorPerClass(t *testing.T) {
	cases := []struct {
		name       string
		e          Error
		wantFirst  string
		wantHint   bool
		wantDetail bool
	}{
		{
			name:      "blocking with hint and diagnostics",
			e:         New(InvalidUsage, WithParams(UsageParams{Reason: "unknown flag: --bogus"})),
			wantFirst: "error[invalid_usage]: Invalid usage",
			wantHint:  true,
		},
		{
			name:      "warning without hint or diagnostics",
			e:         New(RebaseAlreadyUpToDate),
			wantFirst: "warning[rebase_already_up_to_date]: Already up to date",
		},
		{
			name:       "needs_action with hint",
			e:          New(ParentWorktreesDirty, WithDiagnostics("")),
			wantFirst:  "needs-action[parent_worktrees_dirty]: Parent worktrees are dirty",
			wantHint:   true,
			wantDetail: false,
		},
		{
			name:       "blocking with diagnostics only",
			e:          New(DesktopLaunchFailed, WithDiagnostics("application is not registered")),
			wantFirst:  "error[desktop_launch_failed]: Desktop launch failed",
			wantHint:   true,
			wantDetail: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := renderLines(t, tc.e)
			if len(lines) < 2 {
				t.Fatalf("rendered %d lines; want heading and summary:\n%v", len(lines), lines)
			}
			if lines[0] != tc.wantFirst {
				t.Errorf("first line = %q; want %q", lines[0], tc.wantFirst)
			}
			if lines[1] != "  "+tc.e.Summary {
				t.Errorf("summary line = %q; want indented %q", lines[1], tc.e.Summary)
			}
			var hasHint, hasDetail bool
			for _, line := range lines[1:] {
				if strings.HasPrefix(line, "  hint: ") {
					hasHint = true
				}
				if strings.HasPrefix(line, "  detail: ") {
					hasDetail = true
				}
			}
			if hasHint != tc.wantHint {
				t.Errorf("hint line present = %v; want %v", hasHint, tc.wantHint)
			}
			if hasDetail != tc.wantDetail {
				t.Errorf("detail line present = %v; want %v", hasDetail, tc.wantDetail)
			}
			assertNoTrailingWhitespace(t, lines)
		})
	}
}

func TestFprintOmitsHintWhenEntryHasNone(t *testing.T) {
	// RebaseAlreadyUpToDate authors no remediation hint.
	rendered := New(RebaseAlreadyUpToDate)
	if rendered.Remediation != nil {
		t.Fatalf("rebase_already_up_to_date unexpectedly carries a hint: %#v", rendered.Remediation)
	}
	for _, line := range renderLines(t, rendered) {
		if strings.HasPrefix(line, "  hint:") {
			t.Fatalf("hint line rendered for a hint-less entry: %q", line)
		}
	}
}

func TestFprintIndentsEveryMultiLineDiagnosticsLine(t *testing.T) {
	e := New(InvalidUsage, WithDiagnostics("first line\nsecond line\n\nthird line"))
	lines := renderLines(t, e)
	want := []string{
		"error[invalid_usage]: Invalid usage",
		"  " + e.Summary,
		"  hint: " + e.Remediation.Hint,
		"  detail: first line",
		"    second line",
		"",
		"    third line",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rendered:\n%q\nwant:\n%q", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	assertNoTrailingWhitespace(t, lines)
}

func TestFprintRendersContextBlocksAsKeyValuesBetweenHintAndDetail(t *testing.T) {
	// The renderer test populates every context block directly: no single
	// catalog code declares all four today, and the renderer must render
	// whatever blocks a rendered error carries.
	e := New(ParentWorktreesDirty, WithDiagnostics("raw failure text"))
	e.Context = &Context{
		Repositories: []CodeRepository{
			{Name: "web", Branch: "main", DirtyFiles: []string{"a.go", "b.go"}},
			{Name: "api", ConflictFiles: []string{"go.mod"}, MergeHEAD: "abc123"},
		},
		SetupTask: &CodeSetupTask{Key: "worktree:beta", Kind: "worktree", Label: "Worktree: beta"},
		Phase:     &CodePhase{Name: "implement", Iteration: 2},
		Command:   &CodeCommand{ExitCode: 1, LogPaths: []string{"/tmp/a.log", "/tmp/b.log"}},
	}

	lines := renderLines(t, e)
	want := []string{
		"needs-action[parent_worktrees_dirty]: Parent worktrees are dirty",
		"  " + e.Summary,
		"  hint: " + e.Remediation.Hint,
		"  repository: web, branch main",
		"    dirty_files: a.go, b.go",
		"  repository: api",
		"    conflict_files: go.mod",
		"    merge_head: abc123",
		"  setup_task: Worktree: beta, kind worktree",
		"  phase: implement (iteration 2)",
		"  exit_code: 1",
		"  log_paths: /tmp/a.log, /tmp/b.log",
		"  detail: raw failure text",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rendered:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	assertNoTrailingWhitespace(t, lines)
}

func TestFprintZeroValueErrorRendersFallbackOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := Fprint(&buf, Error{}); err != nil {
		t.Fatalf("Fprint() error = %v", err)
	}
	want := "error[internal_error]: Internal error\n  The server could not complete the request.\n"
	if buf.String() != want {
		t.Fatalf("zero-value render = %q; want %q", buf.String(), want)
	}
}

func TestFprintNeverDuplicatesTitleInSummary(t *testing.T) {
	e := New(BadRequest)
	lines := renderLines(t, e)
	if len(lines) < 2 {
		t.Fatalf("rendered %d lines; want heading and summary", len(lines))
	}
	if lines[1] == lines[0] {
		t.Fatalf("summary line duplicates the heading: %q", lines[1])
	}
}

func TestPartWritersComposeHeadingSummaryAndHint(t *testing.T) {
	e := New(ProtocolViolation, WithParams(ViolationParams{Check: "artifact contract", Count: 2}))
	var buf bytes.Buffer
	if err := FprintHeading(&buf, e); err != nil {
		t.Fatalf("FprintHeading() error = %v", err)
	}
	if err := FprintSummary(&buf, e); err != nil {
		t.Fatalf("FprintSummary() error = %v", err)
	}
	buf.WriteString("  - review-feedback.md: malformed verdict\n  - progress.md: missing\n")
	if err := FprintHint(&buf, e); err != nil {
		t.Fatalf("FprintHint() error = %v", err)
	}
	want := "error[protocol_violation]: Protocol violation\n" +
		"  The artifact contract check found 2 violations.\n" +
		"  - review-feedback.md: malformed verdict\n" +
		"  - progress.md: missing\n" +
		"  hint: " + e.Remediation.Hint + "\n"
	// The composed block must also contain the summary; FprintSummary writes it.
	var full bytes.Buffer
	_ = FprintHeading(&full, e)
	_ = FprintSummary(&full, e)
	_ = FprintHint(&full, e)
	if !strings.Contains(full.String(), e.Summary) {
		t.Fatalf("FprintSummary() output missing the summary: %q", full.String())
	}
	if buf.String() != want {
		t.Fatalf("composed block = %q; want %q", buf.String(), want)
	}
}

func TestRenderedOutputHasNoTimestamps(t *testing.T) {
	e := New(ShutdownIncomplete, WithDiagnostics("close server: context deadline exceeded"))
	var buf bytes.Buffer
	if err := Fprint(&buf, e); err != nil {
		t.Fatalf("Fprint() error = %v", err)
	}
	// log.Printf-style prefixes look like 2026/09/01 15:46:12; the renderer
	// must never emit one.
	ts := regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)
	if ts.MatchString(buf.String()) {
		t.Fatalf("rendered output carries a timestamp prefix: %q", buf.String())
	}
}

func TestCLICodeClassesAndTemplates(t *testing.T) {
	blocking := []Code{
		InvalidUsage,
		DesktopLaunchFailed,
		UpdateCheckFailed,
		ContractInputUnreadable,
		RuntimeAlreadyRunning,
		RuntimeInitFailed,
		ServerStartFailed,
		ProtocolViolation,
	}
	for _, code := range blocking {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassBlocking {
			t.Errorf("%s: class = %q; want blocking", code, entry.Class)
		}
	}
	warnings := []Code{
		ProviderUnavailable,
		ProviderVersionCheckFailed,
		ModelCatalogDegraded,
		AssetsReconcileFailed,
		GithubCredentialsMissing,
		StartupMaintenanceFailed,
		ShutdownIncomplete,
	}
	for _, code := range warnings {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassWarning {
			t.Errorf("%s: class = %q; want warning", code, entry.Class)
		}
		if entry.Remediation == "" {
			t.Errorf("%s: warning entry needs a remediation hint", code)
		}
	}

	if got := New(InvalidUsage, WithParams(UsageParams{Reason: "unknown flag: --bogus"})).Summary; got != "unknown flag: --bogus" {
		t.Errorf("invalid_usage summary = %q; want the raw parser message", got)
	}
	if got := New(InvalidUsage).Summary; got == "unknown flag: --bogus" || got == "" {
		t.Errorf("invalid_usage static summary = %q; want the authored fallback", got)
	}
	if got := New(ProtocolViolation, WithParams(ViolationParams{Check: "evidence contract", Count: 3})).Summary; got != "The evidence contract check found 3 violations." {
		t.Errorf("protocol_violation summary = %q; want interpolated check and count", got)
	}
	if got := New(ProviderUnavailable, WithParams(ProviderUnavailableParams{SetupCapable: true})).Summary; !strings.Contains(got, "setup-capable") {
		t.Errorf("provider_unavailable setup-capable summary = %q; want it to name setup-capable mode", got)
	}
}
