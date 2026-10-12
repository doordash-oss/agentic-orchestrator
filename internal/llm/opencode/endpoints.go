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

package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// endpointConfig is the slice of an OpenCode config file that names custom
// provider endpoints (`provider.<id>.options.baseURL`).
type endpointConfig struct {
	Provider map[string]struct {
		Options struct {
			BaseURL string `json:"baseURL"`
		} `json:"options"`
	} `json:"provider"`
}

// ConfiguredEndpoints returns the custom provider base URLs declared in the
// user's global OpenCode config (and the OPENCODE_CONFIG file when set),
// deduplicated and sorted. Built-in providers without a baseURL are not
// reported. Unreadable or unparsable files contribute nothing.
func (p *Provider) ConfiguredEndpoints() []string {
	seen := map[string]bool{}
	for _, path := range userConfigPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg endpointConfig
		if json.Unmarshal(stripJSONComments(data), &cfg) != nil {
			continue
		}
		for _, entry := range cfg.Provider {
			if endpoint, ok := llm.EndpointURL(entry.Options.BaseURL); ok {
				seen[endpoint] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for endpoint := range seen {
		out = append(out, endpoint)
	}
	sort.Strings(out)
	return out
}

// userConfigPaths lists the OpenCode config files a launched session merges
// from outside Agentico's managed config: the global config directory
// (XDG_CONFIG_HOME/opencode, else ~/.config/opencode) and OPENCODE_CONFIG.
func userConfigPaths() []string {
	var paths []string
	dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config")
		}
	}
	if dir != "" {
		for _, name := range []string{"config.json", "opencode.json", "opencode.jsonc"} {
			paths = append(paths, filepath.Join(dir, "opencode", name))
		}
	}
	if file := strings.TrimSpace(os.Getenv(configFileEnvVar)); file != "" {
		paths = append(paths, file)
	}
	return paths
}

// stripJSONComments removes // and /* */ comments outside string literals so
// JSONC config files parse with encoding/json.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '/' && i+1 < len(data) && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
			continue
		}
		if c == '/' && i+1 < len(data) && data[i+1] == '*' {
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i++
			continue
		}
		if c == '"' {
			inString = true
		}
		out = append(out, c)
	}
	return out
}

var _ llm.EndpointReporter = (*Provider)(nil)
