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
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	agentsFS "github.com/doordash-oss/agentic-orchestrator/agents"
)

// AgentDef represents a single subagent definition.
type AgentDef struct {
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools,omitempty"`
	Model       string   `json:"model,omitempty"`
}

var (
	jsonOnce sync.Once
	jsonVal  string
)

// JSON returns the JSON string for the --agents CLI flag, built from
// all embedded agent markdown files. The result is cached after first call.
func JSON() string {
	jsonOnce.Do(func() {
		defs, err := ParseEmbedded()
		if err != nil {
			return
		}
		data, err := json.Marshal(defs)
		if err != nil {
			return
		}
		jsonVal = string(data)
	})
	return jsonVal
}

// ParseEmbedded reads all agent definition markdown files from the embedded
// agents FS and returns a map of agent name -> definition.
func ParseEmbedded() (map[string]AgentDef, error) {
	defs := make(map[string]AgentDef)

	err := fs.WalkDir(agentsFS.FS, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "AGENT.md" {
			return nil
		}

		data, err := fs.ReadFile(agentsFS.FS, filePath)
		if err != nil {
			return err
		}

		name, def, err := parseAgentFile(string(data))
		if err != nil {
			return nil
		}
		defs[name] = def
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading embedded agents: %w", err)
	}

	return defs, nil
}

// SelectEmbedded returns the selected embedded agent definitions keyed by name.
// Nil or empty input returns an empty map.
func SelectEmbedded(names []string) (map[string]AgentDef, error) {
	if len(names) == 0 {
		return map[string]AgentDef{}, nil
	}

	defs, err := ParseEmbedded()
	if err != nil {
		return nil, err
	}

	selected := make(map[string]AgentDef)
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate embedded agent %q", name)
		}
		seen[name] = struct{}{}

		def, ok := defs[name]
		if !ok {
			return nil, fmt.Errorf("unknown embedded agent %q", name)
		}
		selected[name] = def
	}

	return selected, nil
}

// JSONForNames returns the JSON string for the selected embedded agent
// definitions. Nil or empty input returns an empty string.
func JSONForNames(names []string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}

	selected, err := SelectEmbedded(names)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			buf.WriteByte(',')
		}

		key, err := json.Marshal(name)
		if err != nil {
			return "", err
		}
		value, err := json.Marshal(selected[name])
		if err != nil {
			return "", err
		}

		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.String(), nil
}

// parseAgentFile extracts the agent definition from a markdown file with YAML
// frontmatter. The frontmatter must contain name and description fields.
// The body after the frontmatter becomes the prompt.
func parseAgentFile(content string) (string, AgentDef, error) {
	if !strings.HasPrefix(content, "---") {
		return "", AgentDef{}, fmt.Errorf("missing frontmatter")
	}

	rest := content[3:]
	idx := strings.Index(rest, "---")
	if idx < 0 {
		return "", AgentDef{}, fmt.Errorf("unterminated frontmatter")
	}

	frontmatter := rest[:idx]
	body := strings.TrimSpace(rest[idx+3:])

	fm := parseFrontmatterFields(frontmatter)

	name := fm["name"]
	if name == "" {
		return "", AgentDef{}, fmt.Errorf("missing name in frontmatter")
	}

	def := AgentDef{
		Description: fm["description"],
		Prompt:      body,
		Model:       fm["model"],
	}

	if tools := fm["tools"]; tools != "" {
		for _, t := range strings.Split(tools, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				def.Tools = append(def.Tools, t)
			}
		}
	}

	return name, def, nil
}

// parseFrontmatterFields does a simple key: value parse of YAML-like frontmatter.
// Only handles single-line scalar values (no nested structures).
func parseFrontmatterFields(fm string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		val := strings.TrimSpace(line[colonIdx+1:])
		if key != "" {
			fields[key] = val
		}
	}
	return fields
}
