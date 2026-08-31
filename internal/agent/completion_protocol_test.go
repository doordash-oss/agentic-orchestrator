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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestCommitPhaseOutcome_ValidatesArtifactsThenWritesHarnessReceipt(t *testing.T) {
	artifactDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactDir, "index.md"), []byte("# Knowledge Base\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcome, receipt, violations, err := CommitPhaseOutcome(CompletionCommitInput{
		Phase:       feature.PhaseKnowledgeBase,
		Role:        RoleKnowledgeBaseBuilder,
		ArtifactDir: artifactDir,
		SessionID:   "root-session-1",
		Intent: llm.CompletionIntent{
			Found:  true,
			Status: llm.CompletionIntentSuccess,
		},
	})
	if err != nil {
		t.Fatalf("CommitPhaseOutcome() error = %v", err)
	}
	if len(violations) != 0 || !outcome.OK {
		t.Fatalf("CommitPhaseOutcome() outcome=%+v violations=%+v", outcome, violations)
	}
	if receipt.Status != llm.CompletionIntentSuccess || receipt.SessionID != "root-session-1" {
		t.Fatalf("receipt = %+v", receipt)
	}

	data, err := os.ReadFile(filepath.Join(artifactDir, PhaseCompleteFile))
	if err != nil {
		t.Fatalf("read phase_complete receipt: %v", err)
	}
	var persisted CompletionReceipt
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("phase_complete is not a JSON receipt: %v\n%s", err, data)
	}
	if persisted != receipt {
		t.Fatalf("persisted receipt = %+v, want %+v", persisted, receipt)
	}
}

func TestCommitPhaseOutcome_RejectsIntentThatDisagreesWithProgress(t *testing.T) {
	phaseDir := t.TempDir()
	iterDir := filepath.Join(phaseDir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeValidProgress(t, filepath.Join(phaseDir, "progress.md"), "", StateRetry)

	outcome, _, violations, err := CommitPhaseOutcome(CompletionCommitInput{
		Phase:       feature.PhaseImplement,
		Role:        RoleImplementer,
		ArtifactDir: iterDir,
		SessionID:   "root-session-1",
		Intent: llm.CompletionIntent{
			Found:  true,
			Status: llm.CompletionIntentSuccess,
		},
	})
	if err != nil {
		t.Fatalf("CommitPhaseOutcome() error = %v", err)
	}
	if !outcome.OK {
		t.Fatalf("artifact contract should be valid before intent comparison: %+v", outcome)
	}
	if len(violations) != 1 || violations[0].Artifact != "agentico-outcome" {
		t.Fatalf("violations = %+v, want intent/progress mismatch", violations)
	}
	if _, err := ReadCompletionReceipt(iterDir); err == nil {
		t.Fatal("phase_complete written for mismatched root intent and progress state")
	}
}

func TestReadCompletionReceiptRejectsAgentAuthoredPlainFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PhaseCompleteFile), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCompletionReceipt(dir); err == nil {
		t.Fatal("ReadCompletionReceipt() accepted a plain file")
	}
}

func TestHasCommittedPhaseOutcomeRevalidatesContractAndIdentity(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.md")
	if err := os.WriteFile(indexPath, []byte("# Knowledge Base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, violations, err := CommitPhaseOutcome(CompletionCommitInput{
		Phase:       feature.PhaseKnowledgeBase,
		Role:        RoleKnowledgeBaseBuilder,
		ArtifactDir: dir,
		SessionID:   "root-session",
		Intent:      llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess},
	})
	if err != nil || len(violations) != 0 {
		t.Fatalf("CommitPhaseOutcome() err=%v violations=%+v", err, violations)
	}
	if !HasCommittedPhaseOutcome(dir, feature.PhaseKnowledgeBase, RoleKnowledgeBaseBuilder) {
		t.Fatal("valid committed outcome was not reusable")
	}
	if HasCommittedPhaseOutcome(dir, feature.PhaseResearch, RoleResearcher) {
		t.Fatal("receipt was reusable for a different phase and role")
	}
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if HasCommittedPhaseOutcome(dir, feature.PhaseKnowledgeBase, RoleKnowledgeBaseBuilder) {
		t.Fatal("receipt remained reusable after its required artifact was removed")
	}
}

func TestCommitPhaseOutcome_InvalidIntentClearsPriorReceipt(t *testing.T) {
	dir := t.TempDir()
	writeTestCompletionReceipt(t, dir)

	_, _, violations, err := CommitPhaseOutcome(CompletionCommitInput{
		Phase:       feature.PhaseKnowledgeBase,
		Role:        RoleKnowledgeBaseBuilder,
		ArtifactDir: dir,
		SessionID:   "root-session-2",
		Intent:      llm.CompletionIntent{},
	})
	if err != nil {
		t.Fatalf("CommitPhaseOutcome() error = %v", err)
	}
	if len(violations) != 1 || violations[0].Artifact != "agentico-outcome" {
		t.Fatalf("violations = %+v, want missing root outcome", violations)
	}
	if _, err := os.Stat(filepath.Join(dir, PhaseCompleteFile)); !os.IsNotExist(err) {
		t.Fatalf("prior receipt survived rejected commit: %v", err)
	}
}
