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

// LedgerAbsent is the sentinel CountPending returns when the progress source
// has no `## Ledger` block at all. It is distinct from 0 (a present ledger
// whose units are all done). The engine treats LedgerAbsent as "no unit
// accounting yet" and never auto-completes on it.
const LedgerAbsent = -1

// ProgressStrategy is the unit-based progress detector for a loop iteration. It
// replaces the prose-fingerprint used by ProgressTracker.CheckWithFingerprint:
// progress is "net pending reduction" rather than "the narrative changed".
//
// Both engines implement it — the blocking-loop engine via LedgerProgressStrategy
// (parsing the agent-maintained `## Ledger` block) and the planning engine via
// axisProgressStrategy (validator-axis approvals) and ledgerProgressStrategy
// (within-attempt continuation ledger).
type ProgressStrategy interface {
	// CountPending parses the persistent progress source identified by path
	// (a handoff file for ledger strategies; ignored by the planning axis
	// strategy, which closes over its plan dir) and returns the count of
	// pending units:
	//
	//	>= 0              a present ledger; the value is the pending-unit count.
	//	LedgerAbsent (-1) no ledger present (fresh / legacy handoff).
	//
	// It returns an error only on I/O failure. A present-but-malformed ledger
	// surfaces as a ParsedHelperHandoff protocol violation (caught before the
	// progress block runs), not as an error here.
	CountPending(path string) (int, error)

	// SummarizeDecisions returns the concatenated decision fields of all done
	// units (the decisions-so-far summary). Returns "" when no done unit carries
	// a decision (inquire/research/kb/planning-axes).
	SummarizeDecisions(path string) (string, error)

	// PendingIDs returns the stable IDs of pending units, nil when the ledger is
	// absent. Consumed by ResumeStrategy.
	PendingIDs(path string) ([]string, error)
}

// ResumeStrategy builds the minimal next-iteration context string — pending unit
// IDs + decisions-so-far + a pointer to the persistent deliverable, never its
// full prose. It is invoked for iterations > 1 and the result is placed on
// BlockingLoopPromptInput.ResumeContext for the phase's BuildPrompt to embed.
type ResumeStrategy interface {
	// Build returns the resume context for iteration. priorHandoffPath is the
	// handoff written by the previous iteration; deliverablePath is the stable
	// persistent deliverable ("" for in-place phases that resume by their own
	// canonical pointer). Planning implementations close over their own dirs and
	// may ignore the arguments.
	Build(iteration int, priorHandoffPath, deliverablePath string) (string, error)
}
