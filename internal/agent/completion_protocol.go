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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

const (
	completionReceiptVersion = 1
	// PhaseCompleteFile is the harness-owned receipt written after a semantic
	// root outcome and the role's artifacts have both been validated.
	PhaseCompleteFile = "phase_complete"
)

// CompletionCommitInput is the complete authority needed to validate and
// durably commit one root-agent outcome.
type CompletionCommitInput struct {
	Phase       feature.Phase
	Role        Role
	ArtifactDir string
	SessionID   string
	Intent      llm.CompletionIntent
}

// CompletionReceipt is the harness-owned phase_complete payload. Its presence
// records a completed commit; it never authorizes one.
type CompletionReceipt struct {
	Version     int                        `json:"version"`
	Status      llm.CompletionIntentStatus `json:"status"`
	Phase       string                     `json:"phase"`
	Role        Role                       `json:"role"`
	SessionID   string                     `json:"session_id"`
	CommittedAt time.Time                  `json:"committed_at"`
}

// CommitPhaseOutcome validates the role artifacts and atomically writes the
// canonical phase_complete receipt. Contract violations leave no receipt.
func CommitPhaseOutcome(in CompletionCommitInput) (Outcome, CompletionReceipt, []ProtocolViolation, error) {
	if in.ArtifactDir == "" {
		return Outcome{}, CompletionReceipt{}, nil, fmt.Errorf("committing phase outcome: empty artifact directory")
	}
	if err := RemoveCompletionReceipt(in.ArtifactDir); err != nil {
		return Outcome{}, CompletionReceipt{}, nil, fmt.Errorf("committing phase outcome: clearing prior receipt: %w", err)
	}
	if !in.Intent.Valid() {
		reason := in.Intent.Error
		if reason == "" {
			reason = "root agent did not emit a valid structured outcome"
		}
		return Outcome{}, CompletionReceipt{}, []ProtocolViolation{{
			Artifact: "agentico-outcome",
			Reason:   reason,
		}}, nil
	}

	outcome, violations, err := ValidateArtifactsPreflight(in.Phase, in.Role, in.ArtifactDir)
	if err != nil {
		return outcome, CompletionReceipt{}, nil, fmt.Errorf("committing phase outcome: validating artifacts: %w", err)
	}
	if len(violations) > 0 || !outcome.OK {
		return outcome, CompletionReceipt{}, violations, nil
	}
	if mismatch := completionIntentMismatch(in.Intent, outcome); mismatch != "" {
		return outcome, CompletionReceipt{}, []ProtocolViolation{{
			Artifact: "agentico-outcome",
			Reason:   mismatch,
		}}, nil
	}

	receipt := CompletionReceipt{
		Version:     completionReceiptVersion,
		Status:      in.Intent.Status,
		Phase:       in.Phase.DirName(),
		Role:        in.Role,
		SessionID:   in.SessionID,
		CommittedAt: time.Now().UTC(),
	}
	if err := writeCompletionReceipt(filepath.Join(in.ArtifactDir, PhaseCompleteFile), receipt); err != nil {
		return outcome, CompletionReceipt{}, nil, fmt.Errorf("committing phase outcome: %w", err)
	}
	return outcome, receipt, nil, nil
}

func completionIntentMismatch(intent llm.CompletionIntent, outcome Outcome) string {
	if outcome.Progress == nil {
		if intent.Status == llm.CompletionIntentRetry {
			return "retry is only valid for a role with a structured iteration state"
		}
		return ""
	}
	switch outcome.Progress.State {
	case StateSuccess:
		if intent.Status != llm.CompletionIntentSuccess {
			return fmt.Sprintf("root outcome status %q disagrees with progress.md iteration state %q", intent.Status, outcome.Progress.State)
		}
	case StateRetry:
		if intent.Status != llm.CompletionIntentRetry {
			return fmt.Sprintf("root outcome status %q disagrees with progress.md iteration state %q", intent.Status, outcome.Progress.State)
		}
	default:
		return fmt.Sprintf("progress.md iteration state %q cannot be committed by a root outcome", outcome.Progress.State)
	}
	return ""
}

func writeCompletionReceipt(path string, receipt CompletionReceipt) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".phase-complete-*.tmp")
	if err != nil {
		return fmt.Errorf("creating completion receipt: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("setting completion receipt mode: %w", err)
	}
	if err := json.NewEncoder(tmp).Encode(receipt); err != nil {
		return fmt.Errorf("encoding completion receipt: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing completion receipt: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing completion receipt: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publishing completion receipt: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("opening completion receipt directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("syncing completion receipt directory: %w", err)
	}
	return nil
}

// ReadCompletionReceipt parses and validates the harness-owned completion
// record. A plain or malformed phase_complete file is never accepted.
func ReadCompletionReceipt(dir string) (CompletionReceipt, error) {
	var receipt CompletionReceipt
	data, err := os.ReadFile(filepath.Join(dir, PhaseCompleteFile))
	if err != nil {
		return receipt, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return CompletionReceipt{}, fmt.Errorf("parsing completion receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return CompletionReceipt{}, fmt.Errorf("parsing completion receipt: multiple JSON values")
		}
		return CompletionReceipt{}, fmt.Errorf("parsing completion receipt: %w", err)
	}
	if receipt.Version != completionReceiptVersion {
		return CompletionReceipt{}, fmt.Errorf("unsupported completion receipt version %d", receipt.Version)
	}
	if receipt.Status != llm.CompletionIntentSuccess && receipt.Status != llm.CompletionIntentRetry {
		return CompletionReceipt{}, fmt.Errorf("invalid completion receipt status %q", receipt.Status)
	}
	if receipt.Phase == "" || receipt.Role == "" || receipt.SessionID == "" || receipt.CommittedAt.IsZero() {
		return CompletionReceipt{}, fmt.Errorf("incomplete completion receipt")
	}
	return receipt, nil
}

// HasCommittedPhaseOutcome reports whether a harness receipt still matches
// the expected phase/role and the current artifacts still satisfy that
// contract. A receipt is therefore a validated restart checkpoint, not a
// filesystem flag whose mere presence skips work.
func HasCommittedPhaseOutcome(dir string, phase feature.Phase, role Role) bool {
	receipt, err := ReadCompletionReceipt(dir)
	if err != nil || receipt.Phase != phase.DirName() || receipt.Role != role {
		return false
	}
	outcome, violations, err := ValidateArtifactsPreflight(phase, role, dir)
	if err != nil || len(violations) != 0 || !outcome.OK {
		return false
	}
	intent := llm.CompletionIntent{Found: true, Status: receipt.Status}
	return completionIntentMismatch(intent, outcome) == ""
}

// RemoveCompletionReceipt removes only the harness completion receipt.
// Missing files are not errors.
func RemoveCompletionReceipt(dir string) error {
	err := os.Remove(filepath.Join(dir, PhaseCompleteFile))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("removing completion receipt: %w", err)
}
