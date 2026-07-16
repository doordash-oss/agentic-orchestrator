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
	"slices"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestSandboxGrantRootForDeniedPath(t *testing.T) {
	const home = "/Users/tester"
	tests := []struct {
		name   string
		denied string
		want   string
	}{
		{name: "npm cache", denied: "/Users/tester/.npm/_cacache/tmp/x", want: "/Users/tester/.npm"},
		{name: "electron app support", denied: "/Users/tester/Library/Application Support/Agentico/Cookies", want: "/Users/tester/Library/Application Support/Agentico"},
		{name: "library file too shallow", denied: "/Users/tester/Library/somefile", want: ""},
		{name: "ssh protected", denied: "/Users/tester/.ssh/known_hosts", want: ""},
		{name: "agentic state protected", denied: "/Users/tester/.agentic-workflow/features/x", want: ""},
		{name: "config app", denied: "/Users/tester/.config/Agentico/state.json", want: "/Users/tester/.config/Agentico"},
		{name: "config gh protected", denied: "/Users/tester/.config/gh/hosts.yml", want: ""},
		{name: "bazel tmp", denied: "/private/var/tmp/_bazel_tester/abc/def", want: "/private/var/tmp/_bazel_tester"},
		{name: "plain private tmp", denied: "/private/tmp/agentico-x/file", want: "/private/tmp/agentico-x"},
		{name: "system path", denied: "/etc/hosts", want: ""},
		{name: "relative path", denied: "relative/path", want: ""},
		{name: "non-dot home dir", denied: "/Users/tester/Documents/x", want: ""},
		{name: "netrc protected", denied: "/Users/tester/.netrc", want: ""},
		{name: "grant root is idempotent", denied: "/Users/tester/.npm", want: "/Users/tester/.npm"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sandboxGrantRootForDeniedPath(home, tc.denied)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("sandboxGrantRootForDeniedPath(%q) = (%q, %v), want %q", tc.denied, got, ok, tc.want)
			}
		})
	}
}

func TestSandboxGrantPersistenceRoundtrip(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	valid := filepath.Join(home, ".agentico-test-cache")
	protected := filepath.Join(home, ".ssh")
	for _, grant := range []sandboxGrant{
		{Root: valid, ItemID: "a", DeniedPath: filepath.Join(valid, "x")},
		{Root: valid, ItemID: "b", DeniedPath: filepath.Join(valid, "y")},
		{Root: protected, ItemID: "c", DeniedPath: filepath.Join(protected, "id_rsa")},
	} {
		if err := appendSandboxGrant(contractPath, grant); err != nil {
			t.Fatalf("appendSandboxGrant() error = %v", err)
		}
	}
	roots := loadSandboxGrantRoots(contractPath)
	if !slices.Equal(roots, []string{valid}) {
		t.Fatalf("loadSandboxGrantRoots() = %v, want deduped %q with protected root filtered", roots, valid)
	}
	if roots := loadSandboxGrantRoots(filepath.Join(t.TempDir(), "missing.yaml")); roots != nil {
		t.Fatalf("loadSandboxGrantRoots(missing) = %v, want nil", roots)
	}
	if err := appendSandboxGrant("", sandboxGrant{Root: valid}); err != nil {
		t.Fatalf("appendSandboxGrant with empty contract path should be a no-op, got %v", err)
	}
}

func TestToolchainCacheRoots(t *testing.T) {
	roots := toolchainCacheRoots("/Users/tester")
	for _, want := range []string{"/Users/tester/.npm", "/Users/tester/.gradle", "/Users/tester/.m2/repository"} {
		if !slices.Contains(roots, want) {
			t.Fatalf("toolchainCacheRoots() = %v, want to include %q", roots, want)
		}
	}
	if roots := toolchainCacheRoots(""); roots != nil {
		t.Fatalf("toolchainCacheRoots(\"\") = %v, want nil", roots)
	}
}

func TestExecuteTestingContractBlocksProtectedPathDenial(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	repo := t.TempDir()
	command := `echo "touch ` + filepath.Join(home, ".ssh", "agentico-test") + `: Operation not permitted" >&2; exit 1`
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "protected-denial", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Repo: "repo",
		Name: "protected path", Command: command, Run: &TestingContractRun{Shell: command, Cwd: ".", Timeout: "30s"},
		Policy: TestingContractItemPolicy{Required: true},
	}}}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	report := BuildContractVerificationReportStub(&contract, contractPath)

	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.BlockedItems) != 1 || out.Report.Results[0].Status != VerificationStatusBlocked {
		t.Fatalf("outcome = %+v, want protected-path denial blocked, never auto-granted", out)
	}
	if _, statErr := os.Stat(sandboxGrantsPath(contractPath)); !os.IsNotExist(statErr) {
		t.Fatalf("sandbox grants file exists for a protected path: %v", statErr)
	}
}
