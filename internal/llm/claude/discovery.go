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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

const (
	claudeModelProbePrompt       = "Return exactly: OK"
	claudeModelProbeMaxBudgetUSD = "0.05"
)

// DiscoverModelCatalog resolves Agentic's curated Claude selectors against the
// live CLI by probing each one with a tiny `claude --model <selector> -p` request
// and reading the resolved model + context window back from the stream-json
// output.
//
// Probing is the only mechanism used because Claude Code exposes no
// machine-readable model catalog command, and probing is the only
// provider-agnostic way to learn what `--model <selector>` actually resolves to on
// this machine (selectors resolve to different concrete models on the Anthropic
// API vs. Bedrock/Vertex/Foundry). Any selector whose probe is skipped — e.g. when
// ctx is cancelled partway through — keeps its hardcoded fallback catalog
// metadata as a fallback.
func (p *Provider) DiscoverModelCatalog(ctx context.Context) ([]llm.ModelInfo, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}

	candidates := claudeModelProbeCandidates()
	models := make([]llm.ModelInfo, 0, len(candidates))
	var failures []string
	for i, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			if len(models) > 0 {
				models = appendClaudeFallbacks(models, candidates[i:])
			}
			break
		}
		out, err := runner(ctx, "claude", claudeModelProbeArgs(candidate.Selector), nil)
		if err != nil {
			if ctx.Err() != nil {
				if len(models) > 0 {
					models = appendClaudeFallbacks(models, candidates[i:])
				}
				break
			}
			failures = append(failures, fmt.Sprintf("%s: %v", candidate.Selector, err))
			continue
		}

		resolved, contextWindow, err := parseClaudeModelProbe(candidate.Selector, out)
		if err != nil {
			if ctx.Err() != nil {
				if len(models) > 0 {
					models = appendClaudeFallbacks(models, candidates[i:])
				}
				break
			}
			failures = append(failures, fmt.Sprintf("%s: %v", candidate.Selector, err))
			continue
		}

		info := claudeModelInfoFromProbe(candidate, contextWindow, resolved)
		models = appendClaudeModelInfo(models, info)
	}

	if len(models) == 0 {
		if len(failures) == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("no Claude model probes succeeded: %s", strings.Join(failures, "; "))
	}
	return models, nil
}

func appendClaudeFallbacks(models []llm.ModelInfo, candidates []claudeModelProbeCandidate) []llm.ModelInfo {
	for _, candidate := range candidates {
		models = appendClaudeModelInfo(models, claudeModelInfoFromProbe(candidate, candidate.FallbackContextWindow, ""))
	}
	return models
}

func appendClaudeModelInfo(models []llm.ModelInfo, info llm.ModelInfo) []llm.ModelInfo {
	for i := range models {
		if !strings.EqualFold(models[i].ID, info.ID) {
			continue
		}
		if info.ContextWindow > 0 {
			models[i].ContextWindow = info.ContextWindow
		}
		for _, alias := range info.Aliases {
			models[i].Aliases = appendClaudeAlias(models[i].Aliases, models[i].ID, alias)
		}
		return models
	}
	return append(models, info)
}

func claudeModelProbeArgs(model string) []string {
	return []string{
		"--model", model,
		"--verbose",
		"-p", claudeModelProbePrompt,
		"--output-format", "stream-json",
		"--tools", "",
		"--no-session-persistence",
		"--max-budget-usd", claudeModelProbeMaxBudgetUSD,
	}
}

func parseClaudeModelProbe(requestedModel string, out []byte) (string, int, error) {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var resolvedModel string
	var contextWindow int
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg llm.SDKMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Init != nil && msg.Init.Model != "" {
			resolvedModel = msg.Init.Model
		}
		if msg.Assistant != nil {
			if msg.Assistant.Message.Model != "" {
				resolvedModel = msg.Assistant.Message.Model
			}
			if msg.Assistant.Message.Usage != nil && msg.Assistant.Message.Usage.ContextWindow > 0 {
				contextWindow = msg.Assistant.Message.Usage.ContextWindow
			}
		}
		if msg.Result != nil {
			if msg.Result.Usage != nil && msg.Result.Usage.ContextWindow > 0 {
				contextWindow = msg.Result.Usage.ContextWindow
			}
			for model, usage := range msg.Result.ModelUsage {
				if resolvedModel == "" && model != "" {
					resolvedModel = model
				}
				if usage.ContextWindow > 0 {
					contextWindow = usage.ContextWindow
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", 0, fmt.Errorf("scan Claude probe output for %s: %w", requestedModel, err)
	}
	if resolvedModel == "" && contextWindow == 0 {
		return "", 0, fmt.Errorf("probe for %s did not include model metadata", requestedModel)
	}
	return resolvedModel, contextWindow, nil
}
