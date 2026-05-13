# Testing Phase 5 Timing Report

This report records the completed Phase 5 `internal/session` determinism and
speed-up pass. Phase 1's baseline remains in `docs/testing-baseline.md`.

## Scope

The session package now keeps stream-driven fast-suite coverage in-process for
protocol parsing, result extraction, permission/control routing, usage capture,
context-window capture, attach-drop reporting, status-channel ordering, and
manager status routing. The remaining subprocess-backed checks are either the
small named fast-suite representatives or are gated behind `testing.Short()` as
extended-regression owners.

Package-level timeout mutations were removed from tests. `criticalAttachSendTimeout`,
`codexHandshakeTimeout`, and `resultShutdownGrace` keep the production defaults
in source, while tests use per-session overrides through `SessionOpts` or
session-scoped test setters.

## Internal Session Package

| Measurement | Before Phase 5 | Final Phase 5 | Budget |
|-------------|----------------|---------------|--------|
| Targeted short command, `go test ./internal/session/... -count=1 -short` | 34.731s warm-cache plan baseline | 10.828s final verification run; 1.386s warm `-json` timing run | Under the 30s command target and under the Phase 1 per-package fast-suite budget |
| Targeted full session command, `go test ./internal/session/... -count=1` | 51.569s iteration-01 reference | 47.266s final verification run | Extended regression |
| Repeated short flake check, `go test ./internal/session/... -count=10 -short` | 40.624s iteration-01 reference | 9.736s final verification run | Passed |
| Whole-repo fast suite, `go test ./... -count=1 -short` | not captured here | Passed; `internal/session` reported 6.871s under whole-repo scheduling | Suite passed |

## Extended Ownership

Fast-suite subprocess representatives:

| Behavior | Owner | File | Reason |
|----------|-------|------|--------|
| Subprocess shutdown | `TestSessionStop` | `internal/session/session_test.go` | Verifies `Stop()` terminates a real subprocess and closes `Done()`. |
| Process-group cleanup | `TestTerminateProcessGroup` | `internal/session/recovery_test.go` | Verifies real process-group termination on the fast path. |
| Attach-channel backpressure | none subprocess-backed | `internal/session/session_test.go` | `MockProtocol` plus a saturated per-test `attachCh` reproduces the drop/delivery path without real pipe buffering. |

Extended-regression subprocess owners that run without `-short`:

| Behavior | Owner tests |
|----------|-------------|
| SIGTERM/SIGKILL escalation | `TestSession_SIGTERMEscalation` |
| Process-group child lifetime | `TestTerminateProcessGroup_ChildOutlivesLeader` |
| Recovery kill of a live process | `TestExecuteRecoveryKillLiveProcess` |
| Stream backpressure stress | `TestSession_StreamBackpressurePreservesCritical` |
| Result/wrapper lifecycle | `TestSession_CodexProviderStaysAliveAfterResult`, `TestSession_ResultUnsticksHungWrapper`, `TestSession_TruncatedResultKeepsStdinForContinuationWhenOptedIn`, `TestTweakSessionDoesNotCloseStdinOnClaudeResult` |
| Stderr and line-size subprocess behavior | `TestSessionStderrCapturedToFile`, `TestSessionStderrNotForwardedToAttachChannel`, `TestSessionStderrDiscardedWithoutLogPath`, `TestCodexSessionDeduplication`, `TestSession_LargeLineWithinBuffer`, `TestSession_LargeLineHandling`, `TestSession_OversizedLineHandling` |
| Real stdin/control-response round trips | `TestSession_RespondToAskUser_E2E`, `TestSession_DenyResponse_E2E`, manager subprocess tests in `manager_test.go`, identity emission table |

## Parallel Audit

No `t.Parallel()` calls were added in this phase. The audit classification is
intended for Phase 10 enablement only.

Parallel-exempt retained tests:

| Test | File | Reason |
|------|------|--------|
| `TestSessionStop` | `internal/session/session_test.go` | Fast subprocess-shutdown representative. |
| `TestSession_CodexProviderStaysAliveAfterResult` | `internal/session/session_test.go` | Extended subprocess lifecycle owner. |
| `TestSession_ResultUnsticksHungWrapper` | `internal/session/session_test.go` | Extended subprocess wrapper-cleanup owner. |
| `TestSession_TruncatedResultKeepsStdinForContinuationWhenOptedIn` | `internal/session/session_test.go` | Extended subprocess continuation owner. |
| `TestSessionInterrupt_FallsBackToSIGINT` | `internal/session/session_test.go` | Extended subprocess signal owner. |
| `TestTweakSessionDoesNotCloseStdinOnClaudeResult` | `internal/session/session_test.go` | Extended subprocess tweak lifecycle owner. |
| `TestSessionStderrCapturedToFile` | `internal/session/session_test.go` | Extended stderr subprocess owner. |
| `TestSessionStderrNotForwardedToAttachChannel` | `internal/session/session_test.go` | Extended stderr attach-channel owner. |
| `TestSessionStderrDiscardedWithoutLogPath` | `internal/session/session_test.go` | Extended stderr discard owner. |
| `TestSession_APIErrorThenSuccess` | `internal/session/session_test.go` | Extended repeated-status subprocess owner. |
| `TestSession_SIGTERMEscalation` | `internal/session/session_test.go` | Extended SIGTERM/SIGKILL owner. |
| `TestSession_LargeLineWithinBuffer` | `internal/session/session_test.go` | Extended large-line scanner owner. |
| `TestSession_LargeLineHandling` | `internal/session/session_test.go` | Extended large-line scanner owner. |
| `TestSession_OversizedLineHandling` | `internal/session/session_test.go` | Extended oversized-line scanner owner. |
| `TestSession_RespondToAskUser_E2E` | `internal/session/session_test.go` | Extended real control-response round trip. |
| `TestSession_DenyResponse_E2E` | `internal/session/session_test.go` | Extended real deny-response round trip. |
| `TestSession_StreamBackpressurePreservesCritical` | `internal/session/session_test.go` | Extended high-volume backpressure stress. |
| `TestSDKEventDelivery` | `internal/session/manager_test.go` | Extended subprocess/event-channel owner. |
| `TestHighVolumePartialStreamNoBoundedGoroutines` | `internal/session/manager_test.go` | Extended high-volume subprocess/event-channel owner. |
| `TestStartSessionWithInitialPrompt` | `internal/session/manager_test.go` | Extended real stdin initial-prompt owner. |
| `TestStartSessionWithoutInitialPrompt` | `internal/session/manager_test.go` | Extended real no-prompt startup owner. |
| `TestManager_SessionDoneMsgNeverDropped` | `internal/session/manager_test.go` | Extended event-channel drop-pressure owner. |
| `TestWaitingPermissionNotClobberedByAssistantPartial` | `internal/session/manager_test.go` | Extended real stream ordering owner. |
| `TestStartSessionWithPipedInput` | `internal/session/manager_test.go` | Extended stderr-path startup owner. |
| `TestEmittedEventsCarryIdentity_TableDriven` | `internal/session/identity_test.go` | Extended manager subprocess identity owner. |
| `TestCodexSessionExecLeavesStderrOutOfAssistantLog` | `internal/session/codex_integration_test.go` | Live Codex integration, gated by `-short` and environment. |
| `TestCodexSessionExecCanLogStderrToFile` | `internal/session/codex_integration_test.go` | Live Codex integration, gated by `-short` and environment. |
| `TestCodexSessionManagerIntegration` | `internal/session/codex_integration_test.go` | Live Codex manager integration, gated by `-short` and environment. |
| `TestCodexSessionANSIStripping` | `internal/session/codex_integration_test.go` | Live Codex ANSI integration, gated by `-short` and environment. |
| `TestCodexSessionDeduplication` | `internal/session/codex_integration_test.go` | Extended stderr redraw subprocess owner. |
| `TestExecuteRecoveryKillLiveProcess` | `internal/session/recovery_test.go` | Extended process-backed recovery owner. |
| `TestTerminateProcessGroup` | `internal/session/recovery_test.go` | Fast process-group representative. |
| `TestTerminateProcessGroup_ChildOutlivesLeader` | `internal/session/recovery_test.go` | Extended process-group child-lifetime owner. |

Parallel-candidate retained tests:

| Area | Files | Tests | Reason |
|------|-------|-------|--------|
| ANSI, transcript, permission, pidfile, recovery adapter, and session view unit tests | `ansi_test.go`, `transcript_test.go`, `permission_test.go`, `pidfile_test.go`, `recovery_adapter_test.go`, `session_view_test.go` | All retained tests not listed as exempt above | Pure functions, per-test temp dirs, or manager/session values scoped to the test. |
| Attach ring and message log | `attach_ring_test.go`, `message_log_test.go` | All retained tests | In-memory state only; concurrency tests use per-test values. |
| Control protocol wire format and direct routing | `control_protocol_test.go` | `TestControlResponseWireFormat_*`, `TestInitializeRequestWireFormat`, `TestHandleControlRequest_*`, `TestAcceptEditsHandler_*`, `TestHasUnansweredQuestion_*`, `TestRespondToAskUser_*`, `TestSendUserMessage_*`, `TestManagerOnMessage_*` | Pure JSON assertions, in-process pipes, or direct manager/session routing. |
| Control-request routing | `control_request_routing_test.go` | All retained tests | Per-test sessions/channels; remaining wait caps are failure deadlines, not shared state. |
| Session mock-replay fast path | `session_test.go` | `TestSessionStartAndCapture`, `TestSessionSendInput`, `TestSessionControlRequest`, `TestSessionSendUserMessage`, `TestSessionResultDeliveredUnderBackpressure`, `TestSession_OnSubagentEventFires`, `TestSession_MalformedJSONWrappedAsAssistant`, usage/context tests, attach-drop reporter tests, drainer/status-channel ordering tests | In-process stdout/protocol replay, per-test attach buffers, per-session timeout overrides. |
| Session pure state helpers | `session_test.go` | `TestSessionInterrupt_DelegatesToProtocol`, `TestSessionInterrupt_NoProcess_NoError`, `TestIsActive`, `TestResetWaitingStatus`, `TestSessionWriteJSON`, `TestContextPercentage`, `TestResultSubtypeToStatus`, `TestAccumulatedUsageZeroDefault`, `TestSetAccumulatedUsage`, `TestAccumulatedUsageFromResult` | Pure state, JSON, or protocol-double assertions. |
| Manager pure state | `manager_test.go` | `TestSessionStatusTransitions`, `TestResetWaitingStatus_JSONProtocol`, `TestManager_StartSessionAfterShutdown` | No subprocess is started in the assertion path; per-test manager state. |
| Recovery filesystem/state tests | `recovery_test.go` | All retained tests not listed as exempt above | Per-test temp dirs and synthetic PID files; no live process. |

## Timeout Defaults

Production defaults are unchanged:

| Default | Source | Value |
|---------|--------|-------|
| `criticalAttachSendTimeout` | `internal/session/session.go` | `5 * time.Second` |
| `codexHandshakeTimeout` | `internal/session/manager.go` | `30 * time.Second` |
| `resultShutdownGrace` | `internal/session/session.go` | `5 * time.Second` |

Verification grep results:

```text
grep -nE "criticalAttachSendTimeout[[:space:]]*=|codexHandshakeTimeout[[:space:]]*=" internal/session/*_test.go
# zero matches; grep exits 1 for no matches

grep -nE "resultShutdownGrace[[:space:]]*=" internal/session/*_test.go
# zero matches; grep exits 1 for no matches
```

## Cross-Package Verification Note

Iteration 01 fixed a cleanup race in
`internal/tui::TestTUI_HandleSessionDone_QAWriteOnlyForResearch/brainstorm_skips`
by gating next-phase advancement and waiting for orchestrator/session cleanup
before temp directory removal. The final whole-repo fast suite still passes
with that fix in place.
