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

package clone

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T, dir string, now func() time.Time) *store {
	t.Helper()
	st, err := newStore(dir, now)
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	return st
}

func testRecord(id, key, dest string, state State) *Record {
	now := time.Now().UTC()
	return &Record{
		ID: id, IdempotencyKey: key, InputFingerprint: "fp-" + key,
		DestinationPath: dest, State: state,
		CreatedAt: now, UpdatedAt: now,
		ResolvedAt: now,
	}
}

func TestStoreSaveGetAndRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := newTestStore(t, dir, time.Now)
	rec := testRecord("clone-1", "key-1", "/root/widget", StateRunning)
	rec.ResolvedAt = time.Time{}
	if err := st.save(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := st.get("clone-1")
	if !ok || got.ID != "clone-1" {
		t.Fatalf("get: %v %v", got, ok)
	}
	// The record is durable: a new store over the same dir sees it.
	st2 := newTestStore(t, dir, time.Now)
	got, ok = st2.get("clone-1")
	if !ok || got.State != StateRunning {
		t.Fatalf("restart lost record: %v %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "clone-1.json")); err != nil {
		t.Errorf("record file missing: %v", err)
	}
}

func TestStoreMalformedRecordsAreSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newTestStore(t, dir, time.Now)
	if _, ok := st.get("broken"); ok {
		t.Error("malformed record loaded")
	}
}

func TestStoreIdempotencyLookup(t *testing.T) {
	t.Parallel()
	st := newTestStore(t, t.TempDir(), time.Now)
	rec := testRecord("clone-1", "key-1", "/root/widget", StateSucceeded)
	if err := st.save(rec); err != nil {
		t.Fatal(err)
	}
	got, ok := st.getByIdempotencyKey("key-1")
	if !ok || got.ID != "clone-1" {
		t.Fatalf("idempotency lookup: %v %v", got, ok)
	}
	if _, ok := st.getByIdempotencyKey("key-2"); ok {
		t.Error("unknown key matched")
	}
}

func TestStoreReservationOnlyForUnresolved(t *testing.T) {
	t.Parallel()
	st := newTestStore(t, t.TempDir(), time.Now)
	dest := "/root/widget"
	running := testRecord("clone-1", "key-1", dest, StateRunning)
	running.ResolvedAt = time.Time{}
	if err := st.save(running); err != nil {
		t.Fatal(err)
	}
	if got, ok := st.reservation(dest); !ok || got.ID != "clone-1" {
		t.Fatalf("reservation: %v %v", got, ok)
	}
	// Terminal resolved records release their reservation.
	resolved := testRecord("clone-2", "key-2", dest, StateSucceeded)
	if err := st.save(resolved); err != nil {
		t.Fatal(err)
	}
	if got, ok := st.reservation(dest); !ok || got.ID != "clone-1" {
		t.Fatalf("resolved record must not hold a reservation: %v %v", got, ok)
	}
	// Cleanup-pending retains the reservation.
	pending := testRecord("clone-3", "key-3", dest, StateCleanupPending)
	pending.ResolvedAt = time.Time{}
	if err := st.save(pending); err != nil {
		t.Fatal(err)
	}
	holders := map[string]bool{}
	for _, rec := range st.all() {
		if UnresolvedStates[rec.State] && rec.DestinationPath == dest {
			holders[rec.ID] = true
		}
	}
	if !holders["clone-1"] || !holders["clone-3"] {
		t.Errorf("unresolved holders wrong: %v", holders)
	}
}

func TestStoreListOrdersUnresolvedFirstAndPaginates(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st := newTestStore(t, t.TempDir(), func() time.Time { return base })
	// Twenty resolved records, then three unresolved records with the
	// newest timestamps, all deterministic.
	for i := 0; i < 20; i++ {
		id := "clone-old-" + string(rune('a'+i))
		rec := testRecord(id, "k-"+id, "/r/"+id, StateSucceeded)
		ts := base.Add(time.Duration(i) * time.Minute)
		rec.ResolvedAt = ts
		rec.CreatedAt = ts
		rec.UpdatedAt = ts
		if err := st.save(rec); err != nil {
			t.Fatal(err)
		}
	}
	states := []State{StateRunning, StateFinalizing, StateCleanupPending}
	ids := []string{"clone-run", "clone-fin", "clone-pend"}
	for i, id := range ids {
		rec := testRecord(id, "k-"+id, "/r/"+id, states[i])
		rec.ResolvedAt = time.Time{}
		ts := base.Add(time.Duration(100+i) * time.Minute)
		rec.CreatedAt = ts
		rec.UpdatedAt = ts
		if err := st.save(rec); err != nil {
			t.Fatal(err)
		}
	}

	page := st.list(ListQuery{Limit: 5})
	if len(page.Operations) != 5 {
		t.Fatalf("page size = %d, want 5", len(page.Operations))
	}
	for i, rec := range page.Operations[:3] {
		if !UnresolvedStates[rec.State] {
			t.Errorf("page item %d (%s, %s) is not unresolved", i, rec.ID, rec.State)
		}
	}
	if page.Operations[0].ID != "clone-pend" {
		t.Errorf("first item = %s, want newest unresolved clone-pend", page.Operations[0].ID)
	}
	if page.NextToken == "" {
		t.Fatal("missing continuation token")
	}

	seen := map[string]bool{}
	for _, rec := range page.Operations {
		seen[rec.ID] = true
	}
	token := page.NextToken
	pages := 1
	for token != "" && pages < 20 {
		next := st.list(ListQuery{Limit: 5, After: token})
		if len(next.Operations) == 0 {
			t.Fatalf("empty page with a continuation token %q", token)
		}
		for _, rec := range next.Operations {
			if seen[rec.ID] {
				t.Errorf("duplicate record across pages: %s", rec.ID)
			}
			seen[rec.ID] = true
		}
		token = next.NextToken
		pages++
	}
	if len(seen) != 23 {
		t.Errorf("paginated records = %d, want 23", len(seen))
	}

	// Active and cleanup-pending records stay discoverable even when a
	// page boundary would otherwise bury them.
	full := st.list(ListQuery{Limit: MaxListLimit})
	firstThree := map[string]bool{}
	for _, rec := range full.Operations[:3] {
		firstThree[rec.ID] = true
	}
	for _, want := range ids {
		if !firstThree[want] {
			t.Errorf("%s not in the first page top", want)
		}
	}
}

func TestStorePruneHonorsRetentionWindow(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	current := base
	st := newTestStore(t, t.TempDir(), func() time.Time { return current })

	young := testRecord("clone-young", "k-young", "/r/young", StateSucceeded)
	young.ResolvedAt = base.Add(6 * 24 * time.Hour) // six days before "now"
	old := testRecord("clone-old", "k-old", "/r/old", StateFailed)
	old.ResolvedAt = base // exactly seven days before "now"
	ancient := testRecord("clone-ancient", "k-ancient", "/r/ancient", StateInterrupted)
	ancient.ResolvedAt = base.Add(-24 * time.Hour) // eight days before "now"
	running := testRecord("clone-run", "k-run", "/r/run", StateRunning)
	running.ResolvedAt = time.Time{}
	pending := testRecord("clone-pend", "k-pend", "/r/pend", StateCleanupPending)
	pending.ResolvedAt = time.Time{}
	for _, rec := range []*Record{young, old, ancient, running, pending} {
		if err := st.save(rec); err != nil {
			t.Fatal(err)
		}
	}

	current = base.Add(7 * 24 * time.Hour)
	pruned := st.prune(current)
	prunedSet := map[string]bool{}
	for _, id := range pruned {
		prunedSet[id] = true
	}
	if !prunedSet["clone-old"] || !prunedSet["clone-ancient"] {
		t.Errorf("expired records not pruned: %v", pruned)
	}
	for _, id := range []string{"clone-old", "clone-ancient"} {
		if _, ok := st.get(id); ok {
			t.Errorf("%s retained after prune", id)
		}
	}
	// Active and cleanup-pending records are never pruned, whatever their
	// age; reads never extend retention.
	for _, keep := range []string{"clone-young", "clone-run", "clone-pend"} {
		if _, ok := st.get(keep); !ok {
			t.Errorf("%s was pruned but must be retained", keep)
		}
	}

	current = base.Add(12 * 24 * time.Hour) // young is six days old
	_, _ = st.get("clone-young")
	st.prune(current)
	if _, ok := st.get("clone-young"); !ok {
		t.Error("young pruned before its window elapsed")
	}
	current = base.Add(13*24*time.Hour + time.Minute) // young is seven days + old
	st.prune(current)
	if _, ok := st.get("clone-young"); ok {
		t.Error("young retained after its window elapsed despite reads")
	}
}
