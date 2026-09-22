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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
)

func TestTestingContractEndpoint(t *testing.T) {
	store, f := seedReadFeature(t)
	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{StateDir: store.BaseDir},
		Features:              store,
		DisableHostValidation: true,
	})
	get := func(t *testing.T) (*http.Response, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/testing-contract", nil))
		resp := w.Result()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		return resp, body
	}

	resp, _ := get(t)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status without contract = %d, want 404", resp.StatusCode)
	}

	contractPath := agent.PhaseTestingContractPath(store.BaseDir, f, f.CurrentRoadmapPhase)
	contract := agent.CompileTestingContract(strings.Join([]string{
		"### Automated Verification",
		"- [ ] Build: `go build ./...`",
		"### Visual Evidence",
		"- [ ] Builder preview [agentico capability: authenticated-browser(slack.com)] [size: 100x100]",
	}, "\n"), contractPath, "collapsed")
	var visualID string
	for _, item := range contract.Items {
		if item.Source == "visual" {
			visualID = item.ID
		}
	}
	revised, err := agent.ReviseTestingContract(&contract, []agent.TestingContractChange{{ItemID: visualID, Action: agent.TestingContractChangeWaive, ChangeReason: "no session", ChangedBy: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.WriteTestingContract(contractPath, *revised); err != nil {
		t.Fatal(err)
	}

	resp, body := get(t)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["feature_id"] != f.ID || body["roadmap_phase"] != float64(1) || body["revision"] != float64(2) {
		t.Fatalf("envelope = %v", body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	var visual, plan map[string]any
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["source"] == "visual" {
			visual = item
		} else {
			plan = item
		}
	}
	if visual["item_id"] != visualID || visual["owner"] != "agent" || visual["allow_waiver"] != true || visual["allow_substitution"] != false {
		t.Fatalf("visual row = %v", visual)
	}
	disposition, _ := visual["disposition"].(map[string]any)
	if disposition["status"] != "waived" || disposition["changed_by"] != "user" || disposition["reason"] != "no session" {
		t.Fatalf("visual disposition = %v", disposition)
	}
	caps, _ := visual["capabilities"].([]any)
	if len(caps) != 1 || caps[0] != "authenticated-browser(slack.com)" {
		t.Fatalf("visual capabilities = %v", caps)
	}
	if _, has := plan["disposition"]; has {
		t.Fatalf("plan row must omit an empty disposition: %v", plan)
	}
	if plan["owner"] != "harness" || plan["allow_substitution"] != true {
		t.Fatalf("plan row = %v", plan)
	}
}

func TestTestingContractEndpointRevisionChangesAcrossPhases(t *testing.T) {
	store, f := seedReadFeature(t)
	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{StateDir: store.BaseDir},
		Features:              store,
		DisableHostValidation: true,
	})
	plan := "### Automated Verification\n- [ ] Build: `go build ./...`\n"
	etag := func(t *testing.T) string {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/testing-contract", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		return w.Header().Get("ETag")
	}
	for _, phase := range []int{1, 2} {
		path := agent.PhaseTestingContractPath(store.BaseDir, f, phase)
		if err := agent.WriteTestingContract(path, agent.CompileTestingContract(plan, path, "collapsed")); err != nil {
			t.Fatal(err)
		}
	}
	first := etag(t)
	f.CurrentRoadmapPhase = 2
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	second := etag(t)
	if first == "" || first == second {
		t.Fatalf("ETag did not change across phases with identical rows: %q vs %q", first, second)
	}
}
