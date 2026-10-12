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

package codex

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// ConfiguredEndpoints returns the custom endpoint Codex is configured to
// call: the base_url of the selected `model_provider` in CODEX_HOME's
// config.toml, else OPENAI_BASE_URL for the built-in provider. The default
// OpenAI endpoint is not reported.
func (p *Provider) ConfiguredEndpoints() []string {
	raw := os.Getenv("OPENAI_BASE_URL")
	if home, err := p.resolveCodexHome(); err == nil {
		if data, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil {
			if baseURL, ok := selectedProviderBaseURL(data); ok {
				raw = baseURL
			}
		}
	}
	if endpoint, ok := llm.EndpointURL(raw); ok {
		return []string{endpoint}
	}
	return nil
}

// selectedProviderBaseURL scans config.toml for the top-level model_provider
// and the base_url of its [model_providers.<id>] table. It understands only
// the flat key = "string" lines those settings use.
func selectedProviderBaseURL(data []byte) (string, bool) {
	selected := ""
	baseURLs := map[string]string{}
	table := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			table = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value, err := strconv.Unquote(strings.TrimSpace(stripTOMLComment(value)))
		if err != nil {
			continue
		}
		switch {
		case table == "" && key == "model_provider":
			selected = value
		case key == "base_url" && strings.HasPrefix(table, "model_providers."):
			id := strings.Trim(strings.TrimPrefix(table, "model_providers."), `"'`)
			baseURLs[id] = value
		}
	}
	url, ok := baseURLs[selected]
	return url, ok && selected != ""
}

// stripTOMLComment drops a trailing # comment after a quoted value.
func stripTOMLComment(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, `"`) {
		if end := strings.Index(value[1:], `"`); end >= 0 {
			return value[:end+2]
		}
	}
	return value
}

var _ llm.EndpointReporter = (*Provider)(nil)
