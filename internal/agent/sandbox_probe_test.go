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
)

// stubSandboxUnavailable makes the cached probe report an unusable sandbox
// for the duration of the test.
func stubSandboxUnavailable(t *testing.T, reason string) {
	t.Helper()
	setSandboxProbeForTest(func() SandboxStatus { return SandboxStatus{Unavailable: true, Reason: reason} })
	t.Cleanup(func() { setSandboxProbeForTest(nil) })
}

func TestSandboxAvailabilityProbesOncePerProcess(t *testing.T) {
	calls := 0
	setSandboxProbeForTest(func() SandboxStatus { calls++; return SandboxStatus{Unavailable: true, Reason: "x"} })
	t.Cleanup(func() { setSandboxProbeForTest(nil) })
	for range 3 {
		if status := SandboxAvailability(); !status.Unavailable || status.Reason != "x" {
			t.Fatalf("SandboxAvailability() = %+v; want stubbed unavailable", status)
		}
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d; want 1", calls)
	}
}

func TestExecuteTestingContractRunsUnsandboxedOnceWhenSandboxUnavailable(t *testing.T) {
	const reason = "nested sandbox-exec is not permitted"
	stubSandboxUnavailable(t, reason)
	for name, script := range map[string]string{
		"pass": "#!/bin/sh\necho ok\n",
		"fail": "#!/bin/sh\necho broken >&2\nexit 3\n",
	} {
		t.Run(name, func(t *testing.T) {
			repo, commit := verificationGitRepo(t, "#!/bin/sh\necho ok\n")
			if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
			contract := executableContract(commit)
			report := buildContractVerificationReportStub(&contract, contractPath)
			if _, err := executeTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, contractPath, "", repo,
				[]feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}}); err != nil {
				t.Fatalf("executeTestingContract() error = %v", err)
			}
			if name == "pass" {
				if got := report.Results[0]; got.Status != verificationStatusPassed || !strings.Contains(got.Notes, "verification sandbox unavailable ("+reason+")") {
					t.Fatalf("result = %+v; want pass noting the unavailable sandbox", got)
				}
			}
			entries, err := os.ReadDir(verificationEvidenceRoot(contractPath, "check"))
			if err != nil {
				t.Fatal(err)
			}
			var candidates int
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(verificationEvidenceRoot(contractPath, "check"), entry.Name(), "run.yaml"))
				if err != nil {
					continue
				}
				text := string(data)
				if strings.Contains(text, "sandboxed: true") || strings.Contains(text, "kind: unsandboxed") {
					t.Fatalf("%s ran the sandbox ladder:\n%s", entry.Name(), text)
				}
				if strings.Contains(text, "kind: candidate") {
					candidates++
					if !strings.Contains(text, "sandbox_unavailable: "+reason) {
						t.Fatalf("candidate run.yaml missing sandbox_unavailable note:\n%s", text)
					}
				}
			}
			if candidates != 1 {
				t.Fatalf("candidate runs = %d; want 1", candidates)
			}
			if items := loadUnsandboxedDispositions(contractPath); len(items) != 0 {
				t.Fatalf("unsandboxed dispositions = %v; want none (host condition, not the command's)", items)
			}
		})
	}
}

func TestRecordSandboxUnavailableKeepsFirstRunRecord(t *testing.T) {
	store, id := newTestStoreWithFeature(t, nil, nil)
	recordSandboxUnavailable(store, id, "first")
	recordSandboxUnavailable(store, id, "second")
	f, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Run().Sandbox; got == nil || got.Available || got.Reason != "first" {
		t.Fatalf("run sandbox = %+v; want {available:false reason:first}", got)
	}
}
