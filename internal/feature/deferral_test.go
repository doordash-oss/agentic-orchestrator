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
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDeferralID_Stable(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// Same phase + description → same ID, regardless of whitespace/casing.
	a := DeferralID(1, "Install Tailwind + shadcn/ui")
	b := DeferralID(1, "install  tailwind + shadcn/ui")
	c := DeferralID(1, "  Install Tailwind + shadcn/ui  ")
	if a != b {
		t.Errorf("ID differs for whitespace+case: %q vs %q", a, b)
	}
	if a != c {
		t.Errorf("ID differs for trimmed whitespace: %q vs %q", a, c)
	}
	// Different phase → different ID (a phase-3 deferral and a phase-1
	// deferral for the same description are distinct commitments).
	d := DeferralID(3, "Install Tailwind + shadcn/ui")
	if a == d {
		t.Errorf("ID collides across phases: %q", a)
	}
	// Different description → different ID.
	e := DeferralID(1, "Install Radix primitives")
	if a == e {
		t.Errorf("ID collides across descriptions")
	}
	// Format check.
	if len(a) != 8 || a[:2] != "D-" {
		t.Errorf("ID shape violates contract: %q", a)
	}
}

func TestMergeDeferrals(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		run    func(time.Time) []Deferral
		assert func(t *testing.T, got []Deferral)
	}{
		{
			name: "fresh entry created",
			run: func(now time.Time) []Deferral {
				return MergeDeferrals(nil, []IncomingDeferral{
					{Description: "Install Tailwind", DueByPhase: 3, Reason: "Scoped to design-system phase"},
				}, 1, now)
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				d := got[0]
				if d.Status != DeferralOpen {
					t.Errorf("fresh entry status = %q, want open", d.Status)
				}
				if d.CreatedInPhase != 1 {
					t.Errorf("CreatedInPhase = %d, want 1", d.CreatedInPhase)
				}
				if d.DueByPhase != 3 {
					t.Errorf("DueByPhase = %d, want 3", d.DueByPhase)
				}
				if len(d.History) != 1 || d.History[0].Kind != DeferralEventCreated {
					t.Errorf("fresh entry History = %+v, want one created event", d.History)
				}
			},
		},
		{
			name: "idempotent re-emission",
			run: func(now time.Time) []Deferral {
				incoming := []IncomingDeferral{{Description: "Install Tailwind", DueByPhase: 3, Reason: "Scope"}}
				first := MergeDeferrals(nil, incoming, 1, now)
				return MergeDeferrals(first, incoming, 1, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				if len(got[0].History) != 1 {
					t.Errorf("re-emission history length = %d, want 1", len(got[0].History))
				}
			},
		},
		{
			name: "re-deferral appends event",
			run: func(now time.Time) []Deferral {
				initial := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "Install Tailwind", DueByPhase: 3, Reason: "Scope"},
				}, 1, now)
				return MergeDeferrals(initial, []IncomingDeferral{
					{Description: "Install Tailwind", DueByPhase: 5, Reason: "Needs coordinated storage refactor"},
				}, 1, now.Add(2*time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				d := got[0]
				if d.DueByPhase != 5 {
					t.Errorf("DueByPhase after re-defer = %d, want 5", d.DueByPhase)
				}
				if d.Status != DeferralRedeferred {
					t.Errorf("Status after re-defer = %q, want open_redeferred", d.Status)
				}
				if d.RedeferralCount() != 1 {
					t.Errorf("RedeferralCount = %d, want 1", d.RedeferralCount())
				}
			},
		},
		{
			name: "closed entry stays closed",
			run: func(now time.Time) []Deferral {
				initial := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "Install Tailwind", DueByPhase: 3, Reason: "Scope"},
				}, 1, now)
				CloseDeferrals(initial, []string{initial[0].ID}, 3, "implement", now)
				return MergeDeferrals(initial, []IncomingDeferral{
					{Description: "Install Tailwind", DueByPhase: 7, Reason: "Actually nope"},
				}, 1, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				if got[0].Status != DeferralClosed {
					t.Errorf("closed entry status = %q, want closed", got[0].Status)
				}
			},
		},
		{
			name: "fresh entry carries repo scope",
			run: func(now time.Time) []Deferral {
				return MergeDeferrals(nil, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"repo-a"}},
				}, 1, now)
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				if !reflect.DeepEqual(got[0].RepoScope, []string{"repo-a"}) {
					t.Errorf("fresh entry RepoScope = %#v, want [repo-a]", got[0].RepoScope)
				}
			},
		},
		{
			name: "scope idempotent re-emission",
			run: func(now time.Time) []Deferral {
				first := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"repo-a"}},
				}, 1, now)
				return MergeDeferrals(first, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"repo-a"}},
				}, 1, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				if len(got[0].History) != 1 {
					t.Errorf("idempotent scope re-emit history length = %d, want 1", len(got[0].History))
				}
				if got[0].Status != DeferralOpen {
					t.Errorf("idempotent scope re-emit status = %q, want open", got[0].Status)
				}
			},
		},
		{
			name: "scope drift records history event",
			run: func(now time.Time) []Deferral {
				first := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"repo-a"}},
				}, 1, now)
				return MergeDeferrals(first, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"repo-a", "repo-b"}},
				}, 1, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				d := got[0]
				if !reflect.DeepEqual(d.RepoScope, []string{"repo-a", "repo-b"}) {
					t.Errorf("RepoScope after drift = %#v, want [repo-a repo-b]", d.RepoScope)
				}
				if d.Status != DeferralRedeferred {
					t.Errorf("Status after scope drift = %q, want open_redeferred", d.Status)
				}
				if got := countDeferralEvents(d, DeferralEventRedeferred); got != 1 {
					t.Errorf("Redeferred event count = %d, want 1", got)
				}
			},
		},
		{
			name: "scope order is invariant",
			run: func(now time.Time) []Deferral {
				first := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"a", "b"}},
				}, 1, now)
				return MergeDeferrals(first, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"b", "a"}},
				}, 1, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				if len(got[0].History) != 1 {
					t.Errorf("set-equal scope history length = %d, want 1", len(got[0].History))
				}
				if got[0].Status != DeferralOpen {
					t.Errorf("set-equal scope status = %q, want open", got[0].Status)
				}
			},
		},
		{
			name: "scope duplicate is invariant",
			run: func(now time.Time) []Deferral {
				first := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"a"}},
				}, 1, now)
				return MergeDeferrals(first, []IncomingDeferral{
					{Description: "X", DueByPhase: 3, Reason: "r", RepoScope: []string{"a", "a"}},
				}, 1, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				if len(got[0].History) != 1 {
					t.Errorf("duplicate-only scope history length = %d, want 1", len(got[0].History))
				}
				if got[0].Status != DeferralOpen {
					t.Errorf("duplicate-only scope status = %q, want open", got[0].Status)
				}
			},
		},
		{
			name: "ID-cited re-deferral tolerates description drift",
			run: func(now time.Time) []Deferral {
				initial := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "Add ui.theme key to config", DueByPhase: 3, Reason: "Scope"},
				}, 1, now)
				return MergeDeferrals(initial, []IncomingDeferral{
					{
						ID:          initial[0].ID,
						Description: "Add ui.theme key to internal/config/UIConfig and wire the Settings surface toggle (auto | dark | light)",
						DueByPhase:  5,
						Reason:      "Phase 5 Settings surface lands it alongside other UI keys",
					},
				}, 3, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				d := got[0]
				if d.DueByPhase != 5 {
					t.Errorf("DueByPhase = %d, want 5", d.DueByPhase)
				}
				if d.Status != DeferralRedeferred {
					t.Errorf("Status = %q, want open_redeferred", d.Status)
				}
				if !strings.Contains(d.Description, "Settings surface toggle") {
					t.Errorf("Description drift not applied; got %q", d.Description)
				}
				if got := countDeferralEvents(d, DeferralEventRedeferred); got != 1 {
					t.Errorf("Redeferred event count = %d, want 1", got)
				}
			},
		},
		{
			name: "unknown ID falls back to description hash",
			run: func(now time.Time) []Deferral {
				initial := MergeDeferrals(nil, []IncomingDeferral{
					{Description: "Existing entry", DueByPhase: 3, Reason: "Scope"},
				}, 1, now)
				return MergeDeferrals(initial, []IncomingDeferral{
					{ID: "D-not-in-ledger", Description: "Existing entry", DueByPhase: 5, Reason: "punting"},
				}, 1, now.Add(time.Hour))
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				if got[0].DueByPhase != 5 {
					t.Errorf("DueByPhase = %d, want 5", got[0].DueByPhase)
				}
			},
		},
		{
			name: "unknown ID and hash miss creates fresh content-derived entry",
			run: func(now time.Time) []Deferral {
				return MergeDeferrals(nil, []IncomingDeferral{
					{ID: "D-stale", Description: "Something genuinely new", DueByPhase: 3, Reason: "new work declared in this phase"},
				}, 1, now)
			},
			assert: func(t *testing.T, got []Deferral) {
				requireDeferralCount(t, got, 1)
				want := DeferralID(1, "Something genuinely new")
				if got[0].ID != want {
					t.Errorf("fresh entry ID = %q, want %q", got[0].ID, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t, tt.run(now))
		})
	}
}

func requireDeferralCount(t *testing.T, got []Deferral, want int) {
	t.Helper()
	if len(got) != want {
		t.Fatalf("MergeDeferrals() produced %d entries, want %d: %+v", len(got), want, got)
	}
}

func countDeferralEvents(d Deferral, kind DeferralEventKind) int {
	n := 0
	for _, e := range d.History {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestDueForPhase_FiltersCorrectly(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	ledger := []Deferral{
		{ID: "D-1", DueByPhase: 3, Status: DeferralOpen},
		{ID: "D-2", DueByPhase: 3, Status: DeferralClosed},
		{ID: "D-3", DueByPhase: 5, Status: DeferralOpen},
		{ID: "D-4", DueByPhase: 3, Status: DeferralRedeferred},
	}
	got := DueForPhase(ledger, 3)
	if len(got) != 2 {
		t.Fatalf("expected 2 due entries (D-1, D-4), got %d: %+v", len(got), got)
	}
	// Sorted by ID.
	if got[0].ID != "D-1" || got[1].ID != "D-4" {
		t.Errorf("DueForPhase not sorted by ID: %+v", got)
	}
}

func TestCloseDeferrals_TransitionsMatchingIDs(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	ledger := []Deferral{
		{ID: "D-1", Status: DeferralOpen},
		{ID: "D-2", Status: DeferralOpen},
		{ID: "D-3", Status: DeferralClosed},
	}
	n := CloseDeferrals(ledger, []string{"D-1", "D-3", "D-nope"}, 3, "implement", now)
	if n != 1 {
		t.Errorf("transitioned %d entries, want 1 (D-1 only; D-3 already closed, D-nope nonexistent)", n)
	}
	if ledger[0].Status != DeferralClosed {
		t.Errorf("D-1 not transitioned")
	}
	if ledger[0].ClosedInPhase != 3 {
		t.Errorf("D-1 ClosedInPhase = %d, want 3", ledger[0].ClosedInPhase)
	}
}

func TestDeferral_YAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	original := Deferral{
		ID:             "D-abc123",
		Description:    "Install Tailwind + shadcn/ui",
		CreatedAt:      now,
		CreatedInPhase: 1,
		CreatedInKind:  "implement",
		DueByPhase:     3,
		Reason:         "Scope",
		Status:         DeferralOpen,
		History: []DeferralEvent{
			{At: now, Kind: DeferralEventCreated, ToPhase: 3, Reason: "Scope"},
		},
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Deferral
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != original.ID ||
		decoded.Description != original.Description ||
		decoded.CreatedInPhase != original.CreatedInPhase ||
		decoded.DueByPhase != original.DueByPhase ||
		decoded.Status != original.Status ||
		len(decoded.History) != 1 {
		t.Errorf("round-trip lost data:\n%+v\nvs\n%+v", decoded, original)
	}
}

func TestDeferral_YAMLRoundTripPreservesRepoScope(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	now := time.Date(2026, 4, 27, 9, 0, 0, 0, time.UTC)
	original := Deferral{
		ID:             "D-scoped",
		Description:    "Cut over to the new image cache",
		CreatedAt:      now,
		CreatedInPhase: 2,
		CreatedInKind:  "implement",
		DueByPhase:     5,
		Reason:         "Repo-local refactor",
		Status:         DeferralOpen,
		RepoScope:      []string{"repo-a", "repo-b"},
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "repo_scope:") {
		t.Errorf("marshalled YAML missing repo_scope key:\n%s", data)
	}
	var decoded Deferral
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded.RepoScope, original.RepoScope) {
		t.Errorf("RepoScope round-trip lost data: got %#v, want %#v", decoded.RepoScope, original.RepoScope)
	}
}

func TestDeferral_YAMLOmitsEmptyRepoScope(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name  string
		scope []string
	}{
		{"nil", nil},
		{"empty", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Deferral{
				ID:          "D-1",
				Description: "X",
				DueByPhase:  3,
				Status:      DeferralOpen,
				RepoScope:   tt.scope,
			}
			data, err := yaml.Marshal(d)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(data), "repo_scope") {
				t.Errorf("expected omitempty to drop repo_scope; got:\n%s", data)
			}
		})
	}
}

func TestDeferral_LegacyYAMLDecodesAsFeatureWide(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	legacy := []byte(`id: D-legacy
description: Pre-Phase-6 entry
created_at: 2026-01-01T00:00:00Z
created_in_phase: 1
created_in_kind: implement
due_by_phase: 3
reason: Scope
status: open
`)
	var decoded Deferral
	if err := yaml.Unmarshal(legacy, &decoded); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if decoded.RepoScope != nil {
		t.Errorf("legacy YAML decoded RepoScope = %#v, want nil (feature-wide)", decoded.RepoScope)
	}
}

func TestDueForPhaseScopedTo(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name     string
		ledger   []Deferral
		phase    int
		repoName string
		wantIDs  []string
	}{
		{
			name: "feature-wide entry always included",
			ledger: []Deferral{
				{ID: "D-1", Description: "X", DueByPhase: 3, Status: DeferralOpen},
			},
			phase:    3,
			repoName: "repo-a",
			wantIDs:  []string{"D-1"},
		},
		{
			name: "repo-scoped entry matches named repo",
			ledger: []Deferral{
				{ID: "D-1", Description: "X", DueByPhase: 3, Status: DeferralOpen, RepoScope: []string{"repo-a"}},
			},
			phase:    3,
			repoName: "repo-a",
			wantIDs:  []string{"D-1"},
		},
		{
			name: "repo-scoped entry excludes other repos",
			ledger: []Deferral{
				{ID: "D-1", Description: "X", DueByPhase: 3, Status: DeferralOpen, RepoScope: []string{"repo-a"}},
			},
			phase:    3,
			repoName: "repo-b",
			wantIDs:  nil,
		},
		{
			name: "multi-repo scope matches every listed repo",
			ledger: []Deferral{
				{ID: "D-1", Description: "X", DueByPhase: 3, Status: DeferralOpen, RepoScope: []string{"repo-a", "repo-b"}},
			},
			phase:    3,
			repoName: "repo-b",
			wantIDs:  []string{"D-1"},
		},
		{
			name: "empty repo name is permissive",
			ledger: []Deferral{
				{ID: "D-feature-wide", Description: "wide", DueByPhase: 3, Status: DeferralOpen},
				{ID: "D-scoped", Description: "scoped", DueByPhase: 3, Status: DeferralOpen, RepoScope: []string{"repo-a"}},
			},
			phase:    3,
			repoName: "",
			wantIDs:  []string{"D-feature-wide", "D-scoped"},
		},
		{
			name: "scope filter respects due-phase status filters",
			ledger: []Deferral{
				{ID: "D-closed", Description: "X", DueByPhase: 3, Status: DeferralClosed, RepoScope: []string{"repo-a"}},
				{ID: "D-wrong-phase", Description: "Y", DueByPhase: 4, Status: DeferralOpen, RepoScope: []string{"repo-a"}},
				{ID: "D-open", Description: "Z", DueByPhase: 3, Status: DeferralOpen, RepoScope: []string{"repo-a"}},
			},
			phase:    3,
			repoName: "repo-a",
			wantIDs:  []string{"D-open"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DueForPhaseScopedTo(tt.ledger, tt.phase, tt.repoName)
			if !deferralIDsEqual(deferralIDs(got), tt.wantIDs) {
				t.Errorf("DueForPhaseScopedTo() IDs = %v, want %v", deferralIDs(got), tt.wantIDs)
			}
		})
	}
}

func deferralIDs(deferrals []Deferral) []string {
	ids := make([]string, 0, len(deferrals))
	for _, d := range deferrals {
		ids = append(ids, d.ID)
	}
	return ids
}

func deferralIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOpenDeferrals_SortedByDueByPhaseThenID(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	ledger := []Deferral{
		{ID: "D-c", DueByPhase: 3, Status: DeferralOpen},
		{ID: "D-a", DueByPhase: 5, Status: DeferralOpen},
		{ID: "D-b", DueByPhase: 3, Status: DeferralOpen},
		{ID: "D-x", DueByPhase: 4, Status: DeferralClosed}, // excluded
	}
	got := OpenDeferrals(ledger)
	if len(got) != 3 {
		t.Fatalf("expected 3 open entries, got %d", len(got))
	}
	// Phase 3 entries come first; within phase 3, D-b before D-c (ID-sorted).
	if got[0].ID != "D-b" || got[1].ID != "D-c" || got[2].ID != "D-a" {
		t.Errorf("unexpected sort order: %+v", got)
	}
}
