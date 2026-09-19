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

package selfupdate

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

const testTxID = "0123456789abcdef0123456789abcdef"

func TestSuppressTargetRecordsAndLooksUpExactVersion(t *testing.T) {
	exec := installExecutable(t, "v1")
	first := time.Now().Add(-time.Hour).UTC()
	if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "2.0.0", Digest: "dd", TransactionID: testTxID, SuppressedAt: first}); err != nil {
		t.Fatalf("SuppressTarget: %v", err)
	}
	lookup, err := LookupSuppression(exec.Path, "2.0.0")
	if err != nil || !lookup.Suppressed {
		t.Fatalf("LookupSuppression(2.0.0) = (%v, %v), want suppressed", lookup.Suppressed, err)
	}
	if lookup.Target.Version != "2.0.0" || lookup.Target.Digest != "dd" {
		t.Fatalf("lookup target = %+v", lookup.Target)
	}
	other, err := LookupSuppression(exec.Path, "2.0.1")
	if err != nil || other.Suppressed {
		t.Fatalf("LookupSuppression(2.0.1) = (%v, %v), want not suppressed: only exact versions match", other.Suppressed, err)
	}

	if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "2.0.0", Digest: "ee", TransactionID: testTxID, SuppressedAt: time.Now()}); err != nil {
		t.Fatalf("re-SuppressTarget: %v", err)
	}
	lookup, err = LookupSuppression(exec.Path, "2.0.0")
	if err != nil || !lookup.Suppressed {
		t.Fatalf("LookupSuppression after re-suppress = (%v, %v), want suppressed", lookup.Suppressed, err)
	}
	if !lookup.Target.SuppressedAt.Equal(first) {
		t.Fatalf("re-suppression replaced the earliest decision: %s, want %s", lookup.Target.SuppressedAt, first)
	}

	if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "3.0.0", TransactionID: testTxID, SuppressedAt: time.Now()}); err != nil {
		t.Fatalf("SuppressTarget second version: %v", err)
	}
	versions, err := SuppressedVersions(exec.Path)
	if err != nil {
		t.Fatalf("SuppressedVersions: %v", err)
	}
	if len(versions) != 2 || versions[0].Version != "2.0.0" || versions[1].Version != "3.0.0" {
		t.Fatalf("suppressed versions = %+v, want [2.0.0 3.0.0] oldest first", versions)
	}

	for _, bad := range []SuppressedTarget{
		{Version: "", TransactionID: testTxID},
		{Version: "2.0.0", TransactionID: "short"},
	} {
		if err := SuppressTarget(exec.Path, bad); err == nil {
			t.Fatalf("SuppressTarget(%+v) = nil error, want malformed-target rejection", bad)
		}
	}
}

func TestSuppressionSurvivesRestartAndReceiptOverwrite(t *testing.T) {
	exec := installExecutable(t, "v1")
	at := time.Now().UTC()
	if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "2.0.0", Digest: "dd", TransactionID: testTxID, SuppressedAt: at}); err != nil {
		t.Fatalf("SuppressTarget: %v", err)
	}
	before, err := os.ReadFile(SuppressionPath(exec.Path))
	if err != nil {
		t.Fatalf("read suppression store: %v", err)
	}

	if err := WriteReceiptDurable(ReceiptPath(exec.Path), sampleReceipt(time.Now())); err != nil {
		t.Fatalf("write unrelated receipt: %v", err)
	}
	after, err := os.ReadFile(SuppressionPath(exec.Path))
	if err != nil {
		t.Fatalf("re-read suppression store: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("receipt overwrite touched the suppression store:\n%s\n%s", before, after)
	}
	lookup, err := LookupSuppression(exec.Path, "2.0.0")
	if err != nil || !lookup.Suppressed || !lookup.Target.SuppressedAt.Equal(at) {
		t.Fatalf("LookupSuppression after receipt write = (%v, %v), want the original record", lookup.Suppressed, err)
	}
}

func TestReconcileSuppression(t *testing.T) {
	rolledBack := sampleReceipt(time.Now())
	rolledBack.Outcome = OutcomeRolledBack
	rolledBack.Phase = PhaseRollbackAttempted
	rolledBack.ToVersion = "2.0.0"

	t.Run("rolled back receipt re-asserts", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := ReconcileSuppression(exec.Path, rolledBack); err != nil {
			t.Fatalf("ReconcileSuppression: %v", err)
		}
		lookup, err := LookupSuppression(exec.Path, "2.0.0")
		if err != nil || !lookup.Suppressed {
			t.Fatalf("LookupSuppression after reconcile = (%v, %v), want suppressed", lookup.Suppressed, err)
		}
	})
	t.Run("pending receipt is a no-op", func(t *testing.T) {
		exec := installExecutable(t, "v2")
		if err := ReconcileSuppression(exec.Path, sampleReceipt(time.Now())); err != nil {
			t.Fatalf("ReconcileSuppression: %v", err)
		}
		if lookup, err := LookupSuppression(exec.Path, "2.0.0"); err != nil || lookup.Suppressed {
			t.Fatalf("LookupSuppression after pending reconcile = (%v, %v), want not suppressed", lookup.Suppressed, err)
		}
	})
	t.Run("unsafe store surfaces never silently empty", func(t *testing.T) {
		exec := installExecutable(t, "v3")
		if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "9.9.9", TransactionID: testTxID, SuppressedAt: time.Now()}); err != nil {
			t.Fatalf("SuppressTarget: %v", err)
		}
		if err := os.Chmod(SuppressionPath(exec.Path), 0o644); err != nil {
			t.Fatalf("chmod suppression store: %v", err)
		}
		if err := ReconcileSuppression(exec.Path, rolledBack); err == nil {
			t.Fatal("ReconcileSuppression over an unsafe store = nil error, want failure")
		}
		if _, err := LookupSuppression(exec.Path, "9.9.9"); err == nil {
			t.Fatal("LookupSuppression over an unsafe store = nil error, want failure")
		}
	})
}

func TestReadSuppressionStateValidatesStore(t *testing.T) {
	t.Run("missing store is empty", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		st, err := ReadSuppressionState(exec.Path)
		if err != nil {
			t.Fatalf("ReadSuppressionState: %v", err)
		}
		if st.SchemaVersion != suppressionSchemaVersion || len(st.Targets) != 0 {
			t.Fatalf("missing store state = %+v, want empty schema-1", st)
		}
	})
	t.Run("wrong mode", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "2.0.0", TransactionID: testTxID, SuppressedAt: time.Now()}); err != nil {
			t.Fatalf("SuppressTarget: %v", err)
		}
		if err := os.Chmod(SuppressionPath(exec.Path), 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := ReadSuppressionState(exec.Path); err == nil {
			t.Fatal("ReadSuppressionState on 0644 store = nil error")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := ensureLeaseDir(exec.Path); err != nil {
			t.Fatalf("ensureLeaseDir: %v", err)
		}
		target := writeExecutableFile(t, t.TempDir(), "elsewhere", 0o600, []byte("x"))
		if err := os.Symlink(target, SuppressionPath(exec.Path)); err != nil {
			t.Fatalf("symlink suppression store: %v", err)
		}
		if _, err := ReadSuppressionState(exec.Path); err == nil {
			t.Fatal("ReadSuppressionState on symlinked store = nil error")
		}
	})
	t.Run("bad schema", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := ensureLeaseDir(exec.Path); err != nil {
			t.Fatalf("ensureLeaseDir: %v", err)
		}
		if err := os.WriteFile(SuppressionPath(exec.Path), []byte(`{"schema_version":2,"targets":[]}`), 0o600); err != nil {
			t.Fatalf("write bad-schema store: %v", err)
		}
		if _, err := ReadSuppressionState(exec.Path); err == nil {
			t.Fatal("ReadSuppressionState on schema-2 store = nil error")
		}
	})
	t.Run("corrupt json", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := ensureLeaseDir(exec.Path); err != nil {
			t.Fatalf("ensureLeaseDir: %v", err)
		}
		if err := os.WriteFile(SuppressionPath(exec.Path), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write corrupt store: %v", err)
		}
		if _, err := ReadSuppressionState(exec.Path); err == nil {
			t.Fatal("ReadSuppressionState on corrupt store = nil error")
		}
	})
	t.Run("owned by another uid", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "2.0.0", TransactionID: testTxID, SuppressedAt: time.Now()}); err != nil {
			t.Fatalf("SuppressTarget: %v", err)
		}
		if err := os.Chown(SuppressionPath(exec.Path), os.Geteuid()+1, -1); err != nil {
			t.Skipf("cannot hand the store to another uid as euid %d: %v", os.Geteuid(), err)
		}
		if _, err := ReadSuppressionState(exec.Path); err == nil {
			t.Fatal("ReadSuppressionState on another uid's store = nil error")
		}
	})
}

func TestSuppressionStoreCarriesOnlyStructuralFields(t *testing.T) {
	exec := installExecutable(t, "v1")
	if err := SuppressTarget(exec.Path, SuppressedTarget{Version: "2.0.0", Digest: "dd", TransactionID: testTxID, SuppressedAt: time.Now()}); err != nil {
		t.Fatalf("SuppressTarget: %v", err)
	}
	data, err := os.ReadFile(SuppressionPath(exec.Path))
	if err != nil {
		t.Fatalf("read suppression store: %v", err)
	}
	var generic map[string]interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("unmarshal store: %v", err)
	}
	for _, key := range []string{"schema_version", "targets"} {
		if _, ok := generic[key]; !ok {
			t.Fatalf("store is missing structural key %q: %s", key, data)
		}
	}
	for key := range generic {
		if key != "schema_version" && key != "targets" {
			t.Fatalf("unexpected key %q in suppression store: %s", key, data)
		}
	}
	targets, ok := generic["targets"].([]interface{})
	if !ok || len(targets) != 1 {
		t.Fatalf("store targets = %v", generic["targets"])
	}
	entry, ok := targets[0].(map[string]interface{})
	if !ok {
		t.Fatalf("target entry = %v", targets[0])
	}
	allowed := map[string]bool{"version": true, "digest": true, "transaction_id": true, "suppressed_at": true}
	for key := range entry {
		if !allowed[key] {
			t.Fatalf("unexpected key %q in suppressed target: %v", key, entry)
		}
	}
}
