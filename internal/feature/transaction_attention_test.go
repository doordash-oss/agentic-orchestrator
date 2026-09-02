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

package feature

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"gopkg.in/yaml.v3"
)

func attentionRecordFixture() *errcat.FailureRecord {
	return &errcat.FailureRecord{
		Code: errcat.IntegrationMergeConflict,
		Context: &errcat.RecordContext{
			Repositories: []errcat.CodeRepository{{
				Name:            "repo-a",
				Branch:          "main",
				ConflictFiles:   []string{"internal/api.go"},
				ParentAnchorSHA: "3f2c1ab",
				ChildHeadSHA:    "9b1e445",
			}},
		},
		Diagnostics: "repo-a: merge conflict: [internal/api.go]",
	}
}

// TestTransactionJournalRoundTripsAttentionRecord pins the durable record
// shape: a journal with one stored attention record survives a YAML marshal
// / unmarshal cycle with an equal value.
func TestTransactionJournalRoundTripsAttentionRecord(t *testing.T) {
	journal := &TransactionJournal{
		Phase: TransactionPhaseAttention,
		Entries: []RepoTransactionEntry{{
			Repo:         "repo-a",
			ParentBranch: "main",
			PrepState:    RepoPrepFailed,
		}},
		Attention: attentionRecordFixture(),
	}
	raw, err := yaml.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "code: integration_merge_conflict") {
		t.Fatalf("YAML does not carry the record code:\n%s", raw)
	}
	var got TransactionJournal
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Phase != TransactionPhaseAttention || len(got.Entries) != 1 {
		t.Fatalf("round-trip lost journal state: %+v", got)
	}
	if got.Attention == nil || got.Attention.Code != errcat.IntegrationMergeConflict ||
		got.Attention.Diagnostics != "repo-a: merge conflict: [internal/api.go]" ||
		got.Attention.Context == nil || len(got.Attention.Context.Repositories) != 1 ||
		len(got.Attention.Context.Repositories[0].ConflictFiles) != 1 {
		t.Fatalf("round-trip lost the attention record: %+v", got.Attention)
	}
}

// TestTransactionJournalIgnoresLegacyAttentionAndEntryKeys pins the load
// contract: a hand-written journal YAML carrying the pre-catalog free-form
// attention string and deleted entry keys (diagnostics, gate_code,
// conflict_files, dirty) loads with no record and no entry duplicates.
func TestTransactionJournalIgnoresLegacyAttentionAndEntryKeys(t *testing.T) {
	legacy := `phase: attention
attention: parent worktrees have uncommitted changes
entries:
  - repo: repo-a
    parent_branch: main
    prep_state: failed
    diagnostics: "repo-a: merge conflict"
    gate_code: rebase_gate_not_ancestor
    conflict_files: [internal/api.go]
    dirty:
      - repo: repo-a
        path: /tmp/repo-a
        untracked: [stray.txt]
`
	var got TransactionJournal
	if err := yaml.Unmarshal([]byte(legacy), &got); err != nil {
		t.Fatalf("legacy journal YAML must load: %v", err)
	}
	if got.Attention != nil {
		t.Fatalf("legacy free-form attention string must load as no record: %+v", got.Attention)
	}
	if got.Phase != TransactionPhaseAttention || len(got.Entries) != 1 {
		t.Fatalf("legacy journal lost its phase or entries: %+v", got)
	}
	entry := got.Entries[0]
	if entry.Repo != "repo-a" || entry.ParentBranch != "main" || entry.PrepState != RepoPrepFailed {
		t.Fatalf("legacy entry lost its progress state: %+v", entry)
	}
}

// TestSavedJournalWritesNoDeletedEntryKeys pins the write contract: a saved
// feature whose journal carries an attention record writes none of the
// deleted entry keys — conflict and dirty data lives only inside the
// record's repositories block.
func TestSavedJournalWritesNoDeletedEntryKeys(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "features"))
	f := &Feature{
		ID: "t1", Slug: "t1", Status: StatusReviewPassed, Created: time.Now().UTC().Truncate(time.Second),
		ActiveRun: 1, RunCount: 1, Pipeline: PipelineMedium,
		Repos: []FeatureRepo{{Name: "repo-a", Path: "/tmp/a"}},
		Parent: &ChildRelationship{
			ParentID: "p1",
			Kind:     ChildKindRefactor,
			Transaction: &TransactionJournal{
				Phase: TransactionPhaseAttention,
				Entries: []RepoTransactionEntry{{
					Repo:         "repo-a",
					ParentBranch: "main",
					PrepState:    RepoPrepFailed,
				}},
				Attention: attentionRecordFixture(),
			},
		},
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.BaseDir, f.ID, "feature.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	parent, _ := doc["parent"].(map[string]any)
	tx, _ := parent["transaction"].(map[string]any)
	entries, _ := tx["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("saved journal entries = %v, want 1", entries)
	}
	entry, _ := entries[0].(map[string]any)
	for _, key := range []string{"diagnostics", "gate_code", "conflict_files", "dirty"} {
		if _, ok := entry[key]; ok {
			t.Fatalf("saved journal entry carries deleted key %q:\n%s", key, raw)
		}
	}
	attention, _ := tx["attention"].(map[string]any)
	if attention == nil || attention["code"] != string(errcat.IntegrationMergeConflict) {
		t.Fatalf("saved journal attention record = %v, want the canonical record", attention)
	}
}

// TestIntegrationAttentionAccessors pins the feature-level attention
// accessors: attention is reported for an attention-phase journal, an
// applied journal carrying a record (a closure-time sync failure), and a
// journal whose entries carry apply attention; it is reported for neither a
// clean prepared journal nor a merged journal.
func TestIntegrationAttentionAccessors(t *testing.T) {
	newChild := func(tx *TransactionJournal) *Feature {
		return &Feature{
			ID:     "c",
			Status: StatusReviewPassed,
			Parent: &ChildRelationship{ParentID: "p", Kind: ChildKindRefactor, Transaction: tx},
		}
	}
	attentionPhase := newChild(&TransactionJournal{Phase: TransactionPhaseAttention})
	appliedWithRecord := newChild(&TransactionJournal{
		Phase:     TransactionPhaseApplied,
		Attention: &errcat.FailureRecord{Code: errcat.IntegrationWorktreeSyncFailed},
	})
	applyAttention := newChild(&TransactionJournal{
		Phase:   TransactionPhaseRollingBack,
		Entries: []RepoTransactionEntry{{Repo: "repo-a", ApplyState: RepoApplyAttention}},
	})
	cleanPrepared := newChild(&TransactionJournal{
		Phase:   TransactionPhasePrepared,
		Entries: []RepoTransactionEntry{{Repo: "repo-a", PrepState: RepoPrepPrepared, CandidateSHA: "abc"}},
	})
	merged := newChild(&TransactionJournal{Phase: TransactionPhaseMerged})
	noJournal := newChild(nil)

	for _, tc := range []struct {
		name string
		f    *Feature
		want bool
	}{
		{"attention phase", attentionPhase, true},
		{"applied with record", appliedWithRecord, true},
		{"apply attention entry", applyAttention, true},
		{"clean prepared", cleanPrepared, false},
		{"merged", merged, false},
		{"no journal", noJournal, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.HasIntegrationAttention(); got != tc.want {
				t.Fatalf("HasIntegrationAttention() = %v, want %v", got, tc.want)
			}
		})
	}

	if rec := attentionPhase.IntegrationAttentionRecord(); rec != nil {
		t.Fatalf("attention-phase journal without a record reports none: %+v", rec)
	}
	withRecord := newChild(&TransactionJournal{Phase: TransactionPhaseAttention, Attention: attentionRecordFixture()})
	if rec := withRecord.IntegrationAttentionRecord(); rec == nil || rec.Code != errcat.IntegrationMergeConflict {
		t.Fatalf("IntegrationAttentionRecord() = %+v, want the stored record", rec)
	}
	if code := withRecord.IntegrationAttentionCode(); code != errcat.IntegrationMergeConflict {
		t.Fatalf("IntegrationAttentionCode() = %q, want integration_merge_conflict", code)
	}
	if code := cleanPrepared.IntegrationAttentionCode(); code != "" {
		t.Fatalf("IntegrationAttentionCode() = %q, want empty without a record", code)
	}
}
