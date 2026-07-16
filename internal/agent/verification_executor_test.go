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
	"errors"
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

func TestExecuteTestingContractClassifiesRepoPrefixedPathAsContractError(t *testing.T) {
	repo, commit := verificationGitRepo(t, "#!/bin/sh\necho ok\n")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("translated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 2, Revision: 1, BaseCommits: map[string]string{"repo": commit}, Items: []TestingContractItem{{
		ID: "bad-path", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "README is translated", Command: "grep -q translated repo/README.md",
		Run:    &TestingContractRun{Shell: "grep -q translated repo/README.md", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 1 || len(out.InheritedItems) != 0 {
		t.Fatalf("outcome = %+v, want one contract error and no inherited failure", out)
	}
	if got := out.Report.Results[0]; got.Status != VerificationStatusFailed || got.Notes != VerificationClassificationContractError {
		t.Fatalf("result = %+v, want failed contract_error", got)
	}
	if got := out.ContractErrors[0].Suggestion; got != `Replace "repo/README.md" with "README.md".` {
		t.Fatalf("suggestion = %q, want repo-relative correction", got)
	}
}

func TestExecuteTestingContractAllowsExistingNestedDirectoryNamedLikeRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "repo", "README.md"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "nested", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "nested README", Command: "grep -q nested repo/README.md",
		Run:    &TestingContractRun{Shell: "grep -q nested repo/README.md", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 0 || out.Report.Results[0].Status != VerificationStatusPassed {
		t.Fatalf("outcome = %+v, want real nested repo path to pass", out)
	}
}

func TestExecuteTestingContractDoesNotTreatQuotedRepoTextAsPath(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("See repo/README.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := "grep -qF 'See repo/README.md' README.md"
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "quoted", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "quoted path text", Command: command, Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 0 || out.Report.Results[0].Status != VerificationStatusPassed {
		t.Fatalf("outcome = %+v, want quoted repo/path content to pass", out)
	}
}

func TestExecuteTestingContractDoesNotTreatUnquotedRepoTextAsPathWhenCommandPasses(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("repo/README.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := "grep -qF repo/README.md README.md"
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "content", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "path text", Command: command, Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 0 || out.Report.Results[0].Status != VerificationStatusPassed {
		t.Fatalf("outcome = %+v, want successful content assertion to pass", out)
	}
}

func TestExecuteTestingContractClassifiesQuotedRepoPrefixedOperandAsContractError(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("translated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := `grep -q translated "repo/README.md"`
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "quoted-operand", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "quoted operand", Command: command, Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 1 || !strings.Contains(out.ContractErrors[0].Suggestion, "README.md") {
		t.Fatalf("outcome = %+v, want quoted bad operand contract error", out)
	}
}

func TestExecuteTestingContractClassifiesRedundantRepoCDAsContractError(t *testing.T) {
	repo := t.TempDir()
	command := "cd repo && make test"
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "redundant-cd", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "repo tests", Command: command, Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 1 || !strings.Contains(out.ContractErrors[0].Suggestion, "Remove the `cd repo`") {
		t.Fatalf("outcome = %+v, want redundant cd contract error", out)
	}
}

func TestExecuteTestingContractClassifiesInvalidShellAsContractError(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "syntax", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "invalid shell", Command: "if then", Run: &TestingContractRun{Shell: "if then", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 1 || out.Report.Results[0].Notes != VerificationClassificationContractError {
		t.Fatalf("outcome = %+v, want invalid shell contract error", out)
	}
}

func TestExecuteTestingContractClassifiesCommandNotFoundAsContractError(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "missing-command", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "missing command", Command: "agentico-command-that-does-not-exist", Run: &TestingContractRun{Shell: "agentico-command-that-does-not-exist", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 1 || out.Report.Results[0].Notes != VerificationClassificationContractError {
		t.Fatalf("outcome = %+v, want command-not-found contract error", out)
	}
}

func TestExecuteTestingContractClassifiesDeletedCommandAsRegression(t *testing.T) {
	repo, commit := verificationGitRepo(t, "#!/bin/sh\necho ok\n")
	if err := os.Remove(filepath.Join(repo, "check.sh")); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 2, Revision: 1, BaseCommits: map[string]string{"repo": commit}, Items: []TestingContractItem{{
		ID: "deleted-command", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "check script", Command: "./check.sh", Run: &TestingContractRun{Shell: "./check.sh", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 0 || len(out.RegressionItems) != 1 {
		t.Fatalf("outcome = %+v, want deleted candidate command classified as regression", out)
	}
}

func TestExecuteTestingContractDoesNotTreatProgramExit127AsSetupError(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "exit-127", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "program failure", Command: "exit 127", Run: &TestingContractRun{Shell: "exit 127", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 0 || out.Report.Results[0].Notes != VerificationClassificationUnclassified {
		t.Fatalf("outcome = %+v, want deliberate exit 127 to remain an ordinary failure", out)
	}
}

func TestExecuteTestingContractMissingRepoExecutableRemainsImplementationFailure(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "missing-output", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "new CLI exists", Command: "./bin/new-cli --help", Run: &TestingContractRun{Shell: "./bin/new-cli --help", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 0 || len(out.InheritedItems) != 0 || out.Report.Results[0].Notes != VerificationClassificationUnclassified {
		t.Fatalf("outcome = %+v, want missing promised repo executable left for implementation review", out)
	}
}

func TestResolveAndValidateVerificationCwdRejectsInvalidLocations(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	filePath := filepath.Join(root, "file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, symlinkPath); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		relative string
		wantErr  bool
	}{
		{name: "root", relative: "."},
		{name: "absolute", relative: outside, wantErr: true},
		{name: "lexical escape", relative: "../outside", wantErr: true},
		{name: "missing", relative: "missing", wantErr: true},
		{name: "file", relative: "file", wantErr: true},
		{name: "symlink escape", relative: "outside-link", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, err := resolveVerificationCwd(root, tc.relative)
			if err == nil && validateVerificationCwd(root, cwd) != "" {
				err = errors.New(validateVerificationCwd(root, cwd))
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolve/validate cwd = (%q, %v), wantErr=%v", cwd, err, tc.wantErr)
			}
		})
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
	contract := TestingContract{Version: 1, Revision: 1, Items: []TestingContractItem{{
		ID: "auth", Source: testingContractPlanSource, Repo: "repo", Name: "protected check", Command: "touch ran",
		Run:          &TestingContractRun{Shell: "touch ran"},
		Capabilities: []TestingContractCapability{{Name: "Okta session", Probe: "exit 1"}},
		Policy:       TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true},
	}}}
	finalizeTestingContractOwnership(&contract)
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if got := out.Report.Results[0].Status; got != VerificationStatusBlocked {
		t.Fatalf("status = %q, want blocked", got)
	}
	if len(out.BlockedItems) != 1 {
		t.Fatalf("BlockedItems = %v, want protected command blocked", out.BlockedItems)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("protected check ran despite failed capability probe: stat err = %v", err)
	}
}

func TestExecuteTestingContractMalformedCapabilityProbeIsContractError(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "bad-probe", Source: testingContractPlanSource, Repo: "repo", Name: "protected check", Command: "printf ok",
		Run:          &TestingContractRun{Shell: "printf ok"},
		Capabilities: []TestingContractCapability{{Name: "Okta session", Probe: "if then"}},
		Policy:       TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 1 || len(out.BlockedItems) != 0 || out.Report.Results[0].Status != VerificationStatusFailed {
		t.Fatalf("outcome = %+v, want malformed probe failed as contract error without user block", out)
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

func TestFinalizeAgentOwnedEvidence(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		content    *string
		wantStatus VerificationRunStatus
		wantIn     string
	}{
		{name: "no canonical path", path: "", content: nil, wantStatus: VerificationStatusFailed, wantIn: "no canonical path"},
		{name: "missing file", path: "observations/x.md", content: nil, wantStatus: VerificationStatusFailed, wantIn: "missing"},
		{name: "empty file", path: "observations/x.md", content: strPtr("  \n"), wantStatus: VerificationStatusFailed, wantIn: "empty"},
		{name: "present file", path: "observations/x.md", content: strPtr("observed"), wantStatus: VerificationStatusPassed, wantIn: "captured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iterDir := t.TempDir()
			if tt.content != nil {
				full := filepath.Join(iterDir, filepath.FromSlash(tt.path))
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(*tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			item := TestingContractItem{
				ID: "manual", Source: testingContractManualSource, Owner: TestingContractOwnerAgent,
				Name: "observe", Command: "manual: observe",
				ExpectedEvidence: TestingContractExpectedEvidence{Kind: testingContractManualKind, Path: tt.path},
			}
			got := finalizeAgentOwnedEvidence(item, iterDir)
			if got.Status != tt.wantStatus || !strings.Contains(got.Evidence, tt.wantIn) {
				t.Fatalf("finalizeAgentOwnedEvidence() = {Status:%q Evidence:%q}, want status %q containing %q", got.Status, got.Evidence, tt.wantStatus, tt.wantIn)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestExecuteTestingContractIgnoresForgedWaiver(t *testing.T) {
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	command := "touch " + marker
	contract := TestingContract{Version: 2, Revision: 2, Items: []TestingContractItem{{
		ID: "forged", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "forged waiver", Command: command,
		Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"}, Policy: TestingContractItemPolicy{Required: true, AllowWaiver: true},
		Disposition: TestingContractItemDisposition{Status: TestingContractDispositionWaived, Reason: "self-authorized", ChangedBy: "agent"},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if got := out.Report.Results[0].Status; got == VerificationStatusWaived {
		t.Fatalf("status = %q; a non-user waiver must not be honored", got)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("command did not run despite forged waiver: %v", statErr)
	}
}

func TestSameVerificationFailureMutualTimeoutBias(t *testing.T) {
	// Documented heuristic limit: two exit-124 runs with no output compare
	// equal, biasing classification toward inherited over regression.
	a := capturedVerificationRun{record: verificationRunRecord{ExitCode: 124}}
	b := capturedVerificationRun{record: verificationRunRecord{ExitCode: 124}}
	if !sameVerificationFailure(a, b) {
		t.Fatal("sameVerificationFailure() = false, want true for mutual empty-output timeouts")
	}
	c := capturedVerificationRun{record: verificationRunRecord{ExitCode: 1}, stderr: "boom"}
	if sameVerificationFailure(a, c) {
		t.Fatal("sameVerificationFailure() = true, want false for differing exit codes")
	}
}

func TestExecuteTestingContractRecordsContractErrorForUnknownRepo(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{
		{
			ID: "ghost", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "ghost",
			Name: "ghost repo", Command: "true", Run: &TestingContractRun{Shell: "true", Cwd: ".", Timeout: "30s"},
			Policy: TestingContractItemPolicy{Required: true},
		},
		{
			ID: "ok", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
			Name: "still runs", Command: "printf verified", Run: &TestingContractRun{Shell: "printf verified", Cwd: ".", Timeout: "30s"},
			Policy: TestingContractItemPolicy{Required: true},
		},
	}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v, want per-item contract error instead of abort", err)
	}
	if len(out.ContractErrors) != 1 || out.ContractErrors[0].ItemID != "ghost" {
		t.Fatalf("ContractErrors = %+v, want one for item ghost", out.ContractErrors)
	}
	if got := out.Report.Results[1].Status; got != VerificationStatusPassed {
		t.Fatalf("second item status = %q, want passed (execution must continue past the bad item)", got)
	}
}

func TestExecuteTestingContractMatchesRepoTagCaseInsensitively(t *testing.T) {
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "case", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "Repo",
		Name: "case drift", Command: "printf verified", Run: &TestingContractRun{Shell: "printf verified", Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 0 || out.Report.Results[0].Status != VerificationStatusPassed {
		t.Fatalf("outcome = %+v, want case-drifted repo tag to resolve", out)
	}
}

func TestPersistVerificationRunSkipsClaimedRunSlots(t *testing.T) {
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	root := verificationEvidenceRoot(contractPath, "item")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A plain file at run-001 is invisible to the numbering scan but blocks
	// Mkdir — the claim loop must move to the next slot instead of failing.
	if err := os.WriteFile(filepath.Join(root, "run-001"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run := capturedVerificationRun{record: verificationRunRecord{ItemID: "item", Kind: "candidate", Command: "true"}}
	if err := persistVerificationRun(contractPath, "item", &run); err != nil {
		t.Fatalf("persistVerificationRun() error = %v", err)
	}
	if run.record.RunID != "item/run-002" {
		t.Fatalf("RunID = %q, want item/run-002", run.record.RunID)
	}
}

type verificationCallCountingRunner struct {
	inner        ports.CommandRunner
	worktreeAdds int
}

func (r *verificationCallCountingRunner) Run(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
	if name == "git" && len(args) >= 3 && args[2] == "worktree" && args[3] == "add" {
		r.worktreeAdds++
	}
	return r.inner.Run(ctx, name, args, opts)
}

func TestExecuteTestingContractReusesCachedBaselineRun(t *testing.T) {
	repo, commit := verificationGitRepo(t, "#!/bin/sh\necho ok\n")
	if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte("#!/bin/sh\necho new-failure >&2\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := executableContract(commit)
	runner := &verificationCallCountingRunner{inner: NewExecCommandRunner()}

	for round := 1; round <= 2; round++ {
		report := BuildContractVerificationReportStub(&contract, contractPath)
		out, err := ExecuteTestingContract(context.Background(), runner, &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
		if err != nil {
			t.Fatalf("round %d: ExecuteTestingContract() error = %v", round, err)
		}
		if len(out.RegressionItems) != 1 {
			t.Fatalf("round %d: RegressionItems = %v, want one regression", round, out.RegressionItems)
		}
	}
	if runner.worktreeAdds != 1 {
		t.Fatalf("baseline worktree creations = %d, want 1 (second round must reuse the cached baseline)", runner.worktreeAdds)
	}
}

type verificationFakeRunner struct{}

func (verificationFakeRunner) Run(context.Context, string, []string, ports.CommandOpts) ([]byte, error) {
	return nil, nil
}

func TestSandboxVerificationArgvOnlyWrapsHostRunner(t *testing.T) {
	argv := []string{"/bin/sh", "-c", "true"}
	got, sandboxed, cleanup := sandboxVerificationArgv(verificationFakeRunner{}, argv, []string{t.TempDir()})
	defer cleanup()
	if sandboxed || len(got) != len(argv) || got[0] != "/bin/sh" {
		t.Fatalf("sandboxVerificationArgv(fake runner) = (%v, %v), want passthrough", got, sandboxed)
	}
}

func TestExecuteTestingContractBlocksEnvironmentWriteDenial(t *testing.T) {
	repo, commit := verificationGitRepo(t, "#!/bin/sh\necho 'mkdir /agentico-denied/.m2: Operation not permitted' >&2\nexit 1\n")
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := executableContract(commit)
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.BlockedItems) != 1 || len(out.InheritedItems) != 0 || len(out.ContractErrors) != 0 {
		t.Fatalf("outcome = %+v, want write denial blocked instead of inherited", out)
	}
	result := out.Report.Results[0]
	if result.Status != VerificationStatusBlocked || !strings.Contains(result.BlockedReason, "/agentico-denied") {
		t.Fatalf("result = %+v, want blocked with the denied path in the reason", result)
	}
}

func TestExecuteTestingContractKeepsWriteDenialInsideWritableRootsAsFailure(t *testing.T) {
	repo := t.TempDir()
	command := `echo "touch $PWD/scratch: Operation not permitted" >&2; exit 1`
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "in-root-denial", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "denial inside worktree", Command: command, Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.BlockedItems) != 0 || out.Report.Results[0].Notes != VerificationClassificationUnclassified {
		t.Fatalf("outcome = %+v, want denial on a writable path to stay an ordinary failure", out)
	}
}

func TestExecuteTestingContractBlocksMissingEnvManagedTool(t *testing.T) {
	repo := t.TempDir()
	command := "PATH=/var/empty devbox version"
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "env-tool", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "devbox check", Command: command, Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	report := BuildContractVerificationReportStub(&contract, "")

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.BlockedItems) != 1 || len(out.ContractErrors) != 0 {
		t.Fatalf("outcome = %+v, want missing env-managed tool blocked instead of contract error", out)
	}
	result := out.Report.Results[0]
	if result.Status != VerificationStatusBlocked || !strings.Contains(result.BlockedReason, "devbox") {
		t.Fatalf("result = %+v, want blocked naming devbox", result)
	}
}

func TestExecuteTestingContractPassesFlakyCheckOnConfirmationRerun(t *testing.T) {
	repo, commit := verificationGitRepo(t, "#!/bin/sh\necho ok\n")
	flaky := "#!/bin/sh\nif [ -f flaky-marker ]; then exit 0; fi\ntouch flaky-marker\necho transient >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte(flaky), 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := executableContract(commit)
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.RegressionItems) != 0 {
		t.Fatalf("RegressionItems = %v, want flake absorbed by the confirmation re-run", out.RegressionItems)
	}
	result := out.Report.Results[0]
	if result.Status != VerificationStatusPassed || !strings.Contains(result.Notes, "confirmation") {
		t.Fatalf("result = %+v, want passed with a confirmation re-run note", result)
	}
}

func TestVerificationWriteDenialDetection(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "read-only file system", output: "mkdir: cannot create directory '/home/x/.cargo': Read-only file system", want: true},
		{name: "operation not permitted outside roots", output: "open /Users/x/.m2/repository/a.pom: operation not permitted", want: true},
		{name: "permission denied inside roots", output: "touch: " + root + "/file: Permission denied", want: false},
		{name: "marker without path", output: "operation not permitted", want: false},
		{name: "unrelated failure", output: "assertion failed: want /etc/passwd entry", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := capturedVerificationRun{record: verificationRunRecord{ExitCode: 1}, stderr: tc.output}
			if _, _, got := verificationWriteDenial(run, []string{root}); got != tc.want {
				t.Fatalf("verificationWriteDenial(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
	run := capturedVerificationRun{record: verificationRunRecord{ExitCode: 1}, stderr: "open /Users/x/.m2/repository/a.pom: operation not permitted"}
	if _, path, _ := verificationWriteDenial(run, []string{root}); path != "/Users/x/.m2/repository/a.pom" {
		t.Fatalf("verificationWriteDenial denied path = %q, want the refused path", path)
	}
}

func TestEnvManagedToolNotFound(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stderr   string
		wantTool string
	}{
		{name: "bash format", exitCode: 127, stderr: "sh: devbox: command not found", wantTool: "devbox"},
		{name: "dash format", exitCode: 127, stderr: "sh: 1: mise: not found", wantTool: "mise"},
		{name: "zsh format", exitCode: 127, stderr: "zsh: command not found: asdf", wantTool: "asdf"},
		{name: "wrapper script", exitCode: 127, stderr: "./run.sh: line 3: devbox: command not found", wantTool: "devbox"},
		{name: "deliberate exit 127", exitCode: 127, stderr: ""},
		{name: "unlisted tool", exitCode: 127, stderr: "sh: frobnicate: command not found"},
		{name: "ordinary failure mentioning tool", exitCode: 1, stderr: "sh: devbox: command not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := capturedVerificationRun{record: verificationRunRecord{ExitCode: tc.exitCode}, stderr: tc.stderr}
			tool, ok := envManagedToolNotFound(run)
			if ok != (tc.wantTool != "") || tool != tc.wantTool {
				t.Fatalf("envManagedToolNotFound(exit %d, %q) = (%q, %v), want %q", tc.exitCode, tc.stderr, tool, ok, tc.wantTool)
			}
		})
	}
}

func TestVerificationWritableRootsFiltersMissingDirs(t *testing.T) {
	existing := t.TempDir()
	roots := verificationWritableRoots(existing, filepath.Join(existing, "missing"), "")
	found := false
	for _, root := range roots {
		if root == existing {
			found = true
		}
		if strings.HasSuffix(root, "missing") {
			t.Fatalf("verificationWritableRoots() included missing dir: %v", roots)
		}
	}
	if !found {
		t.Fatalf("verificationWritableRoots() = %v, want to include %q", roots, existing)
	}
}
