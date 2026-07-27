# Subagent-Aware Session Watchdog Design

## Problem

The OpenCode session watchdog currently treats five minutes without parent
process output as a stalled tool. That assumption is false while the provider is
running subagents: OpenCode reports each subagent as an ACP task tool, but does
not stream the child session's ongoing work through the parent ACP connection.

The production failure that motivated this change launched six repository
research subagents. The parent ACP stream became quiet at 07:34:36, while the
subagents continued writing artifacts through 07:39:18. At 07:39:37 the
watchdog killed the process group because the most recently observed task had
been silent for five minutes.

Two structural problems made this possible:

1. The watchdog retains only one active tool, so concurrent tool calls overwrite
   one another.
2. OpenCode task tool updates are normalized only as generic tool progress.
   They do not participate in Agentico's existing provider-neutral background
   task lifecycle.

The fix must apply to every Agentico phase and every provider that can report
subagent lifecycle. It must not depend on knowledge-base paths, phase names,
task descriptions, or provider-specific strings in the session package.

## Requirements

- A provider-declared live subagent is authoritative. Agentico must never fail a
  session solely because the parent stream is quiet while at least one subagent
  remains live.
- Provider process exit, explicit terminal errors, and user-initiated stop or
  interrupt retain their existing behavior.
- The ordinary pending-tool watchdog resumes only after the final live subagent
  reaches a terminal lifecycle event.
- The watchdog must track concurrent tools independently by stable tool-use ID.
- Provider adapters own translation from native events to Agentico's normalized
  lifecycle. The session/watchdog layer must not inspect OpenCode ACP kinds,
  provider names, task titles, feature phases, or filesystem paths.
- Agentico must periodically emit an operator-visible local status while it is
  waiting on silent subagents. The status is informational and must not be
  interpreted as provider activity.
- Agentico must not send a second prompt while the provider's original turn is
  still in flight. The OpenCode protocol currently has a single active prompt
  request ID, and ACP provides no generic non-mutating turn probe.

## Architecture

### Provider normalization

The normalized `llm.SDKMessage` task lifecycle is the provider boundary:

- `TaskStarted` marks a child task live.
- `TaskProgress` refreshes its metadata but is not required for liveness.
- `TaskNotification` marks the child task terminal, whether it completed,
  failed, or was cancelled.

Claude already emits these messages natively. The OpenCode protocol adapter
will synthesize them from ACP task tool updates that it can identify using the
adapter's native tool metadata. The adapter will emit exactly one start, zero or
more progress messages, and exactly one terminal notification per stable tool
call ID. Duplicate native updates remain idempotent.

OpenCode continues emitting ordinary `ToolProgress` messages for attachment,
transcript, and tool-result behavior. Task lifecycle messages are additional
normalized semantics, not a replacement for tool progress.

### Session task ledger

`Session` retains its existing provider-neutral live-background-task ledger,
keyed by task ID with tool-use ID as the fallback. All normalized task
lifecycle messages update this ledger regardless of phase or provider.

The watchdog observes the same lifecycle messages. It maintains its own
lock-protected live-subagent set because watchdog decisions must be atomic with
its activity timestamps and tool ledger; reading session state in a separate
lock domain would introduce ordering races.

### Concurrent tool ledger

The watchdog replaces its single `watchdogTool` field with a map keyed by
stable tool-use ID. Anonymous tool events receive a conservative singleton key
so existing providers without IDs preserve current behavior.

- Pending or in-progress updates add or update that tool's running entry.
- Completed or failed updates remove the matching running entry.
- When at least one running tool remains, the pending-tool watchdog applies to
  the running set.
- When the final running tool becomes terminal, the watchdog enters the
  existing "awaiting enclosing turn result" state and starts that timer from the
  terminal update.
- A terminal update for one tool cannot clear or replace another running tool.
- A provider result clears all tool and subagent lifecycle state.

The failure detail identifies the relevant tool deterministically. For multiple
ordinary running tools, it reports the number of active tools plus a stable
representative label rather than implying that only the most recently observed
tool exists.

### Watchdog policy

The watchdog decision order is:

1. Completed or failed session: no watchdog action.
2. Pending permission, question, or help wait: park and refresh activity using
   existing behavior.
3. One or more live subagents: never apply an idle failure timer.
4. Ordinary running tools: apply `PendingToolIdleTimeout`.
5. No running tools after a terminal tool update: apply
   `TurnCompletionIdleTimeout`.
6. No active tool lifecycle: no watchdog action.

When the final subagent terminates, the watchdog sets its activity timestamp to
the terminal event time. Any remaining ordinary tool is therefore given a full
pending-tool idle window. If no tools remain but the provider has not completed
the enclosing turn, the full turn-completion idle window begins then.

There is intentionally no maximum duration for a provider-declared live
subagent. This matches the selected policy: lifecycle events and process
termination, not elapsed wall time, determine whether the subagent is live.

### Operator heartbeat

While the live-subagent set is non-empty and the parent stream remains quiet,
the session emits a local status at a fixed interval:

`Waiting for 6 subagents (15m)`

The heartbeat:

- is appended through the existing local status path so it appears in
  transcripts and attached UI surfaces;
- is rate-limited and reports elapsed wait rounded to whole minutes;
- is scheduled once per session, reports the current live-task count, and
  measures elapsed time from the first live task in the current uninterrupted
  wait;
- does not call `watchdog.Observe`, update `lastActivityAt`, or masquerade as
  provider output;
- stops immediately when the final subagent terminates or the session ends.

The initial implementation uses a five-minute heartbeat interval, aligned with
the current operator expectation established by the old watchdog window. It is
not exposed as user configuration because it has no correctness effect. The
interval is carried in the per-session watchdog options so tests use isolated
overrides rather than mutating a package-level timeout.

## Error and Recovery Behavior

- A terminal task notification removes the child from both the session and
  watchdog live sets even when its status is failed or cancelled. The parent
  provider remains responsible for converting child failure into its own
  response or error.
- Malformed task events without any usable task or tool-use ID do not create an
  immortal live entry. They remain ordinary tool progress and retain the
  existing watchdog behavior.
- If the provider process exits while live tasks remain, normal process/session
  completion wins; stale task entries cannot keep a dead process alive.
- If a provider never emits a terminal task event and keeps its process alive,
  Agentico waits indefinitely and continues local heartbeats. This is an
  explicit consequence of treating provider-declared task liveness as
  authoritative.
- No concurrent parent nudge is sent. A nudge is safe only after the current
  prompt has returned; existing post-turn continuation behavior remains
  unchanged.

## Testing

Tests will be written before production changes and will cover:

1. OpenCode protocol normalization:
   - one task start from pending/in-progress native updates;
   - duplicate in-progress updates produce progress without double-starting;
   - completed, failed, and cancelled terminal updates produce one task
     notification;
   - non-task tools never produce task lifecycle messages.
2. Concurrent watchdog tools:
   - two running tools are retained independently;
   - completing one leaves the other protected by its own timer;
   - completing the final tool starts the turn-completion timer.
3. Subagent immunity:
   - a live task remains running beyond the pending-tool timeout without
     failure;
   - ordinary provider messages do not accidentally clear the live task;
   - final task notification rearms the applicable watchdog window;
   - multiple tasks require every terminal notification before rearming.
4. Lifecycle exits:
   - an explicit provider result clears all task and tool state;
   - a dead process is not kept alive by stale task state.
5. Heartbeats:
   - a quiet live-task wait emits a local status without refreshing provider
     activity;
   - heartbeats are rate-limited and stop after the final task notification.

The fast suite is required before handoff. Because this changes session
lifecycle, the relevant extended gates are Isolated integration and E2E Go with
the race detector. Static analysis and build checks also apply:

```bash
make test-fast
go test ./test/integration/... -count=1
go test ./test/e2e/... -count=1 -race
go vet ./...
go build ./...
```

The full Race regression gate is additionally appropriate because the design
introduces concurrent map and timer state in the watchdog:

```bash
go test ./... -count=1 -race
```

## Non-Goals

- Inferring subagent progress from filesystem changes, CPU usage, process-tree
  shape, token cost, or task names.
- Adding provider-specific task logic to `internal/session`.
- Changing feature pipelines or knowledge-base behavior.
- Sending concurrent ACP prompts, cancelling healthy turns, or adding a new
  provider recovery protocol.
- Making heartbeat timing user-configurable.
