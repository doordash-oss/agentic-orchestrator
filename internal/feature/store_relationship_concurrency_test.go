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
	"fmt"
	"sync"
	"testing"
	"time"
)

// seedRelationshipStore persists a parent with count closed children.
func seedRelationshipStore(t *testing.T, count int) (*Store, string) {
	t.Helper()
	store := NewStore(t.TempDir())
	parentID := "parent-concurrency"
	parent := &Feature{ID: parentID, Name: "parent", SchemaVersion: SchemaVersionCurrent}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent): %v", err)
	}
	closedAt := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		child := &Feature{
			ID:            fmt.Sprintf("child-%03d", i),
			Name:          fmt.Sprintf("child %d", i),
			SchemaVersion: SchemaVersionCurrent,
			Parent: &ChildRelationship{
				ParentID:     parentID,
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeCompleted,
				ClosedAt:     timePointer(closedAt.Add(-time.Duration(i) * time.Minute)),
			},
		}
		if err := store.Save(child); err != nil {
			t.Fatalf("Save(%q): %v", child.ID, err)
		}
	}
	return store, parentID
}

// TestStoreRelationshipReadsRunConcurrently proves the relationship scans hold
// the store lock shared: a read proceeds while another read is in flight,
// instead of every concurrent API read queueing behind one full store scan.
func TestStoreRelationshipReadsRunConcurrently(t *testing.T) {
	t.Parallel()

	store, parentID := seedRelationshipStore(t, 8)

	// Standing in for a scan already in progress.
	store.mu.RLock()
	defer store.mu.RUnlock()

	for name, read := range map[string]func() error{
		"RelationshipChildren": func() error {
			_, err := store.RelationshipChildren(parentID)
			return err
		},
		"AllRelationshipChildren": func() error {
			_, err := store.AllRelationshipChildren()
			return err
		},
	} {
		done := make(chan error, 1)
		go func() { done <- read() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s while another read is in flight: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s blocked behind another reader; want a shared lock", name)
		}
	}
}

// TestStoreMutationExcludedByRelationshipRead pins the other half of the
// contract: a shared read still excludes writers, so a mutation cannot
// interleave with a scan.
func TestStoreMutationExcludedByRelationshipRead(t *testing.T) {
	t.Parallel()

	store, _ := seedRelationshipStore(t, 2)

	store.mu.RLock()
	saved := make(chan error, 1)
	go func() {
		saved <- store.Save(&Feature{ID: "late-child", Name: "late", SchemaVersion: SchemaVersionCurrent})
	}()
	select {
	case err := <-saved:
		store.mu.RUnlock()
		t.Fatalf("Save() completed during a relationship read (err = %v); want writes excluded", err)
	case <-time.After(200 * time.Millisecond):
	}
	store.mu.RUnlock()

	select {
	case err := <-saved:
		if err != nil {
			t.Fatalf("Save() after the read released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Save() never completed after the relationship read released the lock")
	}
}

// TestStoreMutationNotStarvedByRelationshipReads proves sustained concurrent
// reads cannot queue a mutation indefinitely: the write completes promptly
// while readers keep scanning.
func TestStoreMutationNotStarvedByRelationshipReads(t *testing.T) {
	t.Parallel()

	store, parentID := seedRelationshipStore(t, 16)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := store.RelationshipChildren(parentID); err != nil {
					t.Errorf("RelationshipChildren: %v", err)
					return
				}
			}
		}()
	}

	saved := make(chan error, 1)
	go func() {
		saved <- store.Modify(parentID, func(f *Feature) error {
			f.Name = "mutated under load"
			return nil
		})
	}()
	select {
	case err := <-saved:
		if err != nil {
			t.Fatalf("Modify() under concurrent reads: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Modify() starved by concurrent relationship reads")
	}
	close(stop)
	readers.Wait()

	updated, err := store.Load(parentID)
	if err != nil {
		t.Fatalf("Load(parent): %v", err)
	}
	if updated.Name != "mutated under load" {
		t.Fatalf("parent name = %q, want the mutation applied", updated.Name)
	}
}
