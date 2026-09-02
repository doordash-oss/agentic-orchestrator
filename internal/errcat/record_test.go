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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func fullFailureRecord() FailureRecord {
	return FailureRecord{
		Code: IterationBudgetExhausted,
		Context: &RecordContext{
			Phase: &CodePhase{Name: "implement", Iteration: 12},
			Repositories: []CodeRepository{
				{Name: "repo-a", Branch: "feature/one"},
				{Name: "repo-b"},
			},
			Command: &CodeCommand{ExitCode: 1, LogPaths: []string{"/tmp/attempt-01-output.txt"}},
		},
		Diagnostics: "multi-repo implementation failed for repos: repo-a, repo-b",
	}
}

// TestFailureRecordRoundTripsYAMLAndJSON pins the durable record shape: a
// run.yaml failure block and its JSON mirror must both survive a marshal /
// unmarshal cycle with an equal value.
func TestFailureRecordRoundTripsYAMLAndJSON(t *testing.T) {
	record := fullFailureRecord()

	yamlBytes, err := yaml.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML FailureRecord
	if err := yaml.Unmarshal(yamlBytes, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML, record) {
		t.Fatalf("YAML round-trip mismatch:\n got %#v\nwant %#v\nyaml:\n%s", fromYAML, record, yamlBytes)
	}
	if !strings.Contains(string(yamlBytes), "code: iteration_budget_exhausted") {
		t.Fatalf("YAML does not carry the code key:\n%s", yamlBytes)
	}

	jsonBytes, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON FailureRecord
	if err := json.Unmarshal(jsonBytes, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON, record) {
		t.Fatalf("JSON round-trip mismatch:\n got %#v\nwant %#v", fromJSON, record)
	}
}

// TestRenderRecordUnknownCodeFallsBackToInternalError pins the trust boundary:
// a stored record whose code is not in the catalog renders as the internal
// error object, not an empty or raw value.
func TestRenderRecordUnknownCodeFallsBackToInternalError(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code:        "no_such_terminal_code",
		Diagnostics: "raw detail",
	})
	if rendered.Code != InternalError {
		t.Fatalf("unknown record code rendered as %q; want internal_error", rendered.Code)
	}
	if rendered.Class != ClassBlocking || rendered.Title == "" || rendered.Summary == "" {
		t.Fatalf("fallback render is malformed: %#v", rendered)
	}
	if rendered.Diagnostics != "raw detail" {
		t.Fatalf("diagnostics not preserved on fallback: %q", rendered.Diagnostics)
	}
}

// TestRenderRecordSummaryReadsContextBlocks pins the projection contract:
// summaries name the phase and iteration when the record carries a phase
// block, fall back to the static summary without context, and never leak raw
// diagnostics text.
func TestRenderRecordSummaryReadsContextBlocks(t *testing.T) {
	withPhase := fullFailureRecord()
	rendered := RenderRecord(withPhase)
	if !strings.Contains(strings.ToLower(rendered.Summary), "implement") {
		t.Fatalf("summary does not name the phase: %q", rendered.Summary)
	}
	if !strings.Contains(rendered.Summary, "12") {
		t.Fatalf("summary does not name the iteration: %q", rendered.Summary)
	}
	if strings.Contains(rendered.Summary, withPhase.Diagnostics) {
		t.Fatalf("summary leaks raw diagnostics: %q", rendered.Summary)
	}
	if rendered.Context == nil || rendered.Context.Phase == nil || rendered.Context.Phase.Name != "implement" {
		t.Fatalf("phase context block not carried: %#v", rendered.Context)
	}
	if rendered.Context == nil || len(rendered.Context.Repositories) != 2 {
		t.Fatalf("repositories context block not carried: %#v", rendered.Context)
	}

	rendered = RenderRecord(FailureRecord{Code: IterationBudgetExhausted, Diagnostics: "raw detail"})
	entry, ok := Lookup(IterationBudgetExhausted)
	if !ok {
		t.Fatal("iteration_budget_exhausted missing from catalog")
	}
	if rendered.Summary != entry.Summary {
		t.Fatalf("no-context summary = %q; want static %q", rendered.Summary, entry.Summary)
	}
	if strings.Contains(rendered.Summary, "raw detail") {
		t.Fatalf("static summary leaks diagnostics: %q", rendered.Summary)
	}
}

// TestRenderRecordDropsUndeclaredBlocks pins that a record whose context the
// code did not declare renders without those blocks.
func TestRenderRecordDropsUndeclaredBlocks(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code: WorktreeSetupFailed,
		Context: &RecordContext{
			Phase: &CodePhase{Name: "implement"},
		},
	})
	if rendered.Context != nil && rendered.Context.Phase != nil {
		t.Fatalf("worktree_setup_failed does not declare the phase block; got %#v", rendered.Context)
	}
}

// TestRenderRecordWorktreeSetupSummaryNamesRepositories pins the
// worktree-setup summary reading repository names from its context block.
func TestRenderRecordWorktreeSetupSummaryNamesRepositories(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code: WorktreeSetupFailed,
		Context: &RecordContext{
			Repositories: []CodeRepository{{Name: "repo-a", Branch: "feature/x"}},
		},
		Diagnostics: "git worktree add failed: no commits yet",
	})
	if !strings.Contains(rendered.Summary, "repo-a") {
		t.Fatalf("summary does not name the repository: %q", rendered.Summary)
	}
	if rendered.Remediation == nil || len(rendered.Remediation.Actions) != 1 || rendered.Remediation.Actions[0] != "setup" {
		t.Fatalf("worktree_setup_failed must reference the setup action: %#v", rendered.Remediation)
	}
}
