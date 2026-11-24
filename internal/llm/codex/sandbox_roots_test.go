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

package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestCodexProtocol_StartTurnSendsDirectoryWritableRoots(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp-dir assertions with no shared state.
	reviewDir := t.TempDir()
	evidence := filepath.Join(reviewDir, "evidence")
	if err := os.Mkdir(evidence, 0o755); err != nil {
		t.Fatal(err)
	}
	existingFeedback := filepath.Join(reviewDir, "review-feedback.md")
	if err := os.WriteFile(existingFeedback, []byte("# Feedback\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingValidation := filepath.Join(t.TempDir(), "validation.md")
	missingDir := filepath.Join(t.TempDir(), "state")

	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{
		Model:         "gpt-5.4",
		WorkDir:       reviewDir,
		WritableRoots: []string{existingFeedback, evidence, missingValidation, missingDir},
	})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-123")

	if err := p.startTurn("review"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}

	var req struct {
		Params struct {
			SandboxPolicy SandboxPolicy `json:"sandboxPolicy"`
		} `json:"params"`
	}
	if err := json.Unmarshal(buf.Bytes(), &req); err != nil {
		t.Fatalf("decode turn/start: %v; payload=%s", err, buf.String())
	}
	want := []string{reviewDir, evidence, filepath.Dir(missingValidation), missingDir}
	if got := req.Params.SandboxPolicy.WritableRoots; !slices.Equal(got, want) {
		t.Fatalf("writableRoots = %v, want %v", got, want)
	}
}
