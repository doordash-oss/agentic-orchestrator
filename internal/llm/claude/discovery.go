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

package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

const catalogRequestID = "agentico-model-catalog"

// DiscoverModelCatalog reads the same initialization catalog exposed by the
// Agent SDK's supportedModels(). No user message or inference request is sent.
func (p *Provider) DiscoverModelCatalog(ctx context.Context) ([]llm.ModelInfo, error) {
	return p.DiscoverModelCatalogWithProgress(ctx, nil)
}

func (p *Provider) DiscoverModelCatalogWithProgress(ctx context.Context, report llm.ModelDiscoveryReporter) ([]llm.ModelInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.cliBinary(), "-p", "--input-format", "stream-json",
		"--output-format", "stream-json", "--verbose", "--safe-mode", "--no-session-persistence")
	// Discovery must not load project customizations or inherit nested-session
	// detection. Authentication and backend configuration remain with the CLI.
	cmd.Env = make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CLAUDECODE=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("Claude catalog stdin: %w", err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("Claude catalog stdout: %w", err)
	}
	defer stdout.Close()
	stopClosing := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopClosing()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Claude catalog initialization: %w", err)
	}
	// The CLI waits for more input after initialization. Stop and reap it on
	// success as well as on malformed output, EOF, and cancellation.
	defer func() { cancel(); _ = cmd.Wait() }()
	request := llm.NewInitializeRequest()
	request.RequestID = catalogRequestID
	request.Request.Hooks = nil
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		return nil, fmt.Errorf("send Claude catalog initialization: %w", err)
	}
	models, err := readClaudeModelCatalog(stdout)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if report != nil {
		for _, model := range models {
			report(model)
		}
	}
	return models, nil
}

type claudeCatalogModel struct {
	Value                 string            `json:"value"`
	ResolvedModel         string            `json:"resolvedModel"`
	DisplayName           string            `json:"displayName"`
	SupportsEffort        bool              `json:"supportsEffort"`
	SupportedEffortLevels []llm.EffortLevel `json:"supportedEffortLevels"`
}

func readClaudeModelCatalog(reader io.Reader) ([]llm.ModelInfo, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var message struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Subtype   string `json:"subtype"`
				Response  struct {
					Models []claudeCatalogModel `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("invalid Claude initialization JSON: %w", err)
		}
		if message.Type != "control_response" || message.Response.RequestID != catalogRequestID {
			continue
		}
		if message.Response.Subtype != "success" {
			// Do not expose arbitrary provider text, which can contain credentials.
			return nil, fmt.Errorf("Claude catalog initialization was rejected")
		}
		return claudeModelsFromInitialization(message.Response.Response.Models)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Claude initialization: %w", err)
	}
	return nil, fmt.Errorf("Claude exited before returning its model catalog")
}

func claudeModelsFromInitialization(raw []claudeCatalogModel) ([]llm.ModelInfo, error) {
	models := make([]llm.ModelInfo, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, model := range raw {
		selector := strings.TrimSpace(model.Value)
		if selector == "" {
			return nil, fmt.Errorf("Claude catalog contains a model without a selector")
		}
		// These are routing policies, not individual models for an Agentico phase.
		if selector == "default" || selector == "opusplan" {
			continue
		}
		key := strings.ToLower(selector)
		if seen[key] {
			return nil, fmt.Errorf("Claude catalog contains duplicate model selectors")
		}
		seen[key] = true
		window := llm.ParseModelContextWindow(selector)
		id := selector
		if window > 0 {
			id = llm.ModelWithContextWindow(llm.StripModelContextWindow(selector), window)
		}
		name := strings.TrimSpace(model.DisplayName)
		if name == "" {
			name = selector
		}
		info := llm.ModelInfo{ID: id, DisplayName: name, ContextWindow: window,
			Category: claudeModelCategory(model.ResolvedModel + " " + selector)}
		// The first alias is always the exact CLI selector, even when only its
		// casing differs from the display ID. BuildCommand must use this value.
		info.Aliases = []string{selector}
		info.Aliases = llm.AppendUniqueAlias(info.Aliases, id, model.ResolvedModel)
		if model.SupportsEffort {
			for _, level := range llm.AllEffortLevels {
				for _, supported := range model.SupportedEffortLevels {
					if level == supported {
						info.EffortCapabilities = append(info.EffortCapabilities, level)
						break
					}
				}
			}
		}
		models = append(models, info)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Claude initialization returned no selectable models")
	}
	// Expose a family alias only when it identifies exactly one advertised
	// entry. Never collapse independently selectable versions of a model.
	for i := range models {
		family := claudeModelFamily(models[i].ID)
		if family == "" {
			continue
		}
		unique := true
		for j := range models {
			if i != j && claudeModelFamily(models[j].ID) == family {
				unique = false
				break
			}
		}
		if unique {
			models[i].Aliases = llm.AppendUniqueAlias(models[i].Aliases, models[i].ID, family)
			if models[i].ContextWindow > 0 {
				models[i].Aliases = llm.AppendUniqueAlias(models[i].Aliases, models[i].ID, llm.ModelWithContextWindow(family, models[i].ContextWindow))
			}
		}
	}
	// A resolved ID can also be an independently advertised, pinned selector.
	// Exact selectors own their names; aliases must not shadow another entry.
	for i := range models {
		aliases := models[i].Aliases[:1]
		for _, alias := range models[i].Aliases[1:] {
			conflict := false
			for j := range models {
				if i != j && (strings.EqualFold(alias, models[j].ID) || strings.EqualFold(alias, models[j].Aliases[0])) {
					conflict = true
					break
				}
			}
			if !conflict {
				aliases = append(aliases, alias)
			}
		}
		models[i].Aliases = aliases
	}
	return models, nil
}

func claudeModelFamily(model string) string {
	model = strings.TrimPrefix(strings.ToLower(llm.StripModelContextWindow(model)), "claude-")
	for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
		if model == family || strings.HasPrefix(model, family+"-") {
			return family
		}
	}
	return ""
}

func claudeModelCategory(model string) string {
	switch model = strings.ToLower(model); {
	case strings.Contains(model, "haiku"):
		return "cheap"
	case strings.Contains(model, "sonnet"):
		return "balanced"
	case strings.Contains(model, "opus"), strings.Contains(model, "fable"):
		return "capable"
	default:
		return ""
	}
}
