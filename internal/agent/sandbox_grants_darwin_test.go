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

func countEvidenceRuns(t *testing.T, contractPath, itemID string) int {
	t.Helper()
	entries, err := os.ReadDir(verificationEvidenceRoot(contractPath, itemID))
	if err != nil {
		return 0
	}
	runs := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "run-") {
			runs++
		}
	}
	return runs
}

// TestExecuteTestingContractEscalatesSandboxLimitedCommand proves the
// environment-differential ladder on the real seatbelt: a nested
// sandbox-exec (the same mechanism that breaks Chromium's sandbox init)
// fails under the harness sandbox, passes unsandboxed, fails the sandboxed
// confirm — so the item passes with a persisted unsandboxed disposition
// instead of surfacing as an unclassified failure.
func TestExecuteTestingContractEscalatesSandboxLimitedCommand(t *testing.T) {
	if !sandboxExecAvailable() {
		t.Skip("requires sandbox-exec")
	}
	repo := t.TempDir()
	command := `/usr/bin/sandbox-exec -p '(version 1) (allow default)' /usr/bin/true`
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "nested-sandbox", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "needs its own sandbox", Command: command,
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
		t.Fatalf("result = %+v (blocked=%v), want ladder-proven pass", result, out.BlockedItems)
	}
	if !strings.Contains(result.Notes, "unsandboxed") {
		t.Fatalf("notes = %q, want unsandboxed execution disclosed", result.Notes)
	}
	if got := loadUnsandboxedDispositions(contractPath); !got["nested-sandbox"] {
		t.Fatalf("dispositions = %v, want nested-sandbox recorded", got)
	}
	if runs := countEvidenceRuns(t, contractPath, "nested-sandbox"); runs != 3 {
		t.Fatalf("persisted runs = %d, want 3 (candidate, unsandboxed, confirm)", runs)
	}

	// Second execution honors the disposition: one direct unsandboxed run.
	report2 := BuildContractVerificationReportStub(&contract, contractPath)
	out2, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report2, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("second ExecuteTestingContract() error = %v", err)
	}
	if got := out2.Report.Results[0]; got.Status != VerificationStatusPassed || !strings.Contains(got.Notes, "disposition") {
		t.Fatalf("second run result = %+v, want direct unsandboxed pass noting the disposition", got)
	}
	if runs := countEvidenceRuns(t, contractPath, "nested-sandbox"); runs != 4 {
		t.Fatalf("persisted runs after second execution = %d, want 4 (single direct run added)", runs)
	}
}

// TestExecuteTestingContractLadderAbsorbsFlakeWithoutBaseline: a flake that
// fails once and passes on the ladder's own re-runs resolves as passed with
// no baseline needed and no lasting unsandboxed disposition.
func TestExecuteTestingContractLadderAbsorbsFlakeWithoutBaseline(t *testing.T) {
	if !sandboxExecAvailable() {
		t.Skip("requires sandbox-exec")
	}
	repo := t.TempDir()
	command := "if [ -f flaky-marker ]; then exit 0; fi; touch flaky-marker; echo transient >&2; exit 1"
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "ladder-flake", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "flaky check", Command: command,
		Run:    &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}

	report := BuildContractVerificationReportStub(&contract, contractPath)
	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	result := out.Report.Results[0]
	if result.Status != VerificationStatusPassed || !strings.Contains(result.Notes, "did not reproduce") {
		t.Fatalf("result = %+v, want flake absorbed by the ladder", result)
	}
	if got := loadUnsandboxedDispositions(contractPath); len(got) != 0 {
		t.Fatalf("dispositions = %v, want none for a flake", got)
	}
}

// TestExecuteTestingContractLadderFailureDoublesAsRegressionConfirmation: a
// deterministic failure pays candidate + unsandboxed + baseline, with the
// ladder's unsandboxed failure standing in for the regression confirmation.
func TestExecuteTestingContractLadderFailureDoublesAsRegressionConfirmation(t *testing.T) {
	if !sandboxExecAvailable() {
		t.Skip("requires sandbox-exec")
	}
	repo, commit := verificationGitRepo(t, "#!/bin/sh\necho ok\n")
	if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte("#!/bin/sh\necho new-failure >&2\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := executableContract(commit)
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.RegressionItems) != 1 {
		t.Fatalf("RegressionItems = %v, want one regression", out.RegressionItems)
	}
	if runs := countEvidenceRuns(t, contractPath, "check"); runs != 3 {
		t.Fatalf("persisted runs = %d, want 3 (candidate, unsandboxed, baseline; no extra confirmation)", runs)
	}
}

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
