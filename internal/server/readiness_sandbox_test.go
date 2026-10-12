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

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestMain pins the host sandbox probe to "usable" so readiness tests that
// assert exact issue lists do not depend on whether the suite itself runs
// inside a macOS sandbox.
func TestMain(m *testing.M) {
	verificationSandboxStatus = func() agent.SandboxStatus { return agent.SandboxStatus{} }
	os.Exit(m.Run())
}

// Not parallel: replaces the package-level sandbox probe.
func TestReadinessWarnsOnceWhenVerificationSandboxUnavailable(t *testing.T) {
	original := verificationSandboxStatus
	t.Cleanup(func() { verificationSandboxStatus = original })
	registry := newReadinessRegistry(&readinessProbeProvider{
		MockProvider: &mocks.MockProvider{ProviderName: "plain", CLIDetected: true, Models: []string{"m"}},
		status:       staticReadiness(llm.ProviderReadiness{Ready: true}),
	})
	handler := NewHandler(HandlerOptions{Registry: registry, Mutations: &createFeatureRecorder{}, DisableHostValidation: true})
	sandboxIssues := func(snapshot ReadinessResponse) []Error {
		var out []Error
		for _, issue := range snapshot.Issues {
			if issue.Code == string(errcat.VerificationSandboxUnavailable) {
				out = append(out, issue)
			}
		}
		return out
	}

	verificationSandboxStatus = func() agent.SandboxStatus { return agent.SandboxStatus{} }
	if got := sandboxIssues(getReadinessSnapshot(t, handler)); len(got) != 0 {
		t.Fatalf("issues with a usable sandbox = %+v; want none", got)
	}

	verificationSandboxStatus = func() agent.SandboxStatus {
		return agent.SandboxStatus{Unavailable: true, Reason: "nested sandbox-exec is not permitted"}
	}
	snapshot := getReadinessSnapshot(t, handler)
	got := sandboxIssues(snapshot)
	if len(got) != 1 {
		t.Fatalf("sandbox issues = %+v; want exactly one", got)
	}
	issue := got[0]
	if issue.Class != "warning" || issue.Diagnostics != "nested sandbox-exec is not permitted" ||
		issue.Summary != "Verification sandbox is unavailable on this machine; contract commands and review validators run unconfined." ||
		issue.Remediation == nil || issue.Remediation.Hint == "" {
		t.Fatalf("sandbox issue = %+v; want warning with probe diagnostics and a remediation hint", issue)
	}
	if !snapshot.Ready {
		t.Fatalf("ready = false; the sandbox warning must not block readiness")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/readiness/runtime", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"`+string(errcat.VerificationSandboxUnavailable)+`"`) {
		t.Fatalf("runtime readiness status %d body lacks the sandbox warning: %s", w.Code, w.Body.String())
	}
}

func TestRunDetailExposesSandboxUnavailableRecord(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t)
	if err := store.Modify(f.ID, func(current *feature.Feature) error {
		current.Run().Sandbox = &feature.SandboxRecord{Reason: "nested sandbox-exec is not permitted"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	sandbox, ok := decodeBodyMap(t, w.Result())["run"].(map[string]any)["sandbox"].(map[string]any)
	if !ok || sandbox["available"] != false || sandbox["reason"] != "nested sandbox-exec is not permitted" {
		t.Fatalf("run.sandbox = %v; want {available:false, reason}", sandbox)
	}
}

func TestRunDetailOmitsSandboxWhenNotRecorded(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/1", nil))
	if _, present := decodeBodyMap(t, w.Result())["run"].(map[string]any)["sandbox"]; present {
		t.Fatalf("run.sandbox present without a record: %s", w.Body.String())
	}
}
