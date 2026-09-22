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
	"path/filepath"
	"strings"
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
