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

package server

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// maxTestingContractItems bounds the response; contracts are compiled from a
// single phase plan and stay far below this in practice.
const maxTestingContractItems = 500

// handleTestingContract serves the current roadmap phase's compiled testing
// contract so clients can show waivable rows and pick item ids for the
// testing-contract-waive action without reading the state directory.
func (h *apiHandler) handleTestingContract(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	stateDir := strings.TrimSpace(h.runtime.StateDir)
	if f.CurrentRoadmapPhase <= 0 || stateDir == "" {
		writeAPIError(w, http.StatusNotFound, errcat.NotFound,
			errcat.WithParams(errcat.SubjectParams{Subject: "Testing contract", Name: featureID}))
		return
	}
	contractPath := agent.PhaseTestingContractPath(stateDir, f, f.CurrentRoadmapPhase)
	contract, err := agent.ReadTestingContract(contractPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeAPIError(w, http.StatusNotFound, errcat.NotFound,
				errcat.WithParams(errcat.SubjectParams{Subject: "Testing contract", Name: featureID}))
			return
		}
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError,
			errcat.WithDiagnostics(fmt.Sprintf("read testing contract for feature %q: %v", featureID, err)))
		return
	}
	resp := TestingContractResponse{
		APIVersion:   APIVersion,
		FeatureID:    featureID,
		ActiveRun:    f.ActiveRun,
		RoadmapPhase: f.CurrentRoadmapPhase,
		Revision:     contract.Revision,
		Items:        make([]TestingContractItem, 0, min(len(contract.Items), maxTestingContractItems)),
	}
	for _, item := range contract.Items {
		if len(resp.Items) == maxTestingContractItems {
			break
		}
		resp.Items = append(resp.Items, testingContractItemDTO(item))
	}
	revision := revisionForAny(struct {
		Run      int
		Phase    int
		Revision int
		Items    []TestingContractItem
	}{f.ActiveRun, f.CurrentRoadmapPhase, contract.Revision, resp.Items})
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func testingContractItemDTO(item agent.TestingContractItem) TestingContractItem {
	capabilities := make([]string, 0, len(item.Capabilities))
	for _, capability := range item.Capabilities {
		if name := strings.TrimSpace(capability.Name); name != "" {
			capabilities = append(capabilities, agent.BoundNeedUserInputVerificationString(name, agent.NeedUserInputVerificationRepoNameMaxLength))
		}
	}
	dto := TestingContractItem{
		ItemID:            agent.BoundNeedUserInputVerificationString(item.ID, agent.NeedUserInputVerificationItemIDMaxLength),
		Source:            item.Source,
		Owner:             item.Owner,
		Repo:              item.Repo,
		Name:              agent.BoundNeedUserInputVerificationString(item.Name, agent.NeedUserInputVerificationContextTextMaxLength),
		Command:           agent.BoundNeedUserInputVerificationString(item.Command, agent.NeedUserInputVerificationContextTextMaxLength),
		Required:          item.Policy.Required,
		AllowSubstitution: item.Policy.AllowSubstitution,
		AllowBlocked:      item.Policy.AllowBlocked,
		AllowWaiver:       item.Policy.AllowWaiver,
		Capabilities:      capabilities,
	}
	if status := strings.TrimSpace(item.Disposition.Status); status != "" {
		dto.Disposition = &TestingContractDisposition{
			Status:    status,
			Reason:    agent.BoundNeedUserInputVerificationString(item.Disposition.Reason, 2000),
			ChangedBy: item.Disposition.ChangedBy,
		}
	}
	return dto
}
