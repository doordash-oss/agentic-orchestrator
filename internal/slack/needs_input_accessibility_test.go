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

package slack

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestRenderPendingInputFallbackCarriesAccessibleActionableMeaning(t *testing.T) {
	tests := []struct {
		name  string
		tag   string
		input ports.SlackPendingInput
		want  []string
	}{
		{
			name: "permission",
			tag:  "#1",
			input: ports.SlackPendingInput{
				Kind:     ports.SlackPendingPermission,
				ToolName: "Bash",
				RepoName: "agentic-orchestrator",
				Phase:    "implement",
				Input:    map[string]any{"command": "npm publish"},
			},
			want: []string{
				"#1 Permission: Bash",
				"Repository: agentic-orchestrator",
				"Phase: implement",
				"Input: npm publish",
				"React ✅ to allow once or ❌ to deny",
				"reply allow, approve, yes, deny, or no",
				"Remember rules are created only in Agentico",
				"Anyone who can see this can respond",
			},
		},
		{
			name: "single-select question",
			tag:  "#2",
			input: ports.SlackPendingInput{
				Kind:          ports.SlackPendingQuestion,
				Header:        "Scope",
				Question:      "Which scope should this cover?",
				QuestionIndex: 0,
				QuestionCount: 2,
				Options: []ports.SlackPendingInputOption{
					{Label: "Focused", Description: "Only Slack rendering", Confidence: 0.86, HasConfidence: true},
					{Label: "Broad", Description: "All notification paths", Confidence: 0.31, HasConfidence: true},
				},
			},
			want: []string{
				"#2 Scope",
				"Question 1 of 2: Which scope should this cover?",
				"1. Focused (recommended) — Only Slack rendering — 86% confidence",
				"2. Broad — All notification paths — 31% confidence",
				"Reply with an option number, its label, or react with a keycap number",
				"Any other text is treated as a free-text answer",
				"Anyone who can see this can respond",
			},
		},
		{
			name: "multi-select question",
			tag:  "#3",
			input: ports.SlackPendingInput{
				Kind:        ports.SlackPendingQuestion,
				Header:      "Gates",
				Question:    "Which gates should run?",
				MultiSelect: true,
				Options: []ports.SlackPendingInputOption{
					{Label: "Fast", Description: "Run the fast suite", Confidence: 0.92, HasConfidence: true},
					{Label: "Race", Description: "Run race checks", Confidence: 0.64, HasConfidence: true},
				},
			},
			want: []string{
				"#3 Gates",
				"Question: Which gates should run?",
				"1. Fast — Run the fast suite — 92% confidence",
				"2. Race — Run race checks — 64% confidence",
				"Reply with a comma- or space-separated list of option numbers or labels",
				"Anyone who can see this can respond",
			},
		},
		{
			name: "free-text question",
			tag:  "#4",
			input: ports.SlackPendingInput{
				Kind:     ports.SlackPendingQuestion,
				Header:   "Details",
				Question: "What should the release note say?",
			},
			want: []string{
				"#4 Details",
				"Question: What should the release note say?",
				"Reply with any text",
				"Anyone who can see this can respond",
			},
		},
		{
			name: "help request",
			tag:  "#5",
			input: ports.SlackPendingInput{
				Kind:         ports.SlackPendingHelp,
				HelpQuestion: "Which account should be used?",
			},
			want: []string{
				"#5 Help request",
				"Question: Which account should be used?",
				"Reply with any text",
				"Anyone who can see this can respond",
			},
		},
		{
			name: "verification gate",
			tag:  "#6",
			input: ports.SlackPendingInput{
				Kind:          ports.SlackPendingGate,
				GateSummary:   "Two checks need an authenticated session.",
				GateQuestions: []string{"Can these checks be waived?", "Who can sign in?"},
				GateBlockers: []ports.SlackPendingInputBlocker{
					{
						Name:        "Integration test",
						RepoName:    "agentic-orchestrator",
						Command:     "go test ./test/integration",
						Reason:      "Okta session expired",
						Remediation: "Sign in and retry",
					},
					{
						Name:        "Publish check",
						RepoName:    "desktop",
						Command:     "npm run publish:check",
						Reason:      "Registry login required",
						Remediation: "Authenticate with the registry",
					},
				},
			},
			want: []string{
				"#6 Verification needs your input",
				"Summary: Two checks need an authenticated session.",
				"Blocker 1: Integration test",
				"Repository: agentic-orchestrator",
				"Command: go test ./test/integration",
				"Reason: Okta session expired",
				"Remediation: Sign in and retry",
				"Blocker 2: Publish check",
				"Question 1: Can these checks be waived?",
				"Question 2: Who can sign in?",
				"waive the blocked checks or retry after signing in",
				"Replies are not read here",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, fallback := renderPendingInput("", tt.tag, tt.input, nil)
			for _, want := range tt.want {
				if !strings.Contains(fallback, want) {
					t.Errorf("renderPendingInput() fallback = %q; want complete meaning %q", fallback, want)
				}
			}
			if got := len(fallback); got > inputFallbackTextLimit {
				t.Errorf("renderPendingInput() fallback length = %d; want <= %d", got, inputFallbackTextLimit)
			}
		})
	}
}

func TestRenderPendingInputFallbackRedactsBeforeBoundedTruncation(t *testing.T) {
	const token = "xoxb-needs-input-accessibility-secret"
	secretAtBoundary := "Authorization: Bearer boundary-secret"
	longDescription := strings.Repeat("recognizable option detail ", 60) + secretAtBoundary +
		strings.Repeat(" trailing detail", 60)

	_, fallback := renderPendingInput(token, "#7", ports.SlackPendingInput{
		Kind:     ports.SlackPendingQuestion,
		Header:   "Scope " + token,
		Question: "Choose a safe scope for agentic-orchestrator.",
		Options: []ports.SlackPendingInputOption{
			{
				Label:         "Focused",
				Description:   longDescription,
				Confidence:    0.91,
				HasConfidence: true,
			},
			{
				Label:       "Broad",
				Description: strings.Repeat("all notification paths ", 80),
			},
		},
	}, nil)

	if got := len(fallback); got > inputFallbackTextLimit {
		t.Errorf("renderPendingInput() fallback length = %d; want <= %d", got, inputFallbackTextLimit)
	}
	for _, secret := range []string{token, "boundary-secret", "Bearer boundary"} {
		if strings.Contains(fallback, secret) {
			t.Errorf("renderPendingInput() fallback contains secret fragment %q: %q", secret, fallback)
		}
	}
	for _, want := range []string{
		"#7 Scope [REDACTED]",
		"Choose a safe scope for agentic-orchestrator.",
		"1. Focused (recommended)",
		"Reply with an option number",
		"Anyone who can see this can respond",
		"Open Agentico for full details.",
	} {
		if !strings.Contains(fallback, want) {
			t.Errorf("renderPendingInput() fallback = %q; want bounded meaning %q", fallback, want)
		}
	}
}
