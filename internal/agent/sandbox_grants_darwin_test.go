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

//go:build darwin

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestExecuteTestingContractAutoGrantsDeniedTempPath drives the real
// sandbox: the command writes outside every writable root, the first run is
// denied, the executor grants the minimal root, retries, and persists the
// grant so a second execution passes without another denial round.
func TestExecuteTestingContractAutoGrantsDeniedTempPath(t *testing.T) {
	if !sandboxExecAvailable() {
		t.Skip("requires sandbox-exec")
	}
	repo := t.TempDir()
	target := filepath.Join("/private/tmp", fmt.Sprintf("agentico-grant-test-%d", os.Getpid()))
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	command := fmt.Sprintf("mkdir -p %q && printf ok > %q/evidence", target, target)
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "grant-retry", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "writes outside sandbox roots", Command: command,
		Run:    &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}

	report := BuildContractVerificationReportStub(&contract, contractPath)
	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	result := out.Report.Results[0]
	if result.Status != VerificationStatusPassed || len(out.BlockedItems) != 0 {
		t.Fatalf("result = %+v (blocked=%v), want auto-granted pass", result, out.BlockedItems)
	}
	if !strings.Contains(result.Notes, target) {
		t.Fatalf("notes = %q, want granted root %q recorded", result.Notes, target)
	}
	roots := loadSandboxGrantRoots(contractPath)
	if len(roots) != 1 || roots[0] != target {
		t.Fatalf("persisted grant roots = %v, want [%q]", roots, target)
	}

	// Second execution loads the persisted grant: no new denial run.
	evidenceRoot := verificationEvidenceRoot(contractPath, "grant-retry")
	before, _ := os.ReadDir(evidenceRoot)
	report2 := BuildContractVerificationReportStub(&contract, contractPath)
	out2, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report2, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("second ExecuteTestingContract() error = %v", err)
	}
	if got := out2.Report.Results[0]; got.Status != VerificationStatusPassed || got.Notes != "" {
		t.Fatalf("second run result = %+v, want clean pass with no grant note", got)
	}
	after, _ := os.ReadDir(evidenceRoot)
	if len(after) != len(before)+1 {
		t.Fatalf("second run persisted %d new runs, want exactly 1 (no denial retry)", len(after)-len(before))
	}
}
