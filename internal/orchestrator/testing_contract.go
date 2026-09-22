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

package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
)

// TestingContractWaiver is a user-authorized waiver for contract items,
// applied outside the harness verification gate.
type TestingContractWaiver struct {
	ItemIDs []string
	Reason  string
	// ExpectedRun, ExpectedPhase, and ExpectedRevision bind the waiver to the
	// contract the user was shown. Zero means unbound; a mismatch is
	// ErrStaleTestingContract. Run matters because phase and revision numbers
	// restart after a rewind.
	ExpectedRun      int
	ExpectedPhase    int
	ExpectedRevision int
}

// testingContractWaiverMu serializes waiver read-revise-write cycles so two
// concurrent submissions cannot both pass the revision check and one silently
// overwrite the other's acknowledged waiver.
var testingContractWaiverMu sync.Mutex

// ErrStaleTestingContract is returned when a waiver names a contract phase or
// revision that is no longer current, so a selection made against one phase
// can never waive identically named rows of the next.
var ErrStaleTestingContract = errors.New("testing contract changed since it was read: reload the contract and select again")

// TestingContractWaiverResult reports the revised contract.
type TestingContractWaiverResult struct {
	ContractPath string
	Revision     int
	WaivedItems  []string
}

// WaiveTestingContractItems records user-authorized waivers on the current
// roadmap phase's testing contract. It is the escape hatch for evidence the
// probes did not anticipate: the harness honours the disposition at the next
// verification pass, and the implement loop re-anchors its contract
// fingerprint on the external amendment.
func (o *Orchestrator) WaiveTestingContractItems(featureID string, waiver TestingContractWaiver) (TestingContractWaiverResult, error) {
	testingContractWaiverMu.Lock()
	defer testingContractWaiverMu.Unlock()
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return TestingContractWaiverResult{}, fmt.Errorf("load feature: %w", err)
	}
	if f.CurrentRoadmapPhase <= 0 {
		return TestingContractWaiverResult{}, errors.New("feature has no active roadmap phase with a testing contract")
	}
	reason := strings.TrimSpace(waiver.Reason)
	if reason == "" {
		return TestingContractWaiverResult{}, errors.New("a waiver reason is required")
	}
	if waiver.ExpectedRun != 0 && waiver.ExpectedRun != f.ActiveRun {
		return TestingContractWaiverResult{}, fmt.Errorf("%w (run %d is active, waiver targeted run %d)", ErrStaleTestingContract, f.ActiveRun, waiver.ExpectedRun)
	}
	if waiver.ExpectedPhase != 0 && waiver.ExpectedPhase != f.CurrentRoadmapPhase {
		return TestingContractWaiverResult{}, fmt.Errorf("%w (phase %d is current, waiver targeted phase %d)", ErrStaleTestingContract, f.CurrentRoadmapPhase, waiver.ExpectedPhase)
	}
	contractPath := agent.PhaseTestingContractPath(o.stateDir(), f, f.CurrentRoadmapPhase)
	contract, err := agent.ReadTestingContract(contractPath)
	if err != nil {
		if os.IsNotExist(err) {
			return TestingContractWaiverResult{}, errors.New("the current phase has no testing contract yet")
		}
		return TestingContractWaiverResult{}, fmt.Errorf("read testing contract: %w", err)
	}
	if waiver.ExpectedRevision != 0 && waiver.ExpectedRevision != contract.Revision {
		return TestingContractWaiverResult{}, fmt.Errorf("%w (revision %d is current, waiver targeted revision %d)", ErrStaleTestingContract, contract.Revision, waiver.ExpectedRevision)
	}
	changes := make([]agent.TestingContractChange, 0, len(waiver.ItemIDs))
	waived := make([]string, 0, len(waiver.ItemIDs))
	seen := make(map[string]bool, len(waiver.ItemIDs))
	for _, raw := range waiver.ItemIDs {
		itemID := strings.TrimSpace(raw)
		if itemID == "" || seen[itemID] {
			continue
		}
		seen[itemID] = true
		changes = append(changes, agent.TestingContractChange{
			ItemID: itemID, Action: agent.TestingContractChangeWaive,
			ChangeReason: "user authorized waiver: " + reason, ChangedBy: "user",
		})
		waived = append(waived, itemID)
	}
	if len(changes) == 0 {
		return TestingContractWaiverResult{}, errors.New("at least one contract item id is required")
	}
	revised, err := agent.ReviseTestingContract(contract, changes)
	if err != nil {
		return TestingContractWaiverResult{}, err
	}
	if err := agent.WriteTestingContract(contractPath, *revised); err != nil {
		return TestingContractWaiverResult{}, fmt.Errorf("write testing contract: %w", err)
	}
	return TestingContractWaiverResult{ContractPath: contractPath, Revision: revised.Revision, WaivedItems: waived}, nil
}
