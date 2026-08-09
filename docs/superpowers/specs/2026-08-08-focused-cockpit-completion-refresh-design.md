# Focused Cockpit Completion Refresh

## Problem

The active feature cockpit can remain on the running presentation after an agent has finished and the server has durably moved the feature to its next state, such as `PlanNeedsReview`. Selecting another feature and returning fixes the view immediately because remounting the cockpit performs a fresh authoritative `getFeature` request.

The current system is already event-driven. The runtime publishes snapshot-required SSE invalidations, the main process forwards them to the renderer, and `FeatureCockpit` routes relevant events through a single-flight refresh scheduler. Hidden work is deferred to protect battery life.

Evidence from the reported run shows:

- the plan session ended at `16:46:57.472`;
- the server published the durable feature lifecycle invalidations at `16:46:57.708`, about 236 ms later; and
- the focused renderer's session-output subscription also received a terminal `done` event, but `useCohortTranscripts` currently ignores every output event except `record`.

The design therefore adds an independent, bounded convergence signal at the focused renderer without introducing continuous feature polling.

## Goals

- Show a durably settled feature state in the focused cockpit within about one second of a streamed session ending.
- Keep SSE invalidations as the primary update mechanism.
- Add no recurring work while the app is idle, hidden, or displaying another feature.
- Coalesce simultaneous session completions and all refresh sources through the existing single-flight coordinator.
- Preserve authoritative server snapshots; do not infer or optimistically set a feature status from transcript content.

## Non-goals

- Do not poll every feature or the overview continuously.
- Do not parse `<agentico-outcome>` text in the renderer.
- Do not change server lifecycle semantics or add feature state to push-event payloads.
- Do not change user-visible copy, roles, labels, or navigation behavior.

## Design

### Terminal output signal

`useCohortTranscripts` will recognize the existing session-output `done` event for a member of the active cohort. It will continue reconciling `record` events exactly as it does today and expose a callback for terminal output instead of deriving feature state locally.

The callback will flow through `CurrentRunInspection` to `FeatureCockpit`. It will carry no domain snapshot and will mean only: "a focused streamed session ended; revalidate the authoritative feature after the server has had time to settle."

### Bounded settle refresh

`FeatureCockpit` will schedule one silent refresh approximately 500 ms after the first terminal signal in a completion burst. Further `done` signals before the timer fires will reuse the same timer. This covers multi-agent cohorts without one request per reviewer or validator.

The timer callback will call the existing `refreshFeature({ silent: true })` path. The feature refresh scheduler remains the sole coordinator, so an SSE-triggered or direct refresh already in flight absorbs or serializes the fallback rather than racing it.

The settle timer will be cancelled when:

- the cockpit unmounts because another sidebar feature was selected;
- the document becomes hidden; or
- the focused inspection is otherwise deactivated.

When visibility returns, normal SSE replay, visibility convergence, or remount loading remains authoritative. The fallback does not create hidden-window work.

### Timing rationale

The observed durable lifecycle transition followed session termination by about 236 ms. A 500 ms delay leaves margin for persistence and event propagation while keeping the visible response under one second. It also avoids an immediate request that is likely to read the pre-transition snapshot.

### Failure handling

The fallback is best-effort and silent. A failed request keeps the last usable cockpit snapshot and the existing stale/refresh indicators. Subsequent SSE invalidations, visibility convergence, manual refresh, or remount can still recover. No retry loop is added.

## Alternatives Considered

### Poll the active feature every second while streaming

This gives a simple latency bound but performs hundreds or thousands of feature reads during long runs. The renderer already has an exact terminal signal, so continuous polling is unnecessary.

### Rely exclusively on the global SSE lifecycle stream

This has no extra requests, but the captured run proves the server emitted the expected final lifecycle events while the mounted cockpit still remained stale. A focused, independent convergence path makes the UI resilient to an intermittent missed or raced invalidation.

### Refresh immediately on `<agentico-outcome>` text

The tag precedes process exit and durable orchestration state changes. Parsing it in the renderer would couple presentation code to an agent protocol and frequently fetch too early.

## Verification

Unit coverage will prove:

- a cohort session-output `done` event reaches the terminal callback;
- non-member, `record`, and `error` events do not falsely signal completion;
- multiple terminal signals coalesce into one delayed silent feature refresh;
- hiding, deactivating, switching features, or unmounting cancels pending work; and
- an SSE invalidation and the terminal fallback still serialize through the existing refresh scheduler.

Repository verification will include the canonical **Fast** tier plus `go vet ./...` and `go build ./...`. Because this changes the Electron renderer and session-output behavior, verification will also run the affected desktop unit tests and a targeted packaged journey that exercises live-session completion or stop convergence. No user-visible strings change, so packaged journey selectors should not require updates; the journey files will still be checked for any affected labels before handoff.

## Success Criteria

- The focused cockpit revalidates once within roughly 500 ms of terminal session output and presents the next durable feature state as soon as that read returns.
- Idle and hidden pages perform no new periodic work.
- A multi-session completion burst adds at most one fallback feature request.
- Existing SSE convergence, refresh failure behavior, and feature-switch loading remain intact.
