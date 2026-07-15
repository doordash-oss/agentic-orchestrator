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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestExecuteTestingContractRunsOnlyExplicitItems(t *testing.T) {
	repo := t.TempDir()
	iterationDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(iterationDir, "observations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iterationDir, "observations", "manual.md"), []byte("observed behavior"), 0o644); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 1, Revision: 1, Items: []TestingContractItem{
		{ID: "manual", Source: testingContractManualSource, Owner: TestingContractOwnerAgent, Name: "observe", Command: "manual: observe", ExpectedEvidence: TestingContractExpectedEvidence{Kind: testingContractManualKind, Path: "observations/manual.md"}},
		{ID: "explicit", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Name: "explicit", Command: "printf verified", Run: &TestingContractRun{Shell: "printf verified"}, Policy: TestingContractItemPolicy{Required: true}},
	}}
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, iterationDir, repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if got := out.Report.Results[0].Status; got != VerificationStatusPassed {
		t.Fatalf("agent evidence status = %q, want passed", got)
	}
	if got := out.Report.Results[1].Status; got != VerificationStatusPassed {
		t.Fatalf("explicit status = %q, want passed", got)
	}
	if !strings.Contains(out.Report.Results[1].EvidenceData.Summary, "exit 0") {
		t.Fatalf("evidence summary = %q, want exit 0", out.Report.Results[1].EvidenceData.Summary)
	}
}

func TestExecuteTestingContractClassifiesInheritedFailure(t *testing.T) {
	repo, commit := verificationGitRepo(t, "#!/bin/sh\necho known-failure >&2\nexit 7\n")
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := executableContract(commit)
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if got := out.Report.Results[0].Status; got != VerificationStatusInheritedFailure {
		t.Fatalf("status = %q, want inherited_failure (evidence=%+v)", got, out.Report.Results[0].EvidenceData)
	}
	if len(out.InheritedItems) != 1 || len(out.RegressionItems) != 0 {
		t.Fatalf("outcome = %+v, want one inherited and no regressions", out)
	}
}

func TestExecuteTestingContractClassifiesRegressionAgainstPassingBase(t *testing.T) {
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
	if got := out.Report.Results[0].Status; got != VerificationStatusFailed {
		t.Fatalf("status = %q, want failed", got)
	}
	if len(out.RegressionItems) != 1 || out.Report.Results[0].Notes != VerificationClassificationRegression {
		t.Fatalf("outcome = %+v, want regression classification", out)
	}
}

func TestExecuteTestingContractDeclaredCapabilityBlocksWithoutRunningCheck(t *testing.T) {
	repo := t.TempDir()
	marker := filepath.Join(repo, "ran")
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 1, Revision: 1, Items: []TestingContractItem{
		{
			ID: "auth", Source: testingContractPlanSource, Repo: "repo", Name: "protected check", Command: "touch ran",
			Run:          &TestingContractRun{Shell: "touch ran"},
			Capabilities: []TestingContractCapability{{Name: "Okta session", Probe: "exit 1"}},
			Policy:       TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true},
		},
		{
			ID: "behavior", Source: testingContractBehavioralSource, Repo: "repo", Name: "Capture command output from `touch ran`", Command: "behavioral: transcript",
			ExpectedEvidence: TestingContractExpectedEvidence{Kind: testingContractBehavioralKind, Matcher: testingContractCommandTranscriptMatcher},
			Policy:           TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true},
		},
	}}
	finalizeTestingContractOwnership(&contract)
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if got := out.Report.Results[0].Status; got != VerificationStatusBlocked {
		t.Fatalf("status = %q, want blocked", got)
	}
	if len(out.BlockedItems) != 2 || out.Report.Results[1].Status != VerificationStatusBlocked {
		t.Fatalf("BlockedItems/results = %v/%+v, want command and dependent transcript blocked", out.BlockedItems, out.Report.Results)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("protected check ran despite failed capability probe: stat err = %v", err)
	}
}

func TestExecuteTestingContractHonorsUserWaiver(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 1, Revision: 2, Items: []TestingContractItem{{
		ID: "waived", Source: testingContractPlanSource, Repo: "repo", Name: "waived check", Command: "exit 1",
		Run: &TestingContractRun{Shell: "exit 1"}, Policy: TestingContractItemPolicy{Required: true, AllowWaiver: true},
		Disposition: TestingContractItemDisposition{Status: TestingContractDispositionWaived, Reason: "approved exception", ChangedBy: "user"},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")
	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if got := out.Report.Results[0].Status; got != VerificationStatusWaived {
		t.Fatalf("status = %q, want waived", got)
	}
}

func TestExecuteTestingContractSynthesizesBehavioralCommandTranscript(t *testing.T) {
	repo := t.TempDir()
	iterationDir := t.TempDir()
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	command := "printf hello"
	contract := TestingContract{Version: 1, Revision: 1, Items: []TestingContractItem{
		{ID: "run", Source: testingContractPlanSource, Repo: "repo", Name: "run", Command: command, Run: &TestingContractRun{Shell: command}, Policy: TestingContractItemPolicy{Required: true}},
		{ID: "behavior", Source: testingContractBehavioralSource, Repo: "repo", Name: "Capture command output from `printf hello`", Command: "behavioral: transcript", ExpectedEvidence: TestingContractExpectedEvidence{Kind: testingContractBehavioralKind, Matcher: testingContractCommandTranscriptMatcher}, Policy: TestingContractItemPolicy{Required: true}},
	}}
	finalizeTestingContractOwnership(&contract)
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, iterationDir, repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if got := out.Report.Results[1].Status; got != VerificationStatusPassed {
		t.Fatalf("behavior status = %q, want passed", got)
	}
	primary := out.Report.Results[1].EvidenceData.Primary
	data, err := os.ReadFile(filepath.Join(iterationDir, filepath.FromSlash(primary)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "COMMAND: printf hello") || !strings.Contains(string(data), "EXIT CODE: 0") || !strings.Contains(string(data), "hello") {
		t.Fatalf("transcript = %q, want command, exit, and output", data)
	}
	gate := ValidateVerificationReportWithContext(out.Report, nil, true, VerificationReportValidationContext{Contract: &contract, IterationDir: iterationDir})
	if gate.Rejected {
		t.Fatalf("harness transcript rejected: %+v", gate.Findings)
	}
}

func TestRedactVerificationOutput(t *testing.T) {
	got := redactVerificationOutput("Authorization: Bearer abc123\ntoken=secret-value\nordinary output")
	if strings.Contains(got, "abc123") || strings.Contains(got, "secret-value") || !strings.Contains(got, "ordinary output") {
		t.Fatalf("redactVerificationOutput() = %q", got)
	}
}

func executableContract(commit string) TestingContract {
	return TestingContract{Version: 1, Revision: 1, BaseCommits: map[string]string{"repo": commit}, Items: []TestingContractItem{{
		ID: "check", Source: testingContractPlanSource, Repo: "repo", Name: "check", Command: "./check.sh",
		Run: &TestingContractRun{Shell: "./check.sh", Cwd: ".", Timeout: "30s"}, Policy: TestingContractItemPolicy{Required: true},
	}}}
}

func verificationGitRepo(t *testing.T, script string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, repo, "git init -q")
	runVerificationTestCommand(t, runner, repo, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, repo, "git config user.name Test")
	if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runVerificationTestCommand(t, runner, repo, "git add check.sh")
	runVerificationTestCommand(t, runner, repo, "git commit -qm base")
	out, err := runner.Run(context.Background(), "git", []string{"rev-parse", "HEAD"}, ports.CommandOpts{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	return repo, strings.TrimSpace(string(out))
}

func runVerificationTestCommand(t *testing.T, runner ports.CommandRunner, dir, command string) {
	t.Helper()
	if _, err := runner.Run(context.Background(), "/bin/sh", []string{"-lc", command}, ports.CommandOpts{Dir: dir}); err != nil {
		t.Fatalf("%s: %v", command, err)
	}
}
