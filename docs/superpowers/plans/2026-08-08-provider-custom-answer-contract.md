# Provider Custom Answer Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Claude custom answers schema-valid and pin the intended custom-answer wire contract for Claude, Codex, and OpenCode with deterministic tests.

**Architecture:** Keep provider-specific answer translation inside each existing protocol adapter. Claude will continue converting unmatched free text into a selectable option, now with every schema-required field; Codex and OpenCode keep their existing native response and framed-follow-up behaviors, protected by explicit contract assertions.

**Tech Stack:** Go, `encoding/json`, provider JSON-RPC/control-response protocols, Go's `testing` package.

## Global Constraints

- Do not change renderer, IPC, HTTP API, session-state, or shared provider interfaces.
- Preserve existing option-selection and auto-pick behavior.
- Use deterministic tests only; do not call live providers or require credentials or network access.
- Do not touch any pre-existing user modifications under `desktop/`.
- Every commit must end with `Co-authored-by: Codex <noreply@openai.com>`.

---

### Task 1: Make Claude's injected custom option schema-valid

**Files:**
- Modify: `internal/llm/claude/protocol_test.go:200-290`
- Modify: `internal/llm/claude/protocol.go:162-223`

**Interfaces:**
- Consumes: `(*Protocol).RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error`
- Produces: schema-valid `updatedInput.questions[*].options[*]` objects with `label` and `description` for injected custom answers.

- [x] **Step 1: Extend the decoded option contract with `description`**

In `askUserControlResponse`, decode both required option fields:

```go
Options []struct {
	Label       string `json:"label"`
	Description string `json:"description"`
} `json:"options"`
```

- [x] **Step 2: Write the failing custom-answer assertion**

Strengthen `TestClaudeProtocol_RespondToAskUser_FreeTextInjectedAsOption` so the injected option must carry a stable description:

```go
const customAnswer = "use a third custom approach"
out := respondToAskUser(t, map[string]string{"Which approach?": customAnswer})
updated := out.Response.Response.UpdatedInput
if got := updated.Answers["Which approach?"]; got != customAnswer {
	t.Errorf("answer = %q, want the free text verbatim", got)
}
opts := updated.Questions[0].Options
if len(opts) != 3 {
	t.Fatalf("options len = %d, want 3", len(opts))
}
if opts[2].Label != customAnswer || opts[2].Description != "User-provided custom answer." {
	t.Errorf("injected option = %+v, want schema-valid custom option", opts[2])
}
```

- [x] **Step 3: Run the test and verify RED**

Run:

```bash
go test ./internal/llm/claude -run '^TestClaudeProtocol_RespondToAskUser_FreeTextInjectedAsOption$' -count=1 -v
```

Expected: FAIL because `opts[2].Description` is empty while the test requires `"User-provided custom answer."`.

- [x] **Step 4: Add the minimal schema-valid Claude option**

Change the unmatched-answer append in `alignAskUserAnswers` to include the required description:

```go
q["options"] = append(opts, map[string]any{
	"label":       answer,
	"description": "User-provided custom answer.",
})
```

- [x] **Step 5: Run focused Claude tests and verify GREEN**

Run:

```bash
go test ./internal/llm/claude -run '^TestClaudeProtocol_RespondToAskUser_(ExactLabelPassesThrough|FreeTextInjectedAsOption|FreeTextForOptionlessQuestion)$' -count=1 -v
```

Expected: PASS. Exact labels remain unchanged, custom text has a valid injected option, and optionless questions still accept free text.

- [x] **Step 6: Commit the Claude fix**

```bash
git add internal/llm/claude/protocol.go internal/llm/claude/protocol_test.go
git commit -m "Keep Claude custom answers from being rejected" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 2: Pin Codex's native custom-answer response

**Files:**
- Modify: `internal/llm/codex/protocol_test.go`

**Interfaces:**
- Consumes: `(*Protocol).RespondToAskUser` and `(*Protocol).SetQuestionIDsForTest(map[string]string)`.
- Produces: a deterministic test proving Codex writes `AskUserResult{Answers: []AskUserAnswer}` with the provider question ID and verbatim custom value.

- [x] **Step 1: Add a native custom-answer contract test**

Add a focused test beside the other `RespondToAskUser` tests:

```go
func TestRespondToAskUser_NativePreservesCustomAnswer(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{})
	p.SetStdin(&buf)
	p.SetQuestionIDsForTest(map[string]string{"Which approach?": "question-7"})

	const customAnswer = "Use a third custom approach"
	if err := p.RespondToAskUser(
		"42",
		json.RawMessage(`{"questions":[{"question":"Which approach?"}]}`),
		map[string]string{"Which approach?": customAnswer},
		nil,
	); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	var out struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      int           `json:"id"`
		Result  AskUserResult `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &out); err != nil {
		t.Fatalf("unmarshal response: %v (raw=%q)", err, buf.String())
	}
	if out.JSONRPC != "2.0" || out.ID != 42 {
		t.Fatalf("response envelope = %+v, want JSON-RPC id 42", out)
	}
	if len(out.Result.Answers) != 1 || out.Result.Answers[0].QuestionID != "question-7" || out.Result.Answers[0].Value != customAnswer {
		t.Fatalf("answers = %+v, want question-7 with verbatim custom value", out.Result.Answers)
	}
}
```

- [x] **Step 2: Run the Codex contract test**

Run:

```bash
go test ./internal/llm/codex -run '^TestRespondToAskUser_NativePreservesCustomAnswer$' -count=1 -v
```

Expected: PASS, characterizing the already-supported native free-text behavior without production changes.

- [x] **Step 3: Commit the Codex contract**

```bash
git add internal/llm/codex/protocol_test.go
git commit -m "Protect Codex custom-answer delivery" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 3: Strengthen OpenCode's custom-answer fallback contract

**Files:**
- Modify: `internal/llm/opencode/control_test.go:365-405`
- Track: `docs/superpowers/plans/2026-08-08-provider-custom-answer-contract.md`

**Interfaces:**
- Consumes: `(*Protocol).RespondToAskUser` for a structured question whose custom answer matches no native option.
- Produces: deterministic assertions that OpenCode cancels the native request and sends a framed `session/prompt` containing both the original question and custom answer.

- [x] **Step 1: Strengthen the existing free-form fallback test**

In `TestRespondToAskUser_StructuredFreeFormFallsBackToFollowUpTurn`, keep the existing method and cancellation assertions, then require the framed payload to contain the answer context:

```go
wire := buf.String()
for _, want := range []string{
	`"outcome":"cancelled"`,
	"[AskUserQuestion answer]",
	"> Pick one",
	"User's selected answer: Actually do C instead",
} {
	if !strings.Contains(wire, want) {
		t.Errorf("custom-answer wire output missing %q: %s", want, wire)
	}
}
```

Remove the narrower standalone cancellation assertion once the table covers it, avoiding duplicate checks.

- [x] **Step 2: Run the OpenCode contract test**

Run:

```bash
go test ./internal/llm/opencode -run '^TestRespondToAskUser_StructuredFreeFormFallsBackToFollowUpTurn$' -count=1 -v
```

Expected: PASS, characterizing the already-supported cancel-and-follow-up behavior without production changes.

- [x] **Step 3: Run all provider protocol packages together**

Run:

```bash
go test ./internal/llm/claude ./internal/llm/codex ./internal/llm/opencode -count=1
```

Expected: PASS for all three packages.

- [x] **Step 4: Run repository verification gates**

Run each command from the repository root:

```bash
make test-fast
go vet ./...
go build ./...
```

Expected: all commands exit zero with no test, vet, or build failures.

- [x] **Step 5: Commit the OpenCode contract and implementation plan**

Because `docs/superpowers/` is globally ignored, force-add only the named plan file. Do not stage any pre-existing user modifications under `desktop/`.

```bash
git add internal/llm/opencode/control_test.go
git add -f docs/superpowers/plans/2026-08-08-provider-custom-answer-contract.md
git commit -m "Guard provider custom-answer contracts" -m "Co-authored-by: Codex <noreply@openai.com>"
```
