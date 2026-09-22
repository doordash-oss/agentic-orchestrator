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

package orchestrator_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
)

func TestWaiveTestingContractItemsRevisesCurrentPhaseContract(t *testing.T) {
	stateRoot := t.TempDir()
	f := &feature.Feature{
		ID: "feat-contract-waive", Name: "Waive", Slug: "waive", Status: feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent, CurrentPhase: feature.PhaseImplement,
		CurrentRoadmapPhase: 2, ActiveRun: 1, RunCount: 1,
		Repos: []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	contractPath := agent.PhaseTestingContractPath(stateRoot, f, 2)
	contract := agent.TestingContract{Version: 2, Revision: 1, Items: []agent.TestingContractItem{
		{ID: "visual_1", Source: "visual", Policy: agent.TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}},
		{ID: "plan_1", Source: "plan", Policy: agent.TestingContractItemPolicy{Required: true, AllowWaiver: false}},
	}}
	if err := agent.WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lifecycleForFeature(f), Store: store}, orchestrator.Hooks{})

	if _, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"visual_1"}}); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("missing reason error = %v", err)
	}
	if _, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"plan_1"}, Reason: "r"}); err == nil || !strings.Contains(err.Error(), "does not allow waiver") {
		t.Fatalf("unwaivable item error = %v", err)
	}
	result, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"visual_1", " visual_1 "}, Reason: "Builder needs a signed-in session the VM lacks"})
	if err != nil {
		t.Fatalf("WaiveTestingContractItems() error = %v", err)
	}
	if result.Revision != 2 || len(result.WaivedItems) != 1 || filepath.Clean(result.ContractPath) != filepath.Clean(contractPath) {
		t.Fatalf("result = %+v", result)
	}
	got, err := agent.ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if !agent.IsTestingContractItemWaived(got.Items[0]) || !strings.Contains(got.Items[0].Disposition.Reason, "signed-in session") || agent.IsTestingContractItemWaived(got.Items[1]) {
		t.Fatalf("contract after waiver = %+v", got.Items)
	}
	// Re-applying the same waiver is idempotent: no revision bump.
	again, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"visual_1"}, Reason: "Builder needs a signed-in session the VM lacks"})
	if err != nil || again.Revision != 2 {
		t.Fatalf("second waiver = %+v, %v", again, err)
	}
}

func TestWaiveTestingContractItemsRequiresPhaseContract(t *testing.T) {
	stateRoot := t.TempDir()
	f := &feature.Feature{
		ID: "feat-no-contract", Name: "None", Slug: "none", Status: feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent, CurrentPhase: feature.PhaseImplement, CurrentRoadmapPhase: 1, ActiveRun: 1, RunCount: 1,
		Repos: []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lifecycleForFeature(f), Store: store}, orchestrator.Hooks{})
	if _, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"x"}, Reason: "r"}); err == nil || !strings.Contains(err.Error(), "no testing contract") {
		t.Fatalf("error = %v, want missing-contract error", err)
	}
}

func TestWaiveTestingContractItemsRejectsStaleSelection(t *testing.T) {
	stateRoot := t.TempDir()
	f := &feature.Feature{
		ID: "feat-stale-waive", Name: "Stale", Slug: "stale", Status: feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent, CurrentPhase: feature.PhaseImplement,
		CurrentRoadmapPhase: 2, ActiveRun: 1, RunCount: 1,
		Repos: []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	contractPath := agent.PhaseTestingContractPath(stateRoot, f, 2)
	contract := agent.TestingContract{Version: 2, Revision: 3, Items: []agent.TestingContractItem{
		{ID: "visual_1", Source: "visual", Policy: agent.TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}},
	}}
	if err := agent.WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lifecycleForFeature(f), Store: store}, orchestrator.Hooks{})
	// A selection made against phase 1 must not waive phase 2's identically
	// named row.
	_, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"visual_1"}, Reason: "r", ExpectedPhase: 1, ExpectedRevision: 3})
	if !errors.Is(err, orchestrator.ErrStaleTestingContract) {
		t.Fatalf("stale phase error = %v", err)
	}
	_, err = o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"visual_1"}, Reason: "r", ExpectedPhase: 2, ExpectedRevision: 2})
	if !errors.Is(err, orchestrator.ErrStaleTestingContract) {
		t.Fatalf("stale revision error = %v", err)
	}
	got, err := agent.ReadTestingContract(contractPath)
	if err != nil || agent.IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("stale selection mutated the contract: %+v, %v", got.Items[0], err)
	}
	if _, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"visual_1"}, Reason: "r", ExpectedPhase: 2, ExpectedRevision: 3}); err != nil {
		t.Fatalf("bound waiver error = %v", err)
	}
}

func TestWaiveTestingContractItemsBindsRunAndSerializesWriters(t *testing.T) {
	stateRoot := t.TempDir()
	f := &feature.Feature{
		ID: "feat-run-waive", Name: "Run", Slug: "run", Status: feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent, CurrentPhase: feature.PhaseImplement,
		CurrentRoadmapPhase: 1, ActiveRun: 2, RunCount: 2,
		Repos: []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	contractPath := agent.PhaseTestingContractPath(stateRoot, f, 1)
	items := make([]agent.TestingContractItem, 0, 8)
	for i := 0; i < 8; i++ {
		items = append(items, agent.TestingContractItem{ID: fmt.Sprintf("visual_%d", i), Source: "visual",
			Policy: agent.TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}})
	}
	if err := agent.WriteTestingContract(contractPath, agent.TestingContract{Version: 2, Revision: 1, Items: items}); err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lifecycleForFeature(f), Store: store}, orchestrator.Hooks{})

	// A run-1 selection must not waive run 2's identically numbered phase.
	_, err := o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{"visual_0"}, Reason: "r", ExpectedRun: 1, ExpectedPhase: 1, ExpectedRevision: 1})
	if !errors.Is(err, orchestrator.ErrStaleTestingContract) {
		t.Fatalf("stale run error = %v", err)
	}

	// Eight concurrent unbound waivers, one per item: every acknowledged
	// waiver must be present in the final contract.
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = o.WaiveTestingContractItems(f.ID, orchestrator.TestingContractWaiver{ItemIDs: []string{fmt.Sprintf("visual_%d", i)}, Reason: "concurrent"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("waiver %d error = %v", i, err)
		}
	}
	got, err := agent.ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got.Items {
		if !agent.IsTestingContractItemWaived(item) {
			t.Fatalf("acknowledged waiver lost for %s: %+v", item.ID, got.Changes)
		}
	}
	if got.Revision != 9 {
		t.Fatalf("revision = %d, want 9 after eight serialized waivers", got.Revision)
	}
}
