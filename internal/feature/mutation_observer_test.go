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
	"sync"
	"testing"
	"time"
)

type mutationCapture struct {
	store           *Store
	mu              sync.Mutex
	events          []Mutation
	callbackLoadErr error
}

func (c *mutationCapture) FeatureMutated(m Mutation) {
	_, c.callbackLoadErr = c.store.Load(func() string {
		if m.After != nil {
			return m.After.ID
		}
		return m.Before.ID
	}())
	c.mu.Lock()
	c.events = append(c.events, m)
	c.mu.Unlock()
}

func TestStoreMutationObserverNotifiesOnceAfterPersistenceAndOutsideLock(t *testing.T) {
	store := NewStore(t.TempDir())
	capture := &mutationCapture{store: store}
	store.SetMutationObserver(capture)
	f := &Feature{ID: "f1", Name: "one", Created: time.Now(), Status: StatusCreated, ActiveRun: 1, RunCount: 1, SchemaVersion: SchemaVersionCurrent}
	f.SetRun(&Run{RunNumber: 1})
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	if capture.callbackLoadErr != nil {
		t.Fatalf("callback could not load persisted feature: %v", capture.callbackLoadErr)
	}
	if len(capture.events) != 1 || capture.events[0].Before != nil || capture.events[0].After.Status != StatusCreated {
		t.Fatalf("create notifications=%+v", capture.events)
	}
	if err := store.Modify("f1", func(*Feature) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 1 {
		t.Fatalf("no-op emitted notification: %d", len(capture.events))
	}
	if err := store.Modify("f1", func(f *Feature) error { f.Status = StatusCodeReady; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 2 || capture.events[1].Before.Status != StatusCreated || capture.events[1].After.Status != StatusCodeReady {
		t.Fatalf("modify notifications=%+v", capture.events)
	}
	if err := store.Delete("f1"); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 3 || capture.events[2].Kind != MutationDeleted {
		t.Fatalf("delete notifications=%+v", capture.events)
	}
}

func TestCreateChildLockedNotifiesForDurableChildCreation(t *testing.T) {
	store := NewStore(t.TempDir())
	parent := &Feature{ID: "parent", Name: "parent", Created: time.Now(), Status: StatusCreated, ActiveRun: 1, RunCount: 1, SchemaVersion: SchemaVersionCurrent}
	parent.SetRun(&Run{RunNumber: 1})
	if err := store.Save(parent); err != nil {
		t.Fatal(err)
	}
	capture := &mutationCapture{store: store}
	store.SetMutationObserver(capture)
	child, err := store.CreateChildLocked(parent.ID, func(parent, activeChild *Feature) (*Feature, *ChildCreationIntent, error) {
		if activeChild != nil {
			t.Fatalf("unexpected active child %s", activeChild.ID)
		}
		child := &Feature{ID: "child", Name: "child", Created: time.Now(), Status: StatusCreated, ActiveRun: 1, RunCount: 1, SchemaVersion: SchemaVersionCurrent,
			Parent: &ChildRelationship{ParentID: parent.ID, Kind: ChildKindRefactor}}
		child.SetRun(&Run{RunNumber: 1})
		return child, &ChildCreationIntent{ChildID: child.ID, Kind: ChildKindRefactor, CreatedAt: child.Created, Child: *child}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.ID != "child" || capture.callbackLoadErr != nil {
		t.Fatalf("child=%+v callback error=%v", child, capture.callbackLoadErr)
	}
	if len(capture.events) != 1 || capture.events[0].Before != nil || capture.events[0].After.ID != child.ID {
		t.Fatalf("notifications=%+v", capture.events)
	}
}
