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
	"strings"
	"testing"
)

// integrationAttentionCodeList is the closed set of integration attention
// codes, pinned by the class, block, and action assertions below.
var integrationAttentionCodeList = []Code{
	IntegrationMergeConflict,
	IntegrationParentDirty,
	IntegrationParentRefDrift,
	IntegrationRefRace,
	IntegrationParentBranchMismatch,
	IntegrationRepositoryMissing,
	IntegrationWorktreeSyncFailed,
	IntegrationRolledBack,
	IntegrationCandidateFailed,
	RebaseGateTargetMissing,
	RebaseGateNotAncestor,
	RebaseGateMergeInProgress,
	RebaseGateConflictMarkers,
	RebaseGatePassthroughModified,
}

// TestIntegrationAttentionCodesAreNeedsAction pins the authored contract for
// every integration attention code: needs_action class, exactly the
// repositories block, and the retry action reference.
func TestIntegrationAttentionCodesAreNeedsAction(t *testing.T) {
	if len(integrationAttentionCodeList) != len(integrationAttentionCodes) {
		t.Fatalf("test list has %d codes; catalog set has %d", len(integrationAttentionCodeList), len(integrationAttentionCodes))
	}
	for _, code := range integrationAttentionCodeList {
		if !IsIntegrationAttention(code) {
			t.Errorf("%s: not recognized as an integration attention code", code)
		}
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassNeedsAction {
			t.Errorf("%s: class = %q; want needs_action", code, entry.Class)
		}
		if entry.Remediation == "" {
			t.Errorf("%s: needs a manual-step remediation hint", code)
		}
		if len(entry.Blocks) != 1 || entry.Blocks[0] != BlockRepositories {
			t.Errorf("%s: blocks = %#v; want exactly the repositories block", code, entry.Blocks)
		}
		if len(entry.Actions) != 1 || entry.Actions[0] != "retry" {
			t.Errorf("%s: actions = %#v; want [retry]", code, entry.Actions)
		}
	}
}

// TestIntegrationAttentionSummaryTemplates pins the repositories-block
// summaries: named repository plus conflict-file count, static fallback
// without context, and no raw diagnostics leaking into the summary.
func TestIntegrationAttentionSummaryTemplates(t *testing.T) {
	rendered := New(
		IntegrationMergeConflict,
		WithRepositories(CodeRepository{
			Name:          "repo-a",
			Branch:        "main",
			ConflictFiles: []string{"internal/api.go", "internal/api_test.go"},
		}),
		WithParams(IntegrationRepoParams{Repositories: []CodeRepository{{
			Name:          "repo-a",
			ConflictFiles: []string{"internal/api.go", "internal/api_test.go"},
		}}}),
		WithDiagnostics("repo-a: merge candidate conflict: [internal/api.go, internal/api_test.go]"),
	)
	if !strings.Contains(rendered.Summary, "repo-a") {
		t.Fatalf("summary does not name the repository: %q", rendered.Summary)
	}
	if !strings.Contains(rendered.Summary, "2 files") {
		t.Fatalf("summary does not name the conflict-file count: %q", rendered.Summary)
	}
	if strings.Contains(rendered.Summary, "merge candidate conflict:") {
		t.Fatalf("summary leaks raw diagnostics: %q", rendered.Summary)
	}
	if rendered.Diagnostics == "" {
		t.Fatal("diagnostics not carried on the rendered error")
	}

	static := New(IntegrationMergeConflict)
	entry, ok := Lookup(IntegrationMergeConflict)
	if !ok {
		t.Fatal("integration_merge_conflict missing from catalog")
	}
	if static.Summary != entry.Summary {
		t.Fatalf("no-context summary = %q; want static %q", static.Summary, entry.Summary)
	}

	dirty := New(
		IntegrationParentDirty,
		WithParams(IntegrationRepoParams{Repositories: []CodeRepository{{
			Name:       "repo-b",
			DirtyFiles: []string{"notes.txt"},
		}}}),
	)
	if !strings.Contains(dirty.Summary, "repo-b") || !strings.Contains(dirty.Summary, "1 uncommitted change") {
		t.Fatalf("parent-dirty summary does not name repo and change count: %q", dirty.Summary)
	}

	drift := New(
		IntegrationParentRefDrift,
		WithParams(IntegrationRepoParams{Repositories: []CodeRepository{{
			Name:            "repo-c",
			ParentAnchorSHA: "3f2c1ab88def777",
			ObservedSHA:     "9b1e4455aa00321",
		}}}),
	)
	if !strings.Contains(drift.Summary, "repo-c") ||
		!strings.Contains(drift.Summary, "3f2c1ab") ||
		!strings.Contains(drift.Summary, "9b1e445") {
		t.Fatalf("drift summary does not name repo and moved tips: %q", drift.Summary)
	}

	race := New(
		IntegrationRefRace,
		WithParams(IntegrationRepoParams{Repositories: []CodeRepository{{
			Name:           "repo-d",
			ExpectedRefSHA: "1111111",
			ObservedSHA:    "2222222",
		}}}),
	)
	if !strings.Contains(race.Summary, "repo-d") || !strings.Contains(race.Summary, "moved from 1111111 to 2222222") {
		t.Fatalf("ref-race summary does not name repo and moved tips: %q", race.Summary)
	}

	multi := New(
		IntegrationRepositoryMissing,
		WithParams(IntegrationRepoParams{Repositories: []CodeRepository{
			{Name: "repo-e"},
			{Name: "repo-f"},
		}}),
	)
	if !strings.Contains(multi.Summary, "repo-e") || !strings.Contains(multi.Summary, "repo-f") {
		t.Fatalf("multi-repository summary does not name both repositories: %q", multi.Summary)
	}
}

// TestIntegrationAttentionDropsUndeclaredBlocks pins that integration codes
// declare only the repositories block: phase and command context is dropped
// at render time.
func TestIntegrationAttentionDropsUndeclaredBlocks(t *testing.T) {
	rendered := New(
		IntegrationMergeConflict,
		WithRepositories(CodeRepository{Name: "repo-a", ConflictFiles: []string{"a.go"}}),
		WithPhase(CodePhase{Name: "implement"}),
		WithCommand(CodeCommand{ExitCode: 1}),
	)
	if rendered.Context == nil {
		t.Fatal("repositories block not carried")
	}
	if len(rendered.Context.Repositories) != 1 {
		t.Fatalf("repositories block not carried: %#v", rendered.Context.Repositories)
	}
	if rendered.Context.Phase != nil {
		t.Fatalf("integration codes do not declare the phase block; got %#v", rendered.Context.Phase)
	}
	if rendered.Context.Command != nil {
		t.Fatalf("integration codes do not declare the command block; got %#v", rendered.Context.Command)
	}
}

// TestRenderRecordIntegrationAttention pins the stored-record projection: a
// journal attention record renders with the repositories-block summary, the
// needs_action class, the retry action reference, and bounded-off raw
// diagnostics.
func TestRenderRecordIntegrationAttention(t *testing.T) {
	rendered := RenderRecord(FailureRecord{
		Code: IntegrationMergeConflict,
		Context: &RecordContext{
			Repositories: []CodeRepository{{
				Name:          "repo-a",
				Branch:        "main",
				ConflictFiles: []string{"internal/api.go"},
			}},
		},
		Diagnostics: "repo-a: merge candidate conflict: [internal/api.go]",
	})
	if rendered.Class != ClassNeedsAction {
		t.Fatalf("class = %q; want needs_action", rendered.Class)
	}
	if !strings.Contains(rendered.Summary, "repo-a") || !strings.Contains(rendered.Summary, "1 file") {
		t.Fatalf("summary does not name repository and conflict count: %q", rendered.Summary)
	}
	if rendered.Remediation == nil || len(rendered.Remediation.Actions) != 1 || rendered.Remediation.Actions[0] != "retry" {
		t.Fatalf("record render must reference the retry action: %#v", rendered.Remediation)
	}
	if rendered.Diagnostics != "repo-a: merge candidate conflict: [internal/api.go]" {
		t.Fatalf("diagnostics not preserved: %q", rendered.Diagnostics)
	}
	if rendered.Context == nil || len(rendered.Context.Repositories) != 1 ||
		len(rendered.Context.Repositories[0].ConflictFiles) != 1 {
		t.Fatalf("repositories block not carried: %#v", rendered.Context)
	}

	static := RenderRecord(FailureRecord{Code: IntegrationParentDirty, Diagnostics: "raw detail"})
	if strings.Contains(static.Summary, "raw detail") {
		t.Fatalf("no-context summary leaks diagnostics: %q", static.Summary)
	}
	if !IsIntegrationAttention(IntegrationParentDirty) || IsIntegrationAttention(IterationBudgetExhausted) {
		t.Fatal("IsIntegrationAttention misclassifies codes")
	}
}
