# Agentico Server Map

This is the Phase 0 ground-truth map for the `feature/agentico-mcp-server`
branch. It documents the current server API shape before the API hardening
phases make behavior changes.

## Server Entry Points

- CLI bootstrap lives in `cmd/agentico/main.go`. `runServer` builds the
  headless server with `serverruntime.Start` and passes `boot.eventCh` as the
  runtime event channel plus `boot.orchestrator.Events()` as the domain event
  channel.
- The HTTP API handler lives in `internal/server`. `newAPIHandler` wires the
  feature store, sessions, mutation target, runtime identity, and event broker.
- REST routes are registered in `internal/server/handler.go`:
  - `GET /api/v1/health`
  - `/api/v1/features` and `/api/v1/features/...`
  - `/api/v1/config/runtime`
  - `GET /api/v1/workspace/browse`
  - `GET /api/v1/catalog/models`
  - `/api/v1/prompts` and `/api/v1/prompts/...`
  - `/api/v1/permissions` and `/api/v1/permissions/...`
  - `GET /api/v1/sessions` and `GET /api/v1/sessions/...`
  - `/api/v1/recovery` and `/api/v1/recovery/actions`
  - `/api/v1/shutdown`
  - `GET /api/v1/events`

## Event Broker

Current implementation: `internal/server/sse.go`.

- The broker owns `nextID atomic.Uint64` and `subs map[chan SSEEventDTO]struct{}`
  behind a mutex.
- `newEventBroker` starts two goroutines:
  - runtime events from `Options.Events <-chan interface{}`
  - domain events from `Options.DomainEvents <-chan ports.Event`
- IDs are assigned by callers with `b.newID()` before `publish`, except
  backpressure marker events, heartbeats, and the initial connected event, which
  allocate their own IDs.
- Every subscriber gets a fixed channel buffer of 16 events.
- `publish` never blocks. It tries to send to each subscriber; if a subscriber
  channel is full, it evicts one queued event, then enqueues a
  `backpressure.coalesced` snapshot-required event carrying the triggering
  resource identity.
- There is no global replay ring, no server epoch, no durable cursor, and no
  `Last-Event-ID` or `after` handling.

Current wire DTO: `SSEEventDTO` in `internal/server/types.go`.

- Fields: `api_version`, `id`, `kind`, `at`, `resource`, `revision`,
  `snapshot_required`, `summary`.
- `ResourceDTO` fields: `type`, `id`, `feature_id`, `phase`.
- `revision` is a content hash from `revisionForAny`; it is not a monotonic
  per-resource version.

Runtime event mapping:

- `session.SDKEventMsg` usually maps to `session.updated`.
- `AskUserQuestion` control messages map to `prompt.updated`.
- Other control messages map to `permission.updated`.
- Tool progress maps to `log.updated`.
- `session.SessionDoneMsg` maps to `session.updated`.
- Unknown runtime events map to `lifecycle.updated` on the runtime resource.

Domain event mapping:

- `FeatureConfigChanged` -> `config.updated`.
- `NeedUserInputRequired` -> `prompt.updated`.
- `RecoveryScanned` and `RecoveryExecuted` -> `recovery.updated`.
- `RuntimeShutdownStarted` -> `shutdown.updated`.
- `SessionOutput` -> `session.updated`.
- Most lifecycle events stay `lifecycle.updated` on a feature resource.

Domain event producers:

- `internal/orchestrator/orchestrator.go` publishes feature lifecycle events
  such as created, started, advanced, interrupted, phase started/completed,
  review required, publish started/completed, feature completed, and feature
  failed.
- `internal/orchestrator/need_user_input.go` publishes user-input-required and
  phase-completed events for the NeedUserInput gate.
- `internal/orchestrator/lifecycle_delegates.go` publishes feature rewinds,
  config changes, repo-status changes, and publish completion state.
- `internal/orchestrator/recovery.go` and setup handling publish recovery/setup
  status into the domain event stream.
- `internal/session/manager.go` forwards each parsed SDK message as a runtime
  `session.SDKEventMsg` and sends `session.SessionDoneMsg` on completion.

Phase 1 touch points:

- Move sequence assignment into one broker publish path so all subscribers see
  identical numbering.
- Replace the per-subscriber eviction marker with the ordered replay ring plus
  bounded latest-value coalescing.
- Add epoch and resource-version fields to the envelope.
- Update all event DTO consumers, especially `internal/server/client_sse.go`
  and TUI refresh handling.

## SSE Handler And Client

Server handler: `internal/server/sse.go`.

- Endpoint: `GET /api/v1/events`.
- Requires `http.Flusher`.
- Response headers: `Content-Type: text/event-stream`,
  `Cache-Control: no-cache`, `Connection: keep-alive`.
- On connect, it immediately sends a `connected` event with
  `snapshot_required: true` and resource type `runtime`.
- Heartbeats are ordinary SSE events with kind `heartbeat`; default interval is
  15 seconds. `heartbeat_ms` query parameter may override it, with a 10 ms
  minimum.
- `writeSSE` already emits the SSE `id:` field and serializes the same ID in
  the JSON payload.
- The handler does not inspect `Last-Event-ID` and does not support `?after=`.

Client: `internal/server/client_sse.go`.

- `SubscribeEvents` loops forever with reconnect delay, defaulting to 250 ms.
- The client sends only `Accept: text/event-stream` and optional
  `heartbeat_ms`; no cursor header or `after` query is sent.
- The scanner permits 1 MB SSE lines.
- Heartbeats without `snapshot_required` are dropped.
- The client converts each event to `RefreshSignal`, then `FetchRefreshSnapshot`
  maps signal kinds/resources to read-model requests.
- `connected` and snapshot-required heartbeats trigger a broad snapshot of
  health, features, prompts, permissions, and sessions.
- Session events currently refetch session detail, the last 50 transcript
  messages, prompts, live preview, and sometimes feature detail.

Phase 1 touch points:

- Add reconnect cursor state to `EventSubscriptionOptions` or client-internal
  state.
- Send `after=<seq>` on reconnect and/or `Last-Event-ID` where appropriate.
- Handle `stream.reset` by taking a snapshot and resubscribing from the
  advertised sequence.
- Preserve current TUI behavior at the reducer/model layer while changing the
  transport semantics.

## Snapshot And Read Model Endpoints

Read DTOs are hand-written in `internal/server/types.go`; handlers live across
`handler.go`, `read_model.go`, `session_model.go`, `content_model.go`, and
`recovery_api.go`.

Current revision model:

- `ResponseMeta` contains `revision`, `generated_at`, and optional `cache_hit`.
- `revisionForAny` hashes marshaled response content with SHA-256 and returns a
  12-byte hex prefix.
- `writeRevisionedJSON` sets `ETag: "<revision>"` and honors both
  `If-None-Match` and `?revision=<revision>`.
- There is no `asOfSeq`, `X-Agentico-Seq`, epoch, or monotonic
  `resourceVersion` on read responses.

Important read surfaces:

- Feature list and detail, including historical run summaries.
- Runtime config, feature config, workspace browse, model catalog, prompts,
  permissions, and recovery snapshots.
- Session list, session detail, and structured transcript snapshots from the
  active in-memory session message log.
- Run artifacts and bounded text content slices.
- Run logs by ID:
  - `session` -> `<runDir>/logs/session.log`
  - `phase` -> `<runDir>/logs/phase.log`
  - `observe` -> `<runDir>/events.jsonl`
- Live preview currently embeds the last 80 message-log transcript entries for
  a feature's latest session.

Phase 1 touch points:

- Add snapshot read sequence (`asOfSeq` body field and `X-Agentico-Seq` header)
  to all snapshot/list endpoints that participate in event convergence.
- Add durable or derived monotonic resource versions. Current hash revisions
  are useful for cache validation but do not provide ordering.
- Decide which resources own versions: feature, session, prompt snapshot,
  permission snapshot, runtime/config, recovery, and possibly log/output views.

Phase 4 touch points:

- `api/openapi.yaml` must model the existing error shape, metadata shape, DTOs,
  event envelope, control events, and all route variants before generation can
  replace handwritten DTO usage incrementally.

## Discovery Metadata

Implementation: `internal/server/discovery.go`.

- Discovery file name: `.agentico-server.json`.
- `PublishDiscovery` creates the runtime dir with `0755`, writes a temporary
  file with `0600`, renames it into place, then chmods the final file to
  `0600`.
- `PrepareDiscovery` validates the existing file before reuse:
  - owner-only permissions (`mode & 077 == 0`)
  - current effective UID owns the file on Unix
  - loopback `base_url`
  - matching API version, runtime identity, and launch policy
  - healthy server response
  - owner process metadata matches
- The discovery record includes schema version, API version, base URL, event
  epoch, auth token, runtime identity, launch policy, start mode, PID, PGID,
  timestamps, and owner.

Client bootstrap:

- `cmd/agentico/main.go` publishes discovery after server startup.
- TUI launch creates `serverruntime.NewClient` with only `BaseURL`.
- Reuse/discovery health checks currently call `/api/v1/health` without auth.

Phase 1 and 3 touch points:

- Add epoch to discovery for event-stream invalidation.
- Add token material or a pointer to token material, while preserving `0600`
  handling.
- Update discovery health checks for auth-required reads once Phase 3 lands.

## Mutation Auth And Browser Boundaries

Implementation: `internal/server/mutation.go` and `internal/server/client.go`.

Current REST mutation gate:

- Every mutation handler calls `requireTrustedMutation`.
- It requires `X-Agentico-Client: local`.
- If an `Origin` header is present, it must be loopback.
- Request body content type must be JSON.
- Request body is capped at 64 KiB.
- Mutation CORS preflight requires loopback origin and only allows
  `Content-Type` plus `X-Agentico-Client`.

Current REST client behavior:

- `ClientOptions` has base URL, HTTP client, and timeout only.
- Reads call `getJSON` without the trusted header.
- Mutations call `doJSON(..., trusted=true)`, which adds
  `X-Agentico-Client: local`.
- No REST request includes `Authorization`.

MCP boundary:

- The MCP adapter was removed during the Phase 3 cleanup checkpoint. The
  client-facing server surface is REST plus SSE only.

Important correction for Phase 3:

- Existing gates mitigate browser-origin and CSRF-style risks; they are not
  authentication for local processes.

Phase 3 touch points:

- Introduce a per-instance token source in the runtime/state directory.
- Add bearer validation to every `/api/v1` route.
- Keep existing loopback/origin/header checks as defense in depth.
- Update `serverruntime.Client`, TUI bootstrap, discovery health checks, and SSE
  connection setup.
- For native browser SSE clients, support the documented `access_token` query
  fallback and avoid logging full query strings for those routes.

## Session Output And Transcripts

Session implementation: `internal/session`.

Structured transcript path:

- `Session` owns a thread-safe `MessageLog`.
- `readMessages` parses each raw stdout JSONL line through the provider
  protocol and dispatches one or more `llm.SDKMessage` values.
- `SessionManager.handleSessionMessage` updates status/message-log state, then
  forwards an SDK event to the runtime event channel.
- `/api/v1/sessions/{id}/transcript` reads from `sess.MessageLog().Messages()`
  with `offset` and `limit`; max limit is 500 messages.
- Live preview reads `sess.MessageLog().LastN(80)`.

Raw durable output path:

- `SessionOpts.LogPath` is the intended raw output path.
- `SessionManager.StartSession` opens `LogPath` with `os.Create` before
  starting the subprocess and calls `SetLogFile`.
- `Session.readMessages` appends each non-empty raw stdout line plus newline to
  that file before parsing.
- The log file is closed in `readMessages` cleanup.
- `Session.LogFilePath()` exposes the currently attached file path while the
  session object exists.

Observed output locations:

- Standard feature phase sessions write `output.txt` under their active run
  phase or artifact directory.
- Setup attempts write
  `<state>/<feature>/runs/run-NNN/setup/attempt-XX-output.txt`.
- Chat utility sessions write `<chatDir>/output.txt`.
- Some helper loops set log files manually, for example final review
  `review-response.txt` and `fix-output.txt`, validation helper outputs, review
  helper outputs, and refactor/rebase/review-comment iteration outputs.
- Existing run-log REST IDs point at `<runDir>/logs/session.log`,
  `<runDir>/logs/phase.log`, and `<runDir>/events.jsonl`; these are separate
  from many session `LogPath` `output.txt` files.

Phase 2 touch points:

- A dedicated output stream should tail the raw `Session.LogFilePath()` for
  active sessions rather than teeing subprocess pipes.
- Historical lookup needs a stable way to resolve a session ID to its log path.
  Today the active session object exposes it, but not every persisted run
  artifact path is session-ID indexed.
- Decide whether the output stream is raw JSONL bytes, rendered text chunks, or
  structured records. The durable writer currently stores raw provider stdout
  lines, while the transcript endpoint exposes parsed message DTOs.
- Remove high-volume `SDKEventMsg`/transcript churn from the global broker or
  throttle it to activity notifications carrying current log offset.

## Repo Conventions Relevant To Later Phases

- `make test-fast` is required before handoff.
- The extended gates in `AGENTS.md` apply by area:
  - isolated integration for lifecycle, state-machine, runs layout, and
    protocol-violation behavior
  - E2E Go for TUI, Bubble Tea model behavior, and session lifecycle
  - TUI observability for observer wiring/events/spans
  - race regression for high-risk or concurrency-sensitive changes
- TUI tests in `internal/tui` should stay at the model/reducer/translator layer;
  full terminal drivers belong in `test/e2e`.
- Prompt template edits require regenerating `.golden` snapshots, but this
  server map does not touch prompt templates.

## Plan Corrections Confirmed By Phase 0

- SSE already emits the SSE `id:` field, but IDs are not resumable because the
  server has no replay contract and ignores `Last-Event-ID`.
- Discovery metadata is already `0600` and validates owner-only permissions.
- The recent coalesced-marker fix is present: the marker carries the triggering
  resource identity.
- Current `revision` fields are content hashes, not monotonic resource
  versions.
- REST `/api/v1` reads and SSE are unauthenticated today. REST mutations rely
  on `X-Agentico-Client: local` plus loopback-origin checks.
- Session output has two different shapes today: raw provider stdout persisted
  to `LogPath`, and structured API transcript snapshots derived from
  in-memory parsed messages.
