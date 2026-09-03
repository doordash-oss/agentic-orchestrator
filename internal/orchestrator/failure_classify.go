// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// Terminal failure classification. Every terminal path at the
// run-completion boundary routes through one stored failure record built
// here: the catalog code, a phase block (name always; iteration when the
// loop result knows it), a repositories block listing the failed
// repositories, and the raw diagnostics that used to be the run's last-error
// string. No rendered text is persisted; markFailedWithEvent renders once
// for the domain event and read models render again at projection time.

// failureRecord builds a stored failure record for a terminal outcome of
// one phase.
func failureRecord(code errcat.Code, phase feature.Phase, diagnostics string) errcat.FailureRecord {
	return errcat.FailureRecord{
		Code: code,
		Context: &errcat.RecordContext{
			Phase: &errcat.CodePhase{Name: phase.FailureName()},
		},
		Diagnostics: diagnostics,
	}
}

// failureRecordWithRepos attaches a repositories block naming the failed
// repositories of a multi-repo outcome.
func failureRecordWithRepos(code errcat.Code, phase feature.Phase, repos []string, diagnostics string) errcat.FailureRecord {
	record := failureRecord(code, phase, diagnostics)
	if len(repos) == 0 {
		return record
	}
	blocks := make([]errcat.CodeRepository, 0, len(repos))
	for _, name := range repos {
		if name == "" {
			continue
		}
		blocks = append(blocks, errcat.CodeRepository{Name: name})
	}
	if len(blocks) > 0 {
		record.Context.Repositories = blocks
	}
	return record
}

// failureRecordWithIteration records the loop iteration a phase failure
// occurred at, when the caller knows it.
func failureRecordWithIteration(record errcat.FailureRecord, iteration int) errcat.FailureRecord {
	if iteration <= 0 || record.Context == nil || record.Context.Phase == nil {
		return record
	}
	record.Context.Phase.Iteration = iteration
	return record
}

// repoFailureCode classifies a multi-repo failed outcome from the per-repo
// inner-loop statuses: a protocol violation anywhere wins, then an iteration
// cap, then a safety rail, defaulting to infrastructure.
func repoFailureCode(result *agent.OrchestratorResult) errcat.Code {
	code := errcat.InfrastructureFailure
	sawSafetyRail := false
	for _, status := range result.RepoStatuses {
		switch status {
		case "protocol_violation":
			return errcat.ProtocolViolation
		case "max_iterations":
			code = errcat.IterationBudgetExhausted
		case "safety_rail":
			sawSafetyRail = true
		}
	}
	if code == errcat.InfrastructureFailure && sawSafetyRail {
		return errcat.SafetyRailTripped
	}
	return code
}

// currentIteration reads the feature's live iteration counter for a phase
// failure record. Best-effort: zero when the feature cannot be loaded.
func (o *Orchestrator) currentIteration(featureID string) int {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return 0
	}
	return f.CurrentIteration
}

// delegateFailureRecord builds a record for the typed delegate boundaries
// (publish UI failure, missing artifact, protocol violation) whose callers
// do not carry a phase: the feature's current phase names the block. The
// record carries no phase block when the feature cannot be loaded.
func (o *Orchestrator) delegateFailureRecord(featureID string, code errcat.Code, diagnostics string) errcat.FailureRecord {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return errcat.FailureRecord{Code: code, Diagnostics: diagnostics}
	}
	return failureRecord(code, f.CurrentPhase, diagnostics)
}
