# Session & SDK Protocol

## Session Lifecycle

The `Session` type (`internal/session/session.go:45-88`) manages a single `claude` CLI subprocess:

```
NewSession() → Start(command, workdir, env, onMessage) → reads JSON messages → processes events → Done()
```

### Session Fields

| Field | Description |
|-------|-------------|
| `ID` | Session identifier (`<featureID>-<phase>`) |
| `FeatureID` | Associated feature |
| `Phase` | Execution phase |
| `Process` | `exec.Cmd` subprocess |
| `Status` | `SessionRunning / WaitingPermission / WaitingHelp / Done / Failed` |
| `PrintMode` | Non-interactive `--print` mode (reads stderr instead) |
| `MessageLog` | Structured message history |
| `StatusCh` | Receives `SUCCESS`/`RETRY` from result messages |
| `attachCh` | Channel for TUI attach mode subscribers |
| `PermHandler` | Permission request handler interface |
| `LastControlRequest` | Pending permission for TUI approval |

### Session Operations

| Method | Description |
|--------|-------------|
| `SendUserMessage(text)` | Send user message via JSON protocol |
| `RespondToControl(requestID, allow, reason)` | Respond to permission requests |
| `RespondToAskUser(requestID, questions, answers)` | Answer agent questions |
| `Stop()` | Graceful SIGTERM then SIGKILL |
| `Wait()` / `Done()` | Wait for completion |
| `AttachCh()` | Get channel for live message streaming |

## SDK Message Types

Defined in `internal/session/sdk_types.go:10-35`:

| Type | Subtype | Struct | Description |
|------|---------|--------|-------------|
| `system` | `init` | `SystemInitMessage` | Session initialization (model, session ID, MCP servers) |
| `assistant` | (various) | `AssistantMessage` | AI responses with content blocks |
| `user` | — | `UserMessage` | User messages |
| `result` | `success/error/max_turns` | `ResultMessage` | Session completion with cost |
| `tool_progress` | — | `ToolProgressMessage` | Tool execution progress |

### ContentBlock Types

| Type | Description |
|------|-------------|
| `text` | Plain text output |
| `tool_use` | Tool invocation (name, input, ID) |
| `tool_result` | Tool execution result |
| `thinking` | Internal reasoning (thinking blocks) |
| `server_tool_use` | MCP server tool invocation |

## Session Manager

The `Manager` (`internal/session/manager.go:26-32`) tracks all active sessions:

| Method | Description |
|--------|-------------|
| `StartSession(...)` | Create session, set up logging, PID files, event routing |
| `StopSession(id)` | Stop a running session |
| `GetSession(id)` | Look up session by ID |
| `ActiveSessions()` | List running sessions |
| `FeatureSessions(featureID)` | List sessions for a feature |
| `Attach(sessionID)` | Enter attach mode for live output |
| `Detach()` | Leave attach mode |
| `Shutdown()` | Stop all sessions |

**`SessionOpts`**: Optional configuration — `PrintMode`, `PermHandler`, `LogPath`, `IterDir`, `Iteration`.

## Permission Handling

### Interface

`PermissionHandler` with method: `CanUseTool(ToolPermissionRequest) (PermissionDecision, error)`

### Implementations

| Handler | Behavior |
|---------|----------|
| `AutoApproveHandler` | Always allow (for `--dangerously-skip-permissions`) |
| `AcceptEditsHandler` | Allow read/edit tools, deny Bash |
| `DenyAllHandler` | Deny everything |

In the TUI, permissions are surfaced to the user via `PermissionsQueue` on the Feature struct. Users approve/deny from the dashboard.

## Message Log

Thread-safe `MessageLog` (`internal/session/message_log.go`) stores SDK messages:

| Method | Description |
|--------|-------------|
| `Append` / `UpdateLast` / `UpdateLastAssistantPartial` | Message management |
| `Messages` / `LastN` / `LastResultMessage` | Query methods |
| `AssistantText()` | Extract all assistant text (excluding tool uses) |
| `ToolUseBlocks()` | Extract all tool use blocks |
| `Text()` | Human-readable formatted transcript |

## Recovery

Recovery handles crashed sessions via PID files:

### PID File Operations

| Function | Description |
|----------|-------------|
| `WritePIDFile(dir, pf)` | Write PID file for active session |
| `ReadPIDFile(dir)` | Read PID file from feature directory |
| `RemovePIDFile(dir)` | Clean up PID file on session end |
| `FindPIDFiles(baseDir)` | Scan for PID files across all features |
| `ProcessAlive(pid)` | Check if process is still running |

### Recovery Operations

| Function | Description |
|----------|-------------|
| `ScanForRecovery(dir, fm)` | Find orphaned PID files from crashed sessions |
| `ExecuteRecovery(items, actions, fm)` | Apply recovery actions |
| `CleanupStalePIDFiles(dir)` | Remove PID files for dead processes |

**Recovery actions**: `RecoveryResume`, `RecoveryKill`, `RecoverySkip`

## ANSI Stripping

`StripANSI(s)` (`internal/session/ansi.go`) removes ANSI escape codes from PTY output for clean message logging.
