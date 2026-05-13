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

package session_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// A2: compile-time proof that *session.RecoveryAdapter satisfies
// ports.RecoveryOperator.
var _ ports.RecoveryOperator = (*session.RecoveryAdapter)(nil)

// A2 (runtime counterpart): construct the adapter and assign it to the
// interface variable. Lets go vet / compile also verify at test time.
func TestRecoveryAdapter_SatisfiesRecoveryOperator(t *testing.T) {
	stateDir := t.TempDir()
	var op ports.RecoveryOperator = session.NewRecoveryAdapter(stateDir, nil)
	// Invoke a method to prove interface dispatch reaches the adapter body.
	items, err := op.ScanForRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanForRecovery on empty state dir: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items from empty state dir, got %d", len(items))
	}
}

// A3: ScanForRecovery delegates to the package-level session.ScanForRecovery.
// Seeds one PID file under the adapter's StateDir and confirms the adapter
// returns exactly that item.
func TestRecoveryAdapter_ScanDelegatesToPackageFunction(t *testing.T) {
	stateDir := t.TempDir()

	// Seed one orphan PID file.
	featDir := filepath.Join(stateDir, "feat-scan", "implement")
	pf := session.PIDFile{PID: 999999999, FeatureID: "feat-scan", Phase: "implement"}
	if err := session.WritePIDFile(featDir, pf); err != nil {
		t.Fatalf("seed PID file: %v", err)
	}

	fm := feature.NewManager(feature.NewStore(stateDir), &config.Config{})
	adapter := session.NewRecoveryAdapter(stateDir, fm)

	items, err := adapter.ScanForRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanForRecovery: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].PIDFile.FeatureID != "feat-scan" {
		t.Errorf("items[0].FeatureID = %q, want %q", items[0].PIDFile.FeatureID, "feat-scan")
	}
	if items[0].PIDFile.PID != 999999999 {
		t.Errorf("items[0].PIDFile.PID = %d, want %d", items[0].PIDFile.PID, 999999999)
	}
}

// A4: ExecuteRecovery delegates to the package-level session.ExecuteRecovery.
// Seeds a stale PID file and issues RecoverySkip for it; package-level
// ExecuteRecovery removes the stale file on skip when the process is dead.
func TestRecoveryAdapter_ExecuteDelegatesToPackageFunction(t *testing.T) {
	stateDir := t.TempDir()

	featDir := filepath.Join(stateDir, "feat-exec", "implement")
	pf := session.PIDFile{PID: 999999999, FeatureID: "feat-exec", Phase: "implement", Dir: featDir}
	if err := session.WritePIDFile(featDir, pf); err != nil {
		t.Fatalf("seed PID file: %v", err)
	}

	fm := feature.NewManager(feature.NewStore(stateDir), &config.Config{})
	adapter := session.NewRecoveryAdapter(stateDir, fm)

	// Discover items first (PIDFile.Dir is only populated via ScanForRecovery).
	items, err := adapter.ScanForRecovery(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	actions := map[string]session.RecoveryAction{
		session.RecoveryActionKey("feat-exec", ""): session.RecoverySkip,
	}
	if err := adapter.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery: %v", err)
	}

	// A second scan should find no pending items — the skip path cleaned up
	// the stale PID file.
	remaining, err := adapter.ScanForRecovery(context.Background())
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 items after skip, got %d", len(remaining))
	}
}
