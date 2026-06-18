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
)

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

// BuildPRDescriptionFallback synthesizes a readable title and body from the
// same PRContext when the model call fails or returns unusable output. The
// fallback is deterministic and never empty.
func BuildPRDescriptionFallback(ctx PRContext) (title, body string) {
	title = fallbackTitle(ctx)

	var b strings.Builder
	b.WriteString("## Summary\n\n")
	if ctx.FeatureDescription != "" {
		b.WriteString(ctx.FeatureDescription)
		b.WriteString("\n\n")
	} else if ctx.FeatureName != "" {
		b.WriteString(ctx.FeatureName)
		b.WriteString("\n\n")
	} else {
		b.WriteString("Implementation per plan.\n\n")
	}

	if subjects := commitSubjects(ctx.CommitBodies, 10); len(subjects) > 0 {
		b.WriteString("## Commits\n\n")
		for _, s := range subjects {
			b.WriteString("- ")
			b.WriteString(s)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}

	if ctx.DiffStat != "" {
		b.WriteString("## Changes\n\n```\n")
		b.WriteString(strings.TrimSpace(ctx.DiffStat))
		b.WriteString("\n```\n\n")
	}

	b.WriteString("## Test plan\n\n- [ ] Manual testing\n")
	return title, strings.TrimRight(b.String(), "\n")
}

// fallbackTitle picks a concise title from the available context. Preference
// order: feature name → first commit subject → generic label. Truncates to 70.
func fallbackTitle(ctx PRContext) string {
	candidates := []string{ctx.FeatureName}
	if subjects := commitSubjects(ctx.CommitBodies, 1); len(subjects) > 0 {
		candidates = append(candidates, subjects[0])
	}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c != "" {
			return truncateTitle(c, 70)
		}
	}
	return "Feature implementation"
}

// commitSubjects returns the first line of each commit body (up to limit),
// skipping the "---commit---" separator emitted by CommitBodies. Order is
// preserved (git log shows newest first by default).
func commitSubjects(bodies string, limit int) []string {
	if bodies == "" || limit <= 0 {
		return nil
	}
	var subjects []string
	var currentStarted bool
	for _, line := range strings.Split(bodies, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---commit---" {
			currentStarted = false
			continue
		}
		if currentStarted || trimmed == "" {
			continue
		}
		// Skip the Agentic signature trailer lines if they surface first.
		if strings.HasPrefix(trimmed, "Agentic-Signature:") || strings.HasPrefix(trimmed, "Co-authored-by:") {
			continue
		}
		subjects = append(subjects, trimmed)
		currentStarted = true
		if len(subjects) >= limit {
			break
		}
	}
	return subjects
}

func truncateTitle(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimRight(string(runes[:n-1]), " ") + "…"
}

// RunDescriptionGeneration runs the bounded utility helper to generate a PR
// title/body from a structured PRContext. On any helper error, the
// deterministic fallback is used and the error is returned for observability.
func (pr *PhaseRunner) RunDescriptionGeneration(ctx context.Context, featureID, model string, prCtx PRContext) (title, body string, err error) {
	result, runErr := pr.RunUtilitySession(ctx, UtilityRunConfig{
		SessionID:   fmt.Sprintf("publish-description-%d", time.Now().UnixNano()),
		FeatureID:   featureID,
		Label:       "description generation",
		Model:       model,
		Prompt:      BuildPRDescriptionPrompt(prCtx),
		Phase:       feature.PhasePublish,
		RequireText: true,
	})
	if runErr != nil {
		title, body = BuildPRDescriptionFallback(prCtx)
		return title, body, fmt.Errorf("generating description: %w", runErr)
	}

	title, body = ParsePRDescription(result.Text)
	if title == "" || body == "" {
		fbTitle, fbBody := BuildPRDescriptionFallback(prCtx)
		if title == "" {
			title = fbTitle
		}
		if body == "" {
			body = fbBody
		}
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
