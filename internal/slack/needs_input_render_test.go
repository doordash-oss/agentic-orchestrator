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

func TestRenderPendingInputKinds(t *testing.T) {
	tests := []struct {
		name        string
		tag         string
		input       ports.SlackPendingInput
		want        []string
		doesNotWant []string
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
				"#1 · Permission: Bash",
				"*Repository:* agentic-orchestrator",
				"*Phase:* implement",
				"```\nnpm publish\n```",
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
					{Label: "Minimal", Description: "Documentation only", Confidence: 0.12, HasConfidence: true},
				},
			},
			want: []string{
				"#2 · Scope",
				"Question 1 of 2",
				"Which scope should this cover?",
				"*1. Focused (recommended)*",
				"Only Slack rendering",
				"86% confidence",
				"*2. Broad*",
				"31% confidence",
				"Reply with an option number, its label, or react with a keycap number",
				"Any other text is treated as a free-text answer",
				"Anyone who can see this can respond",
			},
			doesNotWant: []string{"Broad (recommended)", "Minimal (recommended)"},
		},
		{
			name: "multi-select question",
			tag:  "#3",
			input: ports.SlackPendingInput{
				Kind:          ports.SlackPendingQuestion,
				Header:        "Gates",
				Question:      "Which gates should run?",
				QuestionIndex: 1,
				QuestionCount: 2,
				MultiSelect:   true,
				Options: []ports.SlackPendingInputOption{
					{Label: "Fast", Confidence: 0.92, HasConfidence: true},
					{Label: "Race", Confidence: 0.64, HasConfidence: true},
				},
			},
			want: []string{
				"#3 · Gates",
				"Question 2 of 2",
				"*1. Fast*",
				"92% confidence",
				"*2. Race*",
				"Reply with a comma- or space-separated list of option numbers or labels",
				"Anyone who can see this can respond",
			},
			doesNotWant: []string{"(recommended)"},
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
				"#4 · Details",
				"What should the release note say?",
				"Reply with any text",
				"Anyone who can see this can respond",
			},
			doesNotWant: []string{"*1.", "(recommended)"},
		},
		{
			name: "help request",
			tag:  "#5",
			input: ports.SlackPendingInput{
				Kind:         ports.SlackPendingHelp,
				HelpQuestion: "Which account should be used?",
			},
			want: []string{
				"#5 · Help request",
				"Which account should be used?",
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
				"#6 · Verification needs your input",
				"*Summary:*",
				"Two checks need an authenticated session.",
				"*Integration test*",
				"*Repository:* agentic-orchestrator",
				"*Command:* `go test ./test/integration`",
				"*Reason:* Okta session expired",
				"*Remediation:* Sign in and retry",
				"*Publish check*",
				"*Questions:*",
				"1. Can these checks be waived?",
				"waiving the blocked checks or retrying after signing in",
				"Replies are not read here",
			},
			doesNotWant: []string{"Anyone who can see this can respond"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, fallback := renderPendingInput("", tt.tag, tt.input, nil)
			rendered := pendingInputBlockText(blocks)
			for _, want := range tt.want {
				if !strings.Contains(rendered, want) {
					t.Errorf("renderPendingInput() blocks missing %q:\n%s", want, rendered)
				}
			}
			for _, unwanted := range tt.doesNotWant {
				if strings.Contains(rendered, unwanted) {
					t.Errorf("renderPendingInput() blocks contain %q:\n%s", unwanted, rendered)
				}
			}
			if fallback == "" {
				t.Error("renderPendingInput() fallback is empty")
			}
			assertPendingInputBlockLimits(t, blocks)
		})
	}
}

func TestRenderQuestionRecommendedMarkerIsNotDuplicated(t *testing.T) {
	blocks, _ := renderPendingInput("", "#7", ports.SlackPendingInput{
		Kind:     ports.SlackPendingQuestion,
		Header:   "Approach",
		Question: "Which approach?",
		Options: []ports.SlackPendingInputOption{
			{Label: "Focused (recommended)", Confidence: 0.9, HasConfidence: true},
			{Label: "Broad", Confidence: 0.4, HasConfidence: true},
		},
	}, nil)

	rendered := pendingInputBlockText(blocks)
	if got := strings.Count(strings.ToLower(rendered), "(recommended)"); got != 1 {
		t.Errorf("renderPendingInput() recommended marker count = %d; want 1:\n%s", got, rendered)
	}
}

func TestRenderPendingInputTruncatesEscapesAndRedacts(t *testing.T) {
	const (
		token       = "xoxb-needs-input-secret-4242"
		otherSecret = "xoxp-another-secret-9999"
	)
	longCommand := "deploy --repo agentic-orchestrator " +
		token + " " + otherSecret + "\nAuthorization: Bearer auth-secret\n" +
		"https://user:password@example.com/path\n" + strings.Repeat("argument ", 700)

	permissionBlocks, permissionFallback := renderPendingInput(token, "#8", ports.SlackPendingInput{
		Kind:     ports.SlackPendingPermission,
		ToolName: "Bash",
		RepoName: "agentic-orchestrator",
		Phase:    "implement",
		Input:    map[string]any{"command": longCommand},
	}, nil)
	permissionText := pendingInputBlockText(permissionBlocks)
	for _, want := range []string{
		"deploy --repo agentic-orchestrator",
		"[REDACTED]",
		"Input was shortened. Open Agentico for the full input.",
	} {
		if !strings.Contains(permissionText, want) {
			t.Errorf("renderPendingInput(permission) blocks missing %q:\n%s", want, permissionText)
		}
	}
	assertNoPendingInputSecret(t, permissionText+"\n"+permissionFallback, token, otherSecret, "auth-secret", "user:password")
	assertPendingInputBlockLimits(t, permissionBlocks)

	longOption := strings.Repeat("escaped <#ops> & ", 300)
	questionBlocks, questionFallback := renderPendingInput(token, "#9", ports.SlackPendingInput{
		Kind:     ports.SlackPendingQuestion,
		Header:   "Deploy " + token,
		Question: "Choose for <#ops> & <@U123>: " + otherSecret,
		Options: []ports.SlackPendingInputOption{
			{
				Label:         "Safe " + token,
				Description:   longOption + " Authorization: Bearer option-secret",
				Confidence:    0.99,
				HasConfidence: true,
			},
		},
	}, nil)
	questionText := pendingInputBlockText(questionBlocks)
	for _, want := range []string{
		"#9 · Deploy [REDACTED]",
		"&lt;#ops&gt; &amp; &lt;@U123&gt;",
		"Safe [REDACTED] (recommended)",
		"...",
	} {
		if !strings.Contains(questionText, want) {
			t.Errorf("renderPendingInput(question) blocks missing %q:\n%s", want, questionText)
		}
	}
	assertNoPendingInputSecret(t, questionText+"\n"+questionFallback, token, otherSecret, "option-secret")
	assertPendingInputBlockLimits(t, questionBlocks)

	gateBlocks, gateFallback := renderPendingInput(token, "#10", ports.SlackPendingInput{
		Kind:        ports.SlackPendingGate,
		GateSummary: "Blocked for " + token,
		GateBlockers: []ports.SlackPendingInputBlocker{{
			Name:        "Check " + otherSecret,
			RepoName:    "agentic-orchestrator",
			Command:     strings.Repeat("go test ./internal/slack ", 100) + token,
			Reason:      "Authorization: Bearer gate-secret",
			Remediation: "Open https://user:password@example.com/login",
		}},
		GateQuestions: []string{"Retry " + token + "?"},
	}, nil)
	gateText := pendingInputBlockText(gateBlocks)
	for _, want := range []string{
		"*Repository:* agentic-orchestrator",
		"go test ./internal/slack",
		"[REDACTED]",
	} {
		if !strings.Contains(gateText, want) {
			t.Errorf("renderPendingInput(gate) blocks missing %q:\n%s", want, gateText)
		}
	}
	assertNoPendingInputSecret(t, gateText+"\n"+gateFallback, token, otherSecret, "gate-secret", "user:password")
	assertPendingInputBlockLimits(t, gateBlocks)
}

func TestWaitingSummaryTaggedAndCountVariants(t *testing.T) {
	pending := []pendingInputRecord{
		{Kind: string(ports.SlackPendingHelp), Tag: "#12"},
		{Kind: string(ports.SlackPendingQuestion), Tag: "#2"},
		{Kind: string(ports.SlackPendingGate), Tag: "#10"},
		{Kind: string(ports.SlackPendingPermission), Tag: "#1"},
	}
	if got, want := waitingSummary(pending, true),
		"#1 permission · #2 question · #10 verification gate · #12 help"; got != want {
		t.Errorf("waitingSummary(tagged) = %q; want %q", got, want)
	}

	counted := []pendingInputRecord{
		{Kind: string(ports.SlackPendingQuestion)},
		{Kind: string(ports.SlackPendingPermission)},
		{Kind: string(ports.SlackPendingQuestion)},
		{Kind: string(ports.SlackPendingGate)},
		{Kind: string(ports.SlackPendingHelp)},
	}
	if got, want := waitingSummary(counted, false),
		"1 permission, 2 questions, 1 verification gate, 1 help · Needs input replies are off"; got != want {
		t.Errorf("waitingSummary(counts) = %q; want %q", got, want)
	}
	if got := waitingSummary(nil, true); got != "" {
		t.Errorf("waitingSummary(empty) = %q; want empty", got)
	}
}

func pendingInputBlockText(blocks []Block) string {
	var rendered strings.Builder
	for _, block := range blocks {
		switch value := block.(type) {
		case headerBlock:
			rendered.WriteString(value.Text.Text)
			rendered.WriteByte('\n')
		case sectionBlock:
			if value.Text != nil {
				rendered.WriteString(value.Text.Text)
				rendered.WriteByte('\n')
			}
			for _, field := range value.Fields {
				rendered.WriteString(field.Text)
				rendered.WriteByte('\n')
			}
		case contextBlock:
			for _, element := range value.Elements {
				rendered.WriteString(element.Text)
				rendered.WriteByte('\n')
			}
		}
	}
	return rendered.String()
}

func assertPendingInputBlockLimits(t testing.TB, blocks []Block) {
	t.Helper()
	if got := len(blocks); got > messageBlockLimit {
		t.Errorf("renderPendingInput() block count = %d; want <= %d", got, messageBlockLimit)
	}
	for _, block := range blocks {
		switch value := block.(type) {
		case headerBlock:
			if got := len(value.Text.Text); got > headerTextLimit {
				t.Errorf("renderPendingInput() header length = %d; want <= %d", got, headerTextLimit)
			}
		case sectionBlock:
			if value.Text != nil && len(value.Text.Text) > sectionTextLimit {
				t.Errorf("renderPendingInput() section length = %d; want <= %d", len(value.Text.Text), sectionTextLimit)
			}
		case contextBlock:
			for _, element := range value.Elements {
				if got := len(element.Text); got > contextTextLimit {
					t.Errorf("renderPendingInput() context length = %d; want <= %d", got, contextTextLimit)
				}
			}
		}
	}
}

func assertNoPendingInputSecret(t testing.TB, rendered string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(rendered, secret) {
			t.Errorf("renderPendingInput() output contains secret %q", secret)
		}
	}
}
