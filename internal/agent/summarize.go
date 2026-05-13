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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const (
	summaryModel = "haiku"
)

var summaryTimeout = 15 * time.Second

// BuildSummaryPrompt constructs a prompt that asks Claude to distill a feature
// request into a concise 1-2 sentence summary capturing the core intent.
//
// The prose lives in internal/agent/prompts/templates/summary.user.tmpl.
func BuildSummaryPrompt(name, description string) string {
	return prompts.SummaryUserPrompt(prompts.SummaryUserInput{
		Name:        name,
		Description: description,
	})
}

// RunSummaryGeneration runs the bounded utility helper to generate a short
// feature summary. Uses the fast haiku model with a timeout.
func (pr *PhaseRunner) RunSummaryGeneration(ctx context.Context, name, description string) (string, error) {
	result, err := pr.RunUtilitySession(ctx, UtilityRunConfig{
		SessionID:   fmt.Sprintf("summary-%d", time.Now().UnixNano()),
		Label:       "summary generation",
		Model:       summaryModel,
		Prompt:      BuildSummaryPrompt(name, description),
		Timeout:     summaryTimeout,
		Phase:       feature.PhaseResearch,
		RequireText: true,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || (result != nil && result.Status == BoundedHelperStatusTimedOut) {
			return "", fmt.Errorf("summary generation timed out after %s", summaryTimeout)
		}
		return "", fmt.Errorf("summary generation: %w", err)
	}
	return ParseSummary(result.Text), nil
}

// ParseSummary trims whitespace and strips common markdown artifacts from the
// raw Claude output, returning a clean summary string.
func ParseSummary(output string) string {
	s := strings.TrimSpace(output)
	// Strip wrapping quotes if the model decided to add them
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return s
}
