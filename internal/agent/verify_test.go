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
	"testing"
)

func TestParsePlanVerification(t *testing.T) {
	tests := []struct {
		name     string
		planText string
		want     []VerificationStep
	}{
		{
			name:     "standard format with colon separator",
			planText: "## Phase 1: Setup\n\n### Success Criteria:\n\n#### Automated Verification:\n- [ ] Migration applies cleanly: `make migrate`\n- [ ] Unit tests pass: `go test ./... -race`\n- [ ] Linting passes: `golangci-lint run`\n\n#### Manual Verification:\n- [ ] Feature works in the UI\n",
			want: []VerificationStep{
				{Description: "Migration applies cleanly", Command: "make migrate"},
				{Description: "Unit tests pass", Command: "go test ./... -race"},
				{Description: "Linting passes", Command: "golangci-lint run"},
			},
		},
		{
			name:     "command only, no description",
			planText: "#### Automated Verification:\n- [ ] `go test ./...`\n",
			want: []VerificationStep{
				{Description: "", Command: "go test ./..."},
			},
		},
		{
			name:     "checked items still parsed",
			planText: "#### Automated Verification:\n- [x] Tests pass: `go test ./...`\n- [ ] Lint passes: `go vet ./...`\n",
			want: []VerificationStep{
				{Description: "Tests pass", Command: "go test ./..."},
				{Description: "Lint passes", Command: "go vet ./..."},
			},
		},
		{
			name:     "multiple phases collected",
			planText: "## Phase 1\n\n#### Automated Verification:\n- [ ] Build passes: `make build`\n\n## Phase 2\n\n#### Automated Verification:\n- [ ] Integration tests pass: `make test-integration`\n",
			want: []VerificationStep{
				{Description: "Build passes", Command: "make build"},
				{Description: "Integration tests pass", Command: "make test-integration"},
			},
		},
		{
			name:     "no automated verification section",
			planText: "## Phase 1\n\n### Success Criteria:\n- Feature implemented\n- Tests pass\n\n## Testing Strategy\n- Unit tests for all functions\n",
			want:     nil,
		},
		{
			name:     "empty plan text",
			planText: "",
			want:     nil,
		},
		{
			name:     "checklist items without backtick commands ignored",
			planText: "#### Automated Verification:\n- [ ] Make sure everything works\n- [ ] Tests should pass: `go test ./...`\n- [ ] Check the logs\n",
			want: []VerificationStep{
				{Description: "Tests should pass", Command: "go test ./..."},
			},
		},
		{
			name:     "parenthesized command format",
			planText: "#### Automated Verification:\n- [ ] API returns 200 (`curl localhost:8080/health`)\n",
			want: []VerificationStep{
				{Description: "API returns 200", Command: "curl localhost:8080/health"},
			},
		},
		{
			name:     "multiple backticks uses last as command",
			planText: "#### Automated Verification:\n- [ ] `commands/chat.md` is embedded: verify `agent.ReadCommand(\"chat\")` returns content (add a test)\n- [ ] Config loads with `chat` model: verify defaults are applied (`go test ./internal/config/...`)\n",
			want: []VerificationStep{
				{Description: "`commands/chat.md` is embedded: verify  returns content (add a test", Command: `agent.ReadCommand("chat")`},
				{Description: "Config loads with `chat` model: verify defaults are applied", Command: "go test ./internal/config/..."},
			},
		},
		{
			name:     "varied header levels",
			planText: "### Automated Verification:\n- [ ] Tests pass: `make test`\n\n##### Automated Verification:\n- [ ] Lint ok: `make lint`\n",
			want: []VerificationStep{
				{Description: "Tests pass", Command: "make test"},
				{Description: "Lint ok", Command: "make lint"},
			},
		},
		{
			name:     "ignores content inside fenced code blocks",
			planText: "#### Automated Verification:\n- [ ] Real command: `make test`\n\n```go\n#### Automated Verification:\n- [ ] Fake command inside fence: `echo injected`\n```\n",
			want: []VerificationStep{
				{Description: "Real command", Command: "make test"},
			},
		},
		{
			name:     "ignores fake headers and checklists in fenced blocks",
			planText: "## Phase 1\n\n```markdown\n#### Automated Verification:\n- [ ] Should be ignored: `rm -rf /`\n- [ ] Also ignored: `drop database`\n```\n\n#### Automated Verification:\n- [ ] Real step: `go vet ./...`\n",
			want: []VerificationStep{
				{Description: "Real step", Command: "go vet ./..."},
			},
		},
		{
			name:     "handles multiple fenced blocks interspersed with real sections",
			planText: "```\nfake header\n#### Automated Verification:\n- [ ] fake: `bad1`\n```\n\n#### Automated Verification:\n- [ ] Good: `good1`\n\n```bash\n- [ ] Another fake: `bad2`\n```\n\n#### Automated Verification:\n- [ ] Also good: `good2`\n",
			want: []VerificationStep{
				{Description: "Good", Command: "good1"},
				{Description: "Also good", Command: "good2"},
			},
		},
		{
			name:     "ignores Go code snippet with backtick fragments",
			planText: "#### Automated Verification:\n- [ ] Tests pass: `go test ./...`\n\n```go\nfunc example() {\n\t// #### Automated Verification:\n\t// - [ ] Description: ` + \"`command`\" + `\n}\n```\n",
			want: []VerificationStep{
				{Description: "Tests pass", Command: "go test ./..."},
			},
		},
		{
			name: "real plan with embedded code examples in fences",
			planText: `## Phase 1: Parser

#### Automated Verification:
- [ ] Build passes: ` + "`go build ./...`" + `

### Code example:

` + "```go" + `
// VerificationStep represents a single automated verification command
type VerificationStep struct {
	Description string
	Command     string
}

#### Automated Verification:
- [ ] Fake build: ` + "`go build fake`" + `
` + "```" + `

## Phase 2: Prompt

#### Automated Verification:
- [ ] Vet clean: ` + "`go vet ./...`" + `
`,
			want: []VerificationStep{
				{Description: "Build passes", Command: "go build ./..."},
				{Description: "Vet clean", Command: "go vet ./..."},
			},
		},
		{
			// Inverted format — command first, prose trailing. The leftover
			// prose ("succeeds.", "passes.", "shows exactly X.") is a sentence
			// fragment, so Description must be "" so the testing-contract
			// extractor falls back to using the command as the name.
			// See agentic-in-napolitan run for the original symptom.
			name:     "inverted format yields empty description",
			planText: "#### Automated Verification:\n- [ ] `go build ./...` succeeds.\n- [ ] `go test ./... -race -short` passes.\n- [ ] `git diff --name-only HEAD~1` shows exactly `README.md`.\n",
			want: []VerificationStep{
				{Description: "", Command: "go build ./..."},
				{Description: "", Command: "go test ./... -race -short"},
				{Description: "", Command: "git diff --name-only HEAD~1"},
			},
		},
		{
			// Bullet whose command itself contains a literal triple-backtick
			// inside a single-quoted regex (a common shape for verification
			// of markdown fence preservation). The legacy regex split this
			// span into multiple pieces; the quote-aware scanner keeps it
			// whole.
			name:     "command with literal backticks inside single quotes",
			planText: "#### Automated Verification:\n- [ ] `grep -cE '^" + "```" + "' README.md` returns the same count as the snapshot.\n",
			want: []VerificationStep{
				{Description: "", Command: "grep -cE '^" + "```" + "' README.md"},
			},
		},
		{
			// Same shape as above but in the canonical "description: `cmd`"
			// order — the description is preserved untouched and the
			// quote-aware scanner still keeps the command intact.
			name:     "canonical format with literal backticks inside command",
			planText: "#### Automated Verification:\n- [ ] Fence count preserved: `grep -cE '^" + "```" + "' README.md`\n",
			want: []VerificationStep{
				{Description: "Fence count preserved", Command: "grep -cE '^" + "```" + "' README.md"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePlanVerification(tt.planText)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d steps, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Description != tt.want[i].Description {
					t.Errorf("step %d description = %q, want %q", i, got[i].Description, tt.want[i].Description)
				}
				if got[i].Command != tt.want[i].Command {
					t.Errorf("step %d command = %q, want %q", i, got[i].Command, tt.want[i].Command)
				}
			}
		})
	}
}

func TestParsePlanManualVerification(t *testing.T) {
	plan := "## Success Criteria\n\n" +
		"### Manual Verification\n" +
		"- [ ] Create a feature from the TUI and observe it reaches PlanReady.\n" +
		"- [x] Run the smoke command against a real workspace.\n" +
		"\n" +
		"```markdown\n" +
		"### Manual Verification\n" +
		"- [ ] Fake manual check in a fence.\n" +
		"```\n"

	got := ParsePlanManualVerification(plan)
	if len(got) != 2 {
		t.Fatalf("got %d manual steps, want 2: %+v", len(got), got)
	}
	if got[0].Description != "Create a feature from the TUI and observe it reaches PlanReady." {
		t.Fatalf("first manual step = %q", got[0].Description)
	}
	if got[1].Description != "Run the smoke command against a real workspace." {
		t.Fatalf("second manual step = %q", got[1].Description)
	}
}

func TestParsePlanManualVerification_NoneRequiredIgnored(t *testing.T) {
	plan := "### Manual Verification\n- [ ] None required: internal parser-only change.\n"
	if got := ParsePlanManualVerification(plan); len(got) != 0 {
		t.Fatalf("None required marker should not produce manual contract items, got %+v", got)
	}
}

func TestParsePlanEvidenceRequirements(t *testing.T) {
	plan := "## Success Criteria\n\n" +
		"### Manual Verification\n" +
		"- [ ] Click through the release flow.\n" +
		"### Visual Evidence\n" +
		"- [ ] Capture the dashboard after import.\n" +
		"- [x] Capture the empty state.\n" +
		"### Behavioral Evidence\n" +
		"- [ ] Record the import command output.\n" +
		"- [ ] None required: this marker should be ignored.\n" +
		"\n" +
		"```markdown\n" +
		"### Visual Evidence\n" +
		"- [ ] Fake screenshot inside a fence.\n" +
		"### Behavioral Evidence\n" +
		"- [ ] Fake behavior inside a fence.\n" +
		"```\n"

	visual := ParsePlanVisualEvidence(plan)
	if len(visual) != 2 {
		t.Fatalf("ParsePlanVisualEvidence() got %d requirements, want 2: %+v", len(visual), visual)
	}
	if visual[0].Description != "Capture the dashboard after import." {
		t.Fatalf("visual[0] = %q", visual[0].Description)
	}
	if visual[1].Description != "Capture the empty state." {
		t.Fatalf("visual[1] = %q", visual[1].Description)
	}

	behavioral := ParsePlanBehavioralEvidence(plan)
	if len(behavioral) != 1 {
		t.Fatalf("ParsePlanBehavioralEvidence() got %d requirements, want 1: %+v", len(behavioral), behavioral)
	}
	if behavioral[0].Description != "Record the import command output." {
		t.Fatalf("behavioral[0] = %q", behavioral[0].Description)
	}

	manual := ParsePlanManualVerification(plan)
	if len(manual) != 1 || manual[0].Description != "Click through the release flow." {
		t.Fatalf("ParsePlanManualVerification() = %+v, want only manual check", manual)
	}
}

func TestParsePlanEvidence_NoneRequiredIgnored(t *testing.T) {
	plan := "### Visual Evidence\n- [ ] None required: no UI surface.\n" +
		"### Behavioral Evidence\n- [ ] None required: automated tests are the artifact.\n"

	if got := ParsePlanVisualEvidence(plan); len(got) != 0 {
		t.Fatalf("ParsePlanVisualEvidence() None required marker produced rows: %+v", got)
	}
	if got := ParsePlanBehavioralEvidence(plan); len(got) != 0 {
		t.Fatalf("ParsePlanBehavioralEvidence() None required marker produced rows: %+v", got)
	}
}

func TestParseChecklistItem(t *testing.T) {
	tests := []struct {
		line    string
		wantOk  bool
		wantCmd string
	}{
		{"- [ ] Tests pass: `go test ./...`", true, "go test ./..."},
		{"- [x] Build: `make build`", true, "make build"},
		{"- [ ] `npm run lint`", true, "npm run lint"},
		{"- [ ] No backtick command here", false, ""},
		{"Not a checklist item: `command`", false, ""},
		{"- [ ] Empty backticks: ``", false, ""},
		{"- [ ] `file.go` exists and compiles: `go build ./...`", true, "go build ./..."},
		{"- [ ] Config `chat` model works (`go test ./internal/config/...`)", true, "go test ./internal/config/..."},

		// Real-world shapes observed in feature plans that the legacy
		// "last backtick" heuristic got wrong. The command is a proper
		// shell invocation with whitespace/operators; the remaining
		// backticks are inline code references (paths, identifiers).
		// See the Phase-01 Native App plan for the canonical example.
		{
			"- [x] Backend test suite passes: `go test ./... -race -short` (includes new `internal/indexer/`, `internal/skilldisco/`, `internal/modelcat/`, `internal/app/` tests).",
			true,
			"go test ./... -race -short",
		},
		{
			"- [ ] Module hygiene: `go mod tidy` leaves `go.mod` / `go.sum` clean.",
			true,
			"go mod tidy",
		},
		{
			"- [ ] Static analysis clean: `go vet ./...` on both tagged and untagged passes.",
			true,
			"go vet ./...",
		},
		// Command-first, description-after with inline-code identifiers.
		// The canonical format puts description before the command, but
		// some plans write it backwards — the filter still picks up the
		// shell-invocation span.
		{
			"- [x] `go test ./internal/kafka/... -tags \"franzgosmoke mockery\" -run TestSmoke` — smoke passes. The `franzgosmoke` tag activates only the smoke test; `mockery` activates mocks.",
			true,
			`go test ./internal/kafka/... -tags "franzgosmoke mockery" -run TestSmoke`,
		},
		// Single-word command falls back to last-raw because it lacks
		// shell features. We'd rather parse than drop the step.
		{"- [ ] Build works: `make`", true, "make"},
		// Environment-variable-prefixed invocation — the `=` shell op
		// triggers the command-shape filter.
		{
			"- [ ] Headless build compiles: `CGO_ENABLED=0 go build ./cmd/agentic`.",
			true,
			"CGO_ENABLED=0 go build ./cmd/agentic",
		},
		// Quote-aware scanner: a single-quoted region containing literal
		// triple-backticks is part of the command, not three separate spans.
		{
			"- [ ] Fence count preserved: `grep -cE '^" + "```" + "' README.md`",
			true,
			"grep -cE '^" + "```" + "' README.md",
		},
		// Inverted format with literal-backtick command — both fixes engage.
		{
			"- [ ] `grep -cE '^" + "```" + "' README.md` returns the same count.",
			true,
			"grep -cE '^" + "```" + "' README.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			step, ok := parseChecklistItem(tt.line)
			if ok != tt.wantOk {
				t.Errorf("ok = %v, want %v", ok, tt.wantOk)
			}
			if ok && step.Command != tt.wantCmd {
				t.Errorf("command = %q, want %q", step.Command, tt.wantCmd)
			}
		})
	}
}
