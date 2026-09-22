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

// PRContext is the lean input for one stack layer's PR body generation. It
// replaces the raw `base..HEAD` diff with structured, bounded signals that
// fit comfortably inside the Claude CLI prompt budget even for very large
// features. The layer fields scope the session to a single delivery-stack
// layer: LayerPosition is the 1-based position, LayerTitle/LayerPhases/
// LayerRationale come from the layer's roadmap pull-request row, and Stack
// lists every layer (one prompts.PRStackLayerView per layer) so the model
// can place this layer inside the stack. CommitBodies is
// `git log --format=%B base..HEAD` and DiffStat is
// `git diff --stat base...HEAD`.
type PRContext struct {
	FeatureName        string
	FeatureDescription string
	LayerPosition      int
	LayerTitle         string
	LayerPhases        []int
	LayerRationale     string
	Stack              []prompts.PRStackLayerView
	CommitBodies       string
	DiffStat           string
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

// BuildPRDescriptionPrompt constructs the prompt for generating one stack
// layer's PR body from a lean PRContext. Empty sections are omitted so the
// model is not asked to reason about them.
//
// The prose lives in internal/agent/prompts/templates/pr_description.user.tmpl.
func BuildPRDescriptionPrompt(ctx PRContext) string {
	return prompts.PRDescriptionUserPrompt(prompts.PRDescriptionUserInput{
		FeatureName:        ctx.FeatureName,
		FeatureDescription: ctx.FeatureDescription,
		LayerPosition:      ctx.LayerPosition,
		LayerTitle:         ctx.LayerTitle,
		LayerPhases:        ctx.LayerPhases,
		LayerRationale:     ctx.LayerRationale,
		Stack:              ctx.Stack,
		CommitBodies:       ctx.CommitBodies,
		DiffStat:           ctx.DiffStat,
	})
}

// RunDescriptionGeneration runs the bounded utility helper to generate one
// stack layer's PR body from a structured PRContext. It is tool-free and
// returns errors without synthesizing replacement content.
func (pr *PhaseRunner) RunDescriptionGeneration(ctx context.Context, featureID, model string, prCtx PRContext) (body string, err error) {
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
		return "", fmt.Errorf("generating description: %w", runErr)
	}

	body = ParsePRDescription(result.Text)
	if body == "" {
		return "", fmt.Errorf("generating description: model returned an empty body")
	}
	return body, nil
}

// ParsePRDescription extracts the PR body from the generation output. The
// reply is body-only: the whole trimmed output is the body. As a lenient
// fallback for a reply that still carries the retired TITLE: marker, the
// marker line (and an optional BODY: marker) is stripped and the remainder
// is the body. Returns an empty string when no body can be extracted so
// callers can decide to fail deterministically.
func ParsePRDescription(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	if strings.Contains(output, "TITLE:") {
		return parseMarkedBody(output)
	}
	return output
}

// parseMarkedBody strips a legacy TITLE: marker line (and an optional BODY:
// marker) from a reply, returning the remaining text as the body.
func parseMarkedBody(output string) string {
	lines := strings.Split(output, "\n")
	hasBodyMarker := strings.Contains(output, "BODY:")
	var bodyLines []string
	inBody := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBody {
			if strings.HasPrefix(trimmed, "TITLE:") {
				continue
			}
			if hasBodyMarker && trimmed == "BODY:" {
				inBody = true
				continue
			}
			// Allow blank lines between the markers.
			if trimmed == "" {
				continue
			}
		}
		inBody = true
		bodyLines = append(bodyLines, line)
	}
	return strings.TrimSpace(strings.Join(bodyLines, "\n"))
}
