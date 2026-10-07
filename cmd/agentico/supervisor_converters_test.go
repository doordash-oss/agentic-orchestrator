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

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

func TestSupervisorConvertersRegisterEveryHarness(t *testing.T) {
	converters := supervisorConverters(nil)
	openCodeName := opencode.New().Name()
	for _, harness := range []string{"claude", "codex", openCodeName} {
		c, ok := converters[harness]
		if !ok || c.Harness() != harness {
			t.Fatalf("converter for %s = %v", harness, c)
		}
	}
	assigned, ok := converters["codex"].(supervisor.HarnessAssignedIDs)
	if !ok || !assigned.HarnessAssignsSessionID() {
		t.Fatal("the Codex converter must declare that Codex assigns its own thread ids")
	}
	if _, ok := converters["claude"].(supervisor.HarnessAssignedIDs); ok {
		t.Fatal("Claude resumes under a pre-assigned id")
	}
	seeder, ok := converters[openCodeName].(supervisor.HistorySeeder)
	if !ok || !seeder.SeedsHistory() {
		t.Fatal("the OpenCode converter must declare that it seeds history")
	}
	for _, harness := range []string{"claude", "codex"} {
		if _, ok := converters[harness].(supervisor.HistorySeeder); ok {
			t.Fatalf("%s resumes a native session and must not seed history", harness)
		}
	}
}

// TestSupervisorConvertersSizeOpenCodeSeedFromCatalog proves the boot
// wiring reads the OpenCode model's context window from the registry: a
// tiny window drops the oldest turn that the 200K default would keep.
func TestSupervisorConvertersSizeOpenCodeSeedFromCatalog(t *testing.T) {
	p := opencode.New()
	p.SetModelCatalog([]llm.ModelInfo{{ID: "vendor/tiny", ContextWindow: 30, Category: "cheap"}})
	reg := llm.NewRegistry()
	reg.Register(p)

	records := []supervisor.Record{
		userRecord(1, "turn-1", strings.Repeat("a", 60)),
		userRecord(2, "turn-2", "short"),
	}
	rebuild := func(model string) string {
		t.Helper()
		in := supervisor.RebuildInput{ConversationID: "c", ConversationDir: t.TempDir(), Model: model, Records: records}
		res, err := supervisorConverters(reg)[p.Name()].Rebuild(context.Background(), in)
		if err != nil || !res.Resume || res.Path == "" || res.SessionID != "" {
			t.Fatalf("Rebuild = %+v, %v", res, err)
		}
		data, err := os.ReadFile(res.Path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if seed := rebuild("vendor/tiny"); !strings.Contains(seed, "Earlier messages omitted: 1.") || strings.Contains(seed, "aaaa") {
		t.Fatalf("catalog window was not applied:\n%s", seed)
	}
	if seed := rebuild("vendor/unknown"); !strings.Contains(seed, "Earlier messages omitted: 0.") {
		t.Fatalf("unknown model did not use the default window:\n%s", seed)
	}
}

func userRecord(seq int64, turn, text string) supervisor.Record {
	return supervisor.Record{
		Seq: seq, TurnID: turn, Kind: supervisor.KindUser, Visibility: supervisor.VisibilityContent,
		Data: []byte(`{"text":"` + text + `"}`),
	}
}
