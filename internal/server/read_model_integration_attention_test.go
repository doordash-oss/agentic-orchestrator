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

package server

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// attentionDiagnosticsBudget is the safe-display bound wireIntegrationAttention
// applies to raw attention diagnostics, plus the "..." truncation marker.
const attentionDiagnosticsBudget = 243

// removedEntryWireKeys are the per-entry journal properties deleted with the
// free-form attention era; they must never reappear on the wire.
var removedEntryWireKeys = []string{"conflict_files", "dirty", "diagnostics", "gate_code"}

// removedAttentionItemCodes are the synthesized per-repository attention item
// codes the read model no longer emits; attention is one canonical object or
// nothing.
var removedAttentionItemCodes = []string{"dirty_parent", "integration_conflict", "integration_attention"}

// mergeConflictRecord is the stored canonical record for a child parked on a
// merge conflict: one repository with two conflict files and oversized raw
// diagnostics, so projections pin both the catalog rendering and the
// diagnostics bound.
func mergeConflictRecord() *errcat.FailureRecord {
	return &errcat.FailureRecord{
		Code: errcat.IntegrationMergeConflict,
		Context: &errcat.RecordContext{
			Repositories: []errcat.CodeRepository{{
				Name:            repoNameSelf,
				Branch:          "main",
				ConflictFiles:   []string{"internal/api.go", "internal/server/handler.go"},
				ParentAnchorSHA: "3f2c1d0b7e9a",
				ChildHeadSHA:    "9b1e7a2c4d6f",
			}},
		},
		Diagnostics: strings.Repeat("conflict hunk ", 40),
	}
}

// seedIntegrationChild persists an active refactor child whose journal is the
// given transaction and returns it.
func seedIntegrationChild(t *testing.T, store *feature.Store, parentID string, journal *feature.TransactionJournal) *feature.Feature {
	t.Helper()
	child := seedReadChildFeature(t, store, parentID, feature.StatusImplementing, feature.SetupStatusDone, "")
	child.Parent.Transaction = journal
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(child) error = %v", err)
	}
	return child
}

// canonicalAttentionFrom decodes the attention object on a projection that
// must carry one.
func canonicalAttentionFrom(t *testing.T, projection map[string]any) map[string]any {
	t.Helper()
	raw, ok := projection["attention"]
	if !ok {
		t.Fatalf("projection missing attention: %#v", projection)
	}
	attention, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("attention = %#v, want canonical error object", raw)
	}
	return attention
}

// assertCanonicalMergeConflict pins the catalog-rendered canonical object for
// the merge-conflict record: needs_action class, authored title, a summary
// naming the repository and the conflict-file count, the repositories block
// with the conflict files, a retry-referencing remediation, and bounded
// diagnostics.
func assertCanonicalMergeConflict(t *testing.T, attention map[string]any) {
	t.Helper()
	if attention["code"] != string(errcat.IntegrationMergeConflict) {
		t.Fatalf("attention code = %v, want %q", attention["code"], errcat.IntegrationMergeConflict)
	}
	if attention["class"] != string(errcat.ClassNeedsAction) {
		t.Fatalf("attention class = %v, want %q", attention["class"], errcat.ClassNeedsAction)
	}
	if attention["title"] != "Integration merge conflict" {
		t.Fatalf("attention title = %v, want the catalog title %q", attention["title"], "Integration merge conflict")
	}
	summary, _ := attention["summary"].(string)
	if !strings.Contains(summary, repoNameSelf) || !strings.Contains(summary, "2 files") {
		t.Fatalf("attention summary = %q, want %q and the conflict-file count named", summary, repoNameSelf)
	}
	context, ok := attention["context"].(map[string]any)
	if !ok {
		t.Fatalf("attention context = %#v, want repositories block", attention["context"])
	}
	repos, ok := context["repositories"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("attention context.repositories = %#v, want one repository", context["repositories"])
	}
	repo := repos[0].(map[string]any)
	if repo["name"] != repoNameSelf || repo["branch"] != "main" {
		t.Fatalf("attention repository = %#v, want %q on main", repo, repoNameSelf)
	}
	if repo["parent_anchor_sha"] != "3f2c1d0b7e9a" || repo["child_head_sha"] != "9b1e7a2c4d6f" {
		t.Fatalf("attention repository SHAs = %#v, want the recorded anchor and child head", repo)
	}
	files, _ := repo["conflict_files"].([]any)
	if len(files) != 2 || files[0] != "internal/api.go" || files[1] != "internal/server/handler.go" {
		t.Fatalf("attention conflict_files = %#v, want the two recorded files", files)
	}
	remediation, ok := attention["remediation"].(map[string]any)
	if !ok {
		t.Fatalf("attention remediation = %#v, want remediation block", attention["remediation"])
	}
	if hint, _ := remediation["hint"].(string); !strings.Contains(strings.ToLower(hint), "retry") {
		t.Fatalf("attention remediation hint = %q, want a retry reference", hint)
	}
	sawRetryAction := false
	if actions, ok := remediation["actions"].([]any); ok {
		for _, raw := range actions {
			if raw == actionRetry {
				sawRetryAction = true
			}
		}
	}
	if !sawRetryAction {
		t.Fatalf("attention remediation actions = %#v, want %q", remediation["actions"], actionRetry)
	}
	diagnostics, _ := attention["diagnostics"].(string)
	if len(diagnostics) > attentionDiagnosticsBudget || !strings.HasSuffix(diagnostics, "...") {
		t.Fatalf("attention diagnostics length = %d, want bounded to %d with a truncation marker",
			len(diagnostics), attentionDiagnosticsBudget)
	}
}

// assertEntryCarriesOnlyProgressState pins the per-entry wire contract:
// progress state, SHAs, warnings, and the typed pending-sync flag only — none
// of the deleted per-entry attention duplicates.
func assertEntryCarriesOnlyProgressState(t *testing.T, entry map[string]any) {
	t.Helper()
	for _, key := range removedEntryWireKeys {
		if _, exists := entry[key]; exists {
			t.Fatalf("transaction entry carries removed key %q: %#v", key, entry)
		}
	}
}

// TestIntegrationAttentionProjectsCanonicalRecordOnBothSurfaces pins the
// single-owner read model: a child parked with a stored
// integration_merge_conflict record renders the same canonical error object
// on the child's transaction and on the parent's active-child summary on both
// the detail and list routes, and a clean child carries no attention on
// either surface.
func TestIntegrationAttentionProjectsCanonicalRecordOnBothSurfaces(t *testing.T) {
	t.Parallel()

	t.Run("parked merge conflict", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedIntegrationChild(t, store, parent.ID, &feature.TransactionJournal{
			Phase:     feature.TransactionPhaseAttention,
			Attention: mergeConflictRecord(),
			Entries: []feature.RepoTransactionEntry{{
				Repo:         repoNameSelf,
				ParentBranch: "main",
				PrepState:    feature.RepoPrepFailed,
			}},
		})
		handler := NewHandler(baseReadHandlerOptions(store))

		childDetail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID+"-child")[entityFeature].(map[string]any)
		transaction, ok := childDetail["transaction"].(map[string]any)
		if !ok {
			t.Fatalf("child detail transaction = %#v, want journal object", childDetail["transaction"])
		}
		if transaction["phase"] != string(feature.TransactionPhaseAttention) {
			t.Fatalf("transaction phase = %v, want %q", transaction["phase"], feature.TransactionPhaseAttention)
		}
		entries, _ := transaction["entries"].([]any)
		if len(entries) != 1 {
			t.Fatalf("transaction entries = %#v, want one entry", entries)
		}
		assertEntryCarriesOnlyProgressState(t, entries[0].(map[string]any))
		childAttention := canonicalAttentionFrom(t, transaction)
		assertCanonicalMergeConflict(t, childAttention)

		parentDetail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
		activeChild, ok := parentDetail["active_child"].(map[string]any)
		if !ok {
			t.Fatalf("parent detail missing active_child: %#v", parentDetail)
		}
		parentAttention := canonicalAttentionFrom(t, activeChild)
		if got, want := mustMarshalJSON(t, parentAttention), mustMarshalJSON(t, childAttention); got != want {
			t.Fatalf("parent active_child attention = %s, want the child transaction's canonical object %s", got, want)
		}

		list := getJSONMap(t, handler, apiPathFeatures)
		summaryChild := list["features"].([]any)[0].(map[string]any)["active_child"].(map[string]any)
		if got, want := mustMarshalJSON(t, canonicalAttentionFrom(t, summaryChild)), mustMarshalJSON(t, childAttention); got != want {
			t.Fatalf("list summary active_child attention = %s, want the child transaction's canonical object %s", got, want)
		}
	})

	t.Run("clean child carries no attention on either surface", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedIntegrationChild(t, store, parent.ID, &feature.TransactionJournal{
			Phase: feature.TransactionPhaseApplied,
			Entries: []feature.RepoTransactionEntry{{
				Repo:         repoNameSelf,
				ParentBranch: "main",
				ApplyState:   feature.RepoApplyApplied,
				MergeHEAD:    "cccc3333",
				PendingSync:  true,
			}},
		})
		handler := NewHandler(baseReadHandlerOptions(store))

		childDetail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID+"-child")[entityFeature].(map[string]any)
		transaction, ok := childDetail["transaction"].(map[string]any)
		if !ok {
			t.Fatalf("child detail transaction = %#v, want journal object", childDetail["transaction"])
		}
		if _, has := transaction["attention"]; has {
			t.Fatalf("clean child transaction attention = %#v, want absent", transaction["attention"])
		}
		entries, _ := transaction["entries"].([]any)
		if len(entries) != 1 {
			t.Fatalf("transaction entries = %#v, want one entry", entries)
		}
		entry := entries[0].(map[string]any)
		assertEntryCarriesOnlyProgressState(t, entry)
		if entry["pending_sync"] != true {
			t.Fatalf("entry pending_sync = %#v, want the typed flag projected", entry["pending_sync"])
		}

		parentDetail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
		activeChild := parentDetail["active_child"].(map[string]any)
		if _, has := activeChild["attention"]; has {
			t.Fatalf("clean child active_child attention = %#v, want absent", activeChild["attention"])
		}
	})
}

// TestRelationshipProjectionCarriesNoAttentionItemStrings pins the deletion
// of the synthesized per-repository attention items: the active-child
// projection carries either no attention at all (clean journal, or one with
// cleanup warnings only) or exactly one canonical error object, and the old
// item codes appear nowhere in it.
func TestRelationshipProjectionCarriesNoAttentionItemStrings(t *testing.T) {
	t.Parallel()

	assertNoItemCodes := func(t *testing.T, projection map[string]any) {
		t.Helper()
		encoded := mustMarshalJSON(t, projection)
		for _, forbidden := range removedAttentionItemCodes {
			if strings.Contains(encoded, forbidden) {
				t.Fatalf("relationship projection carries removed attention item code %q: %s", forbidden, encoded)
			}
		}
	}

	t.Run("cleanup warnings only", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedIntegrationChild(t, store, parent.ID, &feature.TransactionJournal{
			Phase: feature.TransactionPhaseMerged,
			Entries: []feature.RepoTransactionEntry{{
				Repo:           repoNameSelf,
				ParentBranch:   "main",
				MergeHEAD:      "cccc3333",
				CleanupWarning: "worktree busy",
			}},
		})
		handler := NewHandler(baseReadHandlerOptions(store))

		parentDetail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
		activeChild := parentDetail["active_child"].(map[string]any)
		if _, has := activeChild["attention"]; has {
			t.Fatalf("active_child attention = %#v, want absent for a warnings-only journal", activeChild["attention"])
		}
		warnings := activeChild["cleanup_warnings"].([]any)
		if len(warnings) != 1 || warnings[0].(map[string]any)["message"] != "worktree busy" {
			t.Fatalf("active_child cleanup_warnings = %#v, want the recorded warning", warnings)
		}
		assertNoItemCodes(t, activeChild)
	})

	t.Run("parked record renders one canonical object", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedIntegrationChild(t, store, parent.ID, &feature.TransactionJournal{
			Phase:     feature.TransactionPhaseAttention,
			Attention: mergeConflictRecord(),
		})
		handler := NewHandler(baseReadHandlerOptions(store))

		parentDetail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
		activeChild := parentDetail["active_child"].(map[string]any)
		assertCanonicalMergeConflict(t, canonicalAttentionFrom(t, activeChild))
		assertNoItemCodes(t, activeChild)
	})
}

// TestChildRetryActionTracksParkedIntegration pins the child action catalog:
// retry is enabled while the transaction journal is parked (attention phase
// or a stored record) and disabled with not_failed for a running clean pass.
func TestChildRetryActionTracksParkedIntegration(t *testing.T) {
	t.Parallel()
	publishable := true

	tests := []struct {
		name        string
		journal     *feature.TransactionJournal
		wantEnabled bool
	}{
		{
			name:        "attention phase without a record",
			journal:     &feature.TransactionJournal{Phase: feature.TransactionPhaseAttention},
			wantEnabled: true,
		},
		{
			name: "stored record on an applied journal",
			journal: &feature.TransactionJournal{
				Phase: feature.TransactionPhaseApplied,
				Attention: &errcat.FailureRecord{
					Code:    errcat.IntegrationWorktreeSyncFailed,
					Context: &errcat.RecordContext{Repositories: []errcat.CodeRepository{{Name: repoNameSelf, Branch: "main"}}},
				},
			},
			wantEnabled: true,
		},
		{
			name:        "running clean pass",
			journal:     &feature.TransactionJournal{Phase: feature.TransactionPhasePrepared},
			wantEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := actionCatalogTestFeature(feature.StatusImplementing, feature.Checkpoints{}, &publishable)
			f.Pipeline = feature.PipelineMedium
			f.Parent = &feature.ChildRelationship{
				ParentID:    "parent-1",
				Kind:        feature.ChildKindRefactor,
				Transaction: tt.journal,
			}

			retry := actionDTOByID(t, actionCatalogDTOs(f), actionRetry)
			if retry.Enabled != tt.wantEnabled {
				t.Fatalf("retry enabled = %v, want %v (reasons %+v)", retry.Enabled, tt.wantEnabled, retry.DisabledReasons)
			}
			if tt.wantEnabled {
				return
			}
			if len(retry.DisabledReasons) == 0 || retry.DisabledReasons[0].Code != "not_failed" {
				t.Fatalf("retry disabled reasons = %+v, want %q", retry.DisabledReasons, "not_failed")
			}
		})
	}
}

// TestParentActionsKeepIntegrationAttentionReasonWhileParked pins the
// parent-side signal: while the active child's journal carries an attention
// record, every parent action except cascade delete is disabled and carries
// the integration_attention disabled reason on top of the child guard.
func TestParentActionsKeepIntegrationAttentionReasonWhileParked(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	seedIntegrationChild(t, store, parent.ID, &feature.TransactionJournal{
		Phase:     feature.TransactionPhaseAttention,
		Attention: mergeConflictRecord(),
	})
	handler := NewHandler(baseReadHandlerOptions(store))

	detail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	actions := detail["actions"].([]any)
	if len(actions) == 0 {
		t.Fatal("parent detail actions missing")
	}
	for _, raw := range actions {
		action := raw.(map[string]any)
		if action["id"] == actionDelete {
			if action["enabled"] != true {
				t.Fatalf("parent delete enabled = %v, want cascade delete available while attention is parked", action["enabled"])
			}
			continue
		}
		if action["enabled"] == true {
			t.Fatalf("parent action %q enabled while the child needs integration attention", action["id"])
		}
		hasAttentionReason := false
		if reasons, ok := action["disabled_reasons"].([]any); ok {
			for _, reasonRaw := range reasons {
				if reasonRaw.(map[string]any)["code"] == "integration_attention" {
					hasAttentionReason = true
				}
			}
		}
		if !hasAttentionReason {
			t.Fatalf("parent action %q disabled reasons = %#v, want the integration_attention reason", action["id"], action["disabled_reasons"])
		}
	}
}
