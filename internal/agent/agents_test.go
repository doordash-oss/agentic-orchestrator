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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agentdef"
)

func TestAgentsJSON(t *testing.T) {
	aj := AgentsJSON()
	if aj == "" {
		t.Fatal("AgentsJSON() returned empty string; expected embedded agents")
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(aj), &parsed); err != nil {
		t.Fatalf("AgentsJSON() returned invalid JSON: %v", err)
	}
	if len(parsed) == 0 {
		t.Error("AgentsJSON() returned empty JSON object")
	}
}

func TestAgentsJSONForNames(t *testing.T) {
	defs, err := agentdef.ParseEmbedded()
	if err != nil {
		t.Fatalf("ParseEmbedded() error: %v", err)
	}

	tests := []struct {
		name        string
		input       []string
		wantNames   []string
		wantErrPart string
	}{
		{
			name:      "known subset preserves caller order",
			input:     []string{"web-search-researcher", "codebase-locator"},
			wantNames: []string{"web-search-researcher", "codebase-locator"},
		},
		{
			name:      "empty selection returns empty string",
			input:     nil,
			wantNames: nil,
		},
		{
			name:      "explicit empty selection returns empty string",
			input:     []string{},
			wantNames: nil,
		},
		{
			name:        "unknown names fail",
			input:       []string{"missing-agent"},
			wantErrPart: `unknown embedded agent "missing-agent"`,
		},
		{
			name:        "duplicate names fail",
			input:       []string{"codebase-locator", "codebase-locator"},
			wantErrPart: `duplicate embedded agent "codebase-locator"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AgentsJSONForNames(tt.input)
			if tt.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
					t.Fatalf("AgentsJSONForNames() error = %v, want substring %q", err, tt.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("AgentsJSONForNames() error: %v", err)
			}
			if len(tt.wantNames) == 0 {
				if got != "" {
					t.Fatalf("AgentsJSONForNames() = %q, want empty string", got)
				}
				return
			}

			var parsed map[string]agentdef.AgentDef
			if err := json.Unmarshal([]byte(got), &parsed); err != nil {
				t.Fatalf("AgentsJSONForNames() returned invalid JSON: %v", err)
			}
			if len(parsed) != len(tt.wantNames) {
				t.Fatalf("AgentsJSONForNames() returned %d agents, want %d", len(parsed), len(tt.wantNames))
			}

			prevIndex := -1
			for _, name := range tt.wantNames {
				gotDef, ok := parsed[name]
				if !ok {
					t.Fatalf("AgentsJSONForNames() missing %q", name)
				}
				if !reflect.DeepEqual(gotDef, defs[name]) {
					t.Fatalf("AgentsJSONForNames()[%q] = %#v, want %#v", name, gotDef, defs[name])
				}
				idx := strings.Index(got, `"`+name+`"`)
				if idx < 0 {
					t.Fatalf("AgentsJSONForNames() payload missing %q in %q", name, got)
				}
				if idx <= prevIndex {
					t.Fatalf("AgentsJSONForNames() did not preserve order in %q", got)
				}
				prevIndex = idx
			}
		})
	}
}
