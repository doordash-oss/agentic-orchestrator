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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const prDescriptionSystemPrompt = "You must complete using only the context supplied in the prompt. Do not request or invoke tools, inspect files, run commands, browse the web, delegate work, or ask for more information."

type prDescriptionPermissionHandler struct{}

func (*prDescriptionPermissionHandler) ToolFree() bool {
	return true
}

func (*prDescriptionPermissionHandler) CanUseTool(_ ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	return ports.PermissionDecision{
		Behavior: "deny",
		Reason:   "PR narrative generation must complete using only the context supplied in the prompt",
	}, nil
}

// PRContext is the lean input for PR description generation. It replaces the
// raw `master..HEAD` diff with structured, bounded signals that fit comfortably
// inside the Claude CLI prompt budget even for very large features.
type PRContext struct {
	FeatureName        string
	FeatureDescription string
	Roadmap            string // plan/roadmap text (was the prior "plan" arg)
	CommitBodies       string // `git log --format=%B base..HEAD`
	DiffStat           string // `git diff --stat base...HEAD`
}

// extractTextFromStreamJSON parses JSONL stream-json output from the claude CLI
// and returns the concatenated assistant text content. Falls back to the raw
// input if no assistant messages are found (e.g., if the output format changed).
func extractTextFromStreamJSON(output string) string {
	var b strings.Builder
	var resultText string
	foundAssistant := false

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg llm.SDKMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Assistant != nil {
			for _, block := range msg.Assistant.Message.Content {
				if block.IsText() {
					b.WriteString(block.Text)
					foundAssistant = true
				}
			}
		}
		if msg.Result != nil && msg.Result.Result != "" {
			resultText = msg.Result.Result
		}
	}

	if foundAssistant {
		return b.String()
	}
	if resultText != "" {
		return resultText
	}
	// Fallback: return raw output (handles non-JSON output gracefully)
	return output
}

// BuildPRDescriptionPrompt constructs the prompt for generating a PR description
// from a lean PRContext. Empty sections are omitted so the model is not asked to
// reason about them.
//
// The prose lives in internal/agent/prompts/templates/pr_description.user.tmpl.
func BuildPRDescriptionPrompt(ctx PRContext) string {
	return prompts.PRDescriptionUserPrompt(prompts.PRDescriptionUserInput{
		FeatureName:        ctx.FeatureName,
		FeatureDescription: ctx.FeatureDescription,
		Roadmap:            ctx.Roadmap,
		CommitBodies:       ctx.CommitBodies,
		DiffStat:           ctx.DiffStat,
	})
}

func truncateTitle(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimRight(string(runes[:n-1]), " ") + "…"
}

// RunDescriptionGeneration runs the bounded utility helper to generate a PR
// title/body from a structured PRContext. It is tool-free and returns errors
// without synthesizing replacement content.
func (pr *PhaseRunner) RunDescriptionGeneration(ctx context.Context, featureID, model string, prCtx PRContext) (title, body string, err error) {
	result, runErr := pr.RunUtilitySession(ctx, UtilityRunConfig{
		SessionID:    fmt.Sprintf("publish-description-%d", time.Now().UnixNano()),
		FeatureID:    featureID,
		Label:        "description generation",
		Model:        model,
		Prompt:       BuildPRDescriptionPrompt(prCtx),
		SystemPrompt: prDescriptionSystemPrompt,
		Phase:        feature.PhasePublish,
		PermHandler:  &prDescriptionPermissionHandler{},
		RequireText:  true,
	})
	if runErr != nil {
		return "", "", fmt.Errorf("generating description: %w", runErr)
	}

	title, body = ParsePRDescription(result.Text)
	if title == "" || body == "" {
		return "", "", fmt.Errorf("generating description: model returned an incomplete title or body")
	}
	return title, body, nil
}

// ParsePRDescription extracts title and body from the generation output. The
// parser is intentionally lenient: output need not contain explicit TITLE:/BODY:
// markers. When markers are absent it treats the first markdown heading (or the
// first non-empty line) as the title and the rest as the body. Returns empty
// strings for whichever field cannot be extracted so callers can decide to
// fall back deterministically.
func ParsePRDescription(output string) (title, body string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return "", ""
	}

	if strings.Contains(output, "TITLE:") {
		return parseMarked(output)
	}
	return parseUnmarked(output)
}

// parseMarked handles output that contains a TITLE: marker (and optionally BODY:).
// If BODY: is absent, every line after the TITLE: line is treated as the body.
func parseMarked(output string) (title, body string) {
	lines := strings.Split(output, "\n")
	var bodyLines []string
	inBody := false
	titleSeen := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !titleSeen && strings.HasPrefix(trimmed, "TITLE:") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "TITLE:"))
			titleSeen = true
			continue
		}
		if titleSeen && !inBody && trimmed == "BODY:" {
			inBody = true
			continue
		}
		if titleSeen && !inBody && trimmed == "" {
			// Allow blank line between TITLE: and BODY:.
			continue
		}
		if titleSeen {
			// Either BODY: was already seen, or it was omitted — in both cases,
			// everything after the title is body.
			inBody = true
			bodyLines = append(bodyLines, line)
		}
	}
	body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	return title, body
}

// parseUnmarked handles output without TITLE:/BODY: markers. Extracts the first
// markdown heading or first non-empty line as title; the rest becomes body.
func parseUnmarked(output string) (title, body string) {
	lines := strings.Split(output, "\n")
	titleIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		titleIdx = i
		if strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		} else {
			title = truncateTitle(trimmed, 70)
		}
		break
	}
	if titleIdx < 0 {
		return "", ""
	}
	body = strings.TrimSpace(strings.Join(lines[titleIdx+1:], "\n"))
	return title, body
}
