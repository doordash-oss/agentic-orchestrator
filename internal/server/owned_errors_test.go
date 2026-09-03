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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// seedRunFailure persists the run failure record for a terminal phase
// failure on f and returns it.
func seedRunFailure(t *testing.T, store *feature.Store, f *feature.Feature, code errcat.Code) {
	t.Helper()
	f.Status = feature.StatusFailed
	run := f.Run()
	run.Failure = &errcat.FailureRecord{
		Code: code,
		Context: &errcat.RecordContext{
			Phase: &errcat.CodePhase{Name: "implement", Iteration: 3},
		},
		Diagnostics: "phase hit the configured iteration ceiling",
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(%s) error = %v", f.ID, err)
	}
}

// seedSetupFailure persists the durable setup-failure shape on f: the owning
// task carries the full record while the run carries the thin record whose
// context names the task.
func seedSetupFailure(t *testing.T, store *feature.Store, f *feature.Feature) string {
	t.Helper()
	key := "worktree:" + repoNameSelf
	f.Status = feature.StatusFailed
	run := f.Run()
	run.Setup = &feature.SetupState{
		Status: feature.SetupStatusFailed,
		Tasks: map[string]feature.SetupTask{key: {
			Key: key, Kind: feature.SetupTaskWorktree, Label: "Worktree: " + repoNameSelf,
			Repo: repoNameSelf, Status: feature.SetupStatusFailed,
			Error: &errcat.FailureRecord{
				Code: errcat.WorktreeSetupFailed,
				Context: &errcat.RecordContext{
					Repositories: []errcat.CodeRepository{{Name: repoNameSelf, Branch: "feature/rework"}},
				},
				Diagnostics: "worktree could not be created",
			},
		}},
		TaskOrder: []string{key},
	}
	run.Failure = &errcat.FailureRecord{
		Code: errcat.WorktreeSetupFailed,
		Context: &errcat.RecordContext{
			SetupTask: &errcat.CodeSetupTask{Key: key, Kind: "worktree", Label: "Worktree: " + repoNameSelf},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(%s) error = %v", f.ID, err)
	}
	return key
}

// ownedErrorsFrom decodes the errors array on a JSON projection, failing when
// the field is absent.
func ownedErrorsFrom(t *testing.T, projection map[string]any) []map[string]any {
	t.Helper()
	raw, ok := projection["errors"]
	if !ok {
		t.Fatalf("projection missing errors: %#v", projection)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("errors = %#v, want array", raw)
	}
	out := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		out = append(out, entry.(map[string]any))
	}
	return out
}

// assertOwnedEntry pins one projected entry's reference and error shape: the
// scope and snake_case keys of the owner reference, the error class, and the
// catalog title, with no diagnostics crossing the projection.
func assertOwnedEntry(t *testing.T, entry map[string]any, scope, code, featureID, title string, class errcat.Class) {
	t.Helper()
	ref, ok := entry["ref"].(map[string]any)
	if !ok {
		t.Fatalf("entry ref = %#v, want reference object", entry["ref"])
	}
	if ref["scope"] != scope {
		t.Fatalf("ref scope = %v, want %q", ref["scope"], scope)
	}
	if ref["code"] != code {
		t.Fatalf("ref code = %v, want %q", ref["code"], code)
	}
	if ref["feature_id"] != featureID {
		t.Fatalf("ref feature_id = %v, want %q", ref["feature_id"], featureID)
	}
	if _, ok := ref["snapshot_id"]; ok {
		t.Fatalf("ref carries snapshot_id = %#v; want scope-local keys only", ref["snapshot_id"])
	}
	body, ok := entry["error"].(map[string]any)
	if !ok {
		t.Fatalf("entry error = %#v, want canonical error object", entry["error"])
	}
	if body["class"] != string(class) {
		t.Fatalf("error class = %v, want %q", body["class"], class)
	}
	if body["title"] != title {
		t.Fatalf("error title = %v, want catalog title %q", body["title"], title)
	}
	if got, ok := body["diagnostics"]; ok && got != "" {
		t.Fatalf("error diagnostics = %#v, want the projection to carry none", got)
	}
}

// rawJSONBody fetches a path and returns the raw response body.
func rawJSONBody(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d; want 200", path, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s body: %v", path, err)
	}
	return string(data)
}

// TestOwnedErrorsProjectRunFailure pins the run-failure source: a stored
// iteration_budget_exhausted record projects exactly one run entry keyed by
// the feature, carrying the blocking class and the catalog title, with no
// diagnostics.
func TestOwnedErrorsProjectRunFailure(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	seedRunFailure(t, store, parent, errcat.IterationBudgetExhausted)
	handler := NewHandler(baseReadHandlerOptions(store))

	summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
	entries := ownedErrorsFrom(t, summary)
	if len(entries) != 1 {
		t.Fatalf("errors len = %d, want one run entry; entries = %#v", len(entries), entries)
	}
	assertOwnedEntry(t, entries[0], "run", string(errcat.IterationBudgetExhausted), parent.ID,
		"Iteration budget exhausted", errcat.ClassBlocking)
}

// TestOwnedErrorsProjectSetupFailureOnce pins the single-owner rule: a run
// record whose context names a setup task projects exactly one setup entry
// carrying the task key and the task's code — never a second run entry.
func TestOwnedErrorsProjectSetupFailureOnce(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	key := seedSetupFailure(t, store, parent)
	handler := NewHandler(baseReadHandlerOptions(store))

	summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
	entries := ownedErrorsFrom(t, summary)
	if len(entries) != 1 {
		t.Fatalf("errors len = %d, want exactly one setup entry; entries = %#v", len(entries), entries)
	}
	entry := entries[0]
	assertOwnedEntry(t, entry, "setup", string(errcat.WorktreeSetupFailed), parent.ID,
		"Worktree setup failed", errcat.ClassBlocking)
	ref := entry["ref"].(map[string]any)
	if ref["task_key"] != key {
		t.Fatalf("ref task_key = %v, want %q", ref["task_key"], key)
	}
}

// TestOwnedErrorsProjectRepositoryRecords pins the repository source: one
// entry per stored publish-failure record, each keyed by the repository name.
func TestOwnedErrorsProjectRepositoryRecords(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	parent.Repos = append(parent.Repos, feature.FeatureRepo{Name: "beta"})
	record := func(repo string) *errcat.FailureRecord {
		return &errcat.FailureRecord{
			Code: errcat.PublishRebaseConflict,
			Context: &errcat.RecordContext{
				Repositories: []errcat.CodeRepository{{Name: repo, Branch: "main", ConflictFiles: []string{"a.go"}}},
			},
			Diagnostics: "conflict hunk",
		}
	}
	parent.RepoStates[repoNameSelf].Error = record(repoNameSelf)
	parent.RepoStates["beta"] = &feature.RepoState{Touched: true, Error: record("beta")}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
	entries := ownedErrorsFrom(t, summary)
	if len(entries) != 2 {
		t.Fatalf("errors len = %d, want one entry per repository; entries = %#v", len(entries), entries)
	}
	for _, entry := range entries {
		ref := entry["ref"].(map[string]any)
		if ref["scope"] != "repository" {
			t.Fatalf("ref scope = %v, want repository", ref["scope"])
		}
		repo := ref["repository"].(string)
		if repo != repoNameSelf && repo != "beta" {
			t.Fatalf("ref repository = %q, want one of the two failing repositories", repo)
		}
		if ref["feature_id"] != parent.ID {
			t.Fatalf("ref feature_id = %v, want %q", ref["feature_id"], parent.ID)
		}
		if entry["error"].(map[string]any)["class"] != string(errcat.ClassNeedsAction) {
			t.Fatalf("repository entry class = %v, want needs_action", entry["error"].(map[string]any)["class"])
		}
	}
	if entries[0]["ref"].(map[string]any)["repository"] != repoNameSelf ||
		entries[1]["ref"].(map[string]any)["repository"] != "beta" {
		t.Fatalf("repository entries out of declared repo order: %#v", entries)
	}
}

// TestOwnedErrorsProjectChildRecords pins the child sources: a parent whose
// active child is parked projects one transaction entry keyed by the child id
// with the needs_action class, and a parent whose child run failed projects a
// run entry keyed by the child id.
func TestOwnedErrorsProjectChildRecords(t *testing.T) {
	t.Parallel()

	t.Run("parked child projects transaction entry", func(t *testing.T) {
		t.Parallel()

		store, parent := seedReadFeature(t)
		child := seedIntegrationChild(t, store, parent.ID, &feature.TransactionJournal{
			Phase:     feature.TransactionPhaseAttention,
			Attention: mergeConflictRecord(),
		})
		handler := NewHandler(baseReadHandlerOptions(store))

		summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
		entries := ownedErrorsFrom(t, summary)
		if len(entries) != 1 {
			t.Fatalf("errors len = %d, want one transaction entry; entries = %#v", len(entries), entries)
		}
		assertOwnedEntry(t, entries[0], "transaction", string(errcat.IntegrationMergeConflict), child.ID,
			"Integration merge conflict", errcat.ClassNeedsAction)
	})

	t.Run("failed child run projects child-keyed run entry", func(t *testing.T) {
		t.Parallel()

		store, parent := seedReadFeature(t)
		child := seedReadChildFeature(t, store, parent.ID, feature.StatusFailed, feature.SetupStatusDone, "")
		seedRunFailure(t, store, child, errcat.IterationBudgetExhausted)
		handler := NewHandler(baseReadHandlerOptions(store))

		summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
		entries := ownedErrorsFrom(t, summary)
		if len(entries) != 1 {
			t.Fatalf("errors len = %d, want one run entry; entries = %#v", len(entries), entries)
		}
		assertOwnedEntry(t, entries[0], "run", string(errcat.IterationBudgetExhausted), child.ID,
			"Iteration budget exhausted", errcat.ClassBlocking)
	})

	t.Run("failed child setup projects child-keyed setup entry", func(t *testing.T) {
		t.Parallel()

		store, parent := seedReadFeature(t)
		child := seedReadChildFeature(t, store, parent.ID, feature.StatusFailed, feature.SetupStatusFailed, "worktree blew up")
		handler := NewHandler(baseReadHandlerOptions(store))

		summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
		entries := ownedErrorsFrom(t, summary)
		if len(entries) != 1 {
			t.Fatalf("errors len = %d, want one setup entry; entries = %#v", len(entries), entries)
		}
		entry := entries[0]
		assertOwnedEntry(t, entry, "setup", string(errcat.WorktreeSetupFailed), child.ID,
			"Worktree setup failed", errcat.ClassBlocking)
		if ref := entry["ref"].(map[string]any); ref["task_key"] != "worktree:"+repoNameSelf {
			t.Fatalf("ref task_key = %v, want the owning task key", ref["task_key"])
		}
	})
}

// TestOwnedErrorsOmittedForWarningsOnly pins the warning exclusion: a feature
// whose only stored records are warning-class projects no errors field at
// all.
func TestOwnedErrorsOmittedForWarningsOnly(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	parent.RepoStates[repoNameSelf].Error = &errcat.FailureRecord{Code: errcat.RebaseAlreadyUpToDate}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
	if raw, ok := summary["errors"]; ok {
		t.Fatalf("errors = %#v, want omitted for a warnings-only feature", raw)
	}
	detail := getJSONMap(t, handler, apiPathFeatures+"/"+parent.ID)[entityFeature].(map[string]any)
	if raw, ok := detail["errors"]; ok {
		t.Fatalf("detail errors = %#v, want omitted for a warnings-only feature", raw)
	}
}

// TestOwnedErrorsFailedStatusAlwaysBlocking pins the invariant that every
// feature whose status is Failed projects at least one blocking entry, across
// the durable failure shapes that can hold the Failed status.
func TestOwnedErrorsFailedStatusAlwaysBlocking(t *testing.T) {
	t.Parallel()

	t.Run("run failure", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedRunFailure(t, store, parent, errcat.IterationBudgetExhausted)
		assertFirstEntryBlocking(t, ownedErrorsDTO(parent, nil))
	})

	t.Run("setup failure", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedSetupFailure(t, store, parent)
		assertFirstEntryBlocking(t, ownedErrorsDTO(parent, nil))
	})

	t.Run("failed child", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		child := seedReadChildFeature(t, store, parent.ID, feature.StatusFailed, feature.SetupStatusDone, "")
		seedRunFailure(t, store, child, errcat.IterationBudgetExhausted)
		assertFirstEntryBlocking(t, ownedErrorsDTO(parent, child))
	})
}

func assertFirstEntryBlocking(t *testing.T, entries []OwnedError) {
	t.Helper()
	if len(entries) == 0 {
		t.Fatal("errors empty, want at least one blocking entry for a Failed feature")
	}
	if entries[0].Error.Class != ErrorClassBlocking {
		t.Fatalf("errors[0] class = %q, want blocking first", entries[0].Error.Class)
	}
}

// TestOwnedErrorsClassAndOrderInvariants pins the projection invariants: every
// entry is blocking or needs_action, and blocking entries precede
// needs_action entries, stable by scope and key within each class.
func TestOwnedErrorsClassAndOrderInvariants(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	seedRunFailure(t, store, parent, errcat.IterationBudgetExhausted)
	parent.RepoStates[repoNameSelf].Error = &errcat.FailureRecord{
		Code:    errcat.PublishRebaseConflict,
		Context: &errcat.RecordContext{Repositories: []errcat.CodeRepository{{Name: repoNameSelf}}},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	child := seedIntegrationChild(t, store, parent.ID, &feature.TransactionJournal{
		Phase:     feature.TransactionPhaseAttention,
		Attention: mergeConflictRecord(),
	})

	entries := ownedErrorsDTO(parent, child)
	if len(entries) != 3 {
		t.Fatalf("errors len = %d, want the blocking run failure plus the needs_action repository and transaction entries; entries = %#v", len(entries), entries)
	}
	sawNeedsAction := false
	for _, entry := range entries {
		switch entry.Error.Class {
		case ErrorClassBlocking:
			if sawNeedsAction {
				t.Fatalf("blocking entry after a needs_action entry: %#v", entries)
			}
		case ErrorClassNeedsAction:
			sawNeedsAction = true
		default:
			t.Fatalf("entry class = %q, want blocking or needs_action only", entry.Error.Class)
		}
	}
	if entries[0].Ref.Scope != errorScopeRun ||
		entries[1].Ref.Scope != errorScopeRepository ||
		entries[2].Ref.Scope != errorScopeTransaction {
		t.Fatalf("entry scopes = [%q, %q, %q], want run, then repository, then transaction",
			entries[0].Ref.Scope, entries[1].Ref.Scope, entries[2].Ref.Scope)
	}
}

// TestOwnedErrorsWireShapeOnListAndDetail pins the HTTP wire: the list
// response embeds errors with snake_case reference keys, and the detail
// response (which extends the summary) carries the same list.
func TestOwnedErrorsWireShapeOnListAndDetail(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	seedRunFailure(t, store, parent, errcat.IterationBudgetExhausted)
	handler := NewHandler(baseReadHandlerOptions(store))

	raw := rawJSONBody(t, handler, apiPathFeatures)
	if !strings.Contains(raw, `"feature_id"`) || strings.Contains(raw, `"featureId"`) {
		t.Fatalf("list wire does not carry snake_case reference keys: %s", raw)
	}
	listSummary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)
	listEntries := ownedErrorsFrom(t, listSummary)

	detail := getJSONMap(t, handler, apiPathFeatures+"/"+parent.ID)[entityFeature].(map[string]any)
	detailEntries := ownedErrorsFrom(t, detail)
	if len(detailEntries) != len(listEntries) {
		t.Fatalf("detail errors len = %d, want the same list as the summary (%d)", len(detailEntries), len(listEntries))
	}
	assertOwnedEntry(t, detailEntries[0], "run", string(errcat.IterationBudgetExhausted), parent.ID,
		"Iteration budget exhausted", errcat.ClassBlocking)
}
