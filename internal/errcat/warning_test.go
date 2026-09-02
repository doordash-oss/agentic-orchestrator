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

// warningCodeList is the closed set of warning codes, pinned by the class
// and action assertions below.
var warningCodeList = []Code{
	EffortCapabilityDrift,
	FeatureLoadFailed,
	ChildCleanupIncomplete,
	ReviewFeedbackTailIncomplete,
	RewindPullRequestCloseFailed,
	RewindBackupBranchFailed,
	RewindWorktreeResetFailed,
	RepositoryWorktreeUnavailable,
	RepositoryDiffFailed,
}

// orphanSessionCodeList is the closed set of orphan-session recovery codes.
var orphanSessionCodeList = []Code{
	OrphanSessionLive,
	OrphanSessionStale,
}

// TestWarningCodesAreWarningClassWithoutActions pins the authored contract
// for every warning code: warning class and no action references. A warning
// never blocks progress, never gates a lane, and never offers an action.
func TestWarningCodesAreWarningClassWithoutActions(t *testing.T) {
	if len(warningCodeList) != 9 {
		t.Fatalf("warning code list has %d entries; want 9", len(warningCodeList))
	}
	for _, code := range warningCodeList {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassWarning {
			t.Errorf("%s: class = %q; want warning", code, entry.Class)
		}
		if len(entry.Actions) != 0 {
			t.Errorf("%s: actions = %#v; want none", code, entry.Actions)
		}
		if entry.Remediation == "" {
			t.Errorf("%s: needs a remediation hint", code)
		}
	}
}

// TestOrphanSessionCodesAreNeedsActionResume pins the authored contract for
// both orphan-session codes: needs_action class, exactly the resume action
// reference, and exactly the phase and repositories blocks.
func TestOrphanSessionCodesAreNeedsActionResume(t *testing.T) {
	if len(orphanSessionCodeList) != 2 {
		t.Fatalf("orphan code list has %d entries; want 2", len(orphanSessionCodeList))
	}
	for _, code := range orphanSessionCodeList {
		if !IsOrphanSession(code) {
			t.Errorf("%s: not recognized as an orphan-session code", code)
		}
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassNeedsAction {
			t.Errorf("%s: class = %q; want needs_action", code, entry.Class)
		}
		if len(entry.Actions) != 1 || entry.Actions[0] != "resume" {
			t.Errorf("%s: actions = %#v; want [resume]", code, entry.Actions)
		}
		blocks := map[Block]bool{}
		for _, block := range entry.Blocks {
			blocks[block] = true
		}
		if len(blocks) != 2 || !blocks[BlockPhase] || !blocks[BlockRepositories] {
			t.Errorf("%s: blocks = %#v; want exactly phase and repositories", code, entry.Blocks)
		}
		if entry.Remediation == "" {
			t.Errorf("%s: needs a remediation hint", code)
		}
	}
}

// TestWarningSummaryTemplates pins the param-driven warning summaries: the
// effort-drift message names role, effort, and model; a stored relationship
// record names its repository; an orphan code names its phase, iteration,
// and repository.
func TestWarningSummaryTemplates(t *testing.T) {
	rendered := New(EffortCapabilityDrift, WithParams(EffortDriftParams{
		Role:   "Implementation",
		Effort: "high",
		Model:  "claude-opus-4",
	}))
	want := `Implementation effort "high" is not supported by model "claude-opus-4"; using Auto until the configuration is updated`
	if rendered.Summary != want {
		t.Fatalf("effort_capability_drift summary is %q; want %q", rendered.Summary, want)
	}

	rendered = New(FeatureLoadFailed, WithParams(FeatureLoadFailedParams{FeatureID: "feature-42"}))
	if !strings.Contains(rendered.Summary, `"feature-42"`) {
		t.Fatalf("feature_load_failed summary does not name the feature: %q", rendered.Summary)
	}

	rendered = RenderRecord(FailureRecord{
		Code: ChildCleanupIncomplete,
		Context: &RecordContext{
			Repositories: []CodeRepository{{Name: "web", Branch: "agentico/pass-3"}},
		},
		Diagnostics: "remove worktree: directory busy",
	})
	if !strings.Contains(rendered.Summary, `"web"`) || !strings.Contains(rendered.Summary, "agentico/pass-3") {
		t.Fatalf("child_cleanup_incomplete stored-record summary does not name repository and branch: %q", rendered.Summary)
	}
	if rendered.Class != ClassWarning {
		t.Fatalf("child_cleanup_incomplete renders as %q; want warning", rendered.Class)
	}

	fresh := New(ChildCleanupIncomplete, WithParams(WarningRepoParams{
		Repositories: []CodeRepository{{Name: "web", Branch: "agentico/pass-3"}},
	}))
	if fresh.Summary != rendered.Summary {
		t.Fatalf("stored and fresh child_cleanup_incomplete summaries differ: %q vs %q", rendered.Summary, fresh.Summary)
	}

	rendered = New(OrphanSessionLive, WithParams(OrphanSessionParams{
		Phase:        "implement",
		Iteration:    3,
		Repositories: []string{"web"},
	}))
	if !strings.Contains(rendered.Summary, "Implement") || !strings.Contains(rendered.Summary, "iteration 3") || !strings.Contains(rendered.Summary, `"web"`) {
		t.Fatalf("orphan_session_live summary does not name phase, iteration, and repository: %q", rendered.Summary)
	}

	rendered = New(OrphanSessionStale, WithParams(OrphanSessionParams{
		Phase:     "final_review",
		Iteration: 2,
	}))
	if !strings.Contains(rendered.Summary, "Final review") || !strings.Contains(rendered.Summary, "iteration 2") {
		t.Fatalf("orphan_session_stale summary does not name phase and iteration: %q", rendered.Summary)
	}
	if rendered.Remediation == nil || len(rendered.Remediation.Actions) != 1 || rendered.Remediation.Actions[0] != "resume" {
		t.Fatalf("orphan_session_stale remediation = %#v; want the resume action", rendered.Remediation)
	}
}
