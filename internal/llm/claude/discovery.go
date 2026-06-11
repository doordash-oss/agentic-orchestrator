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
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

const (
	claudeModelProbePrompt       = "Return exactly: OK"
	claudeModelProbeMaxBudgetUSD = "0.05"
	claudeModelProbeAttempts     = 2
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
	return p.DiscoverModelCatalogWithProgress(ctx, nil)
}

// DiscoverModelCatalogWithProgress resolves Claude selectors concurrently and
// reports each successful probe as soon as it completes. The final catalog is
// still returned in the curated selector order so model defaults stay stable.
func (p *Provider) DiscoverModelCatalogWithProgress(ctx context.Context, report llm.ModelDiscoveryReporter) ([]llm.ModelInfo, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}

	candidates := claudeModelProbeCandidates()
	results := make([]claudeModelProbeResult, len(candidates))
	var wg sync.WaitGroup
	var reportMu sync.Mutex
	reported := make(map[string]bool, len(candidates))
	wg.Add(len(candidates))
	for i, candidate := range candidates {
		go func(i int, candidate claudeModelProbeCandidate) {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				results[i] = claudeModelProbeResult{candidate: candidate, err: err}
				return
			}
			resolved, contextWindow, err := probeClaudeModel(ctx, runner, candidate)
			if err != nil {
				results[i] = claudeModelProbeResult{candidate: candidate, err: err}
				return
			}

			info := claudeModelInfoFromProbe(candidate, contextWindow, resolved)
			results[i] = claudeModelProbeResult{candidate: candidate, info: info}
			reportClaudeModelDiscovery(report, info, &reportMu, reported)
		}(i, candidate)
	}
	wg.Wait()

	models := make([]llm.ModelInfo, 0, len(candidates))
	var failures []string
	var canceled []claudeModelProbeCandidate
	for _, result := range results {
		if result.err != nil {
			if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
				canceled = append(canceled, result.candidate)
			} else {
				failures = append(failures, fmt.Sprintf("%s: %v", result.candidate.Selector, result.err))
			}
			continue
		}
		models = appendClaudeModelInfo(models, result.info)
	}
	if len(canceled) > 0 && len(models) > 0 {
		models = appendClaudeFallbacks(models, canceled)
	}

	if len(models) == 0 {
		if len(failures) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(canceled) > 0 {
				return nil, fmt.Errorf("no Claude model probes succeeded before cancellation")
			}
		}
		return nil, fmt.Errorf("no Claude model probes succeeded: %s", strings.Join(failures, "; "))
	}
	return models, nil
}

type claudeModelProbeResult struct {
	candidate claudeModelProbeCandidate
	info      llm.ModelInfo
	err       error
}

func reportClaudeModelDiscovery(report llm.ModelDiscoveryReporter, info llm.ModelInfo, mu *sync.Mutex, reported map[string]bool) {
	if report == nil {
		return
	}
	key := strings.ToLower(info.ID)
	mu.Lock()
	if reported[key] {
		mu.Unlock()
		return
	}
	reported[key] = true
	mu.Unlock()
	report(info)
}

func probeClaudeModel(ctx context.Context, runner clirun.CommandRunner, candidate claudeModelProbeCandidate) (string, int, error) {
	var lastErr error
	for range claudeModelProbeAttempts {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		out, err := runner(ctx, "claude", claudeModelProbeArgs(candidate.Selector), nil)
		if err != nil {
			if ctx.Err() != nil {
				return "", 0, ctx.Err()
			}
			lastErr = err
			continue
		}

		resolved, contextWindow, err := parseClaudeModelProbe(candidate.Selector, out)
		if err == nil {
			return resolved, contextWindow, nil
		}
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("probe for %s failed", candidate.Selector)
	}
	return "", 0, lastErr
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
			if len(msg.Result.ModelUsage) == 1 && resolvedModel == "" {
				for model := range msg.Result.ModelUsage {
					resolvedModel = model
				}
			}
			if window := contextWindowForResolvedModel(resolvedModel, msg.Result.ModelUsage); window > 0 {
				contextWindow = window
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

func contextWindowForResolvedModel(resolvedModel string, usage map[string]llm.ModelUsageEntry) int {
	if len(usage) == 0 {
		return 0
	}
	if resolvedModel != "" {
		if entry, ok := usage[resolvedModel]; ok && entry.ContextWindow > 0 {
			return entry.ContextWindow
		}
		for model, entry := range usage {
			if strings.EqualFold(model, resolvedModel) && entry.ContextWindow > 0 {
				return entry.ContextWindow
			}
		}
	}
	if len(usage) == 1 {
		for _, entry := range usage {
			if entry.ContextWindow > 0 {
				return entry.ContextWindow
			}
		}
	}
	return 0
}
