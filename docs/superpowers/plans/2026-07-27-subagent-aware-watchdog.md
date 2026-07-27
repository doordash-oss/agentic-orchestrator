# Subagent-Aware Session Watchdog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent the generic session watchdog from terminating quiet parent turns while provider-declared subagents are still live.

**Architecture:** Provider adapters normalize their native task events into the existing `llm.SDKMessage` task lifecycle. The session watchdog maintains independent active-tool and live-subagent ledgers, parks all idle failure timers while the subagent ledger is non-empty, and emits rate-limited local status heartbeats without treating those statuses as provider activity.

**Tech Stack:** Go, OpenCode ACP JSON-RPC adapter, Agentico normalized LLM messages, session lifecycle/watchdog, standard `testing` package.

## Global Constraints

- Apply to every phase and every provider that emits normalized task lifecycle messages.
- Never use phase names, knowledge-base paths, task titles, or provider names in watchdog decisions.
- Never time out a provider-declared live subagent.
- Never send a concurrent parent prompt as a liveness probe.
- Preserve explicit provider errors, process exit, permission/question parking, and user stop behavior.
- Every new `*.go`, `*.sh`, or `*.py` file requires the repository Apache 2.0 header; this plan creates no new source files.
- Tests that mutate process state or own subprocesses must not use `t.Parallel()`.

---

### Task 1: Normalize OpenCode Task Lifecycle

**Files:**
- Modify: `internal/llm/opencode/protocol.go`
- Modify: `internal/llm/opencode/protocol_test.go`
- Modify: `internal/llm/message.go`

**Interfaces:**
- Consumes: `SessionUpdate`, `toolCallState`, and existing `TaskStartedMessage`, `TaskProgressMessage`, `TaskNotificationMessage`.
- Produces: `func (p *Protocol) taskLifecycleFromUpdate(update SessionUpdate) []llm.SDKMessage`.

- [ ] **Step 1: Write failing protocol lifecycle tests**

Extend `TestParseLine_OpenCodeTaskUpdateEmitsTaskToolUsePrompt` and add a
table-driven terminal-status test. Assert literal normalized behavior:

```go
if msgs[1].TaskStarted == nil ||
	msgs[1].TaskStarted.TaskID != "call_task" ||
	msgs[1].TaskStarted.ToolUseID != "call_task" {
	t.Fatalf("task start = %+v, want normalized lifecycle for call_task", msgs)
}

for _, status := range []string{"completed", "failed", "cancelled"} {
	t.Run(status, func(t *testing.T) {
		// Parse a fresh recognizable task start, then its terminal update.
		// Assert exactly one TaskNotification with the literal status and ID.
	})
}
```

Also assert that a normal `kind: "read"` tool update yields no `TaskStarted`,
`TaskProgress`, or `TaskNotification`.

- [ ] **Step 2: Verify the protocol tests fail for missing lifecycle**

Run:

```bash
go test ./internal/llm/opencode -run 'OpenCodeTask|TaskLifecycle' -count=1
```

Expected: FAIL because current OpenCode task updates emit only assistant
tool-use and generic tool-progress messages.

- [ ] **Step 3: Implement idempotent task lifecycle normalization**

Extend `toolCallState` with lifecycle flags:

```go
taskStartedEmitted  bool
taskTerminalEmitted bool
```

Add adapter-local normalization:

```go
func (p *Protocol) taskLifecycleFromUpdate(update SessionUpdate) []llm.SDKMessage {
	// Merge native metadata under p.mu.
	// A recognizable kind=think update with prompt and stable ID emits one
	// TaskStarted, repeated non-terminal updates emit TaskProgress, and the
	// first completed/failed/cancelled update emits TaskNotification.
}
```

Use `TaskID` and `ToolUseID` equal to the stable ACP `ToolCallID`. Populate
`Description` from `description` with title fallback, `TaskType` from
`subagent_type`, and `Prompt` from `prompt`. Append lifecycle messages before
the ordinary `ToolProgress` message so the watchdog knows a task is live before
it observes the matching in-progress tool event.

Update stale comments in `internal/llm/message.go` that say only Claude emits
task lifecycle; describe the lifecycle as provider-normalized while retaining
Claude wire-format details on the concrete structs.

- [ ] **Step 4: Verify protocol tests pass**

Run:

```bash
go test ./internal/llm/opencode -run 'OpenCodeTask|TaskLifecycle' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the normalization**

```bash
git add internal/llm/message.go internal/llm/opencode/protocol.go internal/llm/opencode/protocol_test.go
git commit -m "Expose provider task activity to shared session lifecycle"
```

### Task 2: Track Concurrent Tools and Live Subagents

**Files:**
- Modify: `internal/session/watchdog.go`
- Modify: `internal/session/session_test.go`

**Interfaces:**
- Consumes: normalized `TaskStarted`, `TaskProgress`, `TaskNotification`, and `ToolProgress` SDK messages.
- Produces: watchdog-local `tools map[string]watchdogTool`, `liveSubagents map[string]struct{}`, and deterministic stall selection.

- [ ] **Step 1: Write failing concurrent-tool tests**

Replace the single-state transition test with behavior against a real
`sessionWatchdog`:

```go
watchdog.Observe(toolProgress("tool-a", "Write", "in_progress"))
watchdog.Observe(toolProgress("tool-b", "Read", "in_progress"))
watchdog.Observe(toolProgress("tool-a", "Write", "completed"))

watchdog.mu.Lock()
_, toolAExists := watchdog.tools["tool-a"]
toolB := watchdog.tools["tool-b"]
watchdog.mu.Unlock()
if toolAExists || toolB.name != "Read" {
	t.Fatalf("concurrent tools not retained independently")
}
```

Add a final-terminal assertion that no running tools remain and the
turn-completion phase is armed with the final tool label.

- [ ] **Step 2: Write failing subagent-immunity tests**

Create a watchdog with millisecond test-specific timeouts, observe two task
starts and matching task tool progress, force `lastActivityAt` into the past,
and assert `toolStall()` is false. Notify only one task terminal and assert it
remains false. Notify the final task terminal and assert the timer is freshly
rearmed rather than immediately stalled.

The production mutation caught by this test is removal or premature clearing of
the live-subagent gate.

- [ ] **Step 3: Verify watchdog tests fail**

Run:

```bash
go test ./internal/session -run 'Watchdog.*Concurrent|Watchdog.*Subagent' -count=1
```

Expected: FAIL because `sessionWatchdog` stores one tool and ignores normalized
task lifecycle.

- [ ] **Step 4: Implement the ledgers and watchdog decision order**

Replace:

```go
tool watchdogTool
```

with:

```go
tools             map[string]watchdogTool
awaitingTurn      watchdogTool
liveSubagents     map[string]struct{}
subagentWaitSince time.Time
```

Use the stable tool-use ID as the tool key and a package constant singleton key
for anonymous tool events. Pending events insert/update one running entry.
Terminal events delete only the matching entry. When the final running entry is
deleted, set `awaitingTurn` to that terminal tool. A Result clears every ledger.

Add `observeWatchdogTaskLifecycle` using `backgroundTaskKey` semantics. Task
start/progress insert a live ID; terminal notification deletes it. Ignore events
without a usable ID.

In `toolStall`, after permission/question/help parking and terminal session
checks, return no stall whenever `len(liveSubagents) > 0`. On the final terminal
task event set `lastActivityAt = time.Now()` before releasing the watchdog lock.

- [ ] **Step 5: Preserve boundary-race protection**

Update `failTool` to compare the current selected state/ledger generation under
the watchdog mutex before failing. Any Result, task lifecycle transition, tool
transition, or provider stdout newer than the snapshot wins the race.

- [ ] **Step 6: Verify session watchdog tests pass**

Run:

```bash
go test ./internal/session -run 'Watchdog|PendingTool' -count=1
```

Expected: PASS, including all pre-existing permission and post-tool stall
regressions.

- [ ] **Step 7: Commit concurrent lifecycle support**

```bash
git add internal/session/watchdog.go internal/session/session_test.go
git commit -m "Keep live provider subagents outside idle failure windows"
```

### Task 3: Emit Non-Invasive Waiting Heartbeats

**Files:**
- Modify: `internal/ports/session.go`
- Modify: `internal/agent/phase.go`
- Modify: `internal/session/watchdog.go`
- Modify: `internal/session/session_test.go`

**Interfaces:**
- Consumes: `Session.appendLocalStatus`, live-subagent count, and the watchdog run ticker.
- Produces: `SessionWatchdogConfig.SubagentHeartbeatInterval time.Duration`.

- [ ] **Step 1: Write failing heartbeat behavior test**

Create a real `Session`, capture `onMessage`, and construct a watchdog with an
isolated short heartbeat interval:

```go
watchdog := newSessionWatchdog(sess, &ports.SessionWatchdogConfig{
	PendingToolIdleTimeout:    time.Second,
	TurnCompletionIdleTimeout: time.Second,
	PollInterval:              5 * time.Millisecond,
	SubagentHeartbeatInterval: 20 * time.Millisecond,
})
watchdog.Observe(taskStartedMsg("task-a"))
watchdog.Start()
```

Assert one local status with `Waiting for 1 subagent`, assert
`lastActivityAt` is unchanged by the status, then observe the terminal task
notification and assert no later heartbeat arrives.

- [ ] **Step 2: Verify the heartbeat test fails**

Run:

```bash
go test ./internal/session -run 'Watchdog.*Heartbeat' -count=1
```

Expected: FAIL because no heartbeat configuration or emission exists.

- [ ] **Step 3: Implement the per-session heartbeat option**

Add:

```go
SubagentHeartbeatInterval time.Duration
```

to `ports.SessionWatchdogConfig`, default it to five minutes in
`internal/agent/phase.go`, and copy it through the existing per-session
watchdog configuration.

Track the next heartbeat time inside `sessionWatchdog`. During each poll, take a
snapshot of the live count and uninterrupted wait start. If due, release the
watchdog mutex and call:

```go
noun := "subagents"
if count == 1 {
	noun = "subagent"
}
_ = w.session.appendLocalStatus(
	fmt.Sprintf("Waiting for %d %s (%s)",
		count, noun, roundedElapsed),
)
```

Do not pass the local status back through `watchdog.Observe`. Reset heartbeat
state when the final task terminates or the session ends.

- [ ] **Step 4: Verify heartbeat and watchdog tests pass**

Run:

```bash
go test ./internal/session -run 'Watchdog|PendingTool' -count=1
go test ./internal/agent -run 'WatchdogConfig' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the heartbeat**

```bash
git add internal/ports/session.go internal/agent/phase.go internal/session/watchdog.go internal/session/session_test.go
git commit -m "Make indefinite subagent waits visible to operators"
```

### Task 4: Verify the Complete Lifecycle Change

**Files:**
- Modify only if a failing verification gate exposes a defect in the approved behavior.

**Interfaces:**
- Consumes: all changes from Tasks 1-3.
- Produces: verified repository state and handoff evidence.

- [ ] **Step 1: Run focused packages**

```bash
go test ./internal/llm/opencode ./internal/session ./internal/agent -count=1
```

Expected: PASS.

- [ ] **Step 2: Run static analysis and build**

```bash
go vet ./...
go build ./...
```

Expected: PASS.

- [ ] **Step 3: Run Fast suite**

```bash
make test-fast
```

Expected: PASS within the repository's everyday short-mode envelope.

- [ ] **Step 4: Run Isolated integration**

```bash
go test ./test/integration/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Run E2E Go**

```bash
go test ./test/e2e/... -count=1 -race
```

Expected: PASS.

- [ ] **Step 6: Run Race regression**

```bash
go test ./... -count=1 -race
```

Expected: PASS.

- [ ] **Step 7: Inspect final scope**

```bash
git status --short
git diff HEAD~3 --stat
git log -4 --oneline
```

Confirm no unrelated user files changed and no required verification tier was
skipped.
