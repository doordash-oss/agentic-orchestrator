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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func writeTestCompletionReceipt(t testing.TB, dir string) {
	writeTestCompletionReceiptFor(t, dir, feature.PhaseImplement, RoleImplementer)
}

func writeTestCompletionReceiptFor(t testing.TB, dir string, phase feature.Phase, role Role) {
	t.Helper()
	receipt := CompletionReceipt{
		Version:     completionReceiptVersion,
		Status:      llm.CompletionIntentSuccess,
		Phase:       phase.DirName(),
		Role:        role,
		SessionID:   "test-root-session",
		CommittedAt: time.Now().UTC(),
	}
	if err := writeCompletionReceipt(filepath.Join(dir, PhaseCompleteFile), receipt); err != nil {
		t.Fatalf("write test completion receipt: %v", err)
	}
}
