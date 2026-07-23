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

package agentdef

import (
	"encoding/json"
	"io/fs"
	"path"
	"reflect"
	"strings"
	"testing"

	agentsFS "github.com/doordash-oss/agentic-orchestrator/agents"
)

func TestParseAgentFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantName  string
		wantDef   AgentDef
		wantError bool
	}{
		{
			name: "full frontmatter",
			content: `---
name: test-agent
description: A test agent
tools: Read, Grep, Glob
model: sonnet
---

You are a test agent.`,
			wantName: "test-agent",
			wantDef: AgentDef{
				Description: "A test agent",
				Prompt:      "You are a test agent.",
				Tools:       []string{"Read", "Grep", "Glob"},
				Model:       "sonnet",
			},
		},
		{
			name: "no tools or model",
			content: `---
name: minimal
description: Minimal agent
---

Do things.`,
			wantName: "minimal",
			wantDef: AgentDef{
				Description: "Minimal agent",
				Prompt:      "Do things.",
			},
		},
		{
			name:      "no frontmatter",
			content:   "Just some text",
			wantError: true,
		},
		{
			name: "missing name",
			content: `---
description: No name
---

Prompt text.`,
			wantError: true,
		},
		{
			name: "extra fields ignored",
			content: `---
name: extra
description: Has extras
color: yellow
model: opus
---

Prompt.`,
			wantName: "extra",
			wantDef: AgentDef{
				Description: "Has extras",
				Prompt:      "Prompt.",
				Model:       "opus",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, def, err := parseAgentFile(tt.content)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if def.Description != tt.wantDef.Description {
				t.Errorf("description = %q, want %q", def.Description, tt.wantDef.Description)
			}
			if def.Prompt != tt.wantDef.Prompt {
				t.Errorf("prompt = %q, want %q", def.Prompt, tt.wantDef.Prompt)
			}
			if def.Model != tt.wantDef.Model {
				t.Errorf("model = %q, want %q", def.Model, tt.wantDef.Model)
			}
			if len(def.Tools) != len(tt.wantDef.Tools) {
				t.Fatalf("tools len = %d, want %d", len(def.Tools), len(tt.wantDef.Tools))
			}
			for i, tool := range def.Tools {
				if tool != tt.wantDef.Tools[i] {
					t.Errorf("tools[%d] = %q, want %q", i, tool, tt.wantDef.Tools[i])
				}
			}
		})
	}
}

func TestParseEmbedded(t *testing.T) {
	defs, err := ParseEmbedded()
	if err != nil {
		t.Fatalf("ParseEmbedded() error: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("ParseEmbedded() returned empty map")
	}
	for name, def := range defs {
		if def.Description == "" {
			t.Errorf("agent %q has empty description", name)
		}
		if def.Prompt == "" {
			t.Errorf("agent %q has empty prompt", name)
		}
	}
}

func TestEmbeddedUpstreamAssets(t *testing.T) {
	upstreamAgents := []string{
		"codebase-analyzer",
		"codebase-locator",
		"codebase-pattern-finder",
		"web-search-researcher",
	}
	assets := []string{"AGENT.md", "ATTRIBUTION.md", "LICENSE.upstream.txt"}

	for _, agentName := range upstreamAgents {
		for _, assetName := range assets {
			assetPath := path.Join(agentName, assetName)
			data, err := fs.ReadFile(agentsFS.FS, assetPath)
			if err != nil {
				t.Errorf("reading embedded agent asset %s: %v", assetPath, err)
				continue
			}
			if len(data) == 0 {
				t.Errorf("embedded agent asset %s is empty", assetPath)
			}
		}
	}
}

func TestJSON(t *testing.T) {
	j := JSON()
	if j == "" {
		t.Fatal("JSON() returned empty string; expected embedded agents")
	}

	var parsed map[string]AgentDef
	if err := json.Unmarshal([]byte(j), &parsed); err != nil {
		t.Fatalf("JSON() returned invalid JSON: %v", err)
	}

	expectedAgents := []string{
		"api-surface-researcher",
		"architecture-researcher",
		"codebase-analyzer",
		"codebase-locator",
		"codebase-pattern-finder",
		"conventions-researcher",
		"dependencies-researcher",
		"verification-researcher",
		"web-search-researcher",
	}

	for _, name := range expectedAgents {
		def, ok := parsed[name]
		if !ok {
			t.Errorf("expected agent %q not found in JSON", name)
			continue
		}
		if def.Description == "" {
			t.Errorf("agent %q has empty description", name)
		}
		if def.Prompt == "" {
			t.Errorf("agent %q has empty prompt", name)
		}
	}
}

func TestSelectEmbedded(t *testing.T) {
	defs, err := ParseEmbedded()
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
			name:      "known subset",
			input:     []string{"codebase-locator", "web-search-researcher"},
			wantNames: []string{"codebase-locator", "web-search-researcher"},
		},
		{
			name:      "empty selection",
			input:     nil,
			wantNames: nil,
		},
		{
			name:        "unknown name",
			input:       []string{"missing-agent"},
			wantErrPart: `unknown embedded agent "missing-agent"`,
		},
		{
			name:        "duplicate name",
			input:       []string{"codebase-locator", "codebase-locator"},
			wantErrPart: `duplicate embedded agent "codebase-locator"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectEmbedded(tt.input)
			if tt.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
					t.Fatalf("SelectEmbedded() error = %v, want substring %q", err, tt.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectEmbedded() error: %v", err)
			}
			if len(got) != len(tt.wantNames) {
				t.Fatalf("SelectEmbedded() returned %d agents, want %d", len(got), len(tt.wantNames))
			}
			for _, name := range tt.wantNames {
				if !reflect.DeepEqual(got[name], defs[name]) {
					t.Fatalf("SelectEmbedded()[%q] = %#v, want %#v", name, got[name], defs[name])
				}
			}
		})
	}
}

func TestJSONForNames(t *testing.T) {
	tests := []struct {
		name          string
		input         []string
		wantNames     []string
		wantErrPart   string
		wantEmptyJSON bool
	}{
		{
			name:      "known subset preserves caller order",
			input:     []string{"web-search-researcher", "codebase-locator"},
			wantNames: []string{"web-search-researcher", "codebase-locator"},
		},
		{
			name:          "empty selection",
			input:         nil,
			wantEmptyJSON: true,
		},
		{
			name:        "unknown name",
			input:       []string{"missing-agent"},
			wantErrPart: `unknown embedded agent "missing-agent"`,
		},
		{
			name:        "duplicate name",
			input:       []string{"codebase-locator", "codebase-locator"},
			wantErrPart: `duplicate embedded agent "codebase-locator"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := JSONForNames(tt.input)
			if tt.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
					t.Fatalf("JSONForNames() error = %v, want substring %q", err, tt.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("JSONForNames() error: %v", err)
			}
			if tt.wantEmptyJSON {
				if got != "" {
					t.Fatalf("JSONForNames() = %q, want empty string", got)
				}
				return
			}
			if got == "" {
				t.Fatal("JSONForNames() returned empty string for non-empty selection")
			}

			var parsed map[string]AgentDef
			if err := json.Unmarshal([]byte(got), &parsed); err != nil {
				t.Fatalf("JSONForNames() returned invalid JSON: %v", err)
			}
			if len(parsed) != len(tt.wantNames) {
				t.Fatalf("JSONForNames() returned %d agents, want %d", len(parsed), len(tt.wantNames))
			}

			prevIndex := -1
			for _, name := range tt.wantNames {
				if _, ok := parsed[name]; !ok {
					t.Fatalf("JSONForNames() missing %q", name)
				}
				idx := strings.Index(got, `"`+name+`"`)
				if idx < 0 {
					t.Fatalf("JSONForNames() payload missing %q in %q", name, got)
				}
				if idx <= prevIndex {
					t.Fatalf("JSONForNames() did not preserve order in %q", got)
				}
				prevIndex = idx
			}
		})
	}
}
