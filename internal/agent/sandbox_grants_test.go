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
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
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
		{name: "docker buildx grantable", denied: "/Users/tester/.docker/buildx/activity/default", want: "/Users/tester/.docker/buildx"},
		{name: "docker buildx grant root is idempotent", denied: "/Users/tester/.docker/buildx", want: "/Users/tester/.docker/buildx"},
		{name: "docker config protected", denied: "/Users/tester/.docker/config.json", want: ""},
		{name: "docker root protected", denied: "/Users/tester/.docker", want: ""},
		{name: "agentic state protected", denied: "/Users/tester/.agentic-workflow/features/x", want: ""},
		{name: "orchestrator state protected", denied: "/Users/tester/.agentic-orchestrator/features/x", want: ""},
		{name: "orchestrator config protected", denied: "/Users/tester/.agentic-orchestrator/config.yaml", want: ""},
		{name: "orchestrator worktree grantable", denied: "/Users/tester/.agentic-orchestrator/worktrees/feat-1/repo/file.go", want: "/Users/tester/.agentic-orchestrator/worktrees/feat-1"},
		{name: "legacy worktree grantable", denied: "/Users/tester/.agentic-workflow/worktrees/feat-1/repo/file.go", want: "/Users/tester/.agentic-workflow/worktrees/feat-1"},
		{name: "worktrees base too shallow", denied: "/Users/tester/.agentic-orchestrator/worktrees", want: ""},
		{name: "worktree grant root is idempotent", denied: "/Users/tester/.agentic-orchestrator/worktrees/feat-1", want: "/Users/tester/.agentic-orchestrator/worktrees/feat-1"},
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

// TestStateParentProtectionSharedWithGuardrail pins the shared state-parent
// definition so the sandbox grant policy and the permission guardrail cannot
// drift on which directories host the worktrees carve-out.
func TestStateParentProtectionSharedWithGuardrail(t *testing.T) {
	want := []string{".agentic-workflow", ".agentic-orchestrator"}
	if !slices.Equal(permission.StateParentComponents, want) {
		t.Fatalf("permission.StateParentComponents = %v, want %v", permission.StateParentComponents, want)
	}
	for _, parent := range want {
		if got, ok := sandboxGrantRootForDeniedPath("/Users/tester", "/Users/tester/"+parent+"/features/x"); ok {
			t.Fatalf("state parent %s should be protected, got grant root %q", parent, got)
		}
		wantRoot := "/Users/tester/" + parent + "/worktrees/feat-1"
		if got, ok := sandboxGrantRootForDeniedPath("/Users/tester", wantRoot+"/repo/file.go"); !ok || got != wantRoot {
			t.Fatalf("worktrees carve-out for %s = (%q, %v), want %q", parent, got, ok, wantRoot)
		}
	}
}

func TestSandboxGrantPersistenceRoundtrip(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	valid := filepath.Join(home, ".agentico-test-cache")
	buildx := filepath.Join(home, ".docker", "buildx")
	protected := filepath.Join(home, ".ssh")
	for _, grant := range []sandboxGrant{
		{Root: valid, ItemID: "a", DeniedPath: filepath.Join(valid, "x")},
		{Root: valid, ItemID: "b", DeniedPath: filepath.Join(valid, "y")},
		{Root: protected, ItemID: "c", DeniedPath: filepath.Join(protected, "id_rsa")},
		{Root: buildx, ItemID: "d", DeniedPath: filepath.Join(buildx, "activity", "default")},
		{Root: filepath.Join(home, ".docker"), ItemID: "e", DeniedPath: filepath.Join(home, ".docker", "config.json")},
	} {
		if err := appendSandboxGrant(contractPath, grant); err != nil {
			t.Fatalf("appendSandboxGrant() error = %v", err)
		}
	}
	roots := loadSandboxGrantRoots(contractPath)
	if !slices.Equal(roots, []string{valid, buildx}) {
		t.Fatalf("loadSandboxGrantRoots() = %v, want deduped [%q %q] with protected roots filtered", roots, valid, buildx)
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
	for _, want := range []string{"/Users/tester/.npm", "/Users/tester/.gradle", "/Users/tester/.m2/repository", "/Users/tester/.docker/buildx"} {
		if !slices.Contains(roots, want) {
			t.Fatalf("toolchainCacheRoots() = %v, want to include %q", roots, want)
		}
	}
	if roots := toolchainCacheRoots(""); roots != nil {
		t.Fatalf("toolchainCacheRoots(\"\") = %v, want nil", roots)
	}
}

func TestUnsandboxedDispositionRoundtrip(t *testing.T) {
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	for _, d := range []sandboxUnsandboxedDisposition{
		{ItemID: "a", Reason: "sandbox blocks GUI"},
		{ItemID: "a", Reason: "duplicate"},
		{ItemID: "b", Reason: "nested sandbox"},
	} {
		if err := recordUnsandboxedDisposition(contractPath, d); err != nil {
			t.Fatalf("recordUnsandboxedDisposition() error = %v", err)
		}
	}
	got := loadUnsandboxedDispositions(contractPath)
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("loadUnsandboxedDispositions() = %v, want deduped {a, b}", got)
	}
	if got := loadUnsandboxedDispositions(filepath.Join(t.TempDir(), "missing.yaml")); len(got) != 0 {
		t.Fatalf("loadUnsandboxedDispositions(missing) = %v, want empty", got)
	}
	// Dispositions and grants share the sidecar without clobbering each other.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	grantRoot := filepath.Join(home, ".agentico-test-cache")
	if err := appendSandboxGrant(contractPath, sandboxGrant{Root: grantRoot, ItemID: "a"}); err != nil {
		t.Fatal(err)
	}
	if got := loadUnsandboxedDispositions(contractPath); len(got) != 2 {
		t.Fatalf("dispositions lost after grant append: %v", got)
	}
	if roots := loadSandboxGrantRoots(contractPath); len(roots) != 1 {
		t.Fatalf("grants lost after disposition append: %v", roots)
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
