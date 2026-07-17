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

package agent

import (
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func verificationStatusTestStore(t *testing.T) (*feature.Store, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(filepath.Join(t.TempDir(), "state"))
	f := &feature.Feature{
		ID:            "verif-status-feat",
		Name:          "Verif",
		Slug:          "verif",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return store, f
}

func TestBeginVerificationStatusesSeedsOrderedItems(t *testing.T) {
	t.Parallel()
	store, f := verificationStatusTestStore(t)
	contract := &TestingContract{Items: []TestingContractItem{
		{ID: "t1", Name: "Zebra tests", Owner: TestingContractOwnerHarness, Run: &TestingContractRun{Shell: "go test ./..."}},
		{ID: "t2", Name: "Agent evidence", Owner: TestingContractOwnerAgent},
		{ID: "t3", Name: "Alpha build", Owner: TestingContractOwnerHarness, Run: &TestingContractRun{Shell: "go build ./..."}},
	}}

	beginVerificationStatuses(store, f.ID, contract)

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.CurrentPhaseStatus != "verifying" {
		t.Errorf("CurrentPhaseStatus = %q, want verifying", got.CurrentPhaseStatus)
	}
	want := []feature.VerificationItemStatus{
		{Name: "Zebra tests", State: "pending"},
		{Name: "Alpha build", State: "pending"},
	}
	if len(got.VerificationItems) != len(want) {
		t.Fatalf("VerificationItems = %+v, want %+v", got.VerificationItems, want)
	}
	for i := range want {
		if got.VerificationItems[i] != want[i] {
			t.Errorf("VerificationItems[%d] = %+v, want %+v (contract order must be preserved)", i, got.VerificationItems[i], want[i])
		}
	}
	if len(got.ValidatorStatuses) != 0 {
		t.Errorf("ValidatorStatuses = %v, want empty: harness verification must not masquerade as review validators", got.ValidatorStatuses)
	}
}

func TestUpdateVerificationStatusPreservesOrder(t *testing.T) {
	t.Parallel()
	store, f := verificationStatusTestStore(t)
	contract := &TestingContract{Items: []TestingContractItem{
		{ID: "t1", Name: "Unit tests", Owner: TestingContractOwnerHarness, Run: &TestingContractRun{Shell: "x"}},
		{ID: "t2", Name: "Build", Owner: TestingContractOwnerHarness, Run: &TestingContractRun{Shell: "y"}},
	}}
	beginVerificationStatuses(store, f.ID, contract)

	updateVerificationStatus(store, f.ID, "Build", "running")
	updateVerificationStatus(store, f.ID, "Build", "passed")

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []feature.VerificationItemStatus{
		{Name: "Unit tests", State: "pending"},
		{Name: "Build", State: "passed"},
	}
	for i := range want {
		if got.VerificationItems[i] != want[i] {
			t.Errorf("VerificationItems[%d] = %+v, want %+v", i, got.VerificationItems[i], want[i])
		}
	}
}

func TestVerificationStatusTransitionsNotifyAfterPersistence(t *testing.T) {
	t.Parallel()
	store, f := verificationStatusTestStore(t)
	contract := &TestingContract{Items: []TestingContractItem{
		{ID: "t1", Name: "Unit tests", Owner: TestingContractOwnerHarness, Run: &TestingContractRun{Shell: "x"}},
	}}
	var snapshots [][]feature.VerificationItemStatus
	onProgress := func(featureID string) {
		if featureID != f.ID {
			t.Errorf("notification feature ID = %q, want %q", featureID, f.ID)
		}
		got, err := store.Load(featureID)
		if err != nil {
			t.Errorf("load notified feature: %v", err)
			return
		}
		snapshots = append(snapshots, append([]feature.VerificationItemStatus(nil), got.VerificationItems...))
	}

	beginVerificationStatuses(store, f.ID, contract, onProgress)
	updateVerificationStatus(store, f.ID, "Unit tests", "running", onProgress)
	updateVerificationStatus(store, f.ID, "Unit tests", "passed", onProgress)
	clearVerificationStatuses(store, f.ID, onProgress)

	wantStates := []string{"pending", "running", "passed", ""}
	if len(snapshots) != len(wantStates) {
		t.Fatalf("notifications = %d, want %d", len(snapshots), len(wantStates))
	}
	for i, want := range wantStates {
		if want == "" {
			if len(snapshots[i]) != 0 {
				t.Errorf("snapshot %d = %+v, want cleared", i, snapshots[i])
			}
			continue
		}
		if len(snapshots[i]) != 1 || snapshots[i][0].State != want {
			t.Errorf("snapshot %d = %+v, want state %q", i, snapshots[i], want)
		}
	}
}

func TestClearVerificationStatusesRemovesItems(t *testing.T) {
	t.Parallel()
	store, f := verificationStatusTestStore(t)
	contract := &TestingContract{Items: []TestingContractItem{
		{ID: "t1", Name: "Unit tests", Owner: TestingContractOwnerHarness, Run: &TestingContractRun{Shell: "x"}},
	}}
	beginVerificationStatuses(store, f.ID, contract)

	clearVerificationStatuses(store, f.ID)

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.CurrentPhaseStatus != "" {
		t.Errorf("CurrentPhaseStatus = %q, want empty", got.CurrentPhaseStatus)
	}
	if len(got.VerificationItems) != 0 {
		t.Errorf("VerificationItems = %+v, want empty", got.VerificationItems)
	}
}
