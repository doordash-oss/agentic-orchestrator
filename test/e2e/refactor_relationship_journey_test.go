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

package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
)

// TestRefactorRelationshipHistoryJourney proves that parent list/detail and
// direct child detail share one complete relationship projection.
func TestRefactorRelationshipHistoryJourney(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "features")
	store := feature.NewStore(stateDir)
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	parent := relationshipJourneyFeature("history-parent", now.Add(-4*time.Hour))
	parent.Status = feature.StatusCodeReady
	closed := relationshipJourneyFeature("history-closed", now.Add(-3*time.Hour))
	closed.Parent = &feature.ChildRelationship{
		ParentID:     parent.ID,
		Kind:         feature.ChildKindRefactor,
		CloseOutcome: feature.ChildCloseOutcomeCompleted,
		ClosedAt:     timePtr(now.Add(-time.Hour)),
	}
	active := relationshipJourneyFeature("history-active", now.Add(-30*time.Minute))
	active.Parent = &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor}
	for _, record := range []*feature.Feature{parent, closed, active} {
		if err := store.Save(record); err != nil {
			t.Fatalf("Save(%s) error = %v", record.ID, err)
		}
	}

	httpServer := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Config:                config.NewDefault(),
		DisableHostValidation: true,
	}))
	t.Cleanup(httpServer.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: httpServer.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	list, err := client.Features(t.Context())
	if err != nil {
		t.Fatalf("Features() error = %v", err)
	}
	if len(list.Features) != 1 || list.Features[0].ID != parent.ID {
		t.Fatalf("Features() = %+v, want parent only", list.Features)
	}
	summary := list.Features[0]
	if summary.ActiveChild == nil || summary.ActiveChild.ID != active.ID {
		t.Fatalf("active child = %+v, want %s", summary.ActiveChild, active.ID)
	}
	if len(summary.ChildHistory) != 1 || summary.ChildHistory[0].ID != closed.ID {
		t.Fatalf("child history = %+v, want %s", summary.ChildHistory, closed.ID)
	}

	parentDetail, err := client.FeatureDetail(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("FeatureDetail(parent) error = %v", err)
	}
	if len(parentDetail.Feature.ChildHistory) != 1 ||
		parentDetail.Feature.ChildHistory[0].DisplayToken != summary.ChildHistory[0].DisplayToken {
		t.Fatalf("parent detail history = %+v, want list projection %+v", parentDetail.Feature.ChildHistory, summary.ChildHistory)
	}
	childDetail, err := client.FeatureDetail(t.Context(), closed.ID)
	if err != nil {
		t.Fatalf("FeatureDetail(closed child) error = %v", err)
	}
	if childDetail.Feature.Relationship == nil ||
		childDetail.Feature.Relationship.DisplayState != "Closed — Completed" {
		t.Fatalf("closed relationship = %+v, want Closed — Completed", childDetail.Feature.Relationship)
	}
	for _, action := range childDetail.Feature.Actions {
		if action.Enabled || len(action.DisabledReasons) == 0 || action.DisabledReasons[0].Code != "relationship_closed" {
			t.Fatalf("closed child action = %+v, want disabled relationship_closed", action)
		}
	}
}

// TestRefactorCascadeDeleteRecoveryJourney proves the API-driven TUI retains
// retryable relationship state until the cascade reports completion.
func TestRefactorCascadeDeleteRecoveryJourney(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "features")
	store := feature.NewStore(stateDir)
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	parent := relationshipJourneyFeature("cascade-parent", now.Add(-time.Hour))
	child := relationshipJourneyFeature("cascade-child", now)
	child.Parent = &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor}
	for _, record := range []*feature.Feature{parent, child} {
		if err := store.Save(record); err != nil {
			t.Fatalf("Save(%s) error = %v", record.ID, err)
		}
	}

	responses := []server.DeleteFeatureResponse{
		{
			FeatureID: parent.ID, OperationID: "cascade:" + parent.ID,
			Status: feature.CascadeDeleteCleanupPending,
			Diagnostics: []server.CascadeDiagnostic{{
				Code: "worktree_cleanup_failed", Message: "remove child worktree: device busy", Repo: "agentic-orchestrator",
			}},
		},
		{
			FeatureID: parent.ID, OperationID: "cascade:" + parent.ID,
			Status: feature.CascadeDeleteAttentionRequired,
			Diagnostics: []server.CascadeDiagnostic{{
				Code: "ref_moved", Message: "candidate ref moved externally", Repo: "agentic-orchestrator",
				Ref: "refs/heads/cascade-parent", AnchorSHA: "anchor", CandidateSHA: "candidate", ObservedSHA: "observed",
			}},
		},
		{
			FeatureID: parent.ID, OperationID: "cascade:" + parent.ID,
			Status: feature.CascadeDeleteCompleted,
		},
	}
	mutations := &cascadeJourneyMutationTarget{
		store: store, parentID: parent.ID, childID: child.ID, responses: responses,
	}
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{t.TempDir()}
	httpServer := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Config:                cfg,
		Mutations:             mutations,
		DisableHostValidation: true,
	}))
	t.Cleanup(httpServer.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: httpServer.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	app, err := tui.NewAPIAppModel(t.Context(), client, tui.APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	t.Cleanup(app.Close)
	model, _ := app.Update(tea.WindowSizeMsg{Width: 320, Height: 40})
	app = model.(tui.APIAppModel)

	for i, response := range responses[:2] {
		app, _ = runCascadeJourneyDelete(t, app, true)
		detail, detailErr := client.FeatureDetail(t.Context(), parent.ID)
		if detailErr != nil {
			t.Fatalf("FeatureDetail(parent) after %s error = %v", response.Status, detailErr)
		}
		if detail.Feature.ActiveChild == nil || detail.Feature.ActiveChild.ID != child.ID {
			t.Fatalf("active child after %s = %+v, want retained %s", response.Status, detail.Feature.ActiveChild, child.ID)
		}
		childDetail, childErr := client.FeatureDetail(t.Context(), child.ID)
		if childErr != nil {
			t.Fatalf("FeatureDetail(child) after %s error = %v", response.Status, childErr)
		}
		if childDetail.Feature.Relationship == nil || childDetail.Feature.ParentID != parent.ID {
			t.Fatalf("child relationship after %s = %+v, want retained parent %s", response.Status, childDetail.Feature.Relationship, parent.ID)
		}
		view := app.View().Content
		for _, want := range []string{
			parent.ID, string(response.Status),
			response.Diagnostics[0].Code, response.Diagnostics[0].Message,
		} {
			if !strings.Contains(view, want) {
				t.Fatalf("delete response %d View() missing %q after %s:\n%s", i, want, response.Status, view)
			}
		}
		if response.Status == feature.CascadeDeleteAttentionRequired {
			for _, want := range []string{"anchor", "candidate", "observed"} {
				if !strings.Contains(view, want) {
					t.Fatalf("attention-required View() missing ref diagnostic %q:\n%s", want, view)
				}
			}
		}
	}

	app, refreshCmd := runCascadeJourneyDelete(t, app, false)
	if refreshCmd != nil {
		t.Fatal("completed cascade returned a refresh command, want immediate eviction")
	}
	view := app.View().Content
	for _, removed := range []string{parent.ID, child.ID} {
		if strings.Contains(view, removed) {
			t.Fatalf("completed cascade View() retained %q:\n%s", removed, view)
		}
		if _, detailErr := client.FeatureDetail(t.Context(), removed); detailErr == nil {
			t.Fatalf("FeatureDetail(%s) succeeded after completed cascade", removed)
		}
	}
	if mutations.calls != len(responses) {
		t.Fatalf("DeleteFeature calls = %d, want %d convergent retries", mutations.calls, len(responses))
	}
}

// TestRefactorCascadeDeleteStartupRecovery proves a crash after durable intent
// creation converges on startup and repeated reconciliation is a no-op.
func TestRefactorCascadeDeleteStartupRecovery(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "features")
	store := feature.NewStore(stateDir)
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	parent := relationshipJourneyFeature("recovery-parent", now.Add(-time.Hour))
	child := relationshipJourneyFeature("recovery-child", now)
	child.Parent = &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor}
	for _, record := range []*feature.Feature{parent, child} {
		if err := store.Save(record); err != nil {
			t.Fatalf("Save(%s) error = %v", record.ID, err)
		}
	}
	if _, err := store.BeginCascadeDelete(parent.ID, now); err != nil {
		t.Fatalf("BeginCascadeDelete() error = %v", err)
	}

	manager := feature.NewManager(store, config.NewDefault())
	orch := orchestrator.New(orchestrator.Deps{Store: store, Lifecycle: manager}, orchestrator.Hooks{})
	if err := orch.ReconcileCascadeDeletes(); err != nil {
		t.Fatalf("ReconcileCascadeDeletes() error = %v", err)
	}
	for _, id := range []string{child.ID, parent.ID} {
		if _, err := store.Load(id); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load(%s) error = %v, want not exist", id, err)
		}
	}
	if err := orch.ReconcileCascadeDeletes(); err != nil {
		t.Fatalf("second ReconcileCascadeDeletes() error = %v", err)
	}
}

// TestRefactorCascadeDeleteRecoveryRefreshBundle proves startup recovery
// publishes retained parent and child snapshots when ref safety needs attention.
func TestRefactorCascadeDeleteRecoveryRefreshBundle(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "features")
	store := feature.NewStore(stateDir)
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	parent := relationshipJourneyFeature("recovery-bundle-parent", now.Add(-time.Hour))
	parent.Repos = []feature.FeatureRepo{{
		Name: "agentic-orchestrator", Path: "/repos/agentico",
		WorktreePath: "/worktrees/recovery-bundle-parent", Branch: "feature/recovery-bundle-parent",
	}}
	child := relationshipJourneyFeature("recovery-bundle-child", now)
	child.Repos = []feature.FeatureRepo{{
		Name: "agentic-orchestrator", Path: "/repos/agentico",
		WorktreePath: "/worktrees/recovery-bundle-child", Branch: "feature/recovery-bundle-child",
	}}
	child.Parent = &feature.ChildRelationship{
		ParentID: parent.ID, Kind: feature.ChildKindRefactor,
		Transaction: &feature.TransactionJournal{Entries: []feature.RepoTransactionEntry{{
			Repo: "agentic-orchestrator", ParentBranch: parent.Repos[0].Branch,
			ParentAnchorSHA: "anchor", CandidateSHA: "candidate",
			ApplyState: feature.RepoApplyApplied,
		}}},
	}
	for _, record := range []*feature.Feature{parent, child} {
		if err := store.Save(record); err != nil {
			t.Fatalf("Save(%s) error = %v", record.ID, err)
		}
	}
	if _, err := store.BeginCascadeDelete(parent.ID, now); err != nil {
		t.Fatalf("BeginCascadeDelete() error = %v", err)
	}

	orch := orchestrator.New(orchestrator.Deps{Store: store}, orchestrator.Hooks{})
	eventCh := make(chan any, 8)
	bridgeCtx, stopBridge := context.WithCancel(t.Context())
	t.Cleanup(stopBridge)
	go func() {
		for {
			select {
			case ev := <-orch.Events():
				eventCh <- ev
			case <-bridgeCtx.Done():
				return
			}
		}
	}()
	httpServer := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime: server.RuntimeIdentity{StateDir: stateDir}, Features: store,
		FeatureStore: store, Events: eventCh, Config: config.NewDefault(),
		DisableHostValidation: true,
	}))
	t.Cleanup(httpServer.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: httpServer.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	subscribeCtx, cancelSubscribe := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancelSubscribe)
	signals, errs := client.SubscribeEvents(subscribeCtx, server.EventSubscriptionOptions{})
	if connected := receiveRefreshSignal(t, signals, errs); connected.Event.Kind != "connected" {
		t.Fatalf("first SSE event = %q, want connected", connected.Event.Kind)
	}

	if err := orch.ReconcileCascadeDeletes(); err != nil {
		t.Fatalf("ReconcileCascadeDeletes() error = %v", err)
	}
	signal := receiveRefreshSignal(t, signals, errs)
	if !signal.SnapshotRequired ||
		signal.Resource.ParentID != parent.ID ||
		signal.Resource.ChildID != child.ID ||
		signal.Resource.RelationshipDeleted {
		t.Fatalf("cascade recovery signal = %+v, want retained relationship snapshot", signal)
	}
	snapshot, err := client.FetchRefreshSnapshot(t.Context(), signal)
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if snapshot.Relationship == nil ||
		snapshot.Relationship.Parent.Feature.ID != parent.ID ||
		snapshot.Relationship.Child == nil ||
		snapshot.Relationship.Child.Feature.ID != child.ID {
		t.Fatalf("cascade recovery bundle = %+v, want retained parent and child", snapshot.Relationship)
	}
}

type cascadeJourneyMutationTarget struct {
	server.MutationTarget
	store     *feature.Store
	parentID  string
	childID   string
	responses []server.DeleteFeatureResponse
	calls     int
}

func (t *cascadeJourneyMutationTarget) DeleteFeature(featureID string) (server.DeleteFeatureResponse, error) {
	if featureID != t.parentID {
		return server.DeleteFeatureResponse{}, fmt.Errorf("delete feature %q, want %q", featureID, t.parentID)
	}
	if t.calls >= len(t.responses) {
		return server.DeleteFeatureResponse{}, fmt.Errorf("unexpected delete retry %d", t.calls+1)
	}
	response := t.responses[t.calls]
	t.calls++
	if response.Status == feature.CascadeDeleteCompleted {
		if err := t.store.Delete(t.childID); err != nil {
			return server.DeleteFeatureResponse{}, err
		}
		if err := t.store.Delete(t.parentID); err != nil {
			return server.DeleteFeatureResponse{}, err
		}
	}
	return response, nil
}

func (t *cascadeJourneyMutationTarget) ScanRecovery(context.Context) ([]ports.RecoveryItem, error) {
	return nil, nil
}

func runCascadeJourneyDelete(t *testing.T, app tui.APIAppModel, wantRefresh bool) (tui.APIAppModel, tea.Cmd) {
	t.Helper()
	model, cmd := app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if cmd != nil {
		t.Fatal("Update(d) returned command before delete confirmation")
	}
	model, cmd = model.(tui.APIAppModel).Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil delete command")
	}
	model, refreshCmd := model.(tui.APIAppModel).Update(cmd())
	updated := model.(tui.APIAppModel)
	if wantRefresh {
		if refreshCmd == nil {
			t.Fatal("non-terminal cascade returned nil parent refresh command")
		}
		model, _ = updated.Update(refreshCmd())
		updated = model.(tui.APIAppModel)
	}
	return updated, refreshCmd
}

func relationshipJourneyFeature(id string, created time.Time) *feature.Feature {
	f := &feature.Feature{
		ID: id, Name: id, Slug: id, Created: created,
		Status: feature.StatusCreated, CurrentPhase: feature.PhasePlan,
		Pipeline: feature.PipelineMedium, ActiveRun: 1, RunCount: 1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	f.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusDone}})
	return f
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func receiveRefreshSignal(
	t *testing.T,
	signals <-chan server.RefreshSignal,
	errs <-chan error,
) server.RefreshSignal {
	t.Helper()
	select {
	case signal := <-signals:
		return signal
	case err := <-errs:
		t.Fatalf("SubscribeEvents() error = %v", err)
	case <-t.Context().Done():
		t.Fatal("SubscribeEvents() timed out")
	}
	return server.RefreshSignal{}
}
