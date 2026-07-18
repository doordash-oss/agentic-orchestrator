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
	"errors"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func newGuardStoreAndFeature(t *testing.T) (*feature.Store, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:            "feat-guard",
		Name:          "Guard",
		Slug:          "guard",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     3,
		RunCount:      3,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return store, f
}

func TestValidateRewindGuardAcceptsCurrentRevision(t *testing.T) {
	store, f := newGuardStoreAndFeature(t)
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	target := serverMutationTarget{store: store}
	if _, err := target.validateRewindGuard(f.ID, serverruntime.RewindFeatureRequest{
		SourceRunNumber: loaded.ActiveRun,
		SourceRevision:  feature.RewindRevision(loaded),
	}); err != nil {
		t.Fatalf("validateRewindGuard current revision error = %v; want nil", err)
	}
}

func TestValidateRewindGuardUnguardedWhenNoRevision(t *testing.T) {
	store, f := newGuardStoreAndFeature(t)
	target := serverMutationTarget{store: store}
	got, err := target.validateRewindGuard(f.ID, serverruntime.RewindFeatureRequest{})
	if err != nil {
		t.Fatalf("validateRewindGuard unguarded error = %v; want nil", err)
	}
	if got == nil || got.ActiveRun != f.ActiveRun {
		t.Fatalf("validateRewindGuard unguarded feature = %#v; want active run %d", got, f.ActiveRun)
	}
}

func TestValidateRewindGuardRejectsStaleRevision(t *testing.T) {
	store, f := newGuardStoreAndFeature(t)
	target := serverMutationTarget{store: store}
	_, err := target.validateRewindGuard(f.ID, serverruntime.RewindFeatureRequest{
		SourceRunNumber: f.ActiveRun,
		SourceRevision:  "stale-revision-token",
	})
	if err == nil {
		t.Fatal("validateRewindGuard stale revision error = nil; want rejection")
	}
	var stale staleRewindError
	if !errors.As(err, &stale) {
		t.Fatalf("error = %v; want staleRewindError", err)
	}
}

func TestValidateRewindGuardRejectsChangedActiveRun(t *testing.T) {
	store, f := newGuardStoreAndFeature(t)
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	target := serverMutationTarget{store: store}
	_, err = target.validateRewindGuard(f.ID, serverruntime.RewindFeatureRequest{
		SourceRunNumber: f.ActiveRun + 1, // a different (historical/future) run
		SourceRevision:  feature.RewindRevision(loaded),
	})
	if err == nil {
		t.Fatal("validateRewindGuard changed active run error = nil; want rejection")
	}
	var stale staleRewindError
	if !errors.As(err, &stale) {
		t.Fatalf("error = %v; want staleRewindError", err)
	}
}

func TestValidateRewindGuardRejectsWhenStoreMissing(t *testing.T) {
	target := serverMutationTarget{} // no store
	_, err := target.validateRewindGuard("feat-x", serverruntime.RewindFeatureRequest{
		SourceRevision: "some-revision",
	})
	if err == nil {
		t.Fatal("validateRewindGuard missing-store error = nil; want rejection")
	}
}

func TestRedactRewindWarningsUsesSafeDisplayText(t *testing.T) {
	warnings := redactRewindWarnings([]string{" raw initial prompt private-token "})
	if len(warnings) != 1 {
		t.Fatalf("redactRewindWarnings() length = %d; want 1", len(warnings))
	}
	if got, want := warnings[0], "[redacted prompt] [redacted]"; got != want {
		t.Fatalf("redactRewindWarnings() = %q; want %q", got, want)
	}
}

func TestParseServerPhaseStrictUsesSharedDirParser(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  feature.Phase
	}{
		{name: "knowledge base alias", input: " KB ", want: feature.PhaseKnowledgeBase},
		{name: "final review alias", input: "final review", want: feature.PhaseFinalReview},
		{name: "review extra", input: "review", want: feature.PhaseReview},
		{name: "publish extra", input: "publish", want: feature.PhasePublish},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseServerPhaseStrict(tt.input)
			if err != nil {
				t.Fatalf("parseServerPhaseStrict(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseServerPhaseStrict(%q) = %v; want %v", tt.input, got, tt.want)
			}
		})
	}
}
