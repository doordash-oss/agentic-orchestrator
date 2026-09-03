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

package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// TestChatStartWithoutContextReachesTargetUnchanged pins the compatibility
// contract: a chat start with no `context` decodes into the typed request
// and reaches the mutation target exactly as before.
func TestChatStartWithoutContextReachesTargetUnchanged(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	_, handler, _ := newUploadTestAPI(t, target, false)

	w := postTrustedJSON(handler, "/api/v1/prompts/chat/start", map[string]any{
		"message": "What is running?",
		"images":  []string{"/tmp/shot.png"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("chat start status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if target.chatReq == nil {
		t.Fatal("StartChat was not called")
	}
	if target.chatReq.Message != "What is running?" {
		t.Fatalf("message = %q; want the sent message", target.chatReq.Message)
	}
	if !chatContextAbsent(target.chatReq.Context) {
		t.Fatalf("context = %#v; want none", target.chatReq.Context)
	}
}

// TestChatStartAcceptsWellFormedRunReference pins that a validated,
// resolvable reference crosses to the mutation target with its scope,
// code, and keys, alongside the resolved bundle.
func TestChatStartAcceptsWellFormedRunReference(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(t.TempDir())
	chatContextSeedRunFailure(t, store)
	target := &uploadMutationRecorder{}
	handler := newChatContextTestAPI(t, target, store).handler

	w := postTrustedJSON(handler, "/api/v1/prompts/chat/start", map[string]any{
		"message": "Explain this error",
		"context": map[string]any{
			"scope":      "run",
			"code":       "iteration_budget_exhausted",
			"feature_id": "feat-run-failed",
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("chat start status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if target.chatReq == nil {
		t.Fatal("StartChat was not called")
	}
	got := target.chatReq.Context
	if got.Scope != errorScopeRun || got.Code != "iteration_budget_exhausted" || got.FeatureID != "feat-run-failed" {
		t.Fatalf("context = %#v; want the run reference unchanged", got)
	}
	if !strings.Contains(target.chatHiddenContext, "error[iteration_budget_exhausted]") {
		t.Fatalf("hidden context = %q; want the resolved run-failure bundle", target.chatHiddenContext)
	}
}

// TestChatStartRejectsMalformedContextReferences pins the 400
// chat_context_invalid family: unknown scope, a key missing for the scope,
// and a key foreign to the scope are all rejected before the mutation
// target runs, with the offending field named in diagnostics.
func TestChatStartRejectsMalformedContextReferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		context   map[string]any
		wantField string
	}{
		{
			name: "unknown scope",
			context: map[string]any{
				"scope":      "workspace",
				"code":       "iteration_budget_exhausted",
				"feature_id": "abcd1234ef567890",
			},
			wantField: "scope",
		},
		{
			name: "setup scope without task key",
			context: map[string]any{
				"scope":      "setup",
				"code":       "worktree_setup_failed",
				"feature_id": "abcd1234ef567890",
			},
			wantField: "task_key",
		},
		{
			name: "recovery scope without snapshot id and key",
			context: map[string]any{
				"scope": "recovery",
				"code":  "orphan_session_live",
			},
			wantField: "snapshot_id",
		},
		{
			name: "run scope carrying a repository",
			context: map[string]any{
				"scope":      "run",
				"code":       "iteration_budget_exhausted",
				"feature_id": "abcd1234ef567890",
				"repository": "web",
			},
			wantField: "repository",
		},
		{
			name: "missing code",
			context: map[string]any{
				"scope":      "run",
				"feature_id": "abcd1234ef567890",
			},
			wantField: "code",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &uploadMutationRecorder{}
			_, handler, _ := newUploadTestAPI(t, target, false)

			w := postTrustedJSON(handler, "/api/v1/prompts/chat/start", map[string]any{
				"message": "Explain this error",
				"context": tc.context,
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("chat start status = %d body=%s; want 400", w.Code, w.Body.String())
			}
			var body ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != string(errcat.ChatContextInvalid) {
				t.Fatalf("code = %q; want chat_context_invalid", body.Error.Code)
			}
			if body.Error.Class != ErrorClass(errcat.ClassBlocking) {
				t.Fatalf("class = %q; want blocking", body.Error.Class)
			}
			if !strings.Contains(body.Error.Diagnostics, tc.wantField) {
				t.Fatalf("diagnostics = %q; want it to name field %q", body.Error.Diagnostics, tc.wantField)
			}
			if target.chatReq != nil {
				t.Fatal("StartChat was called for a malformed reference")
			}
		})
	}
}

// chatContextLongDiagnostics builds raw diagnostics well past the 240
// character safe-display bound so tests can pin that the hidden bundle
// carries the full stored text, not the bounded projection.
func chatContextLongDiagnostics(marker string) string {
	return "raw failure detail for " + marker + ": " + strings.Repeat("context line; ", 60) + "end " + marker
}

// chatContextTestAPI is one handler instance serving chat-start requests
// against a temp state dir, a feature store, and a recording target.
type chatContextTestAPI struct {
	api      *apiHandler
	handler  http.Handler
	stateDir string
}

// newChatContextTestAPI builds a trusted-mutation handler backed by a temp
// state dir, the feature store, and the recording mutation target.
func newChatContextTestAPI(t *testing.T, target MutationTarget, store *feature.Store) chatContextTestAPI {
	t.Helper()
	stateDir := t.TempDir()
	opts := HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: testRuntimeConfigPath},
		Config:                config.NewDefault(),
		Mutations:             target,
		DisableHostValidation: true,
	}
	if store != nil {
		opts.FeatureStore = store
		opts.Features = store
	}
	api := newAPIHandler(opts)
	return chatContextTestAPI{api: api, handler: api.routes(), stateDir: stateDir}
}

func chatContextSaveFeature(t *testing.T, store *feature.Store, f *feature.Feature) {
	t.Helper()
	if err := store.Save(f); err != nil {
		t.Fatalf("Save feature %s: %v", f.ID, err)
	}
}

func chatContextBaseFeature(id, name string) *feature.Feature {
	return &feature.Feature{
		ID:            id,
		Name:          name,
		Slug:          id,
		Status:        feature.StatusFailed,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
}

// chatContextSeedRunFailure stores a feature whose active run failed with
// iteration_budget_exhausted, full raw diagnostics, and a command-block log
// path.
func chatContextSeedRunFailure(t *testing.T, store *feature.Store) {
	f := chatContextBaseFeature("feat-run-failed", "Run Failed Feature")
	f.Run().Failure = &errcat.FailureRecord{
		Code: errcat.IterationBudgetExhausted,
		Context: &errcat.RecordContext{
			Phase:   &errcat.CodePhase{Name: "implement", Iteration: 3},
			Command: &errcat.CodeCommand{ExitCode: 1, LogPaths: []string{"/tmp/chat-context-run.log"}},
		},
		Diagnostics: chatContextLongDiagnostics("run"),
	}
	chatContextSaveFeature(t, store, f)
}

// chatContextSeedChild stores a refactor child whose integration journal is
// parked on an attention record and whose repo-a entry carries both a
// cleanup and a tail warning.
func chatContextSeedChild(t *testing.T, store *feature.Store) {
	f := chatContextBaseFeature("feat-child-pass", "Child Pass Feature")
	f.Parent = &feature.ChildRelationship{
		ParentID: "feat-parent",
		Kind:     "refactor",
		Transaction: &feature.TransactionJournal{
			Phase: feature.TransactionPhaseAttention,
			Attention: &errcat.FailureRecord{
				Code: errcat.IntegrationMergeConflict,
				Context: &errcat.RecordContext{
					Repositories: []errcat.CodeRepository{{Name: "repo-a", ConflictFiles: []string{"main.go"}}},
				},
				Diagnostics: chatContextLongDiagnostics("attention"),
			},
			Entries: []feature.RepoTransactionEntry{{
				Repo: "repo-a",
				Cleanup: &errcat.FailureRecord{
					Code: errcat.ChildCleanupIncomplete,
					Context: &errcat.RecordContext{
						Repositories: []errcat.CodeRepository{{Name: "repo-a"}},
					},
					Diagnostics: chatContextLongDiagnostics("cleanup"),
				},
				Tail: &errcat.FailureRecord{
					Code: errcat.ReviewFeedbackTailIncomplete,
					Context: &errcat.RecordContext{
						Repositories: []errcat.CodeRepository{{Name: "repo-a"}},
					},
					Diagnostics: chatContextLongDiagnostics("tail"),
				},
			}},
		},
	}
	chatContextSaveFeature(t, store, f)
}

// chatContextSeedRepoError stores a feature whose repo-a publish state
// carries a stored pull-request failure record.
func chatContextSeedRepoError(t *testing.T, store *feature.Store) {
	f := chatContextBaseFeature("feat-repo-error", "Repo Error Feature")
	f.Repos = []feature.FeatureRepo{{Name: "repo-a", Branch: "agentico/feat"}}
	f.RepoStates = map[string]*feature.RepoState{
		"repo-a": {
			Touched: true,
			Error: &errcat.FailureRecord{
				Code: errcat.PublishPullRequestFailed,
				Context: &errcat.RecordContext{
					Repositories: []errcat.CodeRepository{{Name: "repo-a", Branch: "agentico/feat"}},
				},
				Diagnostics: chatContextLongDiagnostics("publish"),
			},
		},
	}
	chatContextSaveFeature(t, store, f)
}

// chatContextSeedSetupFailure stores a feature whose worktree setup task
// failed with a task-level record and a latest setup log path.
func chatContextSeedSetupFailure(t *testing.T, store *feature.Store) {
	f := chatContextBaseFeature("feat-setup-failed", "Setup Failed Feature")
	f.Run().Setup = &feature.SetupState{
		Status:        feature.SetupStatusFailed,
		LatestLogPath: "/tmp/chat-context-setup.log",
		Tasks: map[string]feature.SetupTask{
			"worktree:repo-a": {
				Key:   "worktree:repo-a",
				Kind:  feature.SetupTaskWorktree,
				Label: "Worktree: repo-a",
				Repo:  "repo-a",
				Error: &errcat.FailureRecord{
					Code:        errcat.WorktreeSetupFailed,
					Diagnostics: chatContextLongDiagnostics("setup"),
				},
			},
		},
	}
	chatContextSaveFeature(t, store, f)
}

// chatContextSeedRecovery stores a live orphan recovery snapshot on the
// handler and returns its snapshot id and item key.
func chatContextSeedRecovery(t *testing.T, api *apiHandler) (snapshotID, itemKey string) {
	t.Helper()
	items := []ports.RecoveryItem{{
		PIDFile: ports.PIDFile{
			FeatureID: "feat-orphan",
			Phase:     "implement",
			Iteration: 2,
			LogPath:   "/tmp/chat-context-orphan.log",
		},
		ProcessAlive: true,
		Feature:      &feature.Feature{ID: "feat-orphan", Name: "Orphaned Feature"},
	}}
	dtoItems := make([]RecoveryItem, 0, len(items))
	for _, item := range items {
		dtoItems = append(dtoItems, recoveryItemDTO(item))
	}
	snapshotID = api.storeRecoverySnapshot(items, dtoItems)
	return snapshotID, ports.RecoveryActionKey("feat-orphan", "")
}

// TestChatContextResolverBuildsBundlePerScope pins the resolver's five
// scopes: a matching reference yields 200 and the mutation target receives
// a bundle containing the catalog heading with the code, the full stored
// diagnostics beyond the 240 character bound, and the home's log path.
func TestChatContextResolverBuildsBundlePerScope(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(t.TempDir())
	chatContextSeedRunFailure(t, store)
	chatContextSeedChild(t, store)
	chatContextSeedRepoError(t, store)
	chatContextSeedSetupFailure(t, store)

	tests := []struct {
		name        string
		context     map[string]any
		wantHeading string
		wantDetail  string
		wantLogPath string
		notContains string
	}{
		{
			name: "run failure",
			context: map[string]any{
				"scope":      "run",
				"code":       "iteration_budget_exhausted",
				"feature_id": "feat-run-failed",
			},
			wantHeading: "error[iteration_budget_exhausted]: Iteration budget exhausted",
			wantDetail:  chatContextLongDiagnostics("run"),
			wantLogPath: "/tmp/chat-context-run.log",
		},
		{
			name: "transaction attention",
			context: map[string]any{
				"scope":      "transaction",
				"code":       "integration_merge_conflict",
				"feature_id": "feat-child-pass",
			},
			wantHeading: "needs-action[integration_merge_conflict]: Integration merge conflict",
			wantDetail:  chatContextLongDiagnostics("attention"),
		},
		{
			name: "transaction entry cleanup warning",
			context: map[string]any{
				"scope":      "transaction",
				"code":       "child_cleanup_incomplete",
				"feature_id": "feat-child-pass",
				"repository": "repo-a",
			},
			wantHeading: "warning[child_cleanup_incomplete]: Cleanup incomplete",
			wantDetail:  chatContextLongDiagnostics("cleanup"),
			notContains: "integration_merge_conflict",
		},
		{
			name: "repository publish error",
			context: map[string]any{
				"scope":      "repository",
				"code":       "publish_pull_request_failed",
				"feature_id": "feat-repo-error",
				"repository": "repo-a",
			},
			wantHeading: "needs-action[publish_pull_request_failed]: Pull-request creation failed",
			wantDetail:  chatContextLongDiagnostics("publish"),
		},
		{
			name: "setup task error",
			context: map[string]any{
				"scope":      "setup",
				"code":       "worktree_setup_failed",
				"feature_id": "feat-setup-failed",
				"task_key":   "worktree:repo-a",
			},
			wantHeading: "error[worktree_setup_failed]: Worktree setup failed",
			wantDetail:  chatContextLongDiagnostics("setup"),
			wantLogPath: "/tmp/chat-context-setup.log",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := &uploadMutationRecorder{}
			testAPI := newChatContextTestAPI(t, recorder, store)

			w := postTrustedJSON(testAPI.handler, "/api/v1/prompts/chat/start", map[string]any{
				"message": "Explain this error",
				"context": tc.context,
			})
			if w.Code != http.StatusOK {
				t.Fatalf("chat start status = %d body=%s; want 200", w.Code, w.Body.String())
			}
			if recorder.chatReq == nil {
				t.Fatal("StartChat was not called")
			}
			bundle := recorder.chatHiddenContext
			if !strings.Contains(bundle, tc.wantHeading) {
				t.Fatalf("bundle missing catalog heading %q:\n%s", tc.wantHeading, bundle)
			}
			if len(tc.wantDetail) <= 240 {
				t.Fatalf("test bug: wantDetail must exceed the 240 character bound (len %d)", len(tc.wantDetail))
			}
			if !strings.Contains(bundle, tc.wantDetail) {
				t.Fatalf("bundle missing full diagnostics:\n%s", bundle)
			}
			if tc.wantLogPath != "" && !strings.Contains(bundle, tc.wantLogPath) {
				t.Fatalf("bundle missing log path %q:\n%s", tc.wantLogPath, bundle)
			}
			if tc.notContains != "" && strings.Contains(bundle, tc.notContains) {
				t.Fatalf("bundle must not contain %q:\n%s", tc.notContains, bundle)
			}
			if strings.Contains(w.Body.String(), "Chat context —") || strings.Contains(w.Body.String(), "/tmp/chat-context-") {
				t.Fatalf("response body leaked bundle text or a log path: %s", w.Body.String())
			}
		})
	}

	// The recovery scope resolves through the handler's stored snapshot.
	t.Run("recovery item", func(t *testing.T) {
		t.Parallel()
		recorder := &uploadMutationRecorder{}
		testAPI := newChatContextTestAPI(t, recorder, store)
		snapshotID, itemKey := chatContextSeedRecovery(t, testAPI.api)

		w := postTrustedJSON(testAPI.handler, "/api/v1/prompts/chat/start", map[string]any{
			"message": "Explain this error",
			"context": map[string]any{
				"scope":       "recovery",
				"code":        "orphan_session_live",
				"snapshot_id": snapshotID,
				"key":         itemKey,
			},
		})
		if w.Code != http.StatusOK {
			t.Fatalf("chat start status = %d body=%s; want 200", w.Code, w.Body.String())
		}
		if recorder.chatReq == nil {
			t.Fatal("StartChat was not called")
		}
		bundle := recorder.chatHiddenContext
		for _, want := range []string{
			"needs-action[orphan_session_live]: Orphaned session running",
			`"Orphaned Feature"`,
			"/tmp/chat-context-orphan.log",
		} {
			if !strings.Contains(bundle, want) {
				t.Fatalf("bundle missing %q:\n%s", want, bundle)
			}
		}
		if strings.Contains(w.Body.String(), "Chat context —") || strings.Contains(w.Body.String(), "/tmp/chat-context-") {
			t.Fatalf("response body leaked bundle text or a log path: %s", w.Body.String())
		}
	})
}

// TestChatContextResolverRejectsStaleReferences pins the rejection family:
// a code that differs from the stored record, a run with no failure, and an
// expired recovery snapshot return 404 chat_context_not_found; an unknown
// feature id returns 400 chat_context_invalid. No chat turn starts, no
// response body carries bundle text or a filesystem path, and staged
// uploads stay staged.
func TestChatContextResolverRejectsStaleReferences(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(t.TempDir())
	chatContextSeedRunFailure(t, store)
	chatContextSaveFeature(t, store, chatContextBaseFeature("feat-clean", "Clean Feature"))

	tests := []struct {
		name       string
		context    map[string]any
		wantStatus int
		wantCode   string
	}{
		{
			name: "code differs from stored record",
			context: map[string]any{
				"scope":      "run",
				"code":       "session_crashed",
				"feature_id": "feat-run-failed",
			},
			wantStatus: http.StatusNotFound,
			wantCode:   string(errcat.ChatContextNotFound),
		},
		{
			name: "run with no failure",
			context: map[string]any{
				"scope":      "run",
				"code":       "iteration_budget_exhausted",
				"feature_id": "feat-clean",
			},
			wantStatus: http.StatusNotFound,
			wantCode:   string(errcat.ChatContextNotFound),
		},
		{
			name: "expired recovery snapshot",
			context: map[string]any{
				"scope":       "recovery",
				"code":        "orphan_session_live",
				"snapshot_id": "deadbeef",
				"key":         "feat-orphan",
			},
			wantStatus: http.StatusNotFound,
			wantCode:   string(errcat.ChatContextNotFound),
		},
		{
			name: "unknown feature id",
			context: map[string]any{
				"scope":      "run",
				"code":       "iteration_budget_exhausted",
				"feature_id": "feat-unknown",
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcat.ChatContextInvalid),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &uploadMutationRecorder{}
			testAPI := newChatContextTestAPI(t, target, store)

			// A staged image must survive every rejection untouched.
			staged := stageViaAPI(t, testAPI.handler, uploadKindImage, "shot.png", []byte("chat-image"))

			w := postTrustedJSON(testAPI.handler, "/api/v1/prompts/chat/start", map[string]any{
				"message":       "Explain this error",
				"image_uploads": []string{staged.Reference},
				"context":       tc.context,
			})
			if w.Code != tc.wantStatus {
				t.Fatalf("chat start status = %d body=%s; want %d", w.Code, w.Body.String(), tc.wantStatus)
			}
			var body ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != tc.wantCode {
				t.Fatalf("code = %q; want %q", body.Error.Code, tc.wantCode)
			}
			if target.chatReq != nil {
				t.Fatal("StartChat was called for a rejected reference")
			}
			if _, err := os.Stat(filepath.Join(testAPI.stateDir, uploadStagingDirName, staged.Reference)); err != nil {
				t.Fatalf("staged upload after rejection err = %v; want still staged", err)
			}
			if strings.Contains(w.Body.String(), "Chat context —") || strings.Contains(w.Body.String(), "/tmp/") {
				t.Fatalf("response body leaked bundle text or a filesystem path: %s", w.Body.String())
			}
		})
	}
}
